package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

// testOptions builds a test's client options with an environment of only
// vars, so nothing on the machine running the tests leaks in.
func testOptions(vars map[string]string) Options {
	return Options{Getenv: getenvOf(vars), Debug: DebugOff, DebugOut: nil, Backoff: nil, Now: nil}
}

// getenvOf reads the variables in vars.
func getenvOf(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

// baseURLs points the API and S4 storage base URLs at url, the overrides
// the CLI honors.
func baseURLs(url string) map[string]string {
	return map[string]string{"SALAD_CLI_API_BASE_URL": url, "SALAD_CLI_STORAGE_BASE_URL": url}
}

// newRestTestClient points a Client (with org and project set) at an
// httptest server via the same base URL overrides the CLI honors.
func newRestTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	c, cerr := New(&config.Resolved{
		APIKey:       "test-key",
		Organization: "my-org",
		Project:      "default",
	}, testOptions(baseURLs(baseURL)))
	if cerr != nil {
		t.Fatalf("New: %v", cerr)
	}
	return c
}

// liveGroupBody reproduces the wire shape observed live that the SDK's own
// transport rejected: current_state.description is JSON null (a non-nil
// util.Nullable[string] carrying a `pattern` tag fails the SDK's pattern
// check), and every timestamp has 7 fractional digits.
const liveGroupBody = `{
  "id": "0b1d3c2a-9f4e-4a7b-8c6d-5e4f3a2b1c0d",
  "name": "my-app",
  "display_name": "My App",
  "organization_name": "my-org",
  "project_name": "default",
  "replicas": 1,
  "pending_change": false,
  "version": 3,
  "create_time": "2026-08-29T04:05:06.1234567Z",
  "update_time": "2026-08-30T18:21:12.4390000Z",
  "current_state": {
    "description": null,
    "status": "running",
    "start_time": "2026-08-30T18:21:12.4390000Z",
    "finish_time": "0001-01-01T00:00:00.0000000Z",
    "instance_status_counts": {
      "allocating_count": 0,
      "creating_count": 0,
      "running_count": 1,
      "stopping_count": 0
    }
  }
}`

func TestGetGroupDecodesLiveWireFormat(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkRequest(t, r, http.MethodGet, "/organizations/my-org/projects/default/containers/my-app")
		w.Header().Set("Content-Type", "application/json")
		respond(t, w, liveGroupBody)
	}))
	t.Cleanup(srv.Close)

	c := newRestTestClient(t, srv.URL)
	group, cerr := c.GetGroup(t.Context(), "my-app")
	if cerr != nil {
		t.Fatalf("GetGroup: %v", cerr)
	}
	if group.Name == nil || *group.Name != "my-app" {
		t.Errorf("Name = %v, want my-app", group.Name)
	}
	// The landmine: current_state must survive the null description and
	// 7-digit timestamps, and status must come through intact.
	if group.CurrentState == nil {
		t.Fatal("CurrentState = nil, want populated")
	}
	if group.CurrentState.Status == nil || string(*group.CurrentState.Status) != "running" {
		t.Errorf("CurrentState.Status = %v, want running", group.CurrentState.Status)
	}
	if group.CreateTime == nil || *group.CreateTime != "2026-08-29T04:05:06.1234567Z" {
		t.Errorf("CreateTime = %v, want the verbatim 7-digit timestamp", group.CreateTime)
	}
}

func TestUpdateGroupSendsMergePatchContentType(t *testing.T) {
	t.Parallel()
	var gotContentType, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		respond(t, w, liveGroupBody)
	}))
	t.Cleanup(srv.Close)

	c := newRestTestClient(t, srv.URL)
	if _, cerr := c.UpdateGroup(t.Context(), "my-app", &containergroups.ContainerGroupPatch{}); cerr != nil {
		t.Fatalf("UpdateGroup: %v", cerr)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s, want PATCH", gotMethod)
	}
	if gotContentType != "application/merge-patch+json" {
		t.Errorf("Content-Type = %q, want application/merge-patch+json", gotContentType)
	}
}

// TestUpdateGroupRawSendsBodyVerbatim pins update -f's raw-passthrough
// contract: the wire body is byte-identical to the caller's document —
// nothing is re-marshaled through the SDK struct, so merge-patch clears
// (explicit empty arrays etc.) survive — with the merge-patch Content-Type.
func TestUpdateGroupRawSendsBodyVerbatim(t *testing.T) {
	t.Parallel()
	doc := json.RawMessage(`{"scaling-actions": [], "replicas": 3}`)
	var gotBody []byte
	var gotMethod, gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		gotBody = should(io.ReadAll(r.Body))(t)
		w.Header().Set("Content-Type", "application/json")
		respond(t, w, liveGroupBody)
	}))
	t.Cleanup(srv.Close)

	c := newRestTestClient(t, srv.URL)
	if _, cerr := c.UpdateGroupRaw(t.Context(), "my-app", doc); cerr != nil {
		t.Fatalf("UpdateGroupRaw: %v", cerr)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s, want PATCH", gotMethod)
	}
	if gotContentType != "application/merge-patch+json" {
		t.Errorf("Content-Type = %q, want application/merge-patch+json", gotContentType)
	}
	if !bytes.Equal(gotBody, doc) {
		t.Errorf("wire body = %s, want the input document verbatim: %s", gotBody, doc)
	}
}

