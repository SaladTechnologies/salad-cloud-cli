package input

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/inferenceendpoints"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// TestCheckUnknownFields drives the reflection walker against the real SDK
// request/patch types, one nesting level per case. These are the documents
// that were silently accepted before strict.go existed (the SDK structs'
// custom Unmarshaler bypasses yaml.UnmarshalStrict's field checking).
func TestCheckUnknownFields(t *testing.T) {
	createT := reflect.TypeFor[containergroups.ContainerGroupCreationRequest]()
	patchT := reflect.TypeFor[containergroups.ContainerGroupPatch]()
	jobT := reflect.TypeFor[inferenceendpoints.InferenceEndpointJobPrototype]()

	tests := []struct {
		name    string
		doc     string
		t       reflect.Type
		wantErr string // substring of the error message; "" = clean
	}{
		{"clean create", `{"name":"a","replicas":1,"container":{"image":"b"}}`, createT, ""},
		{"unknown top-level", `{"name":"a","bogus_field":1}`, createT, `unknown field "bogus_field" at the top level`},
		{"unknown nested", `{"container":{"image":"b","bogus":1}}`, createT, `unknown field "bogus" in "container"`},
		{
			"unknown two deep",
			`{"container":{"resources":{"cpu":1,"vram":8}}}`,
			createT,
			`unknown field "vram" in "container.resources"`,
		},
		{
			"unknown inside array element",
			`{"scaling-actions":[{"replicas":1,"schedule":"* * * * *","when":"noon"}]}`,
			createT,
			`unknown field "when" in "scaling-actions[0]"`,
		},
		{"kebab-case gotcha gets a hint", `{"scaling_actions":[]}`, createT, `did you mean "scaling-actions"?`},
		{"reverse hint", `{"restart-policy":"always"}`, createT, `did you mean "restart_policy"?`},
		{"dropped letter gets a hint", `{"replica":1}`, createT, `did you mean "replicas"?`},
		{
			"misspelling deep in the tree gets a hint",
			`{"container":{"enviroment_variables":{"A":"1"}}}`,
			createT,
			`did you mean "environment_variables"?`,
		},
		{"transposition gets a hint", `{"container":{"resources":{"memroy":1024}}}`, createT, `did you mean "memory"?`},
		{"short key has a tight budget", `{"nam":"x"}`, createT, `did you mean "name"?`},
		{"far-off key gets no hint", `{"bogus_field":1}`, createT, `unknown field "bogus_field" at the top level`},
		{
			"env map keys are free-form",
			`{"container":{"environment_variables":{"ANY_KEY":"v","OTHER":null}}}`,
			createT,
			"",
		},
		{
			"nullable probe recurses",
			`{"startup_probe":{"tcp":{"port":1},"bogus":1}}`,
			createT,
			`unknown field "bogus" in "startup_probe"`,
		},
		{"null values are fine", `{"display_name":null,"startup_probe":null}`, patchT, ""},
		{"patch unknown", `{"bogus":1}`, patchT, `unknown field "bogus" at the top level`},
		{
			"patch nested via nullable container",
			`{"container":{"image":"x","bogus":1}}`,
			patchT,
			`unknown field "bogus" in "container"`,
		},
		{"empty patch is not this check's problem", `{}`, patchT, ""},
		{"any-typed input stays free-form", `{"input":{"url":"x","anything_goes":true,"nested":{"deep":1}}}`, jobT, ""},
		{"but the prototype itself is checked", `{"inputs":{}}`, jobT, `unknown field "inputs"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cerr := CheckUnknownFields([]byte(tc.doc), tc.t)
			if tc.wantErr == "" {
				if cerr != nil {
					t.Fatalf("CheckUnknownFields returned %v, want nil", cerr)
				}
				return
			}
			if cerr == nil {
				t.Fatalf("CheckUnknownFields returned nil, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(cerr.Message, tc.wantErr) {
				t.Errorf("error %q does not contain %q", cerr.Message, tc.wantErr)
			}
		})
	}
}

// TestUnknownFieldList covers lenient mode: all unknowns collected, array
// membership flagged (create cannot graft those; update can).
func TestUnknownFieldList(t *testing.T) {
	createT := reflect.TypeFor[containergroups.ContainerGroupCreationRequest]()
	doc := `{
		"name": "a",
		"top_unknown": 1,
		"container": {"image": "b", "nested_unknown": 2},
		"scaling-actions": [{"replicas": 1, "schedule": "* * * * *", "array_unknown": 3}]
	}`
	unknowns := UnknownFieldList([]byte(doc), createT)
	if len(unknowns) != 3 {
		t.Fatalf("got %d unknowns, want 3: %+v", len(unknowns), unknowns)
	}
	byDesc := map[string]bool{} // desc fragment -> InArray
	for _, u := range unknowns {
		switch {
		case strings.Contains(u.Desc, "top_unknown"):
			byDesc["top"] = u.InArray
		case strings.Contains(u.Desc, "nested_unknown"):
			byDesc["nested"] = u.InArray
		case strings.Contains(u.Desc, "array_unknown"):
			byDesc["array"] = u.InArray
		}
	}
	if len(byDesc) != 3 {
		t.Fatalf("descriptions missing expected fields: %+v", unknowns)
	}
	if byDesc["top"] || byDesc["nested"] || !byDesc["array"] {
		t.Errorf("InArray flags wrong: %+v", unknowns)
	}
}

// TestMergeJSON pins create --allow-unknown-fields grafting: the built
// request (overlay) wins per key, the user's unknown fields (base) survive,
// arrays replace wholesale.
func TestMergeJSON(t *testing.T) {
	tests := []struct {
		name, base, overlay, want string
	}{
		{
			name:    "unknown top-level field survives, overlay wins on conflicts",
			base:    `{"name":"file-name","new_api_field":true}`,
			overlay: `{"name":"flag-name","replicas":2}`,
			want:    `{"name":"flag-name","new_api_field":true,"replicas":2}`,
		},
		{
			name:    "nested objects merge recursively",
			base:    `{"container":{"image":"a","new_field":{"x":1}}}`,
			overlay: `{"container":{"image":"b","resources":{"cpu":1}}}`,
			want:    `{"container":{"image":"b","new_field":{"x":1},"resources":{"cpu":1}}}`,
		},
		{
			name:    "arrays replace wholesale (overlay wins)",
			base:    `{"scaling-actions":[{"replicas":1,"array_unknown":3}]}`,
			overlay: `{"scaling-actions":[{"replicas":1}]}`,
			want:    `{"scaling-actions":[{"replicas":1}]}`,
		},
		{
			name:    "overlay null wins over base value",
			base:    `{"display_name":"x"}`,
			overlay: `{"display_name":null}`,
			want:    `{"display_name":null}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, cerr := MergeJSON([]byte(tc.base), []byte(tc.overlay))
			if cerr != nil {
				t.Fatalf("MergeJSON: %v", cerr)
			}
			// Compare as parsed values: key order is not guaranteed.
			var g, w any
			if err := json.Unmarshal(got, &g); err != nil {
				t.Fatalf("result not JSON: %v", err)
			}
			if err := json.Unmarshal([]byte(tc.want), &w); err != nil {
				t.Fatalf("want not JSON: %v", err)
			}
			if !reflect.DeepEqual(g, w) {
				t.Errorf("MergeJSON = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestLoadLenient: unknown fields are returned rather than rejected, and
// the raw document still carries them.
func TestLoadLenient(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "def.json")
	doc := `{"name":"demo","future_field":1,"container":{"image":"busybox"}}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	var req containergroups.ContainerGroupCreationRequest
	raw, unknowns, cerr := LoadLenient(path, &req)
	if cerr != nil {
		t.Fatalf("LoadLenient: %v", cerr)
	}
	if len(unknowns) != 1 || !strings.Contains(unknowns[0].Desc, "future_field") {
		t.Errorf("unknowns = %+v, want one naming future_field", unknowns)
	}
	if !strings.Contains(string(raw), "future_field") {
		t.Errorf("raw document lost the unknown field: %s", raw)
	}
	// And the strict loaders reject the same file with the unknown_field kind.
	if err := Load(path, &req); err == nil || err.Kind != clierr.KindUnknownField {
		t.Errorf("Load kind = %v, want %s", err, clierr.KindUnknownField)
	}
}

// TestFilterTree pins the constructive twin of the unknown-field check:
// unknown keys are removed at every depth and reported, known keys keep
// their values, arrays and maps are walked against their element types,
// and free-form fields pass through.
func TestFilterTree(t *testing.T) {
	patchT := reflect.TypeFor[containergroups.ContainerGroupPatch]()
	doc := map[string]any{
		"replicas":        float64(2),
		"id":              "resp-only",
		"container":       map[string]any{"image": "x", "hash": "h", "environment_variables": map[string]any{"K": "v"}},
		"scaling-actions": []any{map[string]any{"replicas": float64(1), "schedule": "* * * * *", "when": "noon"}},
	}
	var dropped []string
	out := FilterTree(doc, patchT, "", &dropped).(map[string]any)
	sort.Strings(dropped)
	if want := []string{
		"container.hash",
		"id",
		"scaling-actions[0].when",
	}; strings.Join(
		dropped,
		",",
	) != strings.Join(
		want,
		",",
	) {
		t.Errorf("dropped = %v, want %v", dropped, want)
	}
	if out["replicas"] != float64(2) {
		t.Errorf("replicas = %v, want 2", out["replicas"])
	}
	c := out["container"].(map[string]any)
	if c["image"] != "x" || c["environment_variables"].(map[string]any)["K"] != "v" {
		t.Errorf("container not preserved: %v", c)
	}
	if _, has := c["hash"]; has {
		t.Error("container.hash survived")
	}
	act := out["scaling-actions"].([]any)[0].(map[string]any)
	if _, has := act["when"]; has || act["schedule"] != "* * * * *" {
		t.Errorf("array element not filtered correctly: %v", act)
	}
	// The input tree is not mutated.
	if _, has := doc["id"]; !has {
		t.Error("FilterTree mutated its input")
	}
}

// TestSuggestField pins the did-you-mean rules: kebab/snake swaps win
// outright, edit distance is budgeted by key length, and a tie between two
// candidates yields no hint (an unconfident hint would withhold the escape
// hatch from a field that might be real).
func TestSuggestField(t *testing.T) {
	fields := map[string]reflect.Type{
		"replicas": nil, "name": nil, "scaling-actions": nil, "cpu": nil, "gpu": nil, "memory": nil,
	}
	tests := []struct{ key, want string }{
		{"scaling_actions", "scaling-actions"},
		{"replica", "replicas"},
		{"Replicas", "replicas"},
		{"memroy", "memory"},
		{"nam", "name"},
		{"xpu", ""},        // ties between cpu and gpu
		{"replicaz_x", ""}, // distance 3: over budget
		{"nm", ""},         // short key, distance 2 > budget 1
		{"totally_made_up", ""},
	}
	for _, tt := range tests {
		if got := suggestField(tt.key, fields); got != tt.want {
			t.Errorf("suggestField(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}
}

func TestEditDistance(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"a", "", 1},
		{"abc", "abc", 0},
		{"replica", "replicas", 1},
		{"memroy", "memory", 1},
		{"kitten", "sitting", 3},
		{"ab", "ba", 1},
	}
	for _, tt := range tests {
		if got := editDistance(tt.a, tt.b); got != tt.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}
