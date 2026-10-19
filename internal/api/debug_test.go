package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

func TestRedactText(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		`{"secret_key":"abc123=="}`:                                                     `{"secret_key":"REDACTED"}`,
		`{"api_key": "salad_cloud_user_x", "name":"a"}`:                                 `{"api_key":"REDACTED", "name":"a"}`,
		`{"registry_authentication":{"basic":{"username":"u","password":"p\"q"}}}`:      `{"registry_authentication":{"basic":{"username":"u","password":"REDACTED"}}}`,
		`{"url":"https://storage-api.salad.com/organizations/o/files/a.wav?token=t-1"}`: `{"url":"https://storage-api.salad.com/organizations/o/files/a.wav?REDACTED"}`,
		`https://api.salad.com/api/public/organizations/o/containers?page=2`:            `https://api.salad.com/api/public/organizations/o/containers?page=2`,
	} {
		if got := redactText(in); got != want {
			t.Errorf("redactText(%s)\n got %s\nwant %s", in, got, want)
		}
	}
}

// debugRun makes one API call at level and returns what --debug printed.
func debugRun(t *testing.T, level DebugLevel) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		respond(t, w, `{"secret_key":"s3cr3t"}`)
	}))
	defer srv.Close()
	buf, opts := debugCapture(t, level)
	c := newTestClient(t, srv.URL, opts)
	if _, cerr := c.GetWebhookSecret(t.Context()); cerr != nil {
		t.Fatal(cerr)
	}
	return buf.String()
}

func TestDebugRequestsLogsOneLinePerRequest(t *testing.T) {
	t.Parallel()
	out := debugRun(t, DebugRequests)
	if !strings.Contains(out, "debug: GET http") || !strings.Contains(out, "/webhook-secret-key -> 200 (") {
		t.Errorf("debug output = %q", out)
	}
	if strings.Contains(out, "test-key") || strings.Contains(out, "s3cr3t") || strings.Count(out, "\n") != 1 {
		t.Errorf("--debug printed more than the request line: %q", out)
	}
}

func TestDebugBodiesRedactCredentials(t *testing.T) {
	t.Parallel()
	out := debugRun(t, DebugBodies)
	for _, want := range []string{"debug: > Salad-Api-Key: REDACTED", `debug: < {"secret_key":"REDACTED"}`} {
		if !strings.Contains(out, want) {
			t.Errorf("debug output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "test-key") || strings.Contains(out, "s3cr3t") {
		t.Errorf("a credential reached the debug log:\n%s", out)
	}
}

func TestDebugOffLogsNothing(t *testing.T) {
	t.Parallel()
	if out := debugRun(t, DebugOff); out != "" {
		t.Errorf("debug off printed %q", out)
	}
}

// debugCapture returns client options that send --debug output at level to
// the returned buffer.
func debugCapture(t *testing.T, level DebugLevel) (*bytes.Buffer, Options) {
	t.Helper()
	var buf bytes.Buffer
	opts := testOptions(nil)
	opts.Debug, opts.DebugOut = level, &buf
	return &buf, opts
}

// A container group definition carries credentials in places no field name
// gives away: a log sink's and a probe's header values, and environment
// variable values. None reaches --debug=body, in the request or the echoed
// response; the names around them still do.
func TestDebugBodiesRedactDefinitionSecrets(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := should(io.ReadAll(r.Body))(t)
		respond(t, w, string(body))
	}))
	t.Cleanup(srv.Close)
	buf, opts := debugCapture(t, DebugBodies)
	opts.Getenv = getenvOf(baseURLs(srv.URL))
	c, cerr := New(config.Resolved{APIKey: "test-key", Organization: "o", Project: "p"}, opts)
	if cerr != nil {
		t.Fatal(cerr)
	}
	def := `{"name":"g","container":{"image":"busybox",
		"environment_variables":{"HF_TOKEN":"hf-secret"},
		"logging":{"http":{"host":"logs.example.com","headers":[{"name":"Authorization","value":"Bearer sink-secret"}]}},
		"registry_authentication":{"basic":{"username":"u","password":"reg-secret"}}},
		"liveness_probe":{"http":{"path":"/","headers":[{"name":"X-Key","value":"probe-secret"}]}}}`
	if _, cerr := c.CreateGroupRaw(t.Context(), json.RawMessage(def)); cerr != nil {
		t.Fatal(cerr)
	}
	out := buf.String()
	for _, secret := range []string{"hf-secret", "sink-secret", "reg-secret", "probe-secret", "test-key"} {
		if strings.Contains(out, secret) {
			t.Errorf("%s reached the debug log:\n%s", secret, out)
		}
	}
	for _, kept := range []string{`"HF_TOKEN":"REDACTED"`, `"name":"Authorization"`, `"username":"u"`, "debug: > {", "debug: < {"} {
		if !strings.Contains(out, kept) {
			t.Errorf("debug log lacks %s:\n%s", kept, out)
		}
	}
}

