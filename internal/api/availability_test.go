package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// newTestClient builds a Client pointed at the given base URL via the
// SALAD_CLI_API_BASE_URL override New honors.
func newTestClient(t *testing.T, baseURL string, opts ...Options) *Client {
	t.Helper()
	t.Setenv("SALAD_CLI_API_BASE_URL", baseURL)
	o := testOptions()
	if len(opts) > 0 {
		o = opts[0]
	}
	c, cerr := New(config.Resolved{APIKey: "test-key", Organization: "test org"}, o)
	if cerr != nil {
		t.Fatalf("New: %v", cerr)
	}
	return c
}

func TestGetGpuAvailabilityRequest(t *testing.T) {
	var gotMethod, gotPath, gotKey string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotKey = r.Method, r.URL.EscapedPath(), r.Header.Get("Salad-Api-Key")
		gotBody = should(io.ReadAll(r.Body))(t)
		respond(t, w, `{"available_gpu_high":9,"available_gpu_low":30,"unknown_field":true}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	avail, cerr := c.GetGpuAvailability(t.Context(), AvailabilityRequest{
		GpuClasses: []string{"gpu-class-1"}, CPU: ptr.To(int64(4)), StorageAmount: ptr.To(int64(1 << 30)),
		CountryCodes: []string{"us"},
	})
	if cerr != nil {
		t.Fatalf("GetGpuAvailability: %v", cerr)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	// The organization name is path-escaped like every other call.
	if want := "/organizations/test%20org/availability/sce-gpu-availability"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotKey != "test-key" {
		t.Errorf("Salad-Api-Key = %q, want test-key", gotKey)
	}
	if want := `{"gpu_classes":["gpu-class-1"],"cpu":4,"storage_amount":1073741824,"country_codes":["us"]}`; string(
		gotBody,
	) != want {
		t.Errorf("body = %s, want %s", gotBody, want)
	}
	if ptr.Deref(avail.AvailableGpuHigh) != 9 || ptr.Deref(avail.AvailableGpuLow) != 30 ||
		avail.AvailableGpuMedium != nil {
		t.Errorf("decoded = %+v", avail)
	}
}

func TestGetCPUAvailabilityDropsGpuClasses(t *testing.T) {
	var gotPath string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		reportErr(t, json.NewDecoder(r.Body).Decode(&body))
		respond(t, w, `{"available_cpu_batch":120,"on_call_cpu":40}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	avail, cerr := c.GetCPUAvailability(
		t.Context(),
		AvailabilityRequest{GpuClasses: []string{"x"}, Memory: ptr.To(int64(2048))},
	)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if gotPath != "/organizations/test org/availability/sce-cpu-availability" {
		t.Errorf("path = %q", gotPath)
	}
	if _, ok := body["gpu_classes"]; ok || body["memory"] != float64(2048) {
		t.Errorf("body = %v, want memory only", body)
	}
	if ptr.Deref(avail.AvailableCpuBatch) != 120 || ptr.Deref(avail.OnCallCpu) != 40 {
		t.Errorf("decoded = %+v", avail)
	}
}

// The availability calls share the API transport, so a 429 is retried
// before it is reported.
func TestGetGpuAvailabilityRetries429(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
		respond(t, w, `{"title":"slow down"}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	c.backoff = []time.Duration{time.Millisecond}
	_, cerr := c.GetGpuAvailability(t.Context(), AvailabilityRequest{GpuClasses: []string{"g"}})
	if cerr == nil || cerr.Code != clierr.CodeRateLimited || ptr.Deref(cerr.HTTPStatus) != 429 {
		t.Fatalf("error = %+v, want rate limited 429", cerr)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2 (one retry)", calls)
	}
}
