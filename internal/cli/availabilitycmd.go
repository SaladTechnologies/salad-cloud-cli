// availability: how many nodes could take one replica with given
// requirements right now. It is a query command (it acts directly, filters
// are flags) and the only command that calls the availability endpoints,
// which are expensive on the platform: nothing else asks implicitly.

package cli

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/organizationdata"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/organizations"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"
	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// availabilityCombinedMinClasses is the number of GPU classes from which availability adds the combined query.
const availabilityCombinedMinClasses = 2

// bytesPerGB converts --storage (GB) to the API's storage_amount (bytes).
const bytesPerGB = int64(1) << 30

// countryCodeRe is the shape of a country code: two lowercase letters.
var countryCodeRe = regexp.MustCompile(`^[a-z]{2}$`)

// gpuAvailabilityRow is one GPU class's answer.
type gpuAvailabilityRow struct {
	GpuClass     string `json:"gpu_class"`
	GpuClassName string `json:"gpu_class_name"`
	//nolint:embeddedstructfieldcheck // after the class fields, which JSON lists first
	organizations.GpuAvailability
}

// gpuCombinedRow is the answer to one query for all the classes named: how
// many nodes could take a replica of any of them, which is what a group
// listing several classes can draw on.
type gpuCombinedRow struct {
	GpuClasses []string `json:"gpu_classes"`
	//nolint:embeddedstructfieldcheck // after gpu_classes, which JSON lists first
	organizations.GpuAvailability
}

// gpuAvailabilityResult is availability's JSON for a GPU query: one row per
// class named and, with two or more, the combined query's answer. It is
// never a computed sum: a node carrying two of the classes counts once in
// the combined query and once per class in the rows.
type gpuAvailabilityResult struct {
	Items    []gpuAvailabilityRow `json:"items"`
	Combined *gpuCombinedRow      `json:"combined"`
}

// cpuAvailabilityResult is availability's JSON for a CPU-only query: one
// row, and combined always null, so every availability document has the
// same keys.
type cpuAvailabilityResult struct {
	Items    []organizations.CpuAvailability `json:"items"`
	Combined *cpuCombinedNone                `json:"combined"`
}

// cpuCombinedNone is the combined answer of a CPU-only query, which never has one: its Combined is always nil.
type cpuCombinedNone struct{}

// availabilityFlags are the requirement flags, shared in name and meaning
// with container-groups create.
type availabilityFlags struct {
	Gpus      []string
	CPU       int64
	CPUSet    bool
	Memory    int64
	MemorySet bool
	Storage   int64
	StorageOK bool
	Countries []string
	From      string
}

// validateAvailabilityFlags checks the values before any API work.
func validateAvailabilityFlags(f *availabilityFlags) *clierr.Error {
	if f.CPUSet && f.CPU < 1 {
		return clierr.New(
			clierr.CodeInvalidInput,
			clierr.KindInvalidInput,
			"invalid --cpu %d: must be at least 1",
			f.CPU,
		)
	}
	if f.MemorySet && f.Memory < 1 {
		return clierr.New(
			clierr.CodeInvalidInput,
			clierr.KindInvalidInput,
			"invalid --memory %d: must be at least 1 (MB)",
			f.Memory,
		)
	}
	if f.StorageOK && f.Storage < 1 {
		return clierr.New(
			clierr.CodeInvalidInput,
			clierr.KindInvalidInput,
			"invalid --storage %d: must be at least 1 (GB)",
			f.Storage,
		)
	}
	_, cerr := normalizeCountries(f.Countries)
	return cerr
}

// normalizeCountries lowercases --country values and checks their shape.
func normalizeCountries(in []string) ([]string, *clierr.Error) {
	out := make([]string, 0, len(in))
	for _, c := range in {
		lc := strings.ToLower(strings.TrimSpace(c))
		if !countryCodeRe.MatchString(lc) {
			return nil, clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
				`invalid --country "%s"`, c).WithHint("use a two-letter country code such as us or de")
		}
		out = append(out, lc)
	}
	return out, nil
}

