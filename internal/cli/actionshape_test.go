package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The action-result documents are declared in four different files
// (groups.go, instances.go, inference.go, scale.go), so nothing local to any
// one of them keeps the shape consistent. This file is that contract.
//
// It exists because a live run found `scale -o json` emitting
// {name, replicas, ok} while start/stop/delete emitted {name, action, ok}:
// an agent switching on "action" got nothing back, and no test noticed.
// Every no-body-response verb must carry "action" and "ok", plus whatever
// identity fields name the thing acted on.
func TestActionResultsShareActionAndOK(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		payload  any
		identity []string // keys that name the subject of the action
	}{
		{
			name:     "group start/stop/delete",
			payload:  groupActionResult{Name: "web", Action: "started", OK: true},
			identity: []string{"name"},
		},
		{
			name:     "scale",
			payload:  scaleResult{Name: "web", Action: "scaled", Replicas: 2, OK: true},
			identity: []string{"name"},
		},
		{
			name:     "instance restart/recreate/reallocate",
			payload:  instanceActionResult{Group: "web", ID: "abc", Action: "restarted", OK: true},
			identity: []string{"group", "id"},
		},
		{
			name:     "inference job cancel",
			payload:  jobActionResult{Endpoint: "transcribe", ID: "abc", Action: "canceled", OK: true},
			identity: []string{"endpoint", "id"},
		},
		{
			name:     "queue delete",
			payload:  queueActionResult{Name: "q", Action: "deleted", OK: true},
			identity: []string{"name"},
		},
		{
			name:     "queue job cancel",
			payload:  queueJobActionResult{Queue: "q", ID: "abc", Action: "canceled", OK: true},
			identity: []string{"queue", "id"},
		},
		{
			name:     "cp",
			payload:  cpResult{Group: "web", Instance: "abc", Action: "copied", OK: true, Src: "web:/x", Dst: "./x"},
			identity: []string{"group", "instance"},
		},
		{
			// logout acts on no named resource, so it carries no name.
			name:    "logout",
			payload: logoutResult{Removed: true, EnvOverrides: []string{}, Action: "logged_out", OK: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			checkActionDocument(t, tt.name, tt.payload, tt.identity)
		})
	}
}

// checkActionDocument encodes payload, the result named name, and checks the document: a non-empty string action, a
// bool ok, and every identity key present and not empty.
func checkActionDocument(t *testing.T, name string, payload any, identity []string) {
	t.Helper()
	b, got := encodeActionDocument(t, payload)

	action, ok := got["action"]
	if !ok {
		t.Fatalf("%s is missing the \"action\" key: %s", name, b)
	}
	if s, isStr := action.(string); !isStr || s == "" {
		t.Errorf("action = %v, want a non-empty string", action)
	}

	okVal, present := got["ok"]
	if !present {
		t.Fatalf("%s is missing the \"ok\" key: %s", name, b)
	}
	if _, isBool := okVal.(bool); !isBool {
		t.Errorf("ok = %v, want a bool", okVal)
	}

	for _, key := range identity {
		if v, present := got[key]; !present || v == "" {
			t.Errorf("%s is missing identity key %q: %s", name, key, b)
		}
	}
}

// encodeActionDocument encodes payload as JSON and decodes it back, returning both the bytes and the document.
func encodeActionDocument(t *testing.T, payload any) ([]byte, map[string]any) {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return b, got
}

// TestActionResultJSONTags audits the struct tags by reflection rather than
// by naming fields, so a *rename* of the json tag (`json:"action"` ->
// `json:"act"`) fails as a test rather than slipping through — naming the Go
// field in a literal only ever produces a compile error, which does not
// distinguish a deliberate rename from an accidental one.
//
// It also rejects `omitempty` on "action" and "ok": these documents are a
// machine contract, so both keys must be present even when empty/false. A
// consumer should read "ok", not infer success from a missing key.
//
// Scope note: this pins the *documents*. It cannot catch a call site that
// forgets to populate Action — that needs the command driven end-to-end
// against a fake server.
func TestActionResultJSONTags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		typ      reflect.Type
		identity []string
	}{
		{"groupActionResult", reflect.TypeFor[groupActionResult](), []string{"name"}},
		{"scaleResult", reflect.TypeFor[scaleResult](), []string{"name", "replicas"}},
		{"instanceActionResult", reflect.TypeFor[instanceActionResult](), []string{"group", "id"}},
		{"jobActionResult", reflect.TypeFor[jobActionResult](), []string{"endpoint", "id"}},
		{"queueActionResult", reflect.TypeFor[queueActionResult](), []string{"name"}},
		{"queueJobActionResult", reflect.TypeFor[queueJobActionResult](), []string{"queue", "id"}},
		{"cpResult", reflect.TypeFor[cpResult](), []string{"group", "instance"}},
		{"logoutResult", reflect.TypeFor[logoutResult](), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tags := actionResultTags(tt.typ)
			for _, required := range append([]string{"action", "ok"}, tt.identity...) {
				full, present := tags[required]
				if !present {
					t.Errorf("%s has no field tagged json:%q (tags: %v)", tt.name, required, tags)
					continue
				}
				if (required == "action" || required == "ok") && strings.Contains(full, "omitempty") {
					t.Errorf("%s tags %q omitempty; it must always serialize", tt.name, required)
				}
			}
		})
	}
}

// actionResultTags maps the json name of each tagged field of struct typ to its full json tag.
func actionResultTags(typ reflect.Type) map[string]string {
	tags := make(map[string]string) // json name -> full tag
	for i := range typ.NumField() {
		raw := typ.Field(i).Tag.Get("json")
		if raw == "" || raw == "-" {
			continue
		}
		tags[strings.Split(raw, ",")[0]] = raw
	}
	return tags
}
