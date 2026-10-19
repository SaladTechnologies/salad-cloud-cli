package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/util"
	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// instanceActionResult is the Emit payload for the bodiless instance verbs
// (restart/recreate/reallocate).
type instanceActionResult struct {
	Group  string `json:"group"`
	ID     string `json:"id"`
	Action string `json:"action"`
	OK     bool   `json:"ok"`
}

// instancePull renders PullingProgress when the instance is downloading,
// "-" otherwise.
func instancePull(inst *containergroups.ContainerGroupInstance) string {
	if inst.State != nil && *inst.State == containergroups.THE_CONTAINER_GROUP_INSTANCE_STATE_DOWNLOADING &&
		inst.PullingProgress != nil {
		return fmt.Sprintf("%.0f%%", *inst.PullingProgress)
	}
	return "-"
}

// instanceSSH renders ip:port when both are present, "-" otherwise.
func instanceSSH(inst *containergroups.ContainerGroupInstance) string {
	if inst.SshIp != nil && inst.SshPort != nil {
		return fmt.Sprintf("%s:%d", *inst.SshIp, *inst.SshPort)
	}
	return "-"
}

// instanceCPU renders CPU utilization ("-" until the first per-minute sample).
func instanceCPU(inst *containergroups.ContainerGroupInstance) string {
	if inst.CpuPercent == nil {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", *inst.CpuPercent)
}

// instanceMem renders memory utilization as "usedMB (pct%)", degrading to
// whichever of the two per-minute samples is present.
func instanceMem(inst *containergroups.ContainerGroupInstance) string {
	switch {
	case inst.MemoryUsageMb != nil && inst.MemoryUsagePercent != nil:
		return fmt.Sprintf("%.0fMB (%.0f%%)", *inst.MemoryUsageMb, *inst.MemoryUsagePercent)
	case inst.MemoryUsageMb != nil:
		return fmt.Sprintf("%.0fMB", *inst.MemoryUsageMb)
	case inst.MemoryUsagePercent != nil:
		return fmt.Sprintf("%.0f%%", *inst.MemoryUsagePercent)
	default:
		return "-"
	}
}

// instanceState renders the instance state ("-" when the API omits it).
func instanceState(inst *containergroups.ContainerGroupInstance) string {
	if inst.State == nil {
		return "-"
	}
	return string(*inst.State)
}

// writeInstanceDetail writes the two-column key/value table for one instance.
func writeInstanceDetail(w *output.Writer, inst *containergroups.ContainerGroupInstance) {
	row := func(k, v string) { w.Printf("%s\t%s\n", k, v) }
	row("ID", ptr.Deref(inst.Id))
	row("Machine", ptr.Deref(inst.MachineId))
	row("State", instanceState(inst))
	row("Ready", strconv.FormatBool(ptr.Deref(inst.Ready)))
	row("Started", strconv.FormatBool(ptr.Deref(inst.Started)))
	row("CPU", instanceCPU(inst))
	row("Memory", instanceMem(inst))
	row("Pull progress", instancePull(inst))
	row("SSH", instanceSSH(inst))
	row("Version", strconv.FormatInt(ptr.Deref(inst.Version), 10))
	row("Updated", timeCell(inst.UpdateTime))
}

// resolveInstanceID resolves an instance argument that may be an ID prefix
// (users copy the short prefixes 'instances list' output tends to be read
// as) into the full instance UUID. A full UUID passes through without an
// API call; anything else is matched against the group's live instances
// via resolveSSHInstance (unique prefix wins; no match is CodeNotFound,
// an ambiguous prefix is CodeInvalidInput).
func resolveInstanceID(ctx context.Context, client *api.Client, group, id string) (string, *clierr.Error) {
	if isUUID(id) {
		return id, nil
	}
	collection, cerr := client.ListInstances(ctx, group)
	if cerr != nil {
		return "", cerr
	}
	inst, cerr := resolveSSHInstance(collection.Instances, group, id)
	if cerr != nil {
		return "", cerr
	}
	return ptr.Deref(inst.Id), nil
}

// instanceNotFound rewrites the API's bare 404 into a message naming the
// instance and group; every other error passes through unchanged.
func instanceNotFound(cerr *clierr.Error, group, id string) *clierr.Error {
	if cerr != nil && cerr.Code == clierr.CodeNotFound && !cerr.IsFinal() {
		e := clierr.New(clierr.CodeNotFound, clierr.KindNotFound,
			`instance "%s" not found in container group %s`, id, group)
		e.HTTPStatus = cerr.HTTPStatus
		return e.WithHint("to list its instances, run saladcloud container-groups instances list -c %s", group)
	}
	return cerr
}

// emptyInstancesNote explains an empty instances table. Instances that are
// still allocating/creating are not yet assigned to machines, so the
// instances endpoint returns none even while the group is actively
// deploying — without this note an empty table reads as "no deployment".
// It returns "" when there is nothing useful to say (counts claim running
// instances the listing does not show — an inconsistent snapshot).
func emptyInstancesNote(counts *shared.ContainerGroupInstanceStatusCount) string {
	var allocating, creating, running, stopping int64
	if counts != nil {
		allocating = ptr.Deref(counts.AllocatingCount)
		creating = ptr.Deref(counts.CreatingCount)
		running = ptr.Deref(counts.RunningCount)
		stopping = ptr.Deref(counts.StoppingCount)
	}
	var parts []string
	for _, c := range []struct {
		n    int64
		name string
	}{{allocating, "allocating"}, {creating, "creating"}, {stopping, "stopping"}} {
		if c.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c.n, c.name))
		}
	}
	if len(parts) > 0 {
		return fmt.Sprintf("(%s; not yet assigned to machines, try again shortly)", strings.Join(parts, ", "))
	}
	if running > 0 {
		return ""
	}
	return "no instances; the group has no active deployment"
}