// requirementsOf reads the availability requirements of an existing group.
func requirementsOf(g *shared.ContainerGroup) api.AvailabilityRequest {
	var req api.AvailabilityRequest
	if g.Container != nil && g.Container.Resources != nil {
		r := g.Container.Resources
		req.GpuClasses = append([]string(nil), r.GpuClasses...)
		req.CPU, req.Memory, req.StorageAmount = r.Cpu, r.Memory, r.StorageAmount
	}
	for _, c := range g.CountryCodes {
		req.CountryCodes = append(req.CountryCodes, string(c))
	}
	return req
}

// applyAvailabilityFlags lays explicit flags over base (the --from group's
// requirements, or empty). gpuIDs replaces base's classes when non-empty.
func applyAvailabilityFlags(
	base api.AvailabilityRequest,
	f *availabilityFlags,
	gpuIDs []string,
) api.AvailabilityRequest {
	req := base
	if len(gpuIDs) > 0 {
		req.GpuClasses = gpuIDs
	}
	if f.CPUSet {
		req.CPU = new(f.CPU)
	}
	if f.MemorySet {
		req.Memory = new(f.Memory)
	}
	if f.StorageOK {
		req.StorageAmount = new(f.Storage * bytesPerGB)
	}
	if len(f.Countries) > 0 {
		if codes, cerr := normalizeCountries(f.Countries); cerr == nil { // checked pre-flight
			req.CountryCodes = codes
		}
	}
	return req
}

// allZeroGpu reports whether every priority-tier count in a GPU answer is
// zero (or absent): the overloaded service's answer, or a genuinely empty
// class. The on-call count is left out: the API reports it as 0 whatever the
// capacity, so it says nothing.
func allZeroGpu(a *organizations.GpuAvailability) bool {
	for _, v := range []*int64{a.AvailableGpuBatch, a.AvailableGpuLow, a.AvailableGpuMedium, a.AvailableGpuHigh} {
		if v != nil && *v != 0 {
			return false
		}
	}
	return true
}

// allZeroCPU is allZeroGpu for a CPU answer.
func allZeroCPU(a *organizations.CpuAvailability) bool {
	for _, v := range []*int64{a.AvailableCpuBatch} {
		if v != nil && *v != 0 {
			return false
		}
	}
	return true
}

// askTwiceIfZero runs one availability query and, when every count is
// zero, asks once more: the overloaded service answers all zeros between
// good answers, so one retry turns most of those into real numbers. A
// second all-zero answer is returned as is, and its row gets the overload
// warning.
func askTwiceIfZero[T any](ask func() (*T, *clierr.Error), zero func(*T) bool) (*T, *clierr.Error) {
	a, cerr := ask()
	if cerr != nil || !zero(a) {
		return a, cerr
	}
	return ask()
}

// overloadWarning is the stderr warning for an answer labeled label whose every count is zero after the retry: that
// can be an empty class or an overloaded availability service, and the answer cannot say which.
func overloadWarning(label string) string {
	return fmt.Sprintf(
		"warning: %s reports 0 nodes in every priority tier, which could mean the availability API is overloaded; "+
			"treat this row as unknown and run the command again",
		label,
	)
}

// countCell renders one count, "-" when absent.
// reportsOnCall reports whether any on-call count is set and not 0: only
// then does the table show the column (the API reports 0 for every row
// today).
func reportsOnCall(vs ...*int64) bool {
	for _, v := range vs {
		if v != nil && *v != 0 {
			return true
		}
	}
	return false
}

func countCell(v *int64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatInt(*v, 10)
}

