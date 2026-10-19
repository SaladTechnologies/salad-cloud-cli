package cli

import (
	"strconv"

	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
)

// addQuotasCommands adds quotascmd.go's commands to the tree.
func (app *App) addQuotasCommands(root *cobra.Command) {
	// quotas is a singleton (one per organization): it takes get, never
	// list, keeps the API's plural spelling, and has no singular alias.
	quotasCmd := &cobra.Command{
		Use:   "quotas",
		Short: "The organization's container group quotas (get)",
	}
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Show the organization's container group quotas",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, _, cerr := app.NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			q, qerr := client.GetQuotas(cmd.Context())
			if qerr != nil {
				return qerr
			}
			return wrapErr(app.printer.Emit(q, func(w *output.Writer) {
				cg := q.ContainerGroupsQuotas
				if cg == nil {
					w.Println("No container group quotas reported")
					return
				}
				w.Printf("replicas\t%s / %s\n",
					int64Cell(cg.ContainerReplicasUsed), int64Cell(cg.ContainerReplicasQuota))
				if cg.MaxContainerGroupReallocationsPerMinute != nil {
					w.Printf("max reallocations/min\t%d\n", *cg.MaxContainerGroupReallocationsPerMinute)
				}
				if cg.MaxContainerGroupRecreatesPerMinute != nil {
					w.Printf("max recreates/min\t%d\n", *cg.MaxContainerGroupRecreatesPerMinute)
				}
				if cg.MaxContainerGroupRestartsPerMinute != nil {
					w.Printf("max restarts/min\t%d\n", *cg.MaxContainerGroupRestartsPerMinute)
				}
			}))
		},
	}
	quotasCmd.AddCommand(cmd)
	app.orgScoped(quotasCmd)
	root.AddCommand(quotasCmd)
}

func int64Cell(v *int64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatInt(*v, 10)
}