// instanceAction runs one bodiless instance verb and emits its confirmation.
func instanceAction(group, id, pastTense string, call func() *clierr.Error) error {
	if err := call(); err != nil {
		return instanceNotFound(err, group, id)
	}
	v := instanceActionResult{Group: group, ID: id, Action: pastTense, OK: true}
	return wrapErr(output.Emit(v, func(w *output.Writer) {
		w.Printf("Instance %s in container group %s %s\n", v.ID, v.Group, v.Action)
	}))
}

// addGroupFlag registers the required -c/--container-group flag on a
// command whose subject lives inside a container group: the leaf is the
// positional, its ancestor a named flag.
func addGroupFlag(cmd *cobra.Command) {
	cmd.Flags().StringP("container-group", "c", "", "the container group (required)")
	noteTreeErr(cmd.MarkFlagRequired("container-group"))
	annotate(cmd, annotationFlagForArg, "container-group")
}

// groupFlag reads -c/--container-group.
func groupFlag(cmd *cobra.Command) (string, *clierr.Error) {
	fr := readFlags(cmd.Flags())
	g := fr.String("container-group")
	return g, fr.Err()
}

// maxDeletionCost is the API's upper bound on an instance's deletion_cost.
const maxDeletionCost = 100000

// instanceVerb builds one bodiless instance verb (restart, recreate,
// reallocate): <id> positional, -c for the group.
func instanceVerb(
	use, short, pastTense string,
	call func(client *api.Client, cmd *cobra.Command, group, id string) *clierr.Error,
) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use + " <id> -c <group>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			group, cerr := groupFlag(cmd)
			if cerr != nil {
				return cerr
			}
			client, _, cerr := NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			id, cerr := resolveInstanceID(cmd.Context(), client, group, args[0])
			if cerr != nil {
				return cerr
			}
			return instanceAction(group, id, pastTense, func() *clierr.Error {
				return call(client, cmd, group, id)
			})
		},
	}
	addGroupFlag(cmd)
	return cmd
}

