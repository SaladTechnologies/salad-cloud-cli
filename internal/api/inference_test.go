package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/inferenceendpoints"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

func TestListInferenceEndpointsWirePath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if want := "/organizations/my-org/inference-endpoints"; r.URL.Path != want {
			t.Errorf("path = %s, want %s", r.URL.Path, want)
		}
		q := r.URL.Query()
		if q.Get("page") != "1" || q.Get("page_size") != "25" {
			t.Errorf("query = %s, want page=1&page_size=25", r.URL.RawQuery)
		}
		if got := r.Header.Get("Salad-Api-Key"); got != "test-key" {
			t.Errorf("Salad-Api-Key = %q, want %q", got, "test-key")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
		  "items": [{"name": "transcribe", "display_name": "Transcription", "description": "Speech to text"}],
		  "page": 1, "page_size": 100, "total_size": 1
		}`))
	}))
	defer srv.Close()

	c := newRestTestClient(t, srv.URL)
	coll, cerr := c.ListInferenceEndpoints(t.Context())
	if cerr != nil {
		t.Fatalf("ListInferenceEndpoints: %v", cerr)
	}
	if len(coll.Items) != 1 || coll.Items[0].Name == nil || *coll.Items[0].Name != "transcribe" {
		t.Errorf("Items = %+v, want one endpoint named transcribe", coll.Items)
	}
}

// TestCreateInferenceJobRoundTrip pins the jobs create wire contract:
// POST to the org-scoped (no project) jobs path, the prototype as the JSON
// body, and the response job decoded through our transport.
func TestCreateInferenceJobRoundTrip(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{
		  "id": "job-1",
		  "inference_endpoint_name": "transcribe",
		  "organization_name": "my-org",
		  "input": {"url": "https://example.com/a.mp3"},
		  "status": "pending",
		  "events": [{"action": "created", "time": "2026-09-02T00:00:00Z"}],
		  "create_time": "2026-09-02T00:00:00Z",
		  "update_time": "2026-09-02T00:00:00Z"
		}`))
	}))
	defer srv.Close()

	c := newRestTestClient(t, srv.URL)
	proto := inferenceendpoints.InferenceEndpointJobPrototype{
		Input: map[string]any{"url": "https://example.com/a.mp3", "language_code": "en"},
	}
	job, cerr := c.CreateInferenceJob(t.Context(), "transcribe", proto)
	if cerr != nil {
		t.Fatalf("CreateInferenceJob: %v", cerr)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if want := "/organizations/my-org/inference-endpoints/transcribe/jobs"; gotPath != want {
		t.Errorf("path = %s, want %s", gotPath, want)
	}
	var sent map[string]any
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("request body is not JSON: %v (%s)", err, gotBody)
	}
	in, _ := sent["input"].(map[string]any)
	if in == nil || in["url"] != "https://example.com/a.mp3" || in["language_code"] != "en" {
		t.Errorf("sent input = %v, want the prototype's input object", sent["input"])
	}
	if job.Id == nil || *job.Id != "job-1" {
		t.Errorf("Id = %v, want job-1", job.Id)
	}
	if job.Status == nil || *job.Status != inferenceendpoints.STATUS_PENDING {
		t.Errorf("Status = %v, want pending", job.Status)
	}
}

func TestGetAndCancelInferenceJobPaths(t *testing.T) {
	var gotMethods, gotPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethods = append(gotMethods, r.Method)
		gotPaths = append(gotPaths, r.URL.Path)
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusAccepted) // 202, no body
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id": "job-1", "status": "running"}`))
	}))
	defer srv.Close()

	c := newRestTestClient(t, srv.URL)
	if _, cerr := c.GetInferenceJob(t.Context(), "transcribe", "job-1"); cerr != nil {
		t.Fatalf("GetInferenceJob: %v", cerr)
	}
	if cerr := c.CancelInferenceJob(t.Context(), "transcribe", "job-1"); cerr != nil {
		t.Fatalf("CancelInferenceJob: %v", cerr)
	}
	wantPath := "/organizations/my-org/inference-endpoints/transcribe/jobs/job-1"
	if len(gotPaths) != 2 || gotPaths[0] != wantPath || gotPaths[1] != wantPath {
		t.Errorf("paths = %v, want both %s", gotPaths, wantPath)
	}
	if len(gotMethods) != 2 || gotMethods[0] != http.MethodGet || gotMethods[1] != http.MethodDelete {
		t.Errorf("methods = %v, want [GET DELETE]", gotMethods)
	}
}

func TestInferenceEndpoint404Maps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"title": "Not Found"}`))
	}))
	defer srv.Close()

	c := newRestTestClient(t, srv.URL)
	_, cerr := c.GetInferenceEndpoint(t.Context(), "no-such")
	if cerr == nil {
		t.Fatal("GetInferenceEndpoint = nil error, want not_found")
	}
	if cerr.Code != clierr.CodeNotFound || cerr.Kind != clierr.KindNotFound {
		t.Errorf("error = (%d, %s), want (%d, not_found)", cerr.Code, cerr.Kind, clierr.CodeNotFound)
	}
}
