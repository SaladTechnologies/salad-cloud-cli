package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	classA = "aaaaaaaa-0000-4000-8000-000000000001"
	classB = "bbbbbbbb-0000-4000-8000-000000000002"
)

// stubAvailability answers the GPU class list and the availability queries:
// a one-class query gets on_call_gpu 7, a query for several gets 99, so the
// combined row can only be the platform's own answer, never a sum (14).
func stubAvailability(t *testing.T) *[]int {
	t.Helper()
	var classCounts []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/gpu-classes"):
			w.Write([]byte(`{"items":[{"id":"` + classA + `","name":"RTX A (12 GB)"},{"id":"` + classB + `","name":"RTX B (24 GB)"}]}`))
		case strings.HasSuffix(r.URL.Path, "/sce-gpu-availability"):
			var req struct {
				GpuClasses []string `json:"gpu_classes"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			classCounts = append(classCounts, len(req.GpuClasses))
			if len(req.GpuClasses) > 1 {
				w.Write([]byte(`{"available_gpu_high":3,"on_call_gpu":99}`))
				return
			}
			w.Write([]byte(`{"available_gpu_high":5,"on_call_gpu":7}`))
		case strings.HasSuffix(r.URL.Path, "/sce-cpu-availability"):
			w.Write([]byte(`{"available_cpu_batch":4,"on_call_cpu":8}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	stubAPI(t, nil)
	t.Setenv("SALAD_CLI_API_BASE_URL", srv.URL)
	return &classCounts
}

func TestAvailabilityCombinedIsThePlatformsAnswer(t *testing.T) {
	counts := stubAvailability(t)
	out, err := runRoot(t, "availability", "--gpu", classA, "--gpu", classB, "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Items    []map[string]any `json:"items"`
		Combined map[string]any   `json:"combined"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(doc.Items) != 2 || doc.Combined == nil {
		t.Fatalf("items %d, combined %v; want 2 rows and the combined query:\n%s", len(doc.Items), doc.Combined, out)
	}
	if doc.Combined["on_call_gpu"] != float64(99) || doc.Combined["available_gpu_high"] != float64(3) {
		t.Errorf("combined = %v, want the combined query's own numbers (99, 3), not a sum", doc.Combined)
	}
	if classes, _ := doc.Combined["gpu_classes"].([]any); len(classes) != 2 {
		t.Errorf("combined gpu_classes = %v, want both classes", doc.Combined["gpu_classes"])
	}
	if got := *counts; len(got) != 3 || got[0] != 1 || got[1] != 1 || got[2] != 2 {
		t.Errorf("queries asked for %v classes, want one per class then one for both", got)
	}
}

func TestAvailabilityOneClassHasNullCombined(t *testing.T) {
	stubAvailability(t)
	out, err := runRoot(t, "availability", "--gpu", classA, "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	json.Unmarshal([]byte(out), &doc)
	if c, ok := doc["combined"]; !ok || string(c) != "null" {
		t.Errorf("combined = %s (present %v), want null with one class:\n%s", c, ok, out)
	}
}

func TestAvailabilityCombinedTableRow(t *testing.T) {
	stubAvailability(t)
	out, err := runRoot(t, "availability", "--gpu", classA, "--gpu", classB)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "any of the above (one platform query)") || !strings.Contains(out, "RTX B (24 GB)") {
		t.Errorf("table lacks the combined row:\n%s", out)
	}
}

// A CPU-only answer carries combined too, always null, so .combined exists
// in every availability document.
func TestAvailabilityCPUHasNullCombined(t *testing.T) {
	stubAvailability(t)
	out, err := runRoot(t, "availability", "--cpu", "2", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	json.Unmarshal([]byte(out), &doc)
	if c, ok := doc["combined"]; !ok || string(c) != "null" {
		t.Errorf("combined = %s (present %v), want null:\n%s", c, ok, out)
	}
}

// TestAvailabilityOnCallColumn: the table shows ON-CALL only when a row
// reports an on-call count other than 0; the JSON keeps the field.
func TestAvailabilityOnCallColumn(t *testing.T) {
	stubAvailability(t) // on-call counts 7 and 99
	if out, err := runRoot(t, "availability", "--gpu", classA, "--gpu", classB); err != nil || !strings.Contains(out, "ON-CALL") {
		t.Errorf("non-zero on-call counts: err %v, table lacks ON-CALL:\n%s", err, out)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/gpu-classes"):
			w.Write([]byte(`{"items":[{"id":"` + classA + `","name":"RTX A (12 GB)"}]}`))
		case strings.HasSuffix(r.URL.Path, "/sce-gpu-availability"):
			w.Write([]byte(`{"available_gpu_high":5,"on_call_gpu":0}`))
		case strings.HasSuffix(r.URL.Path, "/sce-cpu-availability"):
			w.Write([]byte(`{"available_cpu_batch":4,"on_call_cpu":0}`))
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("SALAD_CLI_API_BASE_URL", srv.URL)
	for _, args := range [][]string{{"availability", "--gpu", classA}, {"availability", "--cpu", "2"}} {
		out, err := runRoot(t, append(args, "-o", "table")...)
		if err != nil || strings.Contains(out, "ON-CALL") || !strings.Contains(out, "BATCH") {
			t.Errorf("%v with on-call 0: err %v, table:\n%s", args, err, out)
		}
	}
	out, err := runRoot(t, "availability", "--gpu", classA, "-o", "json")
	if err != nil || !strings.Contains(out, `"on_call_gpu": 0`) {
		t.Errorf("json drops on_call_gpu: err %v\n%s", err, out)
	}
}
