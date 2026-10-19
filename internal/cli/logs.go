package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/logs"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"
	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
	"github.com/saladtechnologies/salad-cloud-cli/internal/logsq"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

func (app *App) newLogsCmd(o *logsOptions) *cobra.Command {
	return &cobra.Command{
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
		RunE: func(cmd *cobra.Command, args []string) error { return app.runLogs(cmd, args, o) },
	}
}

// containerGroupLogsCmd is 'saladcloud container-groups logs <group>': the
// query with the group as its subject, found from 'container-groups --help'.
// Both logs commands share addLogsFlags.
func (app *App) newContainerGroupLogsCmd(o *logsOptions) *cobra.Command {
	return &cobra.Command{
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
		RunE: func(cmd *cobra.Command, args []string) error { return app.runLogs(cmd, args, o) },
	}
}

// logsOptions are the flags both logs commands share.
type logsOptions struct {
	since, until, instance  string
	severity, filter, query string
	follow                  bool
	pollInterval, lookback  time.Duration
	pageSize                int
}

// addLogsFlags binds the logs flags on c to o; both logs commands use it.
func addLogsFlags(c *cobra.Command, o *logsOptions) {
	c.Flags().
		StringVar(&o.since, "since", "15m", "start of the time range: duration (15m, 2h, 3d), RFC3339 timestamp, or date")
	c.Flags().
		StringVar(&o.until, "until", "", "end of the time range (same forms as --since; default now; conflicts with --follow)")
	c.Flags().
		StringVar(&o.instance, "instance", "", "only logs from this instance (full ID; a unique prefix also works when a container group is given)")
	c.Flags().StringVar(&o.severity, "severity", "", "minimum severity: "+strings.Join(logsq.Severities(), "|"))
	c.Flags().StringVar(&o.filter, "filter", "", "only logs containing this substring (text or JSON)")
	c.Flags().StringVar(&o.query, "query", "", "raw log query-language expression, ANDed onto the generated query")
	c.Flags().BoolVarP(&o.follow, "follow", "f", false, "stream new log entries until interrupted")
	c.Flags().
		DurationVar(&o.pollInterval, "poll-interval", logsq.DefaultPoll, "how often to poll for new entries with --follow (min 2s)")
	c.Flags().
		DurationVar(&o.lookback, "lookback", logsq.DefaultLookback, "with --follow, how far behind the newest seen log time each poll re-scans for late-arriving entries")
	c.Flags().IntVar(&o.pageSize, "page-size", 100, "entries per API request (1-100)")
}

// addLogsCommands adds logs.go's commands to the tree.
func (app *App) addLogsCommands(root, cg *cobra.Command) {
	var logsOpts, groupLogsOpts logsOptions
	logsCmd := app.newLogsCmd(&logsOpts)
	containerGroupLogsCmd := app.newContainerGroupLogsCmd(&groupLogsOpts)
	addLogsFlags(logsCmd, &logsOpts)
	logsCmd.Flags().
		StringVarP(&app.flags.group, "container-group", "c", "", "only this container group's logs (in the current project)")
	logsCmd.Flags().
		StringVar(&app.flags.project, "project", "", "only this project's logs; unlike other commands, the saved project and SALAD_PROJECT do not narrow logs")
	annotate(logsCmd, annotationFlagForArg, "container-group")
	addLogsFlags(containerGroupLogsCmd, &groupLogsOpts)
	app.addHiddenGroupFlag(containerGroupLogsCmd)
	annotate(containerGroupLogsCmd, annotationFlagForArg, "instance")
	root.AddCommand(logsCmd)
	cg.AddCommand(containerGroupLogsCmd)
}

