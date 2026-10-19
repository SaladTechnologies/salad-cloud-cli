package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/logs"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"
	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
	"github.com/saladtechnologies/salad-cloud-cli/internal/logsq"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

var logsCmd = &cobra.Command{
	Use:   "logs [-c <group>] [--project <p>] [--instance <id>]",
	Short: "Query or follow logs for the organization, a project, or a container group",
	Long: `Query or follow logs, including system events.

The scope is the narrowest one you give:

  saladcloud logs                        the whole organization, every project
  saladcloud logs --project <p>          every container group in one project
  saladcloud logs -c <group>             one container group (in the current project)

For one container group, saladcloud container-groups logs <group> is the
same query with the group as its subject.

Only an explicit --project narrows a query without a container group: the
saved default project and SALAD_PROJECT do not, so saladcloud logs, with
neither flag, always means the organization. When the scope is wider than one container group,
each table line names the project and group it came from.

By default the last 15 minutes of logs are printed and the command exits.
--since and --until accept a duration (15m, 2h, 3d, 1d12h, relative to
now), an RFC3339 timestamp (2026-01-02T15:04:05Z), or a date (2026-01-02,
midnight UTC). Logs are retained for 90 days. The API only accepts
millisecond-precision time ranges; finer-grained --since/--until values are
rounded outward (start down, end up) automatically.

With --follow the command streams new log entries as they arrive (polling;
--poll-interval tunes the cadence) until interrupted with Ctrl-C, which
exits 0. --until cannot be combined with --follow.

Caveat: the API can only window on each entry's node emission time, so
--follow re-scans a sliding window reaching --lookback (default 2m) behind
the newest emission time seen, deduplicating as it goes. An entry ingested
more than --lookback after its emission time will be missed; raise
--lookback when tailing workloads whose logs arrive with a long delay.

--filter matches a substring anywhere in the log (text or JSON). --query
appends a raw log query-language expression, for example
'json_log.status >= 500 or text_log startswith "panic"'.

With --output json each entry is printed as one NDJSON line containing the
full API entry (both timestamps, resource labels, span/trace IDs).`,
	Example: `  # Last 15 minutes of logs across the whole organization
  saladcloud logs

  # Errors and worse in one project over the last day
  saladcloud logs --project prod --since 1d --severity error

  # Warnings and worse across the organization that mention "timeout"
  saladcloud logs --since 2h --severity warning --filter timeout

  # One group's system events only (resource.type "container" is the app's own output)
  saladcloud logs -c my-cg --since 1h --query 'resource.type != "container"'`,
	Args: cobra.NoArgs,
	RunE: runLogs,
}

// containerGroupLogsCmd is 'saladcloud container-groups logs <group>': the
// query with the group as its subject, found from 'container-groups --help'.
// Both logs commands share addLogsFlags.
var containerGroupLogsCmd = &cobra.Command{
	Use:   "logs <group> [--instance <id>]",
	Short: "Query or follow one container group's logs",
	Long: `Query or follow the logs and system events of one container group, in the
current project. saladcloud logs --help covers the organization- and
project-wide forms and how --since, --until and --follow behave.`,
	Example: `  # Last 15 minutes of logs for my-cg
  saladcloud container-groups logs my-cg

  # Warnings and worse from the last 2 hours that mention "timeout"
  saladcloud container-groups logs my-cg --since 2h --severity warning --filter timeout

  # System events only (allocation, download, start, probes, exits), then only the app's output
  saladcloud container-groups logs my-cg --since 1h --query 'resource.type != "container"'
  saladcloud container-groups logs my-cg --since 1h --query 'resource.type = "container"'

  # Follow one instance's logs as NDJSON
  saladcloud container-groups logs my-cg --instance 8a2f13ba --follow -o json`,
	Args: cobra.ExactArgs(1),
	RunE: runLogs,
}

