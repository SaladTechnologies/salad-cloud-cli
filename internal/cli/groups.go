// The container group verbs live under the container-groups noun:
// saladcloud container-groups list/get/update/start/stop/delete (create and
// scale are in their own files).

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/util"
	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
	"github.com/saladtechnologies/salad-cloud-cli/internal/input"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// groupActionResult is the Emit payload for verbs whose API response has no
// body (start/stop/delete): json mode gets an object, table mode a sentence.
type groupActionResult struct {
	Name   string `json:"name"`
	Action string `json:"action"`
	OK     bool   `json:"ok"`
}

// validateCreateRequest pre-validates the fields the API requires so a bad
// file fails fast with a friendly message instead of an HTTP 400.
func validateCreateRequest(req *containergroups.ContainerGroupCreationRequest) *clierr.Error {
	if field := missingCreateField(req); field != "" {
		return clierr.New(
			clierr.CodeInvalidInput,
			clierr.KindInvalidInput,
			"invalid container group definition: %s is required",
			field,
		)
	}
	return validateGroupName(*req.Name)
}

// missingCreateField is the first field the API requires that req lacks,
// or "" when it has them all.
func missingCreateField(req *containergroups.ContainerGroupCreationRequest) string {
	switch {
	case req.Name == nil || *req.Name == "":
		return "name"
	case req.Container == nil:
		return "container"
	}
	if field := missingCreateContainerField(req.Container); field != "" {
		return field
	}
	switch {
	case req.Replicas == nil:
		return "replicas"
	case req.RestartPolicy == nil:
		return "restart_policy"
	case req.AutostartPolicy == nil:
		return "autostart_policy"
	}
	return ""
}

// missingCreateContainerField is the first field the API requires in a
// container that c lacks, or "" when it has them all.
func missingCreateContainerField(c *containergroups.ContainerConfiguration) string {
	switch {
	case c.Image == nil || *c.Image == "":
		return "container.image"
	case c.Resources == nil:
		return "container.resources"
	case c.Resources.Cpu == nil:
		return "container.resources.cpu"
	case c.Resources.Memory == nil:
		return "container.resources.memory"
	}
	return ""
}

// groupStatus renders a group's current status ("-" when the API omits it).
func groupStatus(g *shared.ContainerGroup) string {
	if g.CurrentState == nil || g.CurrentState.Status == nil {
		return "-"
	}
	return string(*g.CurrentState.Status)
}

// scheduledActionsShown is how many scaling actions the "Scheduled scaling"
// detail row lists before it says how many more there are.
const scheduledActionsShown = 3

// scheduledScalingString renders the "Scheduled scaling" detail-row value,
// or "" when the group has neither the flag nor any actions (no row).
// Enabled with actions lists up to scheduledActionsShown of them; disabled
// with actions still surfaces that they exist — they silently do nothing
// until re-enabled.
func scheduledScalingString(g *shared.ContainerGroup) string {
	enabled := g.ScheduledScalingEnabled != nil && *g.ScheduledScalingEnabled
	n := len(g.ScalingActions)
	noun := "actions"
	if n == 1 {
		noun = "action"
	}
	switch {
	case enabled && n > 0:
		parts := make([]string, 0, scheduledActionsShown+1)
		for i := range g.ScalingActions {
			if i == scheduledActionsShown {
				parts = append(parts, fmt.Sprintf("and %d more", n-scheduledActionsShown))
				break
			}
			a := &g.ScalingActions[i]
			parts = append(
				parts,
				fmt.Sprintf(`%d replicas @ "%s"`, //nolint:gocritic // shows the schedule as typed; %q would escape it
					ptr.Deref(a.Replicas), ptr.Deref(a.Schedule)),
			)
		}
		return fmt.Sprintf("enabled (%d %s: %s)", n, noun, strings.Join(parts, "; "))
	case enabled:
		return "enabled (no actions)"
	case n > 0:
		return fmt.Sprintf("disabled (%d %s configured)", n, noun)
	}
	return ""
}

