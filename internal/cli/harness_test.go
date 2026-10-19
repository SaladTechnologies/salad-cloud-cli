package cli

// The whole-command harness: one command line, run in-process through
// Execute against a fake SaladCloud API and AI Gateway, with stdin, the
// config file, the environment and (when there is one) the terminal set up
// first. The invariant tests and the setup and context commands' tests
// (login, use, config, whoami, doctor and others) drive it.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

// Credentials the harness hands the CLI. Each has a distinctive word in its
// middle, so a leak shows even when cut; their masks end in the last four.
const (
	testAPIKey          = "salad_cloud_user_SentinelApiKey7fqH13m" // ****...H13m
	testOtherAPIKey     = "salad_cloud_user_OtherApiKey31xyABCD"   // ****...ABCD
	testGatewayKey      = "sk-gw-SentinelGatewayKey42ruEFGH"       // ****...EFGH
	testOtherGatewayKey = "sk-gw-OtherGatewayKey88zqWXYZ"          // ****...WXYZ
)

// testSecrets are the credentials no output may print whole.
var testSecrets = []string{testAPIKey, testOtherAPIKey, testGatewayKey, testOtherGatewayKey}

// fakeAPI is the SaladCloud API and AI Gateway as the harness serves them:
// every API key in keys sees the organizations in orgs, each with its
// projects; a gateway key's completion answers with its status in
// gatewayKeys (absent: 401).
type fakeAPI struct {
	keys        map[string]bool
	orgs        map[string][]string
	orgOrder    []string // the listing's order; nil: sorted by name
	display     map[string]string
	listing     int            // status of the organizations listing; 0 answers 200 with the names
	status      map[string]int // a path suffix answered with this status instead
	gatewayKeys map[string]int
	// cancelOn is a path suffix at which the fake presses Ctrl-C: it cancels
	// the run and holds the request until the CLI gives up on it.
	cancelOn string
	cancel   func()
	// t is the test of the session serving the fake, which its handlers
	// report to.
	t *testing.T

	mu       sync.Mutex
	requests []string // "METHOD path", in order
}

// newFakeAPI is the default account: testAPIKey sees my-org (projects
// default and staging) and acme-prod (default only); testGatewayKey works.
func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		keys:        map[string]bool{testAPIKey: true},
		orgs:        map[string][]string{"my-org": {"default", "staging"}, "acme-prod": {"default"}},
		display:     map[string]string{"my-org": "My Org", "acme-prod": "Acme Production"},
		status:      make(map[string]int),
		gatewayKeys: map[string]int{testGatewayKey: http.StatusOK},
	}
}