// addAvailabilityCommands adds availabilitycmd.go's commands to the tree.
func (app *App) addAvailabilityCommands(root *cobra.Command) {
	var af availabilityFlags
	cmd := &cobra.Command{
		Use:   "availability [--gpu <class>]... [--cpu <n>] [--memory <mb>] [--storage <gb>] [--country <cc>]...",
		Short: "Ask how many nodes could take one replica with these requirements right now",
		Long: `Ask the container engine how many nodes could take one replica with the
given requirements right now, per priority tier. Each GPU class named is
one platform query (plus one for all of them when there are several), so
call this once before container-groups create, not in a loop; no other
command asks for availability.

--gpu takes a GPU class UUID or name (resolved like container-groups
create --gpu) and is repeated for several classes; names contain commas,
so values are never comma-split. Without --gpu the query is for CPU-only
nodes. --cpu, --memory (MB), --storage (GB) and --country (repeatable) are
the requirements of one replica, as on create.

--from <group> or <project>/<group> takes the requirements of an existing
group in this organization (GPU classes, cpu, memory, storage, countries);
explicit flags override them.

The table and the JSON {"items": [...]} have one row per GPU class named.
With two or more classes, one more query asks for all of them at once and
is shown as "any of the above" (JSON "combined", null otherwise): the nodes
a group listing those classes can draw on. It is its own query, taken
separately from the per-class ones, so it neither equals nor bounds their
sum (a node carrying two of the classes counts once in it, and the counts
move between queries); the CLI never adds the rows up. A query whose counts
are all zero is asked once more before it is shown.

Counts are estimates of currently online nodes, one per priority tier
(BATCH, LOW, MEDIUM, HIGH). The API's on-call count (on_call_gpu,
on_call_cpu) is an ON-CALL column only when a row reports one: today the API
reports 0 for every row, and -o json keeps the field either way. ` +
			`A zero is not proof of no capacity: an overloaded availability
service answers 200 with every count 0, so a row that is all zeros after the
retry gets a stderr warning that the availability API may be overloaded (one
class can be all zeros while others answer).`,
		Example: `  # One GPU class, default requirements
  saladcloud availability --gpu "RTX 4090 (24 GB)"

  # Two classes with the requirements of one replica, US only
  saladcloud availability --gpu "RTX 4090 (24 GB)" --gpu "RTX 3090 (24 GB)" --cpu 8 --memory 16384 --country us

  # The requirements of an existing group, on another class
  saladcloud availability --from my-app --gpu "RTX 5090 (32 GB)"

  # CPU-only nodes
  saladcloud availability --cpu 4 --memory 8192`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return app.runAvailability(cmd, args, &af) },
	}
	f := cmd.Flags()
	f.StringArrayVar(
		&af.Gpus,
		"gpu",
		nil,
		"GPU class UUID or name, repeatable (one query per class, plus one for all of them)",
	)
	f.Int64Var(&af.CPU, "cpu", 0, "vCPU cores per replica")
	f.Int64Var(&af.Memory, "memory", 0, "memory per replica in MB")
	f.Int64Var(&af.Storage, "storage", 0, "storage per replica in GB")
	f.StringArrayVar(&af.Countries, "country", nil, "country code the replica may run in, repeatable (for example us)")
	f.StringVar(&af.From, "from", "", "take the requirements of an existing group: <group> or <project>/<group>")
	cmd.Flags().StringVar(&app.flags.project, globalFlagProject, "",
		"the project --from <group> looks in "+
			"(overrides SALAD_PROJECT and the config file; --from <project>/<group> also names it)")
	root.AddCommand(cmd)
}