// groupReason is the platform's description of the group's state (why it
// failed or stopped), or "" when it gives none.
func groupReason(g *shared.ContainerGroup) string {
	if g.CurrentState == nil || g.CurrentState.Description == nil || g.CurrentState.Description.IsNull {
		return ""
	}
	return oneLine(g.CurrentState.Description.Value)
}

// runningCell renders the RUNNING column: running instances out of the
// replicas asked for ("1/2"), "-" for an unknown count.
func runningCell(g *shared.ContainerGroup) string {
	if g.CurrentState == nil || g.CurrentState.InstanceStatusCounts == nil ||
		g.CurrentState.InstanceStatusCounts.RunningCount == nil {
		return fmt.Sprintf("-/%d", ptr.Deref(g.Replicas))
	}
	return fmt.Sprintf("%d/%d", *g.CurrentState.InstanceStatusCounts.RunningCount, ptr.Deref(g.Replicas))
}

// imageCell renders the IMAGE column.
func imageCell(g *shared.ContainerGroup) string {
	if g.Container == nil {
		return "-"
	}
	return orDash(ptr.Deref(g.Container.Image))
}

// priorityString renders the Nullable priority ("-" for unset or null).
func priorityString(p *util.Nullable[shared.ContainerGroupPriority]) string {
	if p == nil || p.IsNull {
		return "-"
	}
	return string(p.Value)
}

// writeGroupDetail writes the two-column key/value table for one group.
func writeGroupDetail(w *output.Writer, g *shared.ContainerGroup) {
	row := func(k, v string) { w.Printf("%s\t%s\n", k, v) }
	row("Name", ptr.Deref(g.Name))
	row("Display name", ptr.Deref(g.DisplayName))
	row("ID", ptr.Deref(g.Id))
	writeGroupContainerRows(row, g)
	row("Priority", priorityString(g.Priority))
	row("Replicas", strconv.FormatInt(ptr.Deref(g.Replicas), 10))
	if s := scheduledScalingString(g); s != "" {
		row("Scheduled scaling", s)
	}
	row("Status", groupStatus(g))
	if reason := groupReason(g); reason != "" {
		row("Reason", reason)
	}
	// A group created without autostart reports "pending" for a few seconds
	// before the backend settles it to "stopped"; note that so the status
	// isn't mistaken for a stuck deploy.
	if groupStatus(g) == string(shared.CONTAINER_GROUP_STATUS_PENDING) && g.AutostartPolicy != nil &&
		!*g.AutostartPolicy {
		row("Note", `settles to "stopped" shortly (created without autostart)`)
	}
	if g.CurrentState != nil && g.CurrentState.InstanceStatusCounts != nil {
		c := g.CurrentState.InstanceStatusCounts
		row("Instances", fmt.Sprintf("allocating %d, creating %d, running %d, stopping %d",
			ptr.Deref(c.AllocatingCount), ptr.Deref(c.CreatingCount),
			ptr.Deref(c.RunningCount), ptr.Deref(c.StoppingCount)))
	}
	if g.Networking != nil && g.Networking.Dns != nil {
		row("DNS", *g.Networking.Dns)
	}
	row("Created", timeCell(g.CreateTime))
}

// writeGroupContainerRows writes writeGroupDetail's rows for the group's
// container (image and resources) through row; none when it has no
// container.
func writeGroupContainerRows(row func(k, v string), g *shared.ContainerGroup) {
	c := g.Container
	if c == nil {
		return
	}
	row("Image", ptr.Deref(c.Image))
	r := c.Resources
	if r == nil {
		return
	}
	row("CPU", strconv.FormatInt(ptr.Deref(r.Cpu), 10))
	row("Memory (MB)", strconv.FormatInt(ptr.Deref(r.Memory), 10))
	if len(r.GpuClasses) > 0 {
		row("GPU classes", strings.Join(r.GpuClasses, ", "))
	}
}

// groupAction runs one bodiless group verb and emits its confirmation.
func (app *App) groupAction(name, pastTense string, call func() *clierr.Error) error {
	if err := call(); err != nil {
		return err
	}
	v := groupActionResult{Name: name, Action: pastTense, OK: true}
	return wrapErr(app.printer.Emit(v, func(w *output.Writer) {
		w.Printf("Container group %s %s\n", v.Name, v.Action)
	}))
}