// addLogsFlags registers the logs flags on c; both logs commands use it.
func addLogsFlags(c *cobra.Command) {
	c.Flags().String("since", "15m", "start of the time range: duration (15m, 2h, 3d), RFC3339 timestamp, or date")
	c.Flags().String("until", "", "end of the time range (same forms as --since; default now; conflicts with --follow)")
	c.Flags().
		String("instance", "", "only logs from this instance (full ID; a unique prefix also works when a container group is given)")
	c.Flags().String("severity", "", "minimum severity: "+strings.Join(logsq.Severities, "|"))
	c.Flags().String("filter", "", "only logs containing this substring (text or JSON)")
	c.Flags().String("query", "", "raw log query-language expression, ANDed onto the generated query")
	c.Flags().BoolP("follow", "f", false, "stream new log entries until interrupted")
	c.Flags().Duration("poll-interval", logsq.DefaultPoll, "how often to poll for new entries with --follow (min 2s)")
	c.Flags().
		Duration("lookback", logsq.DefaultLookback, "with --follow, how far behind the newest seen log time each poll re-scans for late-arriving entries")
	c.Flags().Int("page-size", 100, "entries per API request (1-100)")
}

func init() {
	addLogsFlags(logsCmd)
	logsCmd.Flags().StringP("container-group", "c", "", "only this container group's logs (in the current project)")
	logsCmd.Flags().
		String("project", "", "only this project's logs; unlike other commands, the saved project and SALAD_PROJECT do not narrow logs")
	annotate(logsCmd, annotationFlagForArg, "container-group")
	addLogsFlags(containerGroupLogsCmd)
	addHiddenGroupFlag(containerGroupLogsCmd)
	annotate(containerGroupLogsCmd, annotationFlagForArg, "instance")
	Register(logsCmd)
	RegisterContainerGroups(containerGroupLogsCmd)
}