// Requests returns the requests the fake received, as "METHOD path".
func (f *fakeAPI) Requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	if f.cancelOn != "" && strings.HasSuffix(path, f.cancelOn) {
		f.cancel()
		// Read the body first: until it has, the server cannot see the
		// client hang up.
		should(io.Copy(io.Discard, r.Body))(f.t)
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		return
	}
	for suffix, code := range f.status {
		if strings.HasSuffix(path, suffix) {
			w.WriteHeader(code)
			return
		}
	}
	if strings.HasPrefix(path, "/gw/") {
		f.serveGateway(w, r, strings.TrimPrefix(path, "/gw"))
		return
	}
	if path == "/v1/organizations" && f.listing != 0 {
		w.WriteHeader(f.listing) // the listing is down for every key
		return
	}
	if !f.keys[r.Header.Get("Salad-Api-Key")] {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case path == "/v1/organizations":
		items := []map[string]any{}
		order := f.orgOrder
		if order == nil {
			order = sortedKeys(f.orgs)
		}
		for _, name := range order {
			items = append(items, map[string]any{
				"organization_name": name, "display_name": f.display[name],
				"organization_id": "o-" + name, "create_time": "2026-01-01T00:00:00Z",
			})
		}
		reportErr(f.t, json.NewEncoder(w).Encode(map[string]any{"items": items}))
	case len(parts) >= 2 && parts[0] == "organizations":
		projects, ok := f.orgs[parts[1]]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch {
		case len(parts) == 3 && parts[2] == "quotas":
			respond(f.t, w, `{}`)
		case len(parts) == 3 && parts[2] == "gpu-classes":
			respond(f.t, w, `{"items":[]}`)
		case len(parts) == 7 && parts[2] == "projects" && parts[4] == "containers" && parts[6] == "instances":
			respond(
				f.t,
				w,
				`{"instances":[{"id":"8a2f13ba-01c2-4a29-9c4d-52c7e5a44e37","state":"running","ssh_ip":"203.0.113.7","ssh_port":30123}]}`,
			)
		case len(parts) == 5 && parts[2] == "projects" && parts[4] == "containers" && r.Method == http.MethodGet:
			if !slices.Contains(projects, parts[3]) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			respond(f.t, w, `{"items":[]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeAPI) serveGateway(w http.ResponseWriter, r *http.Request, path string) {
	switch path {
	case "/models":
		respond(f.t, w, `{"object":"list","data":[{"id":"model-a","owned_by":"salad"}]}`)
	case "/chat/completions":
		code, ok := f.gatewayKeys[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if !ok {
			code = http.StatusUnauthorized
		}
		switch code {
		case http.StatusOK:
			respond(
				f.t,
				w,
				`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"total_tokens":2}}`,
			)
		case http.StatusPaymentRequired:
			w.WriteHeader(code)
			respond(f.t, w, `{"error":{"code":"credits_exhausted","message":"out of credits"}}`)
		case http.StatusForbidden:
			w.WriteHeader(code)
			respond(f.t, w, `{"error":{"code":"no_access","message":"no access"}}`)
		default:
			w.WriteHeader(code)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Scripted answers that are not text.
const (
	keyCtrlC = "\x03" // the person presses Ctrl-C
	keyEOF   = "\x04" // the terminal reports end of input
)

// fakeConsole is a terminal whose person gives the scripted answers in
// order, then end of input.
type fakeConsole struct {
	terminal   bool
	answers    []string
	device     bytes.Buffer
	transcript *bytes.Buffer // when set, the device's lines are recorded here too
}

func (c *fakeConsole) Terminal() bool { return c.terminal }

func (c *fakeConsole) Device() io.Writer {
	if c.transcript != nil {
		return io.MultiWriter(&c.device, c.transcript)
	}
	return &c.device
}
func (c *fakeConsole) ReadHidden() (string, error) { return c.next(true) }
func (c *fakeConsole) ReadLine() (string, error)   { return c.next(false) }

// echo writes s to the device and the transcript, as Device does; a write to
// a bytes.Buffer does not fail.
func (c *fakeConsole) echo(s string) {
	c.device.WriteString(s)
	if c.transcript != nil {
		c.transcript.WriteString(s)
	}
}

// next gives the next answer and leaves the device as a terminal would: a
// visible answer echoed, then the line ended.
func (c *fakeConsole) next(hidden bool) (string, error) {
	defer c.echo("\n")
	if len(c.answers) == 0 {
		return "", io.EOF
	}
	a := c.answers[0]
	c.answers = c.answers[1:]
	switch a {
	case keyCtrlC:
		return "", errInterrupt
	case keyEOF:
		return "", io.EOF
	}
	if !hidden {
		c.echo(a)
	}
	return a, nil
}

// session is one command line and the world it runs in.
type session struct {
	args  []string
	env   map[string]string // the run's variables besides the harness's own; nothing comes from the process
	saved *config.File      // the config file before the run; nil: no file
	// rawConfig, when set, is the config file's content before the run.
	rawConfig string
	// os is the operating system the CLI is told it runs on; "": windows.
	os    string
	stdin string   // what stdin holds (not a terminal)
	tty   []string // non-nil: stdin is a terminal, and these are the answers
	api   *fakeAPI // nil: newFakeAPI()
	// sshKey puts a public key at ~/.ssh/id_ed25519.pub in the run's home
	// (sshKeyName names another file).
	sshKey     bool
	sshKeyName string
	// openStdin makes stdin a pipe that is never closed: a run that reads it
	// waits until stdinPatience runs out (outcome.hung).
	openStdin bool
	// deps, when set, changes the run's seams (the sftp program, ssh's
	// presence) before it runs.
	deps func(d *Deps)
}

// outcome is what a session's run did.
type outcome struct {
	code   int
	stdout string
	stderr string
	device string // what was written to the terminal device
	opened []string
	config config.File // the config file afterward
	api    *fakeAPI
	// transcript is stdout, the setup commands' stderr and the terminal, in
	// the order they were written: what a person at the terminal saw.
	transcript string
	hung       bool // still running after stdinPatience (openStdin)
	// configPath is where the run's config file was, apiURL the fake API's
	// address and home the run's home folder.
	configPath, apiURL, home string
}

func (s session) run(t *testing.T) outcome {
	t.Helper()
	api := s.api
	if api == nil {
		api = newFakeAPI()
	}
	// The run's context, which the fake cancels at cancelOn; set before the
	// server starts, so its handlers see it.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	api.cancel = cancel
	api.t = t
	srv := httptest.NewServer(api)
	defer srv.Close()

	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "config")
	home := filepath.Join(dir, "home")
	env := map[string]string{
		"SALAD_CLI_CONFIG_DIR":          cfgDir,
		"SALAD_CLI_API_BASE_URL":        srv.URL,
		"SALAD_CLI_AI_GATEWAY_BASE_URL": srv.URL + "/gw",
		"HOME":                          home,
		"USERPROFILE":                   home,
	}
	maps.Copy(env, s.env)
	d := testDeps(env)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if s.sshKey {
		name := s.sshKeyName
		if name == "" {
			name = "id_ed25519.pub"
		}
		if err := os.WriteFile(
			filepath.Join(home, ".ssh", name),
			[]byte("ssh-ed25519 AAAA test@host\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
	if s.saved != nil {
		if err := config.Save(d.Env, *s.saved); err != nil {
			t.Fatal(err)
		}
	}
	if s.rawConfig != "" {
		if err := os.MkdirAll(cfgDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(s.rawConfig), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	configPath := must(config.Path(d.Env))(t)

	stdin := tempFile(t, dir, "stdin", s.stdin)
	var hold *os.File // the open end of a never-closed stdin
	if s.openStdin {
		noErr(t, stdin.Close())
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		stdin, hold = r, w
	}
	stderr := tempFile(t, dir, "stderr", "")
	var stdout, transcript bytes.Buffer
	fc := &fakeConsole{terminal: s.tty != nil, answers: append([]string(nil), s.tty...), transcript: &transcript}
	var opened []string

	d.Args = s.args
	d.Stdin = stdin
	d.Stdout = io.MultiWriter(&stdout, &transcript)
	d.Stderr = io.MultiWriter(stderr, &transcript)
	d.Console = fc
	d.OpenPage = func(url string) error { opened = append(opened, url); return nil }
	d.Env.GOOS = s.os
	if d.Env.GOOS == "" {
		d.Env.GOOS = "windows"
	}
	if s.deps != nil {
		s.deps(&d)
	}
	done := make(chan int, 1)
	go func() { done <- Run(ctx, d) }()
	var code int
	hung := false
	if hold != nil {
		select {
		case code = <-done:
		case <-time.After(stdinPatience):
			hung = true
			noErr(t, hold.Close()) // let the stuck read end so the run can finish
			code = <-done
		}
		if !hung { // a hung run's hold is closed already
			noErr(t, hold.Close())
		}
	} else {
		code = <-done
	}
	noErr(t, stdin.Close())
	noErr(t, stderr.Close())

	errBytes := must(os.ReadFile(stderr.Name()))(t)
	after, err := config.Load(d.Env)
	if err != nil && s.rawConfig == "" {
		t.Fatalf("the config file after the run: %v", err)
	}
	return outcome{
		code: code, stdout: stdout.String(), stderr: string(errBytes), device: fc.device.String(),
		opened: opened, config: after, api: api, hung: hung, transcript: transcript.String(), configPath: configPath,
		apiURL: srv.URL, home: home,
	}
}

// tempFile creates dir/name holding content, open for reading and writing.
func tempFile(t *testing.T, dir, name, content string) *os.File {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	return f
}

// placeholders replaces the run's config path, fake API address and home
// folder with <path>, <api> and <home>.
func (o outcome) placeholders(s string) string {
	s = strings.ReplaceAll(s, o.configPath, "<path>")
	s = strings.ReplaceAll(s, o.apiURL, "<api>")
	return strings.ReplaceAll(s, o.home, "<home>")
}

// all is everything a person or a log could see from the run.
func (o outcome) all() string { return o.stdout + "\n" + o.stderr + "\n" + o.device }

// lines splits text into its non-empty lines.
func lines(s string) []string {
	var out []string
	for l := range strings.SplitSeq(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// errorDocument decodes the error document on the last stderr line.
func (o outcome) errorDocument() (map[string]any, error) {
	ls := lines(o.stderr)
	if len(ls) == 0 {
		return nil, errors.New("stderr is empty")
	}
	var doc map[string]map[string]any
	if err := json.Unmarshal([]byte(ls[len(ls)-1]), &doc); err != nil {
		return nil, err
	}
	e, ok := doc["error"]
	if !ok {
		return nil, errors.New(`no "error" object`)
	}
	return e, nil
}

// document decodes stdout as one JSON document.
func (o outcome) document(t *testing.T) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(o.stdout), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, o.stdout)
	}
	return doc
}

// respond writes body as an httptest handler's response. The handler runs on
// its own goroutine, so a failed write is reported, not fatal.
func respond(t *testing.T, w io.Writer, body string) {
	t.Helper()
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("writing the response: %v", err)
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

// noErr fails the test when err is set.
func noErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
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
func assertAs[T any](t *testing.T, v any) T {
	t.Helper()
	x, ok := v.(T)
	if !ok {
		t.Fatalf("got %T, want %T", v, x)
	}
	return x
}
