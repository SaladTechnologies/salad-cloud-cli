package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/organizationdata"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/util"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// TestGpusGetNamesTheClass: 'gpus get' takes the class as its argument, so
// a name that resolves to several names the class, not --gpu.
func TestGpusGetNamesTheClass(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	app.stubAPI(t, map[string]string{"/gpu-classes": `{"items":[` +
		`{"id":"11111111-1111-4111-8111-111111111111","name":"RTX 3060 (12 GB)"},` +
		`{"id":"22222222-2222-4222-8222-222222222222","name":"RTX 3060 Ti (8 GB)"}]}`})
	_, err := app.runRoot(t, "gpus", "get", "3060")
	var cerr *clierr.Error
	ok := errors.As(err, &cerr)
	if !ok || cerr.Code != clierr.CodeInvalidInput ||
		!strings.HasPrefix(cerr.Message, `ambiguous GPU class "3060": matches`) ||
		strings.Contains(cerr.Message, "--gpu") {
		t.Errorf("gpus get 3060 = %v, want an invalid-input error naming the class", err)
	}
}

func price(priority shared.ContainerGroupPriority, p string) organizationdata.GpuClassPrice {
	return organizationdata.GpuClassPrice{
		Price:    ptr.To(p),
		Priority: &util.Nullable[shared.ContainerGroupPriority]{Value: priority},
	}
}

// TestGpuNameCellTrims: the API has returned class names with a leading
// space; display (and sorting) must trim them.
func TestGpuNameCellTrims(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   *string
		want string
	}{
		{"nil", nil, "-"},
		{"empty", ptr.To(""), "-"},
		{"whitespace only", ptr.To("  "), "-"},
		{"clean", ptr.To("RTX 4090 (24 GB)"), "RTX 4090 (24 GB)"},
		{"leading space", ptr.To(" RTX 5080 (16 GB)"), "RTX 5080 (16 GB)"},
		{"trailing space", ptr.To("RTX 5080 (16 GB) "), "RTX 5080 (16 GB)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := gpuNameCell(tt.in); got != tt.want {
				t.Errorf("gpuNameCell = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPriceColumnAssembly(t *testing.T) {
	t.Parallel()
	// Fixture in the shape ListGpuClasses returns.
	list := organizationdata.GpuClassesList{
		Items: []organizationdata.GpuClass{
			{
				Id:   ptr.To("id-4090"),
				Name: ptr.To("RTX 4090 (24 GB)"),
				Prices: []organizationdata.GpuClassPrice{
					price(shared.CONTAINER_GROUP_PRIORITY_HIGH, "0.30"),
					price(shared.CONTAINER_GROUP_PRIORITY_MEDIUM, "0.24"),
					price(shared.CONTAINER_GROUP_PRIORITY_LOW, "0.18"),
					price(shared.CONTAINER_GROUP_PRIORITY_BATCH, "0.12"),
				},
			},
		},
	}
	prices := pricesByPriority(list.Items[0].Prices)
	// Prices must come through VERBATIM — never parsed or reformatted.
	for priority, want := range map[string]string{
		"batch": "0.12", "low": "0.18", "medium": "0.24", "high": "0.30",
	} {
		if got := prices[priority]; got != want {
			t.Errorf("prices[%q] = %q, want %q", priority, got, want)
		}
	}
}

func TestPriceColumnAssemblyMissingTiers(t *testing.T) {
	t.Parallel()
	prices := pricesByPriority([]organizationdata.GpuClassPrice{
		price(shared.CONTAINER_GROUP_PRIORITY_HIGH, "1.50"),
	})
	if got := prices["high"]; got != "1.50" {
		t.Errorf("prices[high] = %q, want 1.50", got)
	}
	if got, ok := prices["batch"]; ok {
		t.Errorf("prices[batch] = %q, want absent", got)
	}
	if got := orDash(prices["batch"]); got != "-" {
		t.Errorf("missing tier cell = %q, want -", got)
	}
}

func TestFormatAllPrices(t *testing.T) {
	t.Parallel()
	prices := []organizationdata.GpuClassPrice{
		price(shared.CONTAINER_GROUP_PRIORITY_BATCH, "0.08"),
		{Price: ptr.To("0.99"), Priority: &util.Nullable[shared.ContainerGroupPriority]{IsNull: true}},
		{Price: ptr.To("0.50"), Priority: &util.Nullable[shared.ContainerGroupPriority]{Value: "turbo"}},
	}
	want := "batch=0.08 (none)=0.99 turbo=0.50"
	if got := formatAllPrices(prices); got != want {
		t.Errorf("formatAllPrices = %q, want %q", got, want)
	}
	if got := formatAllPrices(nil); got != "-" {
		t.Errorf("formatAllPrices(nil) = %q, want -", got)
	}
}