func init() {
	instancesCmd := &cobra.Command{
		Use:     "instances",
		Aliases: []string{"instance"},
		Short:   "Inspect and manage the instances of a container group",
		Long: `Inspect and manage the instances of a container group. The instance is the
positional argument (its ID, or a unique prefix of it) and the group is
-c/--container-group:

    saladcloud container-groups instances list -c my-app
    saladcloud container-groups instances restart 8a2f13ba -c my-app`,
	}

	instancesListCmd := &cobra.Command{
		Use:   "list -c <group>",
		Short: "List the instances of a container group",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			group, cerr := groupFlag(cmd)
			if cerr != nil {
				return cerr
			}
			client, _, cerr := NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			return runMaybeWatch(cmd, func(ctx context.Context) *clierr.Error {
				instances, cerr := client.ListInstances(ctx, group)
				if cerr != nil {
					return cerr
				}
				payload := struct {
					Items []containergroups.ContainerGroupInstance `json:"items"`
				}{instances.Instances}
				if payload.Items == nil {
					payload.Items = []containergroups.ContainerGroupInstance{}
				}
				err := output.Emit(payload, func(w *output.Writer) {
					w.Println("ID\tMACHINE\tSTATE\tREADY\tCPU\tMEMORY\tPULL%\tSSH\tUPDATED")
					for i := range instances.Instances {
						inst := &instances.Instances[i]
						w.Printf("%s\t%s\t%s\t%t\t%s\t%s\t%s\t%s\t%s\n",
							ptr.Deref(inst.Id), ptr.Deref(inst.MachineId), instanceState(inst),
							ptr.Deref(inst.Ready), instanceCPU(inst), instanceMem(inst),
							instancePull(inst), instanceSSH(inst), timeCell(inst.UpdateTime))
					}
				})
				if err != nil {
					return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "%v", err)
				}
				// An empty table is ambiguous: allocating/creating instances
				// have no machines yet, so the instances endpoint omits them
				// entirely. Best-effort: fetch the group's status counts and
				// explain on stderr (stdout stays a clean, parseable table).
				if output.Mode() == "table" && len(instances.Instances) == 0 {
					if cg, gerr := client.GetGroup(ctx, group); gerr == nil {
						var counts *shared.ContainerGroupInstanceStatusCount
						if cg.CurrentState != nil {
							counts = cg.CurrentState.InstanceStatusCounts
						}
						if note := emptyInstancesNote(counts); note != "" {
							fprintln(os.Stderr, note)
						}
					}
				}
				return nil
			})
		},
	}
	addGroupFlag(instancesListCmd)
	addWatchFlags(instancesListCmd)
	instancesCmd.AddCommand(instancesListCmd)

	getCmd := &cobra.Command{
		Use:   "get <id> -c <group>",
		Short: "Show one container group instance (the ID may be a unique prefix)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			group, cerr := groupFlag(cmd)
			if cerr != nil {
				return cerr
			}
			client, _, cerr := NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			id, cerr := resolveInstanceID(cmd.Context(), client, group, args[0])
			if cerr != nil {
				return cerr
			}
			inst, cerr := client.GetInstance(cmd.Context(), group, id)
			if cerr != nil {
				return instanceNotFound(cerr, group, id)
			}
			return wrapErr(output.Emit(inst, func(w *output.Writer) { writeInstanceDetail(w, inst) }))
		},
	}
	addGroupFlag(getCmd)
	instancesCmd.AddCommand(getCmd)

	updateCmd := &cobra.Command{
		Use:   "update <id> -c <group> --deletion-cost <n>",
		Short: "Set an instance's deletion cost (the ID may be a unique prefix)",
		Long: `Set an instance's deletion cost, the only writable instance field. When the
group scales down, instances with a lower deletion cost are removed first,
so raising it protects an instance doing long work. 0-100000.`,
		Example: `  # Keep this instance through the next scale-down
  saladcloud container-groups instances update 8a2f13ba -c my-app --deletion-cost 1000`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fr := readFlags(cmd.Flags())
			cost := fr.Int64("deletion-cost")
			if cerr := fr.Err(); cerr != nil {
				return cerr
			}
			if cost < 0 || cost > maxDeletionCost {
				return clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
					"invalid --deletion-cost %d: must be between 0 and %d", cost, maxDeletionCost)
			}
			group, cerr := groupFlag(cmd)
			if cerr != nil {
				return cerr
			}
			client, _, cerr := NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			id, cerr := resolveInstanceID(cmd.Context(), client, group, args[0])
			if cerr != nil {
				return cerr
			}
			patch := containergroups.ContainerGroupInstancePatch{DeletionCost: &util.Nullable[int64]{Value: cost}}
			inst, cerr := client.UpdateInstance(cmd.Context(), group, id, patch)
			if cerr != nil {
				return instanceNotFound(cerr, group, id)
			}
			if inst.Id == nil { // an empty 2xx body: read the instance back
				if inst, cerr = client.GetInstance(cmd.Context(), group, id); cerr != nil {
					return instanceNotFound(cerr, group, id)
				}
			}
			return wrapErr(output.Emit(inst, func(w *output.Writer) { writeInstanceDetail(w, inst) }))
		},
	}
	addGroupFlag(updateCmd)
	updateCmd.Flags().
		Int64("deletion-cost", 0, "deletion cost, 0-100000 (required); lower is removed first on scale-down")
	noteTreeErr(updateCmd.MarkFlagRequired("deletion-cost"))
	instancesCmd.AddCommand(updateCmd)

	instancesCmd.AddCommand(instanceVerb("restart",
		"Restart the container on an instance (same machine; the ID may be a unique prefix)", "restarted",
		func(client *api.Client, cmd *cobra.Command, group, id string) *clierr.Error {
			return client.RestartInstance(cmd.Context(), group, id)
		}))
	instancesCmd.AddCommand(instanceVerb(
		"recreate",
		"Recreate the container on an instance (same machine, fresh container; the ID may be a unique prefix)",
		"recreated",
		func(client *api.Client, cmd *cobra.Command, group, id string) *clierr.Error {
			return client.RecreateInstance(cmd.Context(), group, id)
		},
	))
	instancesCmd.AddCommand(instanceVerb("reallocate",
		"Move an instance to a different machine (the ID may be a unique prefix)", "reallocated",
		func(client *api.Client, cmd *cobra.Command, group, id string) *clierr.Error {
			return client.ReallocateInstance(cmd.Context(), group, id)
		}))

	RegisterContainerGroups(instancesCmd)
}