// TestUpdateGroupRawRejectsEmptyPatch: a nil/empty/whitespace/"null"
// document (a typed nil RawMessage marshals to "null"; an empty YAML file
// converts to "null") must fail fast client-side, before any request.
func TestUpdateGroupRawRejectsEmptyPatch(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("an empty patch must not reach the server")
	}))
	t.Cleanup(srv.Close)

	c := newRestTestClient(t, srv.URL)
	// "{}" is included: a keyless object is a merge patch that changes
	// nothing, and the API rejects it with a bare 400 — catch it client-side.
	for _, patch := range []json.RawMessage{
		nil, {}, json.RawMessage("  \n"), json.RawMessage("null"), json.RawMessage("{}"), json.RawMessage(" { } "),
	} {
		_, cerr := c.UpdateGroupRaw(t.Context(), "my-app", patch)
		if cerr == nil {
			t.Fatalf("UpdateGroupRaw(%q) = nil error, want invalid_input", patch)
		}
		if cerr.Code != clierr.CodeInvalidInput || cerr.Kind != clierr.KindInvalidInput {
			t.Errorf(
				"UpdateGroupRaw(%q) = (%d, %s), want (%d, invalid_input)",
				patch,
				cerr.Code,
				cerr.Kind,
				clierr.CodeInvalidInput,
			)
		}
	}
}

func TestDeleteGroupEmpty202Body(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		w.WriteHeader(http.StatusAccepted) // 202, no body — the live shape
	}))
	t.Cleanup(srv.Close)

	c := newRestTestClient(t, srv.URL)
	if cerr := c.DeleteGroup(t.Context(), "my-app"); cerr != nil {
		t.Fatalf("DeleteGroup: %v, want nil for an empty 202", cerr)
	}
}