// The gateway's bearer token is a header, and is redacted like the API key.
func TestDebugBodiesRedactAuthorizationHeader(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		respond(t, w, `{"object":"list","data":[]}`)
	}))
	t.Cleanup(srv.Close)
	buf, opts := debugCapture(t, DebugBodies)
	if _, cerr := newTestGateway(t, srv.URL, opts).ListModels(t.Context()); cerr != nil {
		t.Fatal(cerr)
	}
	if out := buf.String(); !strings.Contains(out, "debug: > Authorization: REDACTED") ||
		strings.Contains(out, "gw-key") {
		t.Errorf("gateway debug log:\n%s", out)
	}
}

// A file upload's body is never shown.
func TestDebugBodiesHideUploads(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		should(io.Copy(io.Discard, r.Body))(t)
		respond(t, w, `{"url":"`+"http://"+r.Host+`/f.wav?token=t"}`)
	}))
	t.Cleanup(srv.Close)
	buf, opts := debugCapture(t, DebugBodies)
	c := newTestClient(t, srv.URL, opts)
	_, cerr := c.UploadFile(
		t.Context(),
		"f.wav",
		strings.NewReader("audio-bytes"),
		"audio/wav",
		false,
		0,
	)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if out := buf.String(); !strings.Contains(out, "(file upload, not shown)") || strings.Contains(out, "audio-bytes") {
		t.Errorf("upload debug log:\n%s", out)
	}
}

func TestRedactBody(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		`{"headers":{"Authorization":"Bearer x"}}`: `{"headers":{"Authorization":"REDACTED"}}`,
		`{"b":1,"a":[true,null,"<s>"]}`:            `{"a":[true,null,"<s>"],"b":1}`,
		`not json "password":"p"`:                  `not json "password":"REDACTED"`,
	} {
		if got := redactBody([]byte(in)); got != want {
			t.Errorf("redactBody(%s)\n got %s\nwant %s", in, got, want)
		}
	}
}

// Every client the CLI has sends its User-Agent: the API, the AI Gateway,
// S4, and doctor's reachability probe.
func TestUserAgentOnEveryClient(t *testing.T) {
	t.Parallel()
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("User-Agent"))
		should(io.Copy(io.Discard, r.Body))(t)
		respond(t, w, `{"url":"u","object":"list","data":[]}`)
	}))
	t.Cleanup(srv.Close)
	ctx := t.Context()
	c := newTestClient(t, srv.URL)
	if _, cerr := c.GetWebhookSecret(ctx); cerr != nil {
		t.Fatal(cerr)
	}
	if _, cerr := newTestGateway(t, srv.URL).ListModels(ctx); cerr != nil {
		t.Fatal(cerr)
	}
	if _, cerr := c.UploadFile(ctx, "f", strings.NewReader("x"), "text/plain", false, 0); cerr != nil {
		t.Fatal(cerr)
	}
	must(Probe(ctx, srv.URL+"/probe", testOptions(nil)))(t)
	if len(got) != 4 {
		t.Fatalf("saw %d requests, want 4", len(got))
	}
	for i, ua := range got {
		if ua != userAgent() {
			t.Errorf("request %d User-Agent = %q, want %q", i, ua, userAgent())
		}
	}
}
