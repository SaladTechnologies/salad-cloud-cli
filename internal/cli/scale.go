package cli

import (
	"fmt"

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
func reenableSchedulePatch() []byte {
	return []byte(`{"scheduled-scaling-enabled":true}`)
}

// shouldReenableSchedule decides whether --keep-schedule, once given, has
// work to do: the schedule was on before the replicas write and the write
// put it on hold. A group whose schedule was already off is left alone —
// keeping a schedule is not the same as enabling one.
func shouldReenableSchedule(before, after *shared.ContainerGroup) bool {
	if before == nil || after == nil {
		return false
	}
	return ptr.Deref(before.ScheduledScalingEnabled) && !ptr.Deref(after.ScheduledScalingEnabled)
}

// replicas renders a replica count: "1 replica", "3 replicas".
func replicas(n int64) string {
	if n == 1 {
		return "1 replica"
	}
	return fmt.Sprintf("%d replicas", n)
}

// scaleOptions are scale's flags.
type scaleOptions struct {
	replicas     int64
	keepSchedule bool
	settle       settleOptions
}

// addScaleCommands adds scale.go's commands to the tree.
func (app *App) addScaleCommands(cg *cobra.Command) {
	var scaleOpts scaleOptions
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
			return app.runScale(cmd, &scaleOpts, args[0])
		},
	}
	scaleCmd.Flags().Int64Var(&scaleOpts.replicas, "replicas", 0, "desired replica count (0-500)")
	scaleCmd.Flags().
		BoolVar(&scaleOpts.keepSchedule, "keep-schedule", false,
			"re-enable scheduled scaling after the write (setting replicas puts it on hold)")
	addSettleFlags(scaleCmd, &scaleOpts.settle)
	cg.AddCommand(scaleCmd)
}

// runScale is scale's RunE: check the flags pre-flight, write the replica
// count, restore the schedule under --keep-schedule, and emit the result.
func (app *App) runScale(cmd *cobra.Command, opts *scaleOptions, name string) error {
	keep := opts.keepSchedule
	wait, timeout, cerr := opts.settle.values()
	if cerr != nil {
		return cerr
	}
	if !cmd.Flags().Changed("replicas") {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "missing required flag: --replicas <n>")
	}
	n := opts.replicas
	if verr := validateScaleReplicas(n); verr != nil {
		return verr
	}
	client, _, cerr := app.NewClient(cmd)
	if cerr != nil {
		return cerr
	}
	write := func(patch []byte, doneMsg string) (*shared.ContainerGroup, error) {
		return app.applyPatch(
			cmd,
			client,
			name,
			patch,
			nil,
			patchSettle{wait: wait, timeout: timeout, doneMsg: doneMsg, waitReady: false},
		)
	}
	var before *shared.ContainerGroup
	if keep {
		if b, cerr := client.GetGroup(cmd.Context(), name); cerr == nil {
			before = b
		}
	}
	// No settle line: the one success line below says it.
	group, err := write(scalePatch(n), "")
	if err != nil {
		return err
	}
	if keep {
		if err := app.scaleKeepSchedule(before, group, write); err != nil {
			return err
		}
	}
	v := scaleResult{Name: name, Action: "scaled", Replicas: n, OK: true}
	return wrapErr(app.printer.Emit(v, func(w *output.Writer) {
		w.Printf("Scaled %s to %s\n", v.Name, replicas(v.Replicas))
	}))
}

// scaleKeepSchedule is --keep-schedule after the replicas write: switch the
// schedule back on through write when the write put it on hold, or note
// that there was none to keep.
func (app *App) scaleKeepSchedule(
	before, after *shared.ContainerGroup,
	write func(patch []byte, doneMsg string) (*shared.ContainerGroup, error),
) error {
	switch {
	case shouldReenableSchedule(before, after):
		if _, err := write(reenableSchedulePatch(), "scheduled scaling re-enabled"); err != nil {
			return err
		}
		fprintln(app.errw(), "note: scheduled scaling re-enabled (--keep-schedule)")
	case before != nil && !ptr.Deref(before.ScheduledScalingEnabled):
		fprintln(app.errw(), "note: --keep-schedule: scheduled scaling was not enabled, nothing to keep")
	}
	return nil
}
