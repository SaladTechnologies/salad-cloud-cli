package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// TestJobIDsMustBeFullUUIDs: jobs are not resolved by prefix, so a prefix
// is rejected before any request, with where to find the full ID.
func TestJobIDsMustBeFullUUIDs(t *testing.T) {
	for _, tc := range []struct {
		args []string
		list string
	}{
		{[]string{"queues", "jobs", "get", "c39e9f69", "--queue", "q1"}, "saladcloud queues jobs list --queue q1"},
		{[]string{"queues", "jobs", "cancel", "c39e9f69", "--queue", "q1"}, "saladcloud queues jobs list --queue q1"},
		{[]string{"inference", "jobs", "get", "c39e9f69", "--endpoint", "transcribe"}, "saladcloud inference jobs list --endpoint transcribe"},
		{[]string{"inference", "jobs", "cancel", "c39e9f69", "--endpoint", "transcribe"}, "saladcloud inference jobs list --endpoint transcribe"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			stubAPI(t, map[string]string{}) // any request would 404
			_, err := runRoot(t, tc.args...)
			want := `job IDs must be full UUIDs, not "c39e9f69"; to list them, run ` + tc.list
			cerr, ok := err.(*clierr.Error)
			if !ok || cerr.Code != clierr.CodeInvalidInput || cerr.Kind != clierr.KindInvalidInput ||
				errText(cerr) != want {
				t.Errorf("%v, want (6, invalid_input) %s", err, want)
			}
		})
	}
}

// TestAIGatewayModelsListNeedsNoKey: the model list is public, so it works
// with no gateway key and sends none.
func TestAIGatewayModelsListNeedsNoKey(t *testing.T) {
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		w.Write([]byte(`{"object":"list","data":[{"id":"m1","owned_by":"salad"}]}`))
	}))
	defer srv.Close()
	t.Setenv("SALAD_CLI_AI_GATEWAY_BASE_URL", srv.URL)
	t.Setenv("SALAD_CLI_CONFIG_DIR", t.TempDir())
	t.Setenv("SALAD_AI_GATEWAY_API_KEY", "")

	out, err := runRoot(t, "ai-gateway", "models", "list", "-o", "json")
	if err != nil {
		t.Fatalf("models list with no key: %v", err)
	}
	var doc struct {
		Items []map[string]any `json:"items"`
	}
	if json.Unmarshal([]byte(out), &doc) != nil || len(doc.Items) != 1 || doc.Items[0]["id"] != "m1" {
		t.Errorf("models list = %s", out)
	}
	if len(auth) != 1 || auth[0] != "" {
		t.Errorf("Authorization headers sent = %q, want one request with none", auth)
	}

	// A bare 'models' is a noun now.
	if _, err := runRoot(
		t,
		"ai-gateway",
		"models",
	); err == nil ||
		!strings.Contains(err.Error(), "ai-gateway models needs a verb: list") {
		t.Errorf("bare ai-gateway models = %v, want a usage error listing list", err)
	}
}

// TestListEnvelopesAreItemsOnly: inference listings read every page, so the
// collection's paging keys would always be null; the envelope is items
// alone, as on every other list.
func TestListEnvelopesAreItemsOnly(t *testing.T) {
	stubAPI(t, map[string]string{
		"/inference-endpoints":                 `{"items":[{"name":"transcribe"}],"page":1,"page_size":25,"total_size":null}`,
		"/inference-endpoints/transcribe/jobs": `{"items":[{"id":"8a2f13ba-01c2-4a29-9c4d-52c7e5a44e37","create_time":"2026-10-01T00:00:00Z"}],"page":1,"page_size":25,"total_size":null}`,
	})
	for _, args := range [][]string{
		{"inference", "list", "-o", "json"},
		{"inference", "jobs", "list", "--endpoint", "transcribe", "-o", "json"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, err := runRoot(t, args...)
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]json.RawMessage
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatal(err)
			}
			if len(doc) != 1 || doc["items"] == nil {
				keys := make([]string, 0, len(doc))
				for k := range doc {
					keys = append(keys, k)
				}
				t.Errorf("keys = %v, want items alone", keys)
			}
		})
	}
}

// TestQueuesListSaysWhenEmpty: an empty table keeps its header on stdout and
// says so on stderr, as every other list does.
func TestQueuesListSaysWhenEmpty(t *testing.T) {
	stubAPI(t, map[string]string{"/queues": `{"items":[]}`})
	stderr := captureStderr(t, func() {
		if _, err := runRoot(t, "queues", "list"); err != nil {
			t.Fatal(err)
		}
	})
	if want := "no queues in my-org/default\nhint: to create one, run saladcloud queues create <name>\n"; stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
}
