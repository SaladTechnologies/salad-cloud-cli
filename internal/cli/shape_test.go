package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/inferenceendpoints"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/logs"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/organizationdata"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/organizations"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/queues"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/quotas"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/webhooksecretkey"
	"sigs.k8s.io/yaml"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
)

// emitJSON renders v through output.Emit (or Stream) in mode (json or
// yaml) and returns the decoded document.
func (app *testRun) emitJSON(t *testing.T, v any, stream bool, mode string) any {
	t.Helper()
	var buf bytes.Buffer
	old := app.printer
	app.printer = output.NewPrinter(&buf)
	t.Cleanup(func() {
		app.printer = old
		app.setOutputMode(t, "table")
	})
	if err := app.printer.SetMode(mode); err != nil {
		t.Fatal(err)
	}
	var err error
	if stream {
		err = app.printer.Stream(v, "")
	} else {
		err = app.printer.Emit(v, nil)
	}
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	data := buf.Bytes()
	if mode == "yaml" {
		data = bytes.TrimPrefix(data, []byte("---\n")) // a stream record opens with ---
		if data, err = yaml.YAMLToJSON(data); err != nil {
			t.Fatalf("not one YAML document: %v\n%s", err, buf.String())
		}
	}
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("not one JSON document: %v\n%s", err, buf.String())
	}
	return doc
}

// assertShape checks doc against t's json tags: every key present, an
// absent array [] and map {}, a nil pointer null, recursing into structs
// the document carries. It is written apart from output's fill so the two
// check each other.
func assertShape(t *testing.T, path string, typ reflect.Type, doc any) {
	t.Helper()
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Implements(reflect.TypeFor[json.Marshaler]()) ||
		reflect.PointerTo(typ).Implements(reflect.TypeFor[json.Marshaler]()) {
		return // encodes itself
	}
	switch typ.Kind() {
	case reflect.Struct:
		obj, ok := doc.(map[string]any)
		if !ok {
			if doc != nil {
				t.Errorf("%s: want an object, got %T", path, doc)
			}
			return
		}
		assertShapeFields(t, path, typ, obj)
	case reflect.Slice, reflect.Array:
		if arr, ok := doc.([]any); ok {
			for _, el := range arr {
				assertShape(t, path+"[]", typ.Elem(), el)
			}
		}
	}
}

// assertShapeFields checks obj, the object at path, against the json tags of struct st's fields: the key of every
// exported field not tagged "-" present, the fields of an untagged embedded struct taken as st's own.
func assertShapeFields(t *testing.T, path string, st reflect.Type, obj map[string]any) {
	t.Helper()
	for i := range st.NumField() {
		f := st.Field(i)
		tag := f.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if tag == "-" {
			continue
		}
		if f.Anonymous && name == "" {
			assertShapeFields(t, path, f.Type, obj)
			continue
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		val, ok := obj[name]
		if !ok {
			t.Errorf("%s.%s: key missing", path, name)
			continue
		}
		assertShapeValue(t, path+"."+name, f.Type, tag, val)
	}
}

// assertShapeValue checks val, the value of a field of type typ tagged tag at path: a null omitempty slice (other
// than bytes) or map is an absent array or map the fill missed; anything else non-null is checked by assertShape.
func assertShapeValue(t *testing.T, path string, typ reflect.Type, tag string, val any) {
	t.Helper()
	if val != nil {
		assertShape(t, path, typ, val)
		return
	}
	if typ.Kind() == reflect.Slice && typ.Elem().Kind() != reflect.Uint8 && strings.Contains(tag, "omitempty") {
		t.Errorf("%s: absent array rendered null, want []", path)
	}
	if typ.Kind() == reflect.Map && strings.Contains(tag, "omitempty") {
		t.Errorf("%s: absent map rendered null, want {}", path)
	}
}

// TestSDKBackedJSONShape: each type a command emits straight from the SDK
// (or the CLI's own API models) keeps every key, although the SDK tags them
// all omitempty. Zero values cover the top level; the populated cases reach
// nested objects and array elements.
func TestSDKBackedJSONShape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		v      any
		stream bool
	}{
		{"cg get", shared.ContainerGroup{}, false},
		{"cg get nested", shared.ContainerGroup{
			Container:    &shared.Container{Resources: &shared.ContainerResourceRequirements{}},
			CurrentState: &shared.ContainerGroupState{},
		}, false},
		{"cg list", struct {
			Items []shared.ContainerGroup `json:"items"`
		}{[]shared.ContainerGroup{{}}}, false},
		{"cg instances list", containergroups.ContainerGroupInstanceCollection{
			Instances: []containergroups.ContainerGroupInstance{{}},
		}, false},
		{"cg instances get", containergroups.ContainerGroupInstance{}, false},
		{"quotas", quotas.Quotas{ContainerGroupsQuotas: &quotas.ContainerGroupsQuotas{}}, false},
		{"gpus list", struct {
			Items []organizationdata.GpuClass `json:"items"`
		}{[]organizationdata.GpuClass{{}}}, false},
		{"gpus get", organizationdata.GpuClass{}, false},
		{"availability gpu", gpuAvailabilityResult{Items: []gpuAvailabilityRow{{}}}, false},
		{
			"availability gpu combined",
			gpuAvailabilityResult{Items: []gpuAvailabilityRow{{}, {}}, Combined: &gpuCombinedRow{}},
			false,
		},
		{"availability cpu", cpuAvailabilityResult{Items: []organizations.CpuAvailability{{}}}, false},
		{"inference endpoints list", inferenceendpoints.InferenceEndpointCollection{
			Items: []inferenceendpoints.InferenceEndpoint{{}},
		}, false},
		{"inference endpoints get", inferenceendpoints.InferenceEndpoint{}, false},
		{"inference jobs list", inferenceendpoints.InferenceEndpointJobCollection{
			Items: []inferenceendpoints.InferenceEndpointJob{{}},
		}, false},
		{"inference jobs get", inferenceendpoints.InferenceEndpointJob{}, false},
		{"logs", logs.LogEntry{Resource: &logs.LogEntryResource{}}, true},
		{"orgs", api.OrganizationList{Items: []api.Organization{{}}}, false},
		{"ai-gateway models list", gatewayModelsResult{Items: []map[string]any{{}}}, false},
		{"cg instances list items", struct {
			Items []containergroups.ContainerGroupInstance `json:"items"`
		}{[]containergroups.ContainerGroupInstance{{}}}, false},
		{"queues list", struct {
			Items []queues.Queue `json:"items"`
		}{[]queues.Queue{{ContainerGroups: []shared.ContainerGroup{{}}}}}, false},
		{"queues get", queues.Queue{}, false},
		{
			"queues jobs list",
			queues.QueueJobCollection{Items: []queues.QueueJob{{Events: []queues.QueueJobEvent{{}}}}},
			false,
		},
		{"queues jobs get", queues.QueueJob{}, false},
		{"webhook-secret get", webhooksecretkey.WebhookSecretKey{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app := newTestRun(t)
			for _, mode := range []string{"json", "yaml"} {
				doc := app.emitJSON(t, tc.v, tc.stream, mode)
				assertShape(t, tc.name+" ("+mode+")", reflect.TypeOf(tc.v), doc)
			}
		})
	}
}