// startStopRunE builds the shared RunE for start and stop: run the verb,
// then — when --wait is set — hold the stdout confirmation until the group
// reaches the target status, so a timed-out wait exits nonzero with stdout
// still empty (the JSON contract) and the confirmation is never a lie.
func (app *App) startStopRunE(
	pastTense, target string,
	verb func(*api.Client, context.Context, string) *clierr.Error,
	waitOpts *waitOptions,
) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		wait, timeout, cerr := waitOpts.values()
		if cerr != nil {
			return cerr // pre-flight: a bad --timeout must not run the verb
		}
		client, _, cerr := app.NewClient(cmd)
		if cerr != nil {
			return cerr
		}
		err := groupNotFound(verb(client, cmd.Context(), args[0]), args[0], client.Org(), client.Project())
		if err != nil {
			return err
		}
		if wait {
			if _, err := app.runStatusWait(cmd, client, args[0], target, false, timeout); err != nil {
				return err
			}
		}
		v := groupActionResult{Name: args[0], Action: pastTense, OK: true}
		return wrapErr(app.printer.Emit(v, func(w *output.Writer) {
			w.Printf("Container group %s %s\n", v.Name, v.Action)
		}))
	}
}

// validGroupStatuses is the set of --status values list accepts.
func validGroupStatuses() map[string]bool {
	return map[string]bool{
		string(shared.CONTAINER_GROUP_STATUS_PENDING):   true,
		string(shared.CONTAINER_GROUP_STATUS_RUNNING):   true,
		string(shared.CONTAINER_GROUP_STATUS_STOPPED):   true,
		string(shared.CONTAINER_GROUP_STATUS_SUCCEEDED): true,
		string(shared.CONTAINER_GROUP_STATUS_FAILED):    true,
		string(shared.CONTAINER_GROUP_STATUS_DEPLOYING): true,
	}
}

// addGroupsCommands adds groups.go's commands to the tree.
func (app *App) addGroupsCommands(cg *cobra.Command) {
	app.addGroupsListCommand(cg)
	app.addGroupsGetCommand(cg)
	app.addGroupsUpdateCommand(cg)

	var startWait waitOptions
	startCmd := &cobra.Command{
		Use:   "start <group>",
		Short: "Start a container group (--wait blocks until it is running)",
		Args:  cobra.ExactArgs(1),
		RunE: app.startStopRunE(
			"started",
			string(shared.CONTAINER_GROUP_STATUS_RUNNING),
			(*api.Client).StartGroup,
			&startWait,
		),
	}
	addWaitFlags(startCmd, "the group is running", &startWait)
	cg.AddCommand(startCmd)

	var stopWait waitOptions
	stopCmd := &cobra.Command{
		Use:   "stop <group>",
		Short: "Stop a container group (--wait blocks until it is stopped)",
		Args:  cobra.ExactArgs(1),
		RunE: app.startStopRunE(
			"stopped",
			string(shared.CONTAINER_GROUP_STATUS_STOPPED),
			(*api.Client).StopGroup,
			&stopWait,
		),
	}
	addWaitFlags(stopCmd, "the group is stopped", &stopWait)
	cg.AddCommand(stopCmd)

	cg.AddCommand(&cobra.Command{
		Use:   "delete <group>",
		Short: "Delete a container group",
		Long: `Delete a container group, at once and without confirmation.

A deleted group's name is never released: no later group in the project can
take it, so 'delete' then 'create' with the same name fails with
name_conflict. Pick a new name for the replacement.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, cerr := app.NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			return app.groupAction(args[0], "deleted", func() *clierr.Error {
				return groupNotFound(
					client.DeleteGroup(cmd.Context(), args[0]),
					args[0],
					client.Org(),
					client.Project(),
				)
			})
		},
	})
}

// groupsListOptions are container-groups list's flags.
type groupsListOptions struct {
	status   string
	projects []string
	watch    watchOptions
}

// addGroupsListCommand adds container-groups list to the tree.
func (app *App) addGroupsListCommand(cg *cobra.Command) {
	var listOpts groupsListOptions
	listCmd := &cobra.Command{
		Use:   verbNameList,
		Short: "List container groups (current project, or --projects for several)",
		Long: `List the container groups in the current project.

--status filters client-side on the group status. --projects fans the
listing out across several named projects in the organization (repeat the
flag for each: --projects a --projects b) and adds a PROJECT column. Name
the projects you want listed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return app.runGroupsList(cmd, &listOpts)
		},
	}
	listCmd.Flags().
		StringVar(&listOpts.status, "status", "",
			"filter by status: pending|running|stopped|succeeded|failed|deploying")
	listCmd.Flags().
		StringArrayVar(&listOpts.projects, "projects", nil,
			"list this project instead of the current one, repeatable (--projects a --projects b); "+
				"adds a PROJECT column")
	app.addWatchFlags(listCmd, &listOpts.watch)
	cg.AddCommand(listCmd)
}

