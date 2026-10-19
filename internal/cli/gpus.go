package cli

import (
	"slices"
	"strings"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/organizationdata"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/util"
	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// addGpusCommands adds gpus.go's commands to the tree.
func (app *App) addGpusCommands(root *cobra.Command) {
	gpusCmd := &cobra.Command{
		Use:     "gpus",
		Aliases: []string{"gpu", "gpu-classes"},
		Short:   "GPU classes and their prices (list, get)",
		Long: `List the GPU classes available to the organization and their $/hr prices
per priority tier. This is the catalog only: how many nodes could take a
replica right now is what saladcloud availability --gpu <class> answers,
which costs a platform query and so is never run implicitly.`,
	}

	var allPrices bool
	listCmd := &cobra.Command{
		Use:   verbNameList,
		Short: "List GPU classes with their prices per priority tier",
		Long: `List the GPU classes available to the organization, with their $/hr
prices per priority tier, shown verbatim as the API reports them. The table
shows the four known tiers (batch/low/medium/high); --all-prices adds a
column listing every price entry, including any priority the table has no
column for.`,
		Example: `  # GPU classes with prices
  saladcloud gpus list

  # Every price entry per class, as JSON
  saladcloud gpus list --all-prices -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return app.runGpusList(cmd, args, allPrices) },
	}
	listCmd.Flags().BoolVar(&allPrices, "all-prices", false, "add a column listing every price entry verbatim")
	gpusCmd.AddCommand(listCmd)

	gpusCmd.AddCommand(&cobra.Command{
		Use:   "get <class>",
		Short: "Show one GPU class and every price entry (UUID or name)",
		Long: `Show one GPU class with every price entry. The class is a UUID or a name;
names resolve like container-groups create --gpu: case-insensitive exact
match first, then a unique substring.`,
		Example: `  saladcloud gpus get "RTX 4090 (24 GB)"`,
		Args:    cobra.ExactArgs(1),
		RunE:    app.runGpusGet,
	})
	app.orgScoped(gpusCmd)
	root.AddCommand(gpusCmd)
}

// sortedGpuClasses lists the organization's GPU classes, sorted by name.
func (app *App) sortedGpuClasses(cmd *cobra.Command) ([]organizationdata.GpuClass, *clierr.Error) {
	client, _, cerr := app.NewClient(cmd)
	if cerr != nil {
		return nil, cerr
	}
	list, cerr := client.ListGpuClasses(cmd.Context())
	if cerr != nil {
		return nil, cerr
	}
	classes := list.Items
	slices.SortStableFunc(classes, func(a, b organizationdata.GpuClass) int {
		return strings.Compare(gpuNameCell(a.Name), gpuNameCell(b.Name))
	})
	return classes, nil
}

func (app *App) runGpusList(cmd *cobra.Command, _ []string, allPrices bool) error {
	classes, cerr := app.sortedGpuClasses(cmd)
	if cerr != nil {
		return cerr
	}
	payload := struct {
		Items []organizationdata.GpuClass `json:"items"`
	}{classes}
	emitErr := app.printer.Emit(payload, func(w *output.Writer) {
		header := "NAME\tTYPE\tHIGH DEMAND\tBATCH\tLOW\tMEDIUM\tHIGH"
		if allPrices {
			header += "\tPRICES"
		}
		w.Println(header)
		for _, c := range classes {
			prices := pricesByPriority(c.Prices)
			cells := []string{
				gpuNameCell(c.Name),
				gpuClassTypeCell(c.GpuClassType),
				highDemandCell(c.IsHighDemand),
				orDash(prices["batch"]),
				orDash(prices["low"]),
				orDash(prices["medium"]),
				orDash(prices["high"]),
			}
			if allPrices {
				cells = append(cells, formatAllPrices(c.Prices))
			}
			w.Println(strings.Join(cells, "\t"))
		}
	})
	if emitErr == nil && !app.printer.Machine() {
		fprintln(app.errw(), gpusLegend)
	}
	return wrapErr(emitErr)
}

func (app *App) runGpusGet(cmd *cobra.Command, args []string) error {
	classes, cerr := app.sortedGpuClasses(cmd)
	if cerr != nil {
		return cerr
	}
	id := args[0]
	if !isUUID(id) {
		if id, cerr = resolveGpuClassName(args[0], classes); cerr != nil {
			return cerr
		}
	}
	var class *organizationdata.GpuClass
	for i := range classes {
		if strings.EqualFold(ptr.Deref(classes[i].Id), id) {
			class = &classes[i]
		}
	}
	if class == nil {
		return clierr.New(
			clierr.CodeNotFound,
			clierr.KindNotFound,
			`GPU class "%s" not found in this organization's catalog`,
			args[0],
		).WithHint("to list them, run saladcloud gpus list")
	}
	return wrapErr(app.printer.Emit(class, func(w *output.Writer) {
		row := func(k, v string) { w.Printf("%s\t%s\n", k, v) }
		row("Name", gpuNameCell(class.Name))
		row("ID", ptr.Deref(class.Id))
		row("Type", gpuClassTypeCell(class.GpuClassType))
		row("High demand", highDemandCell(class.IsHighDemand))
		row("Prices", formatAllPrices(class.Prices))
	}))
}

// gpusLegend explains the table's compact cells; printed to stderr after
// the table (table mode only) so piped stdout stays clean.
const gpusLegend = "columns: prices are $/hr by priority tier; live availability: saladcloud availability --gpu <class>"

// gpuNameCell renders a GPU class name for display and sorting: trimmed
// of stray whitespace (the API has returned names with a leading space),
// "-" when absent.
func gpuNameCell(name *string) string {
	return orDash(strings.TrimSpace(ptr.Deref(name)))
}

// priorityName extracts a price entry's priority as a plain string
// ("" for absent or null).
func priorityName(p *util.Nullable[shared.ContainerGroupPriority]) string {
	if p == nil || p.IsNull {
		return ""
	}
	return string(p.Value)
}

// pricesByPriority indexes a class's price list by priority name. Prices
// are kept verbatim — they are strings from the API and are never parsed.
func pricesByPriority(prices []organizationdata.GpuClassPrice) map[string]string {
	m := make(map[string]string, len(prices))
	for _, p := range prices {
		if p.Price == nil {
			continue
		}
		m[priorityName(p.Priority)] = *p.Price
	}
	return m
}

// formatAllPrices renders every price entry verbatim as
// "priority=price" pairs (null priority shown as "(none)").
func formatAllPrices(prices []organizationdata.GpuClassPrice) string {
	parts := make([]string, 0, len(prices))
	for _, p := range prices {
		if p.Price == nil {
			continue
		}
		name := priorityName(p.Priority)
		if name == "" {
			name = "(none)"
		}
		parts = append(parts, name+"="+*p.Price)
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}

func orDash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

func gpuClassTypeCell(t *organizationdata.GpuClassType) string {
	if t == nil {
		return "-"
	}
	return string(*t)
}

func highDemandCell(b *bool) string {
	if b == nil {
		return "-"
	}
	if *b {
		return "yes"
	}
	return "no"
}