func runLogs(cmd *cobra.Command, args []string) error {
	fr := readFlags(cmd.Flags())
	// The group is the subject of 'container-groups logs <group>' and a
	// filter (-c) on the top-level query.
	group := ""
	if len(args) == 1 {
		group = args[0]
	} else if cmd.Annotations[annotationGroupIsArg] == "" { // logs -c; container-groups logs only hides its -c
		group = fr.String("container-group")
	}
	sinceStr := fr.String("since")
	untilStr := fr.String("until")
	instance := fr.String("instance")
	severity := fr.String("severity")
	filter := fr.String("filter")
	rawQuery := fr.String("query")
	follow := fr.Bool("follow")
	pollInterval := fr.Duration("poll-interval")
	lookback := fr.Duration("lookback")
	pageSize := fr.Int("page-size")
	if cerr := fr.Err(); cerr != nil {
		return cerr
	}

	// Pre-flight validation: everything below must fail with a usage error
	// before any configuration or client work, so bad input exits 2 even
	// when no API key is set (and never reaches the API).
	if follow && cmd.Flags().Changed("until") {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "--until cannot be combined with --follow")
	}
	if pollInterval < logsq.MinPoll {
		return clierr.New(
			clierr.CodeUsage,
			clierr.KindUsage,
			"--poll-interval must be at least %s (got %s)",
			logsq.MinPoll,
			pollInterval,
		)
	}
	if cmd.Flags().Changed("lookback") && !follow {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "--lookback only applies with --follow")
	}
	if lookback <= 0 {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "--lookback must be positive (got %s)", lookback)
	}
	if pageSize < 1 || pageSize > 100 {
		return clierr.New(
			clierr.CodeUsage,
			clierr.KindUsage,
			"--page-size must be between 1 and 100 (got %d)",
			pageSize,
		)
	}
	if instance != "" && !isUUID(instance) && group == "" {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage,
			"--instance with an ID prefix needs a container group to resolve it against").
			WithHint("pass -c <group>, or the full instance UUID")
	}
	now := time.Now().UTC()
	since, cerr := logsq.ParseTimeRef(sinceStr, now)
	if cerr != nil {
		return cerr
	}
	until := now
	if cmd.Flags().Changed("until") {
		if until, cerr = logsq.ParseTimeRef(untilStr, now); cerr != nil {
			return cerr
		}
	}
	if !follow && !until.After(since) {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "--until (%s) must be after --since (%s)",
			until.Format(time.RFC3339), since.Format(time.RFC3339))
	}
	baseFilters := logsq.Filters{
		Group:       group,
		Instance:    instance,
		MinSeverity: severity,
		Contains:    filter,
		RawQuery:    rawQuery,
	}
	// Validate the filters now; the scope is filled in once the client has
	// resolved the organization, so a placeholder stands in for it here.
	preflight := baseFilters
	preflight.Org = "-"
	if _, cerr = logsq.Compile(preflight); cerr != nil {
		return cerr
	}

	client, res, cerr := NewClient(cmd)
	if cerr != nil {
		return cerr
	}
	if cerr = client.RequireOrg(); cerr != nil {
		return cerr
	}
	// A group's logs are filtered by its project, which the log query never
	// checks: on the assumed "default" project, confirm it exists first, so
	// the command warns and a missing one is no valid project set (exit 2)
	// rather than an empty result.
	if group != "" && res.ProjectSource == config.SourceDefault {
		if cerr = client.CheckProject(cmd.Context()); cerr != nil {
			return cerr
		}
	}
	// The scope: a group is looked up in the resolved project; without a
	// group, only an explicit --project narrows the organization.
	var scope logScope
	switch {
	case group != "":
		baseFilters.Project = res.Project
		scope = logScope{kind: scopeGroup, name: group}
	case cmd.Flags().Changed("project"):
		baseFilters.Project = res.Project
		scope = logScope{kind: scopeProject, name: res.Project}
	default:
		baseFilters.Org = client.Org()
		scope = logScope{kind: scopeOrg, name: client.Org()}
	}

	if output.Mode() == "table" {
		fprintln(os.Stderr, scopeLine(scope, client.Org(), res.Project))
	}

	// --instance accepts an ID prefix (like ssh/cp); the log query language
	// needs the exact instance_id, so a non-UUID value is resolved against
	// the group's live instances first. Full UUIDs skip the extra call.
	if instance != "" && !isUUID(instance) {
		collection, lerr := client.ListInstances(cmd.Context(), group)
		if lerr != nil {
			return groupNotFound(lerr, group, client.Org(), client.Project())
		}
		inst, rerr := resolveSSHInstance(collection.Instances, group, instance)
		if rerr != nil {
			return rerr
		}
		baseFilters.Instance = ptr.Deref(inst.Id)
	}

	fetch := func(ctx context.Context, query, startRFC3339, endRFC3339 string, pageSize int) (*logsq.Page, *clierr.Error) {
		q := logs.LogEntryQuery{}
		q.SetStartTime(startRFC3339)
		q.SetEndTime(endRFC3339)
		q.SetQuery(query)
		q.SetPageSize(int64(pageSize))
		q.SetSortOrder(logs.LOG_ENTRY_QUERY_SORT_ORDER_ASC)
		coll, err := client.QueryLogs(ctx, q)
		if err != nil {
			return nil, err
		}
		page := &logsq.Page{
			PageMinTime: ptr.Deref(coll.PageMinTime),
			PageMaxTime: ptr.Deref(coll.PageMaxTime),
		}
		for i := range coll.Items {
			page.Items = append(page.Items, toLogsqEntry(&coll.Items[i]))
		}
		return page, nil
	}
	emit := func(e logsq.Entry) error {
		return output.Stream(e.Raw, formatLogLine(e, scope.kind))
	}

	if !follow {
		query, err := logsq.Compile(baseFilters)
		if err != nil {
			return err
		}
		emitted := 0
		countingEmit := func(e logsq.Entry) error {
			emitted++
			return emit(e)
		}
		if err := logsq.FetchWindow(cmd.Context(), fetch, query, since, until, pageSize, countingEmit); err != nil {
			return err
		}
		// An empty one-shot result is easy to misread as a hang or a bug;
		// say so on stderr in table mode. JSON/NDJSON stays machine-clean,
		// and --follow's silence correctly means "waiting".
		if emitted == 0 && output.Mode() == "table" {
			fprintln(os.Stderr, noMatchNote(scope, sinceStr, instance, severity, filter))
		}
		return nil
	}

	ctx := cmd.Context() // canceled by the one signal handler in Execute
	if group != "" {
		if note := followGroupNote(ctx, client, group); note != "" {
			fprintln(os.Stderr, note)
		}
	}
	if output.Mode() == "table" {
		fprintln(os.Stderr, "waiting for log entries (Ctrl-C to stop)")
	}
	query, cerr := logsq.Compile(baseFilters)
	if cerr != nil {
		return cerr
	}
	follower := &logsq.Follower{
		Poll:       pollInterval,
		MaxBackoff: logsq.DefaultMaxBackoff,
		Lookback:   lookback,
		Clock:      logsq.RealClock{},
	}
	//nolint:revive // if-return: returned directly, a nil *clierr.Error would be a non-nil error
	if cerr := follower.Run(ctx, query, fetch, since, pageSize, emit); cerr != nil {
		return cerr
	}
	return nil
}