// TestWhoamiOrganizationsUnavailableVsEmpty runs whoami against a stub API:
// a listing that cannot list (404) renders organizations null (unknown), one that
// answers with no items renders [] (none).
func TestWhoamiOrganizationsUnavailableVsEmpty(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"unavailable", http.StatusNotFound, `{}`, "null"},
		{"empty", http.StatusOK, `{"items":[]}`, "[]"},
		{"one", http.StatusOK, `{"items":[{"organization_name":"my-org"}]}`, `["my-org"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app := newTestRun(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/v1/organizations") {
					w.WriteHeader(tc.status)
					respond(t, w, tc.body)
					return
				}
				respond(t, w, `{}`) // the quotas probe
			}))
			defer srv.Close()
			app.setenv("SALAD_CLI_API_BASE_URL", srv.URL)
			app.setenv("SALAD_CLI_CONFIG_DIR", t.TempDir())
			app.setenv("SALAD_API_KEY", "salad_cloud_user_test0000")
			app.setenv("SALAD_ORGANIZATION", "my-org")
			app.setenv("SALAD_PROJECT", "")

			out, err := app.runRoot(t, "whoami", "-o", "json", "--org", "", "--project", "")
			if err != nil {
				t.Fatalf("whoami: %v", err)
			}
			var m map[string]json.RawMessage
			if err := json.Unmarshal([]byte(out), &m); err != nil {
				t.Fatalf("not JSON: %v\n%s", err, out)
			}
			var got bytes.Buffer
			if err := json.Compact(&got, m["organizations"]); err != nil || got.String() != tc.want {
				t.Errorf("organizations = %s, want %s", m["organizations"], tc.want)
			}
		})
	}
}

// TestCLIResultsSameInYAML: every result struct the CLI owns renders the
// same document under -o yaml as under -o json, so the JSON shape tests
// cover both modes.
func TestCLIResultsSameInYAML(t *testing.T) {
	t.Parallel()
	cases := map[string]any{
		"agent init":             agentInitResult{},
		"changelog":              changelogResult{},
		"config list":            configListResult{},
		"doctor check":           checkResult{},
		"ai-gateway test":        gatewayTestResult{},
		"ai-gateway login":       gatewayLoginResult{},
		"ai-gateway logout":      gatewayLogoutResult{},
		"ai-gateway models list": gatewayModelsResult{Items: []map[string]any{{"id": "m"}}},
		"group action":           groupActionResult{},
		"job action":             jobActionResult{},
		"instance action":        instanceActionResult{},
		"logout":                 logoutResult{},
		"queue action":           queueActionResult{},
		"queue job action":       queueJobActionResult{},
		"scale":                  scaleResult{},
		"ssh-key":                sshKeyResult{},
		"version":                versionInfo{},
		"whoami":                 whoamiResult{},
		"availability gpu":       gpuAvailabilityResult{Items: []gpuAvailabilityRow{{}}, Combined: &gpuCombinedRow{}},
		"availability cpu":       cpuAvailabilityResult{Items: []organizations.CpuAvailability{{}}},
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			app := newTestRun(t)
			j, y := app.emitJSON(t, v, false, "json"), app.emitJSON(t, v, false, "yaml")
			if !reflect.DeepEqual(j, y) {
				t.Errorf("yaml differs from json:\njson %v\nyaml %v", j, y)
			}
		})
	}
}
