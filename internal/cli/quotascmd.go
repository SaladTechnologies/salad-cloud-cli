package cli

import (
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
)

func init() {
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
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, cerr := NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			q, qerr := client.GetQuotas(cmd.Context())
			if qerr != nil {
				return qerr
			}
			return wrapErr(output.Emit(q, func(w io.Writer) {
				cg := q.ContainerGroupsQuotas
				if cg == nil {
					fmt.Fprintln(w, "No container group quotas reported")
					return
				}
				fmt.Fprintf(w, "replicas\t%s / %s\n",
					int64Cell(cg.ContainerReplicasUsed), int64Cell(cg.ContainerReplicasQuota))
				if cg.MaxContainerGroupReallocationsPerMinute != nil {
					fmt.Fprintf(w, "max reallocations/min\t%d\n", *cg.MaxContainerGroupReallocationsPerMinute)
				}
				if cg.MaxContainerGroupRecreatesPerMinute != nil {
					fmt.Fprintf(w, "max recreates/min\t%d\n", *cg.MaxContainerGroupRecreatesPerMinute)
				}
				if cg.MaxContainerGroupRestartsPerMinute != nil {
					fmt.Fprintf(w, "max restarts/min\t%d\n", *cg.MaxContainerGroupRestartsPerMinute)
				}
			}))
		},
	}
	quotasCmd.AddCommand(cmd)
	orgScoped(quotasCmd)
	Register(quotasCmd)
}

func int64Cell(v *int64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatInt(*v, 10)
}