func (app *App) runLogs(cmd *cobra.Command, args []string, o *logsOptions) error {
	group := logsGroup(cmd, args, app.flags.group)
	// Pre-flight validation: everything below must fail with a usage error
	// before any configuration or client work, so bad input exits 2 even
	// when no API key is set (and never reaches the API).
	if cerr := logsValidateFlags(cmd, o, group); cerr != nil {
		return cerr
	}
	window, cerr := logsValidateWindow(cmd, o)
	if cerr != nil {
		return cerr
	}
	baseFilters := logsq.Filters{
		Group:       group,
		Instance:    o.instance,
		MinSeverity: o.severity,
		Contains:    o.filter,
		RawQuery:    o.query,
		Org:         "",
		Project:     "",
	}
	// Validate the filters now; the scope is filled in once the client has
	// resolved the organization, so a placeholder stands in for it here.
	preflight := baseFilters
	preflight.Org = "-"
	if _, cerr = logsq.Compile(preflight); cerr != nil {
		return cerr
	}

	client, res, cerr := app.NewClient(cmd)
	if cerr != nil {
		return cerr
	}
	scope, cerr := logsScope(cmd, client, &res, &baseFilters)
	if cerr != nil {
		return cerr
	}
	if app.printer.Mode() == "table" {
		fprintln(app.errw(), scopeLine(scope, client.Org(), res.Project))
	}
	if baseFilters.Instance, cerr = logsInstanceID(cmd.Context(), client, group, o.instance); cerr != nil {
		return cerr
	}

	ctx := cmd.Context() // canceled by the one signal handler in Execute
	if !o.follow {
		cerr = app.logsOnce(ctx, o, client, scope, &baseFilters, window)
	} else {
		cerr = app.logsFollow(ctx, o, client, scope, &baseFilters, window.since)
	}
	// Returned directly, a nil *clierr.Error would be a non-nil error.
	if cerr != nil {
		return cerr
	}
	return nil
}

// logsGroup is the container group a logs command is about: the subject of 'container-groups logs <group>', or the
// filter (-c, given as flagGroup) on the top-level query.
func logsGroup(cmd *cobra.Command, args []string, flagGroup string) string {
	if len(args) == 1 {
		return args[0]
	}
	if cmd.Annotations[annotationGroupIsArg] == "" { // logs -c; container-groups logs only hides its -c
		return flagGroup
	}
	return ""
}

// logsValidateFlags checks the logs flags against each other, in the order the checks are reported; the time range
// is checked next, by logsValidateWindow.
func logsValidateFlags(cmd *cobra.Command, o *logsOptions, group string) *clierr.Error {
	if cerr := logsValidateFollowFlags(cmd, o); cerr != nil {
		return cerr
	}
	if o.pageSize < 1 || o.pageSize > 100 {
		return clierr.New(
			clierr.CodeUsage,
			clierr.KindUsage,
			"--page-size must be between 1 and 100 (got %d)",
			o.pageSize,
		)
	}
	if o.instance != "" && !isUUID(o.instance) && group == "" {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage,
			"--instance with an ID prefix needs a container group to resolve it against").
			WithHint("pass -c <group>, or the full instance UUID")
	}
	return nil
}

// logsValidateFollowFlags checks --until, --poll-interval and --lookback against --follow.
func logsValidateFollowFlags(cmd *cobra.Command, o *logsOptions) *clierr.Error {
	if o.follow && cmd.Flags().Changed("until") {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "--until cannot be combined with --follow")
	}
	if o.pollInterval < logsq.MinPoll {
		return clierr.New(
			clierr.CodeUsage,
			clierr.KindUsage,
			"--poll-interval must be at least %s (got %s)",
			logsq.MinPoll,
			o.pollInterval,
		)
	}
	if cmd.Flags().Changed("lookback") && !o.follow {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "--lookback only applies with --follow")
	}
	if o.lookback <= 0 {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "--lookback must be positive (got %s)", o.lookback)
	}
	return nil
}

// logsWindow is the time range a logs query covers; --follow reads only since.
type logsWindow struct {
	since, until time.Time
}

// logsValidateWindow parses --since and --until (now when not given) and, for a one-shot query, checks that the
// range is not empty.
func logsValidateWindow(cmd *cobra.Command, o *logsOptions) (logsWindow, *clierr.Error) {
	now := time.Now().UTC()
	since, cerr := logsq.ParseTimeRef(o.since, now)
	if cerr != nil {
		return logsWindow{}, cerr
	}
	until := now
	if cmd.Flags().Changed("until") {
		if until, cerr = logsq.ParseTimeRef(o.until, now); cerr != nil {
			return logsWindow{}, cerr
		}
	}
	if !o.follow && !until.After(since) {
		return logsWindow{}, clierr.New(clierr.CodeUsage, clierr.KindUsage, "--until (%s) must be after --since (%s)",
			until.Format(time.RFC3339), since.Format(time.RFC3339))
	}
	return logsWindow{since: since, until: until}, nil
}