// runGroupsList is container-groups list's RunE: check --status, then list
// (once, or on every --watch tick), filter and emit.
func (app *App) runGroupsList(cmd *cobra.Command, opts *groupsListOptions) error {
	statusFilter, projects := opts.status, opts.projects
	if statusFilter != "" && !validGroupStatuses()[statusFilter] {
		return clierr.New(
			clierr.CodeUsage,
			clierr.KindUsage,
			`invalid --status "%s"`,
			statusFilter,
		).WithHint("use pending, running, stopped, succeeded, failed or deploying")
	}
	client, res, cerr := app.NewClient(cmd)
	if cerr != nil {
		return cerr
	}
	return app.runMaybeWatch(cmd, opts.watch, func(ctx context.Context) *clierr.Error {
		var items []shared.ContainerGroup
		var lerr *clierr.Error
		if len(projects) > 0 {
			items, lerr = app.listGroupsInProjects(ctx, &res, projects)
		} else {
			items, lerr = listGroupsInCurrentProject(ctx, client)
		}
		if lerr != nil {
			return lerr
		}
		return app.emitGroupList(filterGroupsByStatus(items, statusFilter), opts, &res)
	})
}

// listGroupsInCurrentProject lists the current project's container groups.
func listGroupsInCurrentProject(ctx context.Context, client *api.Client) ([]shared.ContainerGroup, *clierr.Error) {
	groups, lerr := client.ListGroups(ctx)
	if lerr != nil {
		// A 404 on the whole collection means the project (or
		// org) itself wasn't found, not any container group.
		// (A missing organization is reported as one before this.)
		return nil, describeNotFound(lerr, "",
			`project "%s" not found in organization %s`, client.Project(), client.Org())
	}
	return groups.Items, nil
}

// listGroupsInProjects lists the container groups of each --projects
// project in turn; a project that is not found is skipped with a warning.
// Each project's client checks the organization after a 404, as the
// command's own does, so an organization the key cannot see fails the
// command as the organization instead of skipping every project.
func (app *App) listGroupsInProjects(
	ctx context.Context,
	res *config.Resolved,
	projects []string,
) ([]shared.ContainerGroup, *clierr.Error) {
	var items []shared.ContainerGroup
	for _, proj := range projects {
		r := *res
		r.Project, r.ProjectSource = proj, flagProvenance
		pc, cerr := api.New(&r, app.apiOptions())
		if cerr != nil {
			return nil, cerr
		}
		app.hookClient(ctx, pc, &r)
		groups, lerr := pc.ListGroups(ctx)
		if lerr != nil {
			// A final not-found is the organization check's answer.
			if lerr.Code == clierr.CodeNotFound && !lerr.IsFinal() {
				fprintf(app.errw(), "warning: project \"%s\" not found in organization %s, skipped\n",
					proj, res.Organization)
				continue
			}
			return nil, lerr
		}
		items = append(items, groups.Items...)
	}
	return items, nil
}

// filterGroupsByStatus keeps the groups whose status is status, in place;
// an empty status keeps them all.
func filterGroupsByStatus(items []shared.ContainerGroup, status string) []shared.ContainerGroup {
	if status == "" {
		return items
	}
	kept := items[:0]
	for i := range items {
		if groupStatus(&items[i]) == status {
			kept = append(kept, items[i])
		}
	}
	return kept
}

