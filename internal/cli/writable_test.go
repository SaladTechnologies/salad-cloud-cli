package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"

	"github.com/saladtechnologies/salad-cloud-cli/internal/input"
)

// A response document with every kind of field the filter must handle:
// response-only scalars, a response-only object, create-only fields, the
// priority relocation, a free-form map, a nullable probe holding null, an
// array, and networking's mostly-unpatchable object.
const richResponse = `{
  "id": "0b1d3c2a-9f4e-4a7b-8c6d-5e4f3a2b1c0d",
  "name": "my-app",
  "display_name": "My App",
  "organization_name": "my-org",
  "project_name": "default",
  "replicas": 2,
  "pending_change": false,
  "version": 3,
  "create_time": "2026-08-29T04:05:06.1234567Z",
  "update_time": "2026-08-30T18:21:12.4390000Z",
  "autostart_policy": true,
  "restart_policy": "always",
  "readme": "# hi",
  "priority": "high",
  "country_codes": ["us"],
  "scheduled-scaling-enabled": true,
  "scaling-actions": [{"replicas": 1, "schedule": "0 9 * * 1-5"}],
  "liveness_probe": null,
  "startup_probe": {"tcp": {"port": 8080}, "initial_delay_seconds": 1, "period_seconds": 3, "timeout_seconds": 1, "success_threshold": 1, "failure_threshold": 3},
  "networking": {"protocol": "http", "port": 8080, "auth": true, "dns": "x.salad.cloud", "load_balancer": "round_robin", "single_connection_limit": false, "client_request_timeout": 100, "server_response_timeout": 100},
  "container": {
    "image": "docker.io/library/busybox:1.36",
    "hash": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
    "size": 12345,
    "image_caching": true,
    "command": ["sleep", "1"],
    "environment_variables": {"A": "1", "weird.KEY-2": "x"},
    "resources": {"cpu": 1, "memory": 1024, "gpu_classes": [], "shm_size": 64}
  },
  "current_state": {"status": "running", "description": null, "start_time": "2026-08-30T18:21:12.4390000Z", "finish_time": "0001-01-01T00:00:00Z", "instance_status_counts": {"allocating_count": 0, "creating_count": 0, "running_count": 2, "stopping_count": 0}}
}`

// writableRichResponse is writableDocument of richResponse: the document,
// the same as a map, and the paths it dropped.
func writableRichResponse(t *testing.T) (any, map[string]any, []string) {
	t.Helper()
	doc, dropped, err := writableDocument(grp(t, richResponse))
	if err != nil {
		t.Fatalf("writableDocument: %v", err)
	}
	return doc, assertAs[map[string]any](t, doc), dropped
}

// TestWritableDocumentDrops: everything the patch schema cannot take is
// dropped, and reported.
func TestWritableDocumentDrops(t *testing.T) {
	t.Parallel()
	_, m, dropped := writableRichResponse(t)
	for _, want := range []string{
		"id", "name", "organization_name", "project_name", "pending_change", "version",
		"create_time", "update_time", "autostart_policy", "restart_policy", "readme",
		"current_state", "container.hash", "container.size",
		"networking.auth", "networking.dns", "networking.protocol", "networking.load_balancer",
	} {
		if !contains(dropped, want) {
			t.Errorf("dropped list %v lacks %q", dropped, want)
		}
	}
	for _, path := range dropped {
		if _, still := lookupPath(m, path); still {
			t.Errorf("%q reported dropped but still present", path)
		}
	}
}

// TestWritableDocumentKeeps: the patchable surface is kept with its values
// intact, and priority is relocated under container.
func TestWritableDocumentKeeps(t *testing.T) {
	t.Parallel()
	_, m, _ := writableRichResponse(t)
	tests := []struct {
		path string
		want any
	}{
		{"replicas", 2},
		{"display_name", "My App"},
		{"scheduled-scaling-enabled", true},
		{"scaling-actions", []any{map[string]any{"replicas": 1, "schedule": "0 9 * * 1-5"}}},
		{"liveness_probe", nil}, // an explicit null
		{"container.environment_variables", map[string]any{"A": "1", "weird.KEY-2": "x"}}, // verbatim
		{
			"container.image_caching",
			true,
		}, // patchable, even if the API ignores it
		{"networking", map[string]any{"port": 8080}}, // only port
		{"container.priority", "high"},               // relocated
	}
	for _, tt := range tests {
		if got, ok := lookupPath(m, tt.path); !ok || !jsonEqual(got, tt.want) {
			t.Errorf("%s = %v (present %v), want %v", tt.path, got, ok, tt.want)
		}
	}
	// Relocated: priority leaves the top level.
	if _, top := m["priority"]; top {
		t.Error("top-level priority survived; requests take container.priority")
	}
}

// TestWritableDocumentIsACleanPatch is the round trip: the strict checker
// accepts the document as a patch.
func TestWritableDocumentIsACleanPatch(t *testing.T) {
	t.Parallel()
	doc, _, _ := writableRichResponse(t)
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	unknowns := input.UnknownFieldList(
		b,
		reflect.TypeFor[containergroups.ContainerGroupPatch](),
	)
	if len(
		unknowns,
	) != 0 {
		t.Errorf("writable document is not a clean patch: %+v", unknowns)
	}
}

func TestSetAndDeletePath(t *testing.T) {
	t.Parallel()
	tree := map[string]any{"a": map[string]any{"b": 1}}
	setPath(tree, "a.c.d", "x")
	if v, ok := lookupPath(tree, "a.c.d"); !ok || v != "x" {
		t.Errorf("setPath: a.c.d = %v (%v)", v, ok)
	}
	setPath(tree, "top", 2)
	if tree["top"] != 2 {
		t.Errorf("setPath at the top level failed: %v", tree)
	}
	deletePath(tree, "a.b")
	if _, ok := lookupPath(tree, "a.b"); ok {
		t.Error("deletePath left a.b in place")
	}
	deletePath(tree, "nope.deeper") // missing prefix is a no-op, not a panic
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s || strings.HasPrefix(x, s+"[") {
			return true
		}
	}
	return false
}
