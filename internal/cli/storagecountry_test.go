package cli

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// --storage is GB on the command line and bytes on the wire; --country is
// normalized to the API's lowercase two-letter codes.
func TestCreateStorageAndCountry(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	req, cerr := app.buildCreateRequest(containergroups.ContainerGroupCreationRequest{}, createFlags{
		Name: "web", Image: "busybox",
		Storage: 10, StorageSet: true,
		Countries: []string{"US", " de "},
	})
	if cerr != nil {
		t.Fatal(cerr)
	}
	if got := ptr.Deref(req.Container.Resources.StorageAmount); got != 10*1<<30 {
		t.Errorf("storage_amount = %d, want %d (10 GB in bytes)", got, int64(10)<<30)
	}
	if want := []shared.CountryCode{"us", "de"}; !reflect.DeepEqual(req.CountryCodes, want) {
		t.Errorf("country_codes = %v, want %v", req.CountryCodes, want)
	}
	for _, bad := range []createFlags{
		{Name: "web", Image: "busybox", Storage: 0, StorageSet: true},
		{Name: "web", Image: "busybox", Countries: []string{"usa"}},
	} {
		if _, cerr := app.buildCreateRequest(
			containergroups.ContainerGroupCreationRequest{},
			bad,
		); cerr == nil ||
			cerr.Code != clierr.CodeInvalidInput {
			t.Errorf("%+v: error %v, want exit 6", bad, cerr)
		}
	}
}

func TestUpdateStorageAndCountry(t *testing.T) {
	t.Parallel()
	flags := updateFlags{Storage: 5, StorageSet: true, Countries: []string{"US"}}
	if cerr := validateUpdateFlags(flags); cerr != nil {
		t.Fatal(cerr)
	}
	wire, _, cerr := buildUpdatePatch(nil, flags, nil)
	if cerr != nil {
		t.Fatal(cerr)
	}
	var doc struct {
		Container struct {
			Resources struct {
				StorageAmount int64 `json:"storage_amount"`
			} `json:"resources"`
		} `json:"container"`
		CountryCodes []string `json:"country_codes"`
	}
	if err := json.Unmarshal(wire, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Container.Resources.StorageAmount != 5<<30 || !reflect.DeepEqual(doc.CountryCodes, []string{"us"}) {
		t.Errorf("patch = %s, want storage_amount %d and country_codes [us]", wire, int64(5)<<30)
	}
	for _, bad := range []updateFlags{
		{Storage: 0, StorageSet: true},
		{Countries: []string{"u1"}},
	} {
		if cerr := validateUpdateFlags(bad); cerr == nil || cerr.Code != clierr.CodeInvalidInput {
			t.Errorf("%+v: error %v, want exit 6", bad, cerr)
		}
	}
}