// emitGroupList emits list's {"items": [...]} document, or its table, with
// a PROJECT column under --projects.
func (app *App) emitGroupList(
	items []shared.ContainerGroup,
	opts *groupsListOptions,
	res *config.Resolved,
) *clierr.Error {
	payload := struct {
		Items []shared.ContainerGroup `json:"items"`
	}{items}
	if err := app.printer.Emit(payload, func(w *output.Writer) { writeGroupList(w, items, opts) }); err != nil {
		return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "%v", err)
	}
	// A bare header is ambiguous for humans; disambiguate an
	// empty listing on stderr so the stdout contract (and json
	// mode) stays unchanged.
	if len(items) == 0 && !app.printer.Machine() {
		app.noteEmptyGroupList(opts, res)
	}
	return nil
}

// writeGroupList writes list's table, with a PROJECT column under
// --projects.
func writeGroupList(w *output.Writer, items []shared.ContainerGroup, opts *groupsListOptions) {
	multi := len(opts.projects) > 0
	if multi {
		w.Print("PROJECT\t")
	}
	w.Println("NAME\tDISPLAY NAME\tPRIORITY\tREPLICAS\tRUNNING\tSTATUS\tPENDING\tIMAGE\tCREATED")
	for i := range items {
		g := &items[i]
		if multi {
			w.Printf("%s\t", ptr.Deref(g.ProjectName))
		}
		w.Printf("%s\t%s\t%s\t%d\t%s\t%s\t%t\t%s\t%s\n",
			ptr.Deref(g.Name), ptr.Deref(g.DisplayName), priorityString(g.Priority),
			ptr.Deref(g.Replicas), runningCell(g), groupStatus(g), ptr.Deref(g.PendingChange),
			imageCell(g), timeCell(g.CreateTime))
	}
}

// noteEmptyGroupList says on stderr where list found no container groups.
func (app *App) noteEmptyGroupList(opts *groupsListOptions, res *config.Resolved) {
	switch statusFilter := opts.status; {
	case len(opts.projects) > 0:
		fprintln(app.errw(), "no container groups in the listed projects")
	case statusFilter != "":
		fprintf(
			app.errw(),
			"no container groups in %s/%s matching --status %s\n",
			res.Organization,
			res.Project,
			statusFilter,
		)
	default:
		fprintf(app.errw(), "no container groups in %s/%s\n", res.Organization, res.Project)
	}
}

// groupsGetOptions are container-groups get's flags.
type groupsGetOptions struct {
	writable bool
	watch    watchOptions
}