func (app *App) runAvailability(cmd *cobra.Command, _ []string, af *availabilityFlags) error {
	fl := cmd.Flags()
	af.CPUSet, af.MemorySet, af.StorageOK = fl.Changed("cpu"), fl.Changed("memory"), fl.Changed("storage")
	if cerr := validateAvailabilityFlags(af); cerr != nil {
		return cerr
	}
	var fromProject, fromGroup string
	if af.From != "" {
		from, cerr := parseFromRef(af.From)
		if cerr != nil {
			return cerr
		}
		fromProject, fromGroup = from.project, from.group
	}

	client, res, cerr := app.NewClient(cmd)
	if cerr != nil {
		return cerr
	}
	cerr = client.RequireOrg()
	if cerr != nil {
		return cerr
	}

	var base api.AvailabilityRequest
	if af.From != "" {
		if base, cerr = app.availabilityFrom(cmd.Context(), &res, fromProject, fromGroup); cerr != nil {
			return cerr
		}
	}
	classes, gpuIDs, cerr := availabilityGpuClasses(cmd.Context(), client, af.Gpus, base)
	if cerr != nil {
		return cerr
	}
	req := applyAvailabilityFlags(base, af, gpuIDs)
	if len(req.GpuClasses) == 0 {
		return app.availabilityCPU(cmd.Context(), client, req)
	}
	return app.availabilityGPU(cmd.Context(), client, req, classes)
}

// availabilityFrom is the requirements of the existing group --from names, read from fromProject when it is given
// and from the resolved project otherwise.
func (app *App) availabilityFrom(
	ctx context.Context,
	res *config.Resolved,
	fromProject, fromGroup string,
) (api.AvailabilityRequest, *clierr.Error) {
	r := *res
	if fromProject != "" {
		r.Project, r.ProjectSource = fromProject, resolveSourceFlag
	}
	src, err := api.New(&r, app.apiOptions())
	if err != nil {
		return api.AvailabilityRequest{}, err
	}
	app.hookClient(ctx, src, &r)
	g, err := src.GetGroup(ctx, fromGroup)
	if err != nil {
		return api.AvailabilityRequest{}, groupNotFound(err, fromGroup, src.Org(), src.Project())
	}
	return requirementsOf(g), nil
}

// availabilityGpuClasses lists the organization's GPU classes when any are asked about (by --gpu or the --from
// group), so the table can name every class, and resolves the --gpu names to class IDs.
func availabilityGpuClasses(
	ctx context.Context,
	client *api.Client,
	gpus []string,
	base api.AvailabilityRequest,
) ([]organizationdata.GpuClass, []string, *clierr.Error) {
	var classes []organizationdata.GpuClass
	if len(gpus) > 0 || len(base.GpuClasses) > 0 {
		list, err := client.ListGpuClasses(ctx)
		if err != nil {
			return nil, nil, err
		}
		classes = list.Items
	}
	var gpuIDs []string
	for _, g := range gpus {
		id := g
		if !isUUID(g) {
			var cerr *clierr.Error
			if id, cerr = resolveGpuClassName(g, classes); cerr != nil {
				return nil, nil, cerr
			}
		}
		gpuIDs = append(gpuIDs, id)
	}
	return classes, gpuIDs, nil
}

// gpuClassDisplayName is the name of the GPU class id among classes, or id when it is not there.
func gpuClassDisplayName(classes []organizationdata.GpuClass, id string) string {
	for _, c := range classes {
		if strings.EqualFold(ptr.Deref(c.Id), id) {
			return strings.TrimSpace(ptr.Deref(c.Name))
		}
	}
	return id
}

// availabilityCPU asks for, and emits, the CPU-only availability of req.
func (app *App) availabilityCPU(ctx context.Context, client *api.Client, req api.AvailabilityRequest) error {
	avail, cerr := askTwiceIfZero(func() (*organizations.CpuAvailability, *clierr.Error) {
		return client.GetCPUAvailability(ctx, req)
	}, allZeroCPU)
	if cerr != nil {
		return cerr
	}
	if allZeroCPU(avail) {
		fprintln(app.errw(), overloadWarning("CPU"))
	}
	result := cpuAvailabilityResult{Items: []organizations.CpuAvailability{*avail}, Combined: nil}
	return wrapErr(app.printer.Emit(result, func(w *output.Writer) {
		if !reportsOnCall(avail.OnCallCpu) {
			w.Println("CLASS\tBATCH")
			w.Printf("CPU\t%s\n", countCell(avail.AvailableCpuBatch))
			return
		}
		w.Println("CLASS\tBATCH\tON-CALL")
		w.Printf("CPU\t%s\t%s\n", countCell(avail.AvailableCpuBatch), countCell(avail.OnCallCpu))
	}))
}