// logsScope checks that an organization is set, works out what a logs query covers, and fills its organization or
// project into filters: a group is looked up in the resolved project; without a group, only an explicit --project
// narrows the organization.
func logsScope(
	cmd *cobra.Command,
	client *api.Client,
	res *config.Resolved,
	filters *logsq.Filters,
) (logScope, *clierr.Error) {
	if cerr := client.RequireOrg(); cerr != nil {
		return logScope{}, cerr
	}
	group := filters.Group
	// A group's logs are filtered by its project, which the log query never
	// checks: on the assumed "default" project, confirm it exists first, so
	// the command warns and a missing one is no valid project set (exit 2)
	// rather than an empty result.
	if group != "" && res.ProjectSource == config.SourceDefault {
		if cerr := client.CheckProject(cmd.Context()); cerr != nil {
			return logScope{}, cerr
		}
	}
	switch {
	case group != "":
		filters.Project = res.Project
		return logScope{kind: scopeGroup, name: group}, nil
	case cmd.Flags().Changed("project"):
		filters.Project = res.Project
		return logScope{kind: scopeProject, name: res.Project}, nil
	default:
		filters.Org = client.Org()
		return logScope{kind: scopeOrg, name: client.Org()}, nil
	}
}

// logsInstanceID is the exact instance ID to query for --instance. It accepts an ID prefix (like ssh/cp); the log
// query language needs the exact instance_id, so a non-UUID value is resolved against the group's live instances
// first. Full UUIDs, and no --instance at all, skip the extra call.
func logsInstanceID(ctx context.Context, client *api.Client, group, instance string) (string, *clierr.Error) {
	if instance == "" || isUUID(instance) {
		return instance, nil
	}
	collection, lerr := client.ListInstances(ctx, group)
	if lerr != nil {
		return "", groupNotFound(lerr, group, client.Org(), client.Project())
	}
	inst, rerr := resolveSSHInstance(collection.Instances, group, instance)
	if rerr != nil {
		return "", rerr
	}
	return ptr.Deref(inst.Id), nil
}

// logsFetcher is the logsq.Fetcher that pages through client's log entries, oldest first.
func logsFetcher(client *api.Client) logsq.Fetcher {
	return func(
		ctx context.Context,
		query, startRFC3339, endRFC3339 string,
		pageSize int,
	) (*logsq.Page, *clierr.Error) {
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
			Items:       nil,
		}
		for i := range coll.Items {
			page.Items = append(page.Items, toLogsqEntry(&coll.Items[i]))
		}
		return page, nil
	}
}

// logsEmitter prints one log entry: its full API entry as NDJSON, or its table line for a query of scope.
func (app *App) logsEmitter(scope logScope) func(logsq.Entry) error {
	return func(e logsq.Entry) error {
		return app.printer.Stream(e.Raw, formatLogLine(e, scope.kind))
	}
}

// logsOnce runs a one-shot logs query over window and prints each entry.
func (app *App) logsOnce(
	ctx context.Context,
	o *logsOptions,
	client *api.Client,
	scope logScope,
	filters *logsq.Filters,
	window logsWindow,
) *clierr.Error {
	query, cerr := logsq.Compile(*filters)
	if cerr != nil {
		return cerr
	}
	emit := app.logsEmitter(scope)
	emitted := 0
	countingEmit := func(e logsq.Entry) error {
		emitted++
		return emit(e)
	}
	cerr = logsq.FetchWindow(
		ctx,
		logsFetcher(client),
		query,
		window.since,
		window.until,
		o.pageSize,
		countingEmit,
		app.stderrLine,
	)
	if cerr != nil {
		return cerr
	}
	// An empty one-shot result is easy to misread as a hang or a bug;
	// say so on stderr in table mode. JSON/NDJSON stays machine-clean,
	// and --follow's silence correctly means "waiting".
	if emitted == 0 && app.printer.Mode() == "table" {
		fprintln(app.errw(), noMatchNote(scope, o.since, o.instance, o.severity, o.filter))
	}
	return nil
}

// logsFollow prints new log entries from since on until ctx ends.
func (app *App) logsFollow(
	ctx context.Context,
	o *logsOptions,
	client *api.Client,
	scope logScope,
	filters *logsq.Filters,
	since time.Time,
) *clierr.Error {
	if filters.Group != "" {
		if note := followGroupNote(ctx, client, filters.Group); note != "" {
			fprintln(app.errw(), note)
		}
	}
	if app.printer.Mode() == "table" {
		fprintln(app.errw(), "waiting for log entries (Ctrl-C to stop)")
	}
	query, cerr := logsq.Compile(*filters)
	if cerr != nil {
		return cerr
	}
	follower := &logsq.Follower{
		Poll:       o.pollInterval,
		MaxBackoff: logsq.DefaultMaxBackoff,
		Lookback:   o.lookback,
		Clock:      logsq.RealClock{},
		Warn:       app.stderrLine,
	}
	return follower.Run(ctx, query, logsFetcher(client), since, o.pageSize, app.logsEmitter(scope))
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
		Severity:    "",
		Project:     "",
		Group:       "",
		InstanceID:  "",
		MachineID:   "",
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