func TestRetryOn429ThenSuccess(t *testing.T) {
	t.Parallel()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			respond(t, w, `{"title":"Too Many Requests"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		respond(t, w, liveGroupBody)
	}))
	t.Cleanup(srv.Close)

	c := newRestTestClient(t, srv.URL)
	c.backoff = []time.Duration{time.Microsecond, time.Microsecond, time.Microsecond}
	group, cerr := c.GetGroup(t.Context(), "my-app")
	if cerr != nil {
		t.Fatalf("GetGroup after 429: %v", cerr)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2 (one 429, one retry)", calls)
	}
	if group.Name == nil || *group.Name != "my-app" {
		t.Errorf("Name = %v, want my-app", group.Name)
	}
}

func TestRetriesExhaustedOn429(t *testing.T) {
	t.Parallel()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)

	c := newRestTestClient(t, srv.URL)
	c.backoff = []time.Duration{time.Microsecond}
	_, cerr := c.GetGroup(t.Context(), "my-app")
	if cerr == nil {
		t.Fatal("GetGroup = nil error, want rate_limited")
	}
	if cerr.Code != clierr.CodeRateLimited || cerr.Kind != clierr.KindRateLimited {
		t.Errorf("error = (%d, %s), want (%d, rate_limited)", cerr.Code, cerr.Kind, clierr.CodeRateLimited)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2 (initial + one retry for a 1-entry backoff)", calls)
	}
}

// TestCreateGroupPreservesExplicitFalseOnTheWire guards a subtle trap: the
// SDK types image_caching as *bool with `omitempty`. A pointer to false is
// NOT dropped by omitempty (only a nil pointer is), so an explicit false has
// to reach the wire — but a refactor to a plain `bool omitempty`, or any
// re-marshal through a lossier struct, would silently erase it.
//
// This matters beyond correctness. The API is known to ignore
// image_caching: false, and the CLI now warns when a requested field does
// not take effect. If the CLI ever stopped sending the field, that warning
// would fire and blame the server for the client's bug. This test keeps the
// warning honest.
func TestCreateGroupPreservesExplicitFalseOnTheWire(t *testing.T) {
	t.Parallel()
	var gotBody []byte
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody = should(io.ReadAll(r.Body))(t)
		w.Header().Set("Content-Type", "application/json")
		respond(t, w, liveGroupBody)
	}))
	t.Cleanup(srv.Close)

	falseVal := false
	req := containergroups.ContainerGroupCreationRequest{
		Name:            strPtr("my-app"),
		Replicas:        int64Ptr(1),
		AutostartPolicy: &falseVal, // the other explicit false on this path
		Container: &containergroups.ContainerConfiguration{
			Image:        strPtr("docker.io/library/busybox:1.36"),
			ImageCaching: &falseVal,

			Resources: &containergroups.CreateContainerResourceRequirements{
				Cpu:    int64Ptr(1),
				Memory: int64Ptr(1024),
			},
		},
	}
	c := newRestTestClient(t, srv.URL)
	if _, cerr := c.CreateGroup(t.Context(), &req); cerr != nil {
		t.Fatalf("CreateGroup: %v", cerr)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotPath == "" {
		t.Error("no request path recorded")
	}
	var sent map[string]any
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("wire body is not JSON: %s", gotBody)
	}
	got, ok := sent["autostart_policy"]
	if on, isBool := got.(bool); !isBool || on {
		t.Errorf("autostart_policy on the wire = %v (present=%t), want false", got, ok)
	}
	container := valueAs[map[string]any](sent["container"])
	if container == nil {
		t.Fatalf("no container on the wire: %s", gotBody)
	}
	got, ok = container["image_caching"]
	if on, isBool := got.(bool); !isBool || on {
		t.Errorf(
			"container.image_caching on the wire = %v (present=%t), want false — an explicit false must not be dropped",
			got,
			ok,
		)
	}
}

// TestCreateGroupRawSendsBodyVerbatim: the --allow-unknown-fields create
// path grafts the built request onto the user's document and POSTs the
// result. Those bytes must go out untouched, or a tolerated unknown field
// would be silently dropped after the CLI promised to send it.
func TestCreateGroupRawSendsBodyVerbatim(t *testing.T) {
	t.Parallel()
	doc := json.RawMessage(
		`{"name":"my-app","replicas":1,"future_field":{"nested":[1,2]},` +
			`"container":{"image":"x","image_caching":false}}`,
	)
	var gotBody []byte
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotBody = should(io.ReadAll(r.Body))(t)
		w.Header().Set("Content-Type", "application/json")
		respond(t, w, liveGroupBody)
	}))
	t.Cleanup(srv.Close)

	c := newRestTestClient(t, srv.URL)
	if _, cerr := c.CreateGroupRaw(t.Context(), doc); cerr != nil {
		t.Fatalf("CreateGroupRaw: %v", cerr)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if !bytes.Equal(gotBody, doc) {
		t.Errorf("wire body = %s, want the document verbatim: %s", gotBody, doc)
	}
}

func strPtr(s string) *string { return &s }
func int64Ptr(i int64) *int64 { return &i }

// respond writes body as an httptest handler's response. The handler runs on
// its own goroutine, so a failed write is reported, not fatal.
func respond(t *testing.T, w io.Writer, body string) {
	t.Helper()
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("writing the response: %v", err)
	}
}

// checkRequest checks an httptest handler's request: its method and path, and the API key newRestTestClient sends.
func checkRequest(t *testing.T, r *http.Request, method, path string) {
	t.Helper()
	if r.Method != method {
		t.Errorf("method = %s, want %s", r.Method, method)
	}
	if r.URL.Path != path {
		t.Errorf("path = %s, want %s", r.URL.Path, path)
	}
	if got := r.Header.Get("Salad-Api-Key"); got != "test-key" {
		t.Errorf("Salad-Api-Key = %q, want %q", got, "test-key")
	}
}

// must returns v and fails the test when err is set, on the test goroutine:
// must(os.ReadFile(p))(t).
func must[T any](v T, err error) func(*testing.T) T {
	return func(t *testing.T) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

// should is must for code off the test goroutine (a handler, a cleanup): it
// reports err and carries on.
func should[T any](v T, err error) func(*testing.T) T {
	return func(t *testing.T) T {
		t.Helper()
		if err != nil {
			t.Error(err)
		}
		return v
	}
}

// reportErr reports err without stopping the test, for a handler or a cleanup.
func reportErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Error(err)
	}
}

// assertAs returns v as a T and fails the test when it holds something else.
func assertAs[T any](t *testing.T, v any) T { //nolint:ireturn // returns the caller's type parameter
	t.Helper()
	x, ok := v.(T)
	if !ok {
		t.Fatalf("got %T, want %T", v, x)
	}
	return x
}

// assertAsOrError is assertAs for a handler: it reports a mismatch and returns
// T's zero value.
func assertAsOrError[T any](t *testing.T, v any) T { //nolint:ireturn // returns the caller's type parameter
	t.Helper()
	x, ok := v.(T)
	if !ok {
		t.Errorf("got %T, want %T", v, x)
	}
	return x
}

// valueAs returns v as a T, or T's zero value when it holds something else
// (a field missing from a decoded document).
func valueAs[T any](v any) T { //nolint:ireturn // returns the caller's type parameter
	if x, ok := v.(T); ok {
		return x
	}
	var zero T
	return zero
}