// availabilityGPU asks for, and emits, the availability of req once per GPU class, and once more for all of them
// together when it names two or more.
func (app *App) availabilityGPU(
	ctx context.Context,
	client *api.Client,
	req api.AvailabilityRequest,
	classes []organizationdata.GpuClass,
) error {
	result := gpuAvailabilityResult{Items: make([]gpuAvailabilityRow, 0, len(req.GpuClasses)), Combined: nil}
	for _, id := range req.GpuClasses {
		one := req
		one.GpuClasses = []string{id}
		avail, cerr := app.askGpuAvailability(ctx, client, one, gpuClassDisplayName(classes, id))
		if cerr != nil {
			return cerr
		}
		row := gpuAvailabilityRow{GpuClass: id, GpuClassName: gpuClassDisplayName(classes, id), GpuAvailability: *avail}
		result.Items = append(result.Items, row)
	}
	if len(req.GpuClasses) >= availabilityCombinedMinClasses {
		avail, cerr := app.askGpuAvailability(ctx, client, req, "the combined query")
		if cerr != nil {
			return cerr
		}
		result.Combined = &gpuCombinedRow{GpuClasses: req.GpuClasses, GpuAvailability: *avail}
	}
	return wrapErr(app.printer.Emit(result, func(w *output.Writer) { writeGpuAvailabilityTable(w, &result) }))
}

// askGpuAvailability asks for the GPU availability of req (twice when the first answer is all zeros) and warns,
// naming the query as name, when the answer is all zeros still.
func (app *App) askGpuAvailability(
	ctx context.Context,
	client *api.Client,
	req api.AvailabilityRequest,
	name string,
) (*organizations.GpuAvailability, *clierr.Error) {
	avail, cerr := askTwiceIfZero(func() (*organizations.GpuAvailability, *clierr.Error) {
		return client.GetGpuAvailability(ctx, req)
	}, allZeroGpu)
	if cerr != nil {
		return nil, cerr
	}
	if allZeroGpu(avail) {
		fprintln(app.errw(), overloadWarning(name))
	}
	return avail, nil
}

// writeGpuAvailabilityTable is the table form of a GPU availability result; the ON-CALL column shows only when an
// answer reports it.
func writeGpuAvailabilityTable(w *output.Writer, result *gpuAvailabilityResult) {
	onCall := make([]*int64, 0, len(result.Items)+1)
	for i := range result.Items {
		onCall = append(onCall, result.Items[i].OnCallGpu)
	}
	if result.Combined != nil {
		onCall = append(onCall, result.Combined.OnCallGpu)
	}
	showOnCall := reportsOnCall(onCall...)
	header := "CLASS\tBATCH\tLOW\tMEDIUM\tHIGH"
	if showOnCall {
		header += "\tON-CALL"
	}
	w.Println(header)
	line := func(name string, a *organizations.GpuAvailability) {
		cells := []string{
			name,
			countCell(a.AvailableGpuBatch),
			countCell(a.AvailableGpuLow),
			countCell(a.AvailableGpuMedium),
			countCell(a.AvailableGpuHigh),
		}
		if showOnCall {
			cells = append(cells, countCell(a.OnCallGpu))
		}
		w.Println(strings.Join(cells, "\t"))
	}
	for i := range result.Items {
		line(result.Items[i].GpuClassName, &result.Items[i].GpuAvailability)
	}
	if result.Combined != nil {
		line("any of the above (one platform query)", &result.Combined.GpuAvailability)
	}
}
