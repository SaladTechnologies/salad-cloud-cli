// availability: how many nodes could take one replica with given
// requirements right now. It is a query command (it acts directly, filters
// are flags) and the only command that calls the availability endpoints,
// which are expensive on the platform: nothing else asks implicitly.

package cli

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/organizationdata"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/organizations"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"
	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// bytesPerGB converts --storage (GB) to the API's storage_amount (bytes).
const bytesPerGB = int64(1) << 30

// countryCodeRe is the shape of a country code: two lowercase letters.
var countryCodeRe = regexp.MustCompile(`^[a-z]{2}$`)

// gpuAvailabilityRow is one GPU class's answer.
type gpuAvailabilityRow struct {
	GpuClass     string `json:"gpu_class"`
	GpuClassName string `json:"gpu_class_name"`
	organizations.GpuAvailability
}

// gpuCombinedRow is the answer to one query for all the classes named: how
// many nodes could take a replica of any of them, which is what a group
// listing several classes can draw on.
type gpuCombinedRow struct {
	GpuClasses []string `json:"gpu_classes"`
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
	Combined *struct{}                       `json:"combined"`
}

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
func validateAvailabilityFlags(f availabilityFlags) *clierr.Error {
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
func requirementsOf(g *shared.ContainerGroup) (req api.AvailabilityRequest) {
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
	f availabilityFlags,
	gpuIDs []string,
) api.AvailabilityRequest {
	req := base
	if len(gpuIDs) > 0 {
		req.GpuClasses = gpuIDs
	}
	if f.CPUSet {
		req.CPU = ptr.To(f.CPU)
	}
	if f.MemorySet {
		req.Memory = ptr.To(f.Memory)
	}
	if f.StorageOK {
		req.StorageAmount = ptr.To(f.Storage * bytesPerGB)
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

// overloadWarning is the stderr warning for an answer whose every count is
// zero after the retry: that can be an empty class or an overloaded
// availability service, and the answer cannot say which. "" otherwise.
func overloadWarning(label string, zero bool) string {
	if !zero {
		return ""
	}
	return fmt.Sprintf(
		"warning: %s reports 0 nodes in every priority tier, which could mean the availability API is overloaded; treat this row as unknown and run the command again",
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

func init() {
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
reports 0 for every row, and -o json keeps the field either way. A zero is not proof of no capacity: an overloaded availability
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
		RunE: runAvailability,
	}
	f := cmd.Flags()
	f.StringArray("gpu", nil, "GPU class UUID or name, repeatable (one query per class, plus one for all of them)")
	f.Int64("cpu", 0, "vCPU cores per replica")
	f.Int64("memory", 0, "memory per replica in MB")
	f.Int64("storage", 0, "storage per replica in GB")
	f.StringArray("country", nil, "country code the replica may run in, repeatable (for example us)")
	f.String("from", "", "take the requirements of an existing group: <group> or <project>/<group>")
	cmd.Flags().
		String("project", "", "the project --from <group> looks in (overrides SALAD_PROJECT and the config file; --from <project>/<group> also names it)")
	Register(cmd)
}

func runAvailability(cmd *cobra.Command, _ []string) error {
	fl := readFlags(cmd.Flags())
	var af availabilityFlags
	af.Gpus = fl.StringArray("gpu")
	af.CPU = fl.Int64("cpu")
	af.Memory = fl.Int64("memory")
	af.Storage = fl.Int64("storage")
	af.Countries = fl.StringArray("country")
	af.From = fl.String("from")
	if cerr := fl.Err(); cerr != nil {
		return cerr
	}
	af.CPUSet, af.MemorySet, af.StorageOK = fl.Changed("cpu"), fl.Changed("memory"), fl.Changed("storage")
	if cerr := validateAvailabilityFlags(af); cerr != nil {
		return cerr
	}
	var fromProject, fromGroup string
	if af.From != "" {
		var cerr *clierr.Error
		if fromProject, fromGroup, cerr = parseFromRef(af.From); cerr != nil {
			return cerr
		}
	}

	client, res, cerr := NewClient(cmd)
	if cerr != nil {
		return cerr
	}
	if cerr = client.RequireOrg(); cerr != nil {
		return cerr
	}

	var base api.AvailabilityRequest
	if af.From != "" {
		r := res
		if fromProject != "" {
			r.Project, r.ProjectSource = fromProject, "flag"
		}
		src, err := api.New(r)
		if err != nil {
			return err
		}
		warnOnAssumedProject(src, r)
		g, err := src.GetGroup(cmd.Context(), fromGroup)
		if err != nil {
			return groupNotFound(err, fromGroup, src.Org(), src.Project())
		}
		base = requirementsOf(g)
	}

	// Resolve --gpu names, and name every class for the table.
	var classes []organizationdata.GpuClass
	className := func(id string) string {
		for _, c := range classes {
			if strings.EqualFold(ptr.Deref(c.Id), id) {
				return strings.TrimSpace(ptr.Deref(c.Name))
			}
		}
		return id
	}
	if len(af.Gpus) > 0 || len(base.GpuClasses) > 0 {
		list, err := client.ListGpuClasses(cmd.Context())
		if err != nil {
			return err
		}
		classes = list.Items
	}
	var gpuIDs []string
	for _, g := range af.Gpus {
		id := g
		if !isUUID(g) {
			if id, cerr = resolveGpuClassName(g, classes); cerr != nil {
				return cerr
			}
		}
		gpuIDs = append(gpuIDs, id)
	}
	req := applyAvailabilityFlags(base, af, gpuIDs)

	if len(req.GpuClasses) == 0 {
		avail, cerr := askTwiceIfZero(func() (*organizations.CpuAvailability, *clierr.Error) {
			return client.GetCPUAvailability(cmd.Context(), req)
		}, allZeroCPU)
		if cerr != nil {
			return cerr
		}
		if w := overloadWarning("CPU", allZeroCPU(avail)); w != "" {
			fprintln(os.Stderr, w)
		}
		result := cpuAvailabilityResult{Items: []organizations.CpuAvailability{*avail}, Combined: nil}
		return wrapErr(output.Emit(result, func(w *output.Writer) {
			if !reportsOnCall(avail.OnCallCpu) {
				w.Println("CLASS\tBATCH")
				w.Printf("CPU\t%s\n", countCell(avail.AvailableCpuBatch))
				return
			}
			w.Println("CLASS\tBATCH\tON-CALL")
			w.Printf("CPU\t%s\t%s\n", countCell(avail.AvailableCpuBatch), countCell(avail.OnCallCpu))
		}))
	}

	result := gpuAvailabilityResult{Items: []gpuAvailabilityRow{}, Combined: nil}
	for _, id := range req.GpuClasses {
		one := req
		one.GpuClasses = []string{id}
		avail, cerr := askTwiceIfZero(func() (*organizations.GpuAvailability, *clierr.Error) {
			return client.GetGpuAvailability(cmd.Context(), one)
		}, allZeroGpu)
		if cerr != nil {
			return cerr
		}
		row := gpuAvailabilityRow{GpuClass: id, GpuClassName: className(id), GpuAvailability: *avail}
		if w := overloadWarning(row.GpuClassName, allZeroGpu(avail)); w != "" {
			fprintln(os.Stderr, w)
		}
		result.Items = append(result.Items, row)
	}
	if len(req.GpuClasses) >= 2 {
		avail, cerr := askTwiceIfZero(func() (*organizations.GpuAvailability, *clierr.Error) {
			return client.GetGpuAvailability(cmd.Context(), req)
		}, allZeroGpu)
		if cerr != nil {
			return cerr
		}
		if w := overloadWarning("the combined query", allZeroGpu(avail)); w != "" {
			fprintln(os.Stderr, w)
		}
		result.Combined = &gpuCombinedRow{GpuClasses: req.GpuClasses, GpuAvailability: *avail}
	}
	return wrapErr(output.Emit(result, func(w *output.Writer) {
		onCall := []*int64{}
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
	}))
}
