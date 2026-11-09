package cli

import (
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/organizations"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

func TestValidateAvailabilityFlags(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		f    availabilityFlags
		code int
	}{
		{"empty is fine", availabilityFlags{}, 0},
		{"cpu 0", availabilityFlags{CPU: 0, CPUSet: true}, clierr.CodeInvalidInput},
		{"memory 0", availabilityFlags{Memory: 0, MemorySet: true}, clierr.CodeInvalidInput},
		{"storage 0", availabilityFlags{Storage: 0, StorageOK: true}, clierr.CodeInvalidInput},
		{"bad country", availabilityFlags{Countries: []string{"usa"}}, clierr.CodeInvalidInput},
		{"upper country ok", availabilityFlags{Countries: []string{"US"}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cerr := validateAvailabilityFlags(&tc.f)
			switch {
			case tc.code == 0 && cerr != nil:
				t.Errorf("unexpected error %v", cerr)
			case tc.code != 0 && (cerr == nil || cerr.Code != tc.code):
				t.Errorf("error = %v, want code %d", cerr, tc.code)
			}
		})
	}
}

// --from's requirements are the base; explicit flags override them and
// --gpu replaces the classes.
func TestApplyAvailabilityFlagsOverFrom(t *testing.T) {
	t.Parallel()
	g := &shared.ContainerGroup{
		Container: &shared.Container{Resources: &shared.ContainerResourceRequirements{
			Cpu: new(int64(4)), Memory: new(int64(8192)), StorageAmount: new(2 * bytesPerGB),
			GpuClasses: []string{"a"},
		}},
		CountryCodes: []shared.CountryCode{"de"},
	}
	base := requirementsOf(g)
	got := applyAvailabilityFlags(
		base,
		&availabilityFlags{CPU: 8, CPUSet: true, Storage: 5, StorageOK: true, Countries: []string{"US"}},
		[]string{"b"},
	)
	want := api.AvailabilityRequest{
		GpuClasses: []string{"b"}, CPU: new(int64(8)), Memory: new(int64(8192)),
		StorageAmount: new(5 * bytesPerGB), CountryCodes: []string{"us"},
	}
	if strings.Join(got.GpuClasses, ",") != "b" || *got.CPU != *want.CPU || *got.Memory != *want.Memory ||
		*got.StorageAmount != *want.StorageAmount || strings.Join(got.CountryCodes, ",") != "us" {
		t.Errorf("got %+v, want %+v", got, want)
	}
	kept := applyAvailabilityFlags(base, &availabilityFlags{}, nil)
	if strings.Join(kept.GpuClasses, ",") != "a" || strings.Join(kept.CountryCodes, ",") != "de" {
		t.Errorf("without flags the group's requirements stay: %+v", kept)
	}
}

func TestOverloadWarning(t *testing.T) {
	t.Parallel()
	w := overloadWarning("RTX 4090")
	if !strings.HasPrefix(w, "warning:") || !strings.Contains(w, "RTX 4090") || !strings.Contains(w, "overloaded") {
		t.Errorf("warning = %q", w)
	}
}

// An all-zero answer is asked once more; a real answer is not.
func TestAskTwiceIfZero(t *testing.T) {
	t.Parallel()
	answers := []*organizations.GpuAvailability{{OnCallGpu: new(int64(0))}, {AvailableGpuHigh: new(int64(7))}}
	calls := 0
	ask := func() (*organizations.GpuAvailability, *clierr.Error) {
		a := answers[calls]
		calls++
		return a, nil
	}
	got, cerr := askTwiceIfZero(ask, allZeroGpu)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if calls != 2 || ptr.Deref(got.AvailableGpuHigh) != 7 {
		t.Errorf("calls = %d, high = %v; want a second ask that answers 7", calls, got.AvailableGpuHigh)
	}
	calls = 1 // start at the good answer
	if _, cerr = askTwiceIfZero(ask, allZeroGpu); cerr != nil {
		t.Fatal(cerr)
	}
	if calls != 2 {
		t.Errorf("a real answer was asked again (calls = %d)", calls)
	}
	if !allZeroGpu(&organizations.GpuAvailability{}) ||
		allZeroGpu(&organizations.GpuAvailability{AvailableGpuLow: new(int64(1))}) {
		t.Error("allZeroGpu misjudged")
	}
	if !allZeroGpu(&organizations.GpuAvailability{OnCallGpu: new(int64(9))}) {
		t.Error("allZeroGpu counted the on-call count")
	}
}
