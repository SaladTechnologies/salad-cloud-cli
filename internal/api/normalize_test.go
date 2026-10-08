package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"

	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

func TestNormalizeNumericEnums(t *testing.T) {
	raw := []byte(`{"items":[{"name":"legacy","restart_policy":1},{"name":"ok","restart_policy":"never"},{"nested":{"restart_policy":3}}]}`)
	fixed, changed := normalizeNumericEnums(raw)
	if !changed {
		t.Fatal("expected a change")
	}
	var doc map[string]any
	if err := json.Unmarshal(fixed, &doc); err != nil {
		t.Fatalf("fixed doc invalid: %v", err)
	}
	items := doc["items"].([]any)
	if got := items[0].(map[string]any)["restart_policy"]; got != "always" {
		t.Errorf("restart_policy 1 = %v, want always", got)
	}
	if got := items[1].(map[string]any)["restart_policy"]; got != "never" {
		t.Errorf("string restart_policy = %v, want unchanged never", got)
	}
	if got := items[2].(map[string]any)["nested"].(map[string]any)["restart_policy"]; got != "never" {
		t.Errorf("nested restart_policy 3 = %v, want never", got)
	}

	// Undefined numeric values (the 0-default leak) are dropped so the
	// document still decodes; the field simply comes back unset.
	fixed2, changed := normalizeNumericEnums([]byte(`{"restart_policy":0,"priority":4}`))
	if !changed {
		t.Fatal("expected a change for undefined enum value")
	}
	var doc2 map[string]any
	if err := json.Unmarshal(fixed2, &doc2); err != nil {
		t.Fatalf("fixed doc invalid: %v", err)
	}
	if _, present := doc2["restart_policy"]; present {
		t.Error("undefined restart_policy 0 should be dropped")
	}
	if got := doc2["priority"]; got != "batch" {
		t.Errorf("priority 4 = %v, want batch", got)
	}
	if _, changed := normalizeNumericEnums([]byte(`{"a":1}`)); changed {
		t.Error("enum-free doc should not change")
	}
}

// TestRestJSONNumericEnumRetry: a group list whose
// legacy entry carries restart_policy as an integer must still decode.
func TestRestJSONNumericEnumRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"name":"legacy","restart_policy":2}]}`))
	}))
	defer srv.Close()
	t.Setenv("SALAD_CLI_API_BASE_URL", srv.URL)
	client, cerr := New(config.Resolved{APIKey: "k", Organization: "o", Project: "p"})
	if cerr != nil {
		t.Fatalf("New: %v", cerr)
	}
	got, cerr := restJSON[containergroups.ContainerGroupCollection](context.Background(), client, "GET", "/x", "", nil)
	if cerr != nil {
		t.Fatalf("restJSON: %v", cerr)
	}
	if len(got.Items) != 1 || got.Items[0].RestartPolicy == nil || string(*got.Items[0].RestartPolicy) != "on_failure" {
		t.Fatalf("decoded = %+v, want on_failure", got.Items)
	}
}