// followGroupNote is the stderr note for following a group that cannot log:
// a failed or stopped group runs nothing, so the follow would stay silent.
// Best effort: a group that cannot be read gets no note.
func followGroupNote(ctx context.Context, client *api.Client, group string) string {
	g, cerr := client.GetGroup(ctx, group)
	if cerr != nil {
		return ""
	}
	return groupSilentNote(g)
}

// groupSilentNote says why a group's follow will stay silent, or "".
func groupSilentNote(g *shared.ContainerGroup) string {
	status := groupStatus(g)
	if status != "failed" && status != "stopped" {
		return ""
	}
	why := ""
	if reason := groupReason(g); reason != "" {
		why = ": " + reason
	}
	return fmt.Sprintf("note: group %s is %s%s; nothing will be logged until it runs", ptr.Deref(g.Name), status, why)
}

// Log query scopes, widest first.
const (
	scopeOrg     = "organization"
	scopeProject = "project"
	scopeGroup   = "group"
)

// logScope is what a logs query covers.
type logScope struct {
	kind string // scopeOrg, scopeProject, or scopeGroup
	name string
}

// scopeLine is the stderr line naming what a table-mode logs query covers,
// since the organization-wide default differs from every other command.
func scopeLine(scope logScope, org, project string) string {
	switch scope.kind {
	case scopeGroup:
		return fmt.Sprintf("scope: container group %s in %s/%s", scope.name, org, project)
	case scopeProject:
		return fmt.Sprintf("scope: project %s in organization %s", scope.name, org)
	}
	return fmt.Sprintf("scope: organization %s, all projects (--project or -c narrows it)", org)
}

// noMatchNote is the stderr note for a one-shot table-mode query that
// emitted nothing. Optional filters appear only when set, compactly.
func noMatchNote(scope logScope, since, instance, severity, filter string) string {
	msg := fmt.Sprintf(`no log entries matched (%s "%s", since %s`, scope.kind, scope.name, since)
	if instance != "" {
		msg += fmt.Sprintf(`, instance "%s"`, instance)
	}
	if severity != "" {
		msg += fmt.Sprintf(`, severity "%s"`, severity)
	}
	if filter != "" {
		msg += fmt.Sprintf(`, filter "%s"`, filter)
	}
	return msg + ")"
}

// toLogsqEntry flattens an SDK log entry for logsq, keeping the original
// SDK entry as Raw for full-fidelity NDJSON output.
func toLogsqEntry(le *logs.LogEntry) logsq.Entry {
	e := logsq.Entry{
		Time:        ptr.Deref(le.Time),
		ReceiveTime: ptr.Deref(le.ReceiveTime),
		Text:        ptr.Deref(le.TextLog),
		JSON:        le.JsonLog,
		Raw:         *le,
	}
	if le.Severity != nil {
		e.Severity = string(*le.Severity)
	}
	if le.Resource != nil {
		labels := le.Resource.GetLabels()
		if p := labels["project_name"]; p != nil {
			e.Project = *p
		}
		if p := labels["container_group_name"]; p != nil {
			e.Group = *p
		}
		if p := labels["instance_id"]; p != nil {
			e.InstanceID = *p
		}
		if p := labels["machine_id"]; p != nil {
			e.MachineID = *p
		}
	}
	return e
}

// formatLogLine renders the table-mode line for one entry:
//
//	<time>  <SEVERITY>  [<instance prefix>] <message>                  one group
//	<time>  <SEVERITY>  [<group>/<instance prefix>] <message>          one project
//	<time>  <SEVERITY>  [<project>/<group>/<instance prefix>] <message> organization
//
// so a wider query still says where each line came from.
func formatLogLine(e logsq.Entry, scope string) string {
	inst := "-"
	if e.InstanceID != "" {
		inst = e.InstanceID
		if len(inst) > 8 {
			inst = inst[:8]
		}
	}
	switch scope {
	case scopeProject:
		inst = orDash(e.Group) + "/" + inst
	case scopeOrg:
		inst = orDash(e.Project) + "/" + orDash(e.Group) + "/" + inst
	}
	msg := e.Text
	if msg == "" && e.JSON != nil {
		msg = jsonLogText(e.JSON)
	}
	sev := strings.ToUpper(e.Severity)
	if sev == "" {
		sev = "-"
	}
	return fmt.Sprintf("%s  %-7s  [%s] %s", timeText(e.Time), sev, inst, msg)
}
