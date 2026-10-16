package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"
	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// scaleResult is the Emit payload for the scale command. Action keeps the
// {name, action, ok} shape shared with start/stop/delete so clients can
// switch on one field; Replicas is scale's own extra.
type scaleResult struct {
	Name     string `json:"name"`
	Action   string `json:"action"`
	Replicas int64  `json:"replicas"`
	OK       bool   `json:"ok"`
}

// validateScaleReplicas bounds-checks --replicas against the API's accepted
// range. 0 is legal (it drains a group to zero instances without deleting
// it), so the check must not treat it as "unset".
func validateScaleReplicas(n int64) *clierr.Error {
	if n < 0 || n > 500 {
		return clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
			"invalid --replicas %d: must be between 0 and 500", n)
	}
	return nil
}

// scalePatch is the merge patch scale sends: exactly what `update -f` would
// send for the same change, so it rides the same write path (applyPatch)
// and gets the same verification. Setting replicas puts scheduled scaling
// on hold by platform rule; the shared path is what reports that.
func scalePatch(n int64) []byte {
	return []byte(fmt.Sprintf(`{"replicas":%d}`, n))
}

// reenableSchedulePatch switches scheduled scaling back on. It has to be a
// second write: the API does not honor scheduled-scaling-enabled true in
// the same patch as replicas.
var reenableSchedulePatch = []byte(`{"scheduled-scaling-enabled":true}`)

// shouldReenableSchedule decides whether --keep-schedule has work to do:
// the schedule was on before the replicas write and the write put it on
// hold. A group whose schedule was already off is left alone — keeping a
// schedule is not the same as enabling one.
func shouldReenableSchedule(keep bool, before, after *shared.ContainerGroup) bool {
	if !keep || before == nil || after == nil {
		return false
	}
	return ptr.Deref(before.ScheduledScalingEnabled) && !ptr.Deref(after.ScheduledScalingEnabled)
}

// plural renders a count with its noun: "1 replica", "3 replicas".
func plural(n int64, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func init() {
	scaleCmd := &cobra.Command{
		Use:   "scale <group> --replicas <n>",
		Short: "Set the replica count of a container group (waits for the change to settle; --no-wait returns at once)",
		Long: `Set the replica count of a container group.

This is the same write as update -f with {"replicas": n} and is verified
the same way. Note that setting replicas puts scheduled scaling on hold
(the flag goes false, the actions are kept); the CLI prints a note when
that happens. --keep-schedule switches it back on afterwards with a second
write (the API does not honor the flag in the same patch as replicas), for
"new setpoint, keep the program".`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			wait, timeout, cerr := settleFlagValues(cmd)
			if cerr != nil {
				return cerr
			}
			if !cmd.Flags().Changed("replicas") {
				return clierr.New(clierr.CodeUsage, clierr.KindUsage, "missing required flag: --replicas <n>")
			}
			n, err := cmd.Flags().GetInt64("replicas")
			if err != nil {
				return clierr.New(clierr.CodeUsage, clierr.KindUsage, "%v", err)
			}
			if cerr := validateScaleReplicas(n); cerr != nil {
				return cerr
			}
			client, _, cerr := NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			keep, _ := cmd.Flags().GetBool("keep-schedule")
			var before *shared.ContainerGroup
			if keep {
				if b, cerr := client.GetGroup(cmd.Context(), args[0]); cerr == nil {
					before = b
				}
			}
			// No settle line: the one success line below says it.
			group, err := applyPatch(cmd, client, args[0], scalePatch(n), nil, wait, timeout, "")
			if err != nil {
				return err
			}
			if keep {
				switch {
				case shouldReenableSchedule(keep, before, group):
					if _, err := applyPatch(
						cmd,
						client,
						args[0],
						reenableSchedulePatch,
						nil,
						wait,
						timeout,
						"scheduled scaling re-enabled",
					); err != nil {
						return err
					}
					fmt.Fprintln(os.Stderr, "note: scheduled scaling re-enabled (--keep-schedule)")
				case before != nil && !ptr.Deref(before.ScheduledScalingEnabled):
					fmt.Fprintln(os.Stderr, "note: --keep-schedule: scheduled scaling was not enabled, nothing to keep")
				}
			}
			v := scaleResult{Name: args[0], Action: "scaled", Replicas: n, OK: true}
			return wrapErr(output.Emit(v, func(w io.Writer) {
				fmt.Fprintf(w, "Scaled %s to %s\n", v.Name, plural(v.Replicas, "replica"))
			}))
		},
	}
	scaleCmd.Flags().Int64("replicas", 0, "desired replica count (0-500)")
	scaleCmd.Flags().
		Bool("keep-schedule", false, "re-enable scheduled scaling after the write (setting replicas puts it on hold)")
	addSettleFlags(scaleCmd)
	RegisterContainerGroups(scaleCmd)
}