// addGroupsGetCommand adds container-groups get to the tree.
func (app *App) addGroupsGetCommand(cg *cobra.Command) {
	var getOpts groupsGetOptions
	getCmd := &cobra.Command{
		Use:   "get <group>",
		Short: "Show one container group (--writable emits a patch document for update -f)",
		Long: `Show one container group.

--writable emits the group as a JSON document that update -f accepts:
response-only fields (id, version, current_state, ...) and create-only
fields are removed, and the one request/response relocation is applied
(responses return priority at the top level; requests take
container.priority). The output is always JSON regardless of -o, so this
round-trips (bash or zsh, then PowerShell):

  saladcloud container-groups get my-app --writable > my-app.json
  saladcloud container-groups get my-app --writable | Set-Content -Encoding ascii my-app.json
  # edit my-app.json
  saladcloud container-groups update my-app -f my-app.json

Note that the document includes replicas, and writing replicas puts
scheduled scaling on hold (a documented platform rule); remove it from the
document unless you mean to change it. The CLI prints a note when the
group has scheduled scaling enabled.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.runGroupsGet(cmd, &getOpts, args[0])
		},
	}
	getCmd.Flags().
		BoolVar(&getOpts.writable, "writable", false,
			"emit a JSON patch document for update -f (response-only and create-only fields removed)")
	app.addWatchFlags(getCmd, &getOpts.watch)
	cg.AddCommand(getCmd)
}

// runGroupsGet is container-groups get's RunE: the patch document under
// --writable, else the group (once, or on every --watch tick).
func (app *App) runGroupsGet(cmd *cobra.Command, opts *groupsGetOptions, name string) error {
	writable, watch := opts.writable, opts.watch.watch
	if watch && writable {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "--writable and --watch cannot be combined")
	}
	client, _, cerr := app.NewClient(cmd)
	if cerr != nil {
		return cerr
	}
	if writable {
		return app.emitWritableGroup(cmd, client, name)
	}
	return app.runMaybeWatch(cmd, opts.watch, func(ctx context.Context) *clierr.Error {
		group, cerr := client.GetGroup(ctx, name)
		if cerr != nil {
			return groupNotFound(cerr, name, client.Org(), client.Project())
		}
		if err := app.printer.Emit(group, func(w *output.Writer) { writeGroupDetail(w, group) }); err != nil {
			return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "%v", err)
		}
		return nil
	})
}

// emitWritableGroup is get --writable: the group as a patch document for
// update -f, always JSON, with notes on stderr about what it leaves out and
// about replicas.
func (app *App) emitWritableGroup(cmd *cobra.Command, client *api.Client, name string) error {
	group, cerr := client.GetGroup(cmd.Context(), name)
	if cerr != nil {
		return groupNotFound(cerr, name, client.Org(), client.Project())
	}
	doc, dropped, err := writableDocument(group)
	if err != nil {
		return clierr.New(clierr.CodeGeneric, clierr.KindInternal, "cannot render group: %v", err)
	}
	app.noteWritableReplicas(doc)
	if len(dropped) > 0 {
		fprintf(
			app.errw(),
			"note: %d field(s) update cannot write were omitted: %s\n",
			len(dropped),
			joinPaths(dropped),
		)
	}
	// Always JSON: a table rendering of a patch document has no use.
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return clierr.New(clierr.CodeGeneric, clierr.KindInternal, "%v", err)
	}
	_, err = fmt.Fprintln(app.printer.Out(), string(output.ASCII(data)))
	return wrapErr(err)
}

// noteWritableReplicas notes on stderr that a writable document of a group
// with scheduled scaling enabled carries replicas, which puts the schedule
// on hold when written.
func (app *App) noteWritableReplicas(doc any) {
	m, ok := doc.(map[string]any)
	if !ok || !asType[bool](m["scheduled-scaling-enabled"]) {
		return
	}
	if _, has := m["replicas"]; has {
		fprintln(
			app.errw(),
			"note: replicas is included; writing it puts scheduled scaling on hold, "+
				"so drop it unless you mean to change it",
		)
	}
}

// groupsUpdateOptions are container-groups update's flags.
type groupsUpdateOptions struct {
	file         string
	allowUnknown bool
	flags        updateFlags
	settle       settleOptions
}

// addGroupsUpdateCommand adds container-groups update to the tree.
func (app *App) addGroupsUpdateCommand(cg *cobra.Command) {
	var updateOpts groupsUpdateOptions
	updateCmd := &cobra.Command{
		Use:   "update <group> [-f <file>] [flags] [-- <command>...]",
		Short: "Update a container group from flags and/or a JSON or YAML patch file (waits for the change to settle)",
		Long: `Update a container group from flags, a JSON or YAML merge patch file (-f),
or both; flags win over the file. The change settles before the command
returns (--no-wait returns at once), and the result is checked against what
you asked for. A container change the platform rejects (an image it cannot
pull, say) exits 6 with the platform's reason, and the group keeps what it
ran before. Registry credentials are never returned by the API, so a new
private image needs them again in the -f file
(container.registry_authentication).

The flags are the create flags that can change on an existing group:
--image, --cpu, --memory, --storage, --gpu (UUID or name; replaces the GPU
classes), --country (replaces the countries), --replicas, --priority,
--port, --display-name, and -e/--env. The API
replaces the whole set of environment variables on every write, so -e reads
the group's current variables and merges into them: other variables are
kept. A patch file's environment_variables, without -e, is sent as written
and replaces the set. The name, restart policy, autostart, gateway auth
and queue connection cannot change after create.

Words after -- replace the container command, which runs in place of the
image's ENTRYPOINT and CMD, so it starts with the program; unlike cg ssh,
there is no shell. Put saladcloud's own flags before --. To go back to the
image's own command, send a patch file with {"container": {"command": []}}.`,
		Example: `  # Roll out a new image
  saladcloud container-groups update my-app --image ghcr.io/me/app:v2

  # Add or change one variable, keeping the others
  saladcloud container-groups update my-app -e LOG_LEVEL=debug

  # A patch file, with a flag on top
  saladcloud container-groups update my-app -f patch.yaml --replicas 4

  # Replace the container command
  saladcloud container-groups update my-app -- python app.py --port 8080`,
		Args: func(cmd *cobra.Command, args []string) error {
			// The one positional is <group>; the words after -- are the
			// container command and are not counted.
			if pos, _ := splitCommand(cmd, args); len(pos) != 1 {
				return fmt.Errorf("%w 1 arg(s) before --, received %d", errAccepts, len(pos))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.runGroupsUpdate(cmd, &updateOpts, args)
		},
	}
	updateCmd.Flags().
		StringVarP(&updateOpts.file, "file", "f", "", "container group patch file (.json/.yaml; \"-\" reads stdin)")
	addUpdateFlags(updateCmd, &updateOpts.flags)
	addAllowUnknownFlag(updateCmd, &updateOpts.allowUnknown)
	addSettleFlags(updateCmd, &updateOpts.settle)
	cg.AddCommand(updateCmd)
}

// runGroupsUpdate is container-groups update's RunE: check the command line
// pre-flight, load the patch file, lay the flags over it, and apply it.
func (app *App) runGroupsUpdate(cmd *cobra.Command, opts *groupsUpdateOptions, args []string) error {
	args, command := splitCommand(cmd, args)
	if cerr := app.validateCommand(command); cerr != nil {
		return cerr
	}
	warnCommand(app.errw(), cmd, command.words)
	wait, timeout, cerr := opts.settle.values()
	if cerr != nil {
		return cerr // pre-flight: a bad --timeout must not patch anything
	}
	flags, cerr := updateCheckFlags(cmd, opts, command.words)
	if cerr != nil {
		return cerr
	}
	raw, cerr := app.updateLoadPatchFile(opts, args[0])
	if cerr != nil {
		return cerr
	}
	client, _, cerr := app.NewClient(cmd)
	if cerr != nil {
		return cerr
	}
	var intent json.RawMessage // what was asked for, when it differs from the wire patch
	if flags.any() {
		flags.ResolveGpu = lazyGpuResolver(cmd, client)
		env, eerr := updateCurrentEnv(cmd, client, args[0], &flags)
		if eerr != nil {
			return eerr
		}
		patch, perr := buildUpdatePatch(raw, &flags, env)
		if perr != nil {
			return perr
		}
		raw, intent = patch.wire, patch.intent
	}
	settle := patchSettle{wait: wait, timeout: timeout, doneMsg: "update settled"}
	group, err := app.applyPatch(cmd, client, args[0], raw, intent, settle)
	if err != nil {
		return err
	}
	return wrapErr(app.printer.Emit(group, func(w *output.Writer) { writeGroupDetail(w, group) }))
}

// updateCheckFlags is update's flags, checked pre-flight: their values, and
// that there is something to update at all.
func updateCheckFlags(cmd *cobra.Command, opts *groupsUpdateOptions, command []string) (updateFlags, *clierr.Error) {
	flags := collectUpdateFlags(cmd, &opts.flags)
	flags.Command = command
	if cerr := validateUpdateFlags(&flags); cerr != nil {
		return flags, cerr
	}
	if opts.file == "" && !flags.any() {
		return flags, clierr.New(clierr.CodeUsage, clierr.KindUsage,
			"nothing to update").
			WithHint("pass -f <file>, a flag such as --image, or both; " +
				"to see the flags, run saladcloud container-groups update --help")
	}
	return flags, nil
}

// updateLoadPatchFile loads update's -f patch file; nil without -f (flags
// only: the patch is built from them later).
//
// The typed struct is validation only (strict parse, friendly errors); the
// RAW document is what goes on the wire. Re-marshaling the struct would drop
// explicit empty values ([], "", 0) via omitempty — and in a merge patch
// those mean "clear this field". Because the wire document is the user's own
// bytes, the --allow-unknown-fields path needs no grafting here: tolerated
// fields ride through untouched.
func (app *App) updateLoadPatchFile(opts *groupsUpdateOptions, group string) (json.RawMessage, *clierr.Error) {
	if opts.file == "" {
		return nil, nil
	}
	var patch containergroups.ContainerGroupPatch
	raw, unknowns, cerr := input.LoadLenient(opts.file, app.Stdin, &patch)
	if cerr != nil {
		return nil, cerr
	}
	if opts.allowUnknown {
		warnUnknowns(app.errw(), unknowns)
		return raw, nil
	}
	// Strict: load leniently, then reject with per-field advice so a
	// create-only field (networking.auth) or a response-only one (piped back
	// from `get -o json`) gets an explanation rather than a suggestion to
	// force it through and have the API discard it.
	if rerr := namingGroup(app.rejectUnknowns(unknowns), group); rerr != nil {
		return nil, rerr
	}
	return raw, nil
}

// updateCurrentEnv is the group's present environment variables under -e,
// which the patch must carry merged whole; nil without -e.
func updateCurrentEnv(
	cmd *cobra.Command,
	client *api.Client,
	group string,
	flags *updateFlags,
) (map[string]*string, *clierr.Error) {
	if len(flags.Env) == 0 {
		return nil, nil
	}
	current, cerr := client.GetGroup(cmd.Context(), group)
	if cerr != nil {
		return nil, groupNotFound(cerr, group, client.Org(), client.Project())
	}
	return envOf(current), nil
}

// patchSettle is how applyPatch settles a patch: whether to wait (unless
// --no-wait), for how long, and the table-mode line when it has settled (""
// for none).
type patchSettle struct {
	wait    bool
	timeout time.Duration
	doneMsg string
}

// applyPatch is the one write path for merge patches: update -f and scale
// both go through it, so every patch gets the same before-GET, the same
// settle-by-default, and the same two intent-diff checks. Having scale on a
// separate typed path once meant the verb most likely to put scheduled
// scaling on hold was the only write that never said so.
//
// The sequence: fetch the pre-state (best effort — a failed GET must not
// block the write), send the raw document, then settle. A patch that stages
// (an image change, say) returns pending_change true, and while it is
// pending the response is the *previous* config, so neither the returned
// document nor the diffs would describe what was written; --no-wait skips
// the settle and says so on stderr. Both diffs no-op on a pending document.
// Their findings are warnings, except a container change that did not land
// (findContainerRejection), which is an error: exit 6, stdout empty.
// intent, when non-nil, is what the caller asked for and is what the diffs
// compare against; it differs from raw only when the wire patch must express
// a request indirectly (update -e removing a variable by omission).
func (app *App) applyPatch(
	cmd *cobra.Command,
	client *api.Client,
	name string,
	raw, intent []byte,
	settle patchSettle,
) (*shared.ContainerGroup, error) {
	if intent == nil {
		intent = raw
	}
	var before *shared.ContainerGroup
	if b, cerr := client.GetGroup(cmd.Context(), name); cerr == nil {
		before = b
	}
	group, cerr := client.UpdateGroupRaw(cmd.Context(), name, raw)
	if cerr != nil {
		return nil, groupNotFound(cerr, name, client.Org(), client.Project())
	}
	if settle.wait {
		final, err := app.runGroupWait(cmd, client, name, settle.doneMsg, settleWaitGoal(), settle.timeout)
		if err != nil {
			return nil, err
		}
		group = final
	} else if ptr.Deref(group.PendingChange) {
		fprintf(
			app.errw(),
			"note: change accepted, still pending, not verified (--no-wait); "+
				"to check it, run saladcloud container-groups get %s\n",
			name,
		)
	}
	// "Did what I asked for happen?" and "did anything else happen?".
	ignored := findIgnoredFields(intent, before, group)
	warnIgnoredFields(app.errw(), ignored)
	warnUnrequestedChanges(app.errw(), findUnrequestedChanges(intent, before, group))
	// A container change that did not land fails the write: the group still
	// runs what it ran before, which must not read as success.
	if r, rejected := findContainerRejection(before, group, ignored); rejected {
		return nil, containerRejectionError(name, intent, r)
	}
	return group, nil
}
