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
	missing := func(field string) *clierr.Error {
		return clierr.New(
			clierr.CodeInvalidInput,
			clierr.KindInvalidInput,
			"invalid container group definition: %s is required",
			field,
		)
	}
	switch {
	case req.Name == nil || *req.Name == "":
		return missing("name")
	case req.Container == nil:
		return missing("container")
	case req.Container.Image == nil || *req.Container.Image == "":
		return missing("container.image")
	case req.Container.Resources == nil:
		return missing("container.resources")
	case req.Container.Resources.Cpu == nil:
		return missing("container.resources.cpu")
	case req.Container.Resources.Memory == nil:
		return missing("container.resources.memory")
	case req.Replicas == nil:
		return missing("replicas")
	case req.RestartPolicy == nil:
		return missing("restart_policy")
	case req.AutostartPolicy == nil:
		return missing("autostart_policy")
	}
	return validateGroupName(*req.Name)
}

// groupStatus renders a group's current status ("-" when the API omits it).
func groupStatus(g *shared.ContainerGroup) string {
	if g.CurrentState == nil || g.CurrentState.Status == nil {
		return "-"
	}
	return string(*g.CurrentState.Status)
}

// scheduledScalingString renders the "Scheduled scaling" detail-row value,
// or "" when the group has neither the flag nor any actions (no row).
// Enabled with actions lists up to 3 of them; disabled with actions still
// surfaces that they exist — they silently do nothing until re-enabled.
func scheduledScalingString(g *shared.ContainerGroup) string {
	enabled := g.ScheduledScalingEnabled != nil && *g.ScheduledScalingEnabled
	n := len(g.ScalingActions)
	noun := "actions"
	if n == 1 {
		noun = "action"
	}
	switch {
	case enabled && n > 0:
		parts := make([]string, 0, 4)
		for i := range g.ScalingActions {
			if i == 3 {
				parts = append(parts, fmt.Sprintf("and %d more", n-3))
				break
			}
			a := &g.ScalingActions[i]
			parts = append(parts, fmt.Sprintf(`%d replicas @ "%s"`, ptr.Deref(a.Replicas), ptr.Deref(a.Schedule)))
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
	if c := g.Container; c != nil {
		row("Image", ptr.Deref(c.Image))
		if r := c.Resources; r != nil {
			row("CPU", strconv.FormatInt(ptr.Deref(r.Cpu), 10))
			row("Memory (MB)", strconv.FormatInt(ptr.Deref(r.Memory), 10))
			if len(r.GpuClasses) > 0 {
				row("GPU classes", strings.Join(r.GpuClasses, ", "))
			}
		}
	}
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
	if groupStatus(g) == "pending" && g.AutostartPolicy != nil && !*g.AutostartPolicy {
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
) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		wait, timeout, cerr := waitFlagValues(cmd)
		if cerr != nil {
			return cerr // pre-flight: a bad --timeout must not run the verb
		}
		client, _, cerr := app.NewClient(cmd)
		if cerr != nil {
			return cerr
		}
		if err := groupNotFound(
			verb(client, cmd.Context(), args[0]),
			args[0],
			client.Org(),
			client.Project(),
		); err != nil {
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
var validGroupStatuses = map[string]bool{
	"pending": true, "running": true, "stopped": true,
	"succeeded": true, "failed": true, "deploying": true,
}

// addGroupsCommands adds groups.go's commands to the tree.
func (app *App) addGroupsCommands(cg *cobra.Command) {
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List container groups (current project, or --projects for several)",
		Long: `List the container groups in the current project.

--status filters client-side on the group status. --projects fans the
listing out across several named projects in the organization (repeat the
flag for each: --projects a --projects b) and adds a PROJECT column. Name
the projects you want listed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fr := readFlags(cmd.Flags())
			statusFilter := fr.String("status")
			projects := fr.StringArray("projects")
			if cerr := fr.Err(); cerr != nil {
				return cerr
			}
			if statusFilter != "" && !validGroupStatuses[statusFilter] {
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
			multi := len(projects) > 0
			return app.runMaybeWatch(cmd, func(ctx context.Context) *clierr.Error {
				var items []shared.ContainerGroup
				if multi {
					for _, proj := range projects {
						r := res
						r.Project, r.ProjectSource = proj, "flag"
						pc, cerr := api.New(r, app.apiOptions())
						if cerr != nil {
							return cerr
						}
						groups, lerr := pc.ListGroups(ctx)
						if lerr != nil {
							if lerr.Code == clierr.CodeNotFound {
								fprintf(app.errw(), "warning: project \"%s\" not found, skipped\n", proj)
								continue
							}
							return lerr
						}
						items = append(items, groups.Items...)
					}
				} else {
					groups, lerr := client.ListGroups(ctx)
					if lerr != nil {
						// A 404 on the whole collection means the project (or
						// org) itself wasn't found, not any container group.
						// (A missing organization is reported as one before this.)
						return describeNotFound(lerr, "",
							`project "%s" not found in organization %s`, client.Project(), client.Org())
					}
					items = groups.Items
				}
				if statusFilter != "" {
					kept := items[:0]
					for i := range items {
						if groupStatus(&items[i]) == statusFilter {
							kept = append(kept, items[i])
						}
					}
					items = kept
				}
				payload := struct {
					Items []shared.ContainerGroup `json:"items"`
				}{items}
				if err := app.printer.Emit(payload, func(w *output.Writer) {
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
				}); err != nil {
					return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "%v", err)
				}
				// A bare header is ambiguous for humans; disambiguate an
				// empty listing on stderr so the stdout contract (and json
				// mode) stays unchanged.
				if len(items) == 0 && app.printer.Mode() == "table" {
					switch {
					case multi:
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
				return nil
			})
		},
	}
	listCmd.Flags().String("status", "", "filter by status: pending|running|stopped|succeeded|failed|deploying")
	listCmd.Flags().
		StringArray("projects", nil, "list this project instead of the current one, repeatable (--projects a --projects b); adds a PROJECT column")
	app.addWatchFlags(listCmd)
	cg.AddCommand(listCmd)

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
			fr := readFlags(cmd.Flags())
			writable := fr.Bool("writable")
			watch := fr.Bool("watch")
			if cerr := fr.Err(); cerr != nil {
				return cerr
			}
			if watch && writable {
				return clierr.New(clierr.CodeUsage, clierr.KindUsage, "--writable and --watch cannot be combined")
			}
			client, _, cerr := app.NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			if writable {
				group, cerr := client.GetGroup(cmd.Context(), args[0])
				if cerr != nil {
					return groupNotFound(cerr, args[0], client.Org(), client.Project())
				}
				doc, dropped, err := writableDocument(group)
				if err != nil {
					return clierr.New(clierr.CodeGeneric, clierr.KindInternal, "cannot render group: %v", err)
				}
				if m, ok := doc.(map[string]any); ok {
					if asType[bool](m["scheduled-scaling-enabled"]) {
						if _, has := m["replicas"]; has {
							fprintln(
								app.errw(),
								"note: replicas is included; writing it puts scheduled scaling on hold, so drop it unless you mean to change it",
							)
						}
					}
				}
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
			return app.runMaybeWatch(cmd, func(ctx context.Context) *clierr.Error {
				group, cerr := client.GetGroup(ctx, args[0])
				if cerr != nil {
					return groupNotFound(cerr, args[0], client.Org(), client.Project())
				}
				if err := app.printer.Emit(group, func(w *output.Writer) { writeGroupDetail(w, group) }); err != nil {
					return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "%v", err)
				}
				return nil
			})
		},
	}
	getCmd.Flags().
		Bool("writable", false, "emit a JSON patch document for update -f (response-only and create-only fields removed)")
	app.addWatchFlags(getCmd)
	cg.AddCommand(getCmd)

	updateCmd := &cobra.Command{
		Use:   "update <group> [-f <file>] [flags] [-- <command>...]",
		Short: "Update a container group from flags and/or a JSON or YAML patch file (waits for the change to settle)",
		Long: `Update a container group from flags, a JSON or YAML merge patch file (-f),
or both; flags win over the file. The change settles before the command
returns (--no-wait returns at once), and the result is checked against what
you asked for.

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
			if pos, _, _ := splitCommand(cmd, args); len(pos) != 1 {
				return fmt.Errorf("%w 1 arg(s) before --, received %d", errAccepts, len(pos))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			fr := readFlags(cmd.Flags())
			file := fr.String("file")
			allowUnknown := fr.Bool("allow-unknown-fields")
			if cerr := fr.Err(); cerr != nil {
				return cerr
			}
			args, command, commandGiven := splitCommand(cmd, args)
			if cerr := app.validateCommand(command, commandGiven); cerr != nil {
				return cerr
			}
			warnCommand(app.errw(), cmd, command)
			wait, timeout, cerr := settleFlagValues(cmd)
			if cerr != nil {
				return cerr // pre-flight: a bad --timeout must not patch anything
			}
			flags, cerr := collectUpdateFlags(cmd)
			if cerr != nil {
				return cerr
			}
			flags.Command = command
			if cerr = validateUpdateFlags(flags); cerr != nil {
				return cerr
			}
			if file == "" && !flags.any() {
				return clierr.New(clierr.CodeUsage, clierr.KindUsage,
					"nothing to update").
					WithHint("pass -f <file>, a flag such as --image, or both; to see the flags, run saladcloud container-groups update --help")
			}
			// The typed struct is validation only (strict parse, friendly
			// errors); the RAW document is what goes on the wire. Re-marshaling
			// the struct would drop explicit empty values ([], "", 0) via
			// omitempty — and in a merge patch those mean "clear this field".
			// Because the wire document is the user's own bytes, the
			// --allow-unknown-fields path needs no grafting here: tolerated
			// fields ride through untouched.
			var patch containergroups.ContainerGroupPatch
			var raw json.RawMessage
			switch {
			case file == "":
				// Flags only: the patch is built from them below.
			case allowUnknown:
				r, unknowns, err := input.LoadLenient(file, app.Stdin, &patch)
				if err != nil {
					return err
				}
				warnUnknowns(app.errw(), unknowns, false)
				raw = r
			default:
				// Strict: load leniently, then reject with per-field advice
				// so a create-only field (networking.auth) or a response-only
				// one (piped back from `get -o json`) gets an explanation
				// rather than a suggestion to force it through and have the
				// API discard it.
				r, u, err := input.LoadLenient(file, app.Stdin, &patch)
				if err != nil {
					return err
				}
				if cerr = namingGroup(app.rejectUnknowns(u), args[0]); cerr != nil {
					return cerr
				}
				raw = r
			}
			client, _, cerr := app.NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			var intent json.RawMessage // what was asked for, when it differs from the wire patch
			if flags.any() {
				flags.ResolveGpu = lazyGpuResolver(cmd, client)
				var env map[string]*string
				if len(flags.Env) > 0 {
					current, err := client.GetGroup(cmd.Context(), args[0])
					if err != nil {
						return groupNotFound(err, args[0], client.Org(), client.Project())
					}
					env = envOf(current)
				}
				if raw, intent, cerr = buildUpdatePatch(raw, flags, env); cerr != nil {
					return cerr
				}
			}
			group, err := app.applyPatch(cmd, client, args[0], raw, intent, wait, timeout, "update settled")
			if err != nil {
				return err
			}
			return wrapErr(app.printer.Emit(group, func(w *output.Writer) { writeGroupDetail(w, group) }))
		},
	}
	updateCmd.Flags().StringP("file", "f", "", "container group patch file (.json/.yaml; \"-\" reads stdin)")
	addUpdateFlags(updateCmd)
	addAllowUnknownFlag(updateCmd)
	addSettleFlags(updateCmd)
	cg.AddCommand(updateCmd)

	startCmd := &cobra.Command{
		Use:   "start <group>",
		Short: "Start a container group (--wait blocks until it is running)",
		Args:  cobra.ExactArgs(1),
		RunE:  app.startStopRunE("started", "running", (*api.Client).StartGroup),
	}
	addWaitFlags(startCmd, "the group is running")
	cg.AddCommand(startCmd)

	stopCmd := &cobra.Command{
		Use:   "stop <group>",
		Short: "Stop a container group (--wait blocks until it is stopped)",
		Args:  cobra.ExactArgs(1),
		RunE:  app.startStopRunE("stopped", "stopped", (*api.Client).StopGroup),
	}
	addWaitFlags(stopCmd, "the group is stopped")
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
// intent, when non-nil, is what the caller asked for and is what the diffs
// compare against; it differs from raw only when the wire patch must express
// a request indirectly (update -e removing a variable by omission).
func (app *App) applyPatch(
	cmd *cobra.Command,
	client *api.Client,
	name string,
	raw, intent []byte,
	wait bool,
	timeout time.Duration,
	doneMsg string,
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
	if wait {
		final, err := app.runGroupWait(cmd, client, name, goalSettle, doneMsg,
			func(g *shared.ContainerGroup) bool { return !ptr.Deref(g.PendingChange) }, nil, timeout)
		if err != nil {
			return nil, err
		}
		group = final
	} else if ptr.Deref(group.PendingChange) {
		fprintf(
			app.errw(),
			"note: change accepted, still pending, not verified (--no-wait); to check it, run saladcloud container-groups get %s\n",
			name,
		)
	}
	// "Did what I asked for happen?" and "did anything else happen?".
	warnIgnoredFields(app.errw(), findIgnoredFields(intent, before, group))
	warnUnrequestedChanges(app.errw(), findUnrequestedChanges(intent, before, group))
	return group, nil
}
