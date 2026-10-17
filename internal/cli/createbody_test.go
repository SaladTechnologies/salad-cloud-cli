package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// probeSource is a stored group as the API returns it: empty lists come back
// as [], including an HTTP probe's headers.
const probeSource = `{"id":"g-1","name":"src","display_name":"src","organization_name":"my-org","project_name":"default",
"container":{"image":"docker.io/library/busybox:1.36","command":[],"resources":{"cpu":1,"memory":1024,"gpu_classes":[]},"priority":"batch"},
"country_codes":[],"replicas":1,"restart_policy":"always","autostart_policy":false,
"readiness_probe":{"http":{"path":"/","port":8000,"scheme":"http","headers":[]},"initial_delay_seconds":0,"period_seconds":5,"timeout_seconds":2,"success_threshold":1,"failure_threshold":3},
"current_state":{"status":"stopped"},"version":1}`

// stubCreate serves create's calls and records the POST body.
func stubCreate(t *testing.T) *[]byte {
	t.Helper()
	var posted []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers"):
			posted, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"name":"new","current_state":{"status":"pending"},"version":1}`))
		case strings.HasSuffix(r.URL.Path, "/containers/src"):
			_, _ = w.Write([]byte(probeSource))
		case strings.HasSuffix(r.URL.Path, "/queues/jobs"):
			_, _ = w.Write([]byte(`{"name":"jobs","container_groups":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("SALAD_CLI_API_BASE_URL", srv.URL)
	t.Setenv("SALAD_CLI_CONFIG_DIR", t.TempDir())
	t.Setenv("SALAD_API_KEY", "salad_cloud_user_test0000")
	t.Setenv("SALAD_ORGANIZATION", "my-org")
	t.Setenv("SALAD_PROJECT", "default")
	return &posted
}

func postedPath(t *testing.T, body []byte, path string) (any, bool) {
	t.Helper()
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("posted body is not JSON: %s", body)
	}
	return lookupPath(doc, path)
}

// An empty list in a definition file reaches the API: the SDK's omitempty
// used to drop it, and the API rejects an HTTP probe without its headers.
func TestCreateSendsEmptyListsFromFile(t *testing.T) {
	posted := stubCreate(t)
	file := filepath.Join(t.TempDir(), "g.yaml")
	def := "readiness_probe:\n  http: {path: /, port: 8000, scheme: http, headers: []}\n" +
		"  initial_delay_seconds: 0\n  period_seconds: 5\n  timeout_seconds: 2\n  success_threshold: 1\n  " +
		"failure_threshold: 3\n"
	if err := os.WriteFile(file, []byte(def), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runRoot(
		t,
		"container-groups",
		"create",
		"new",
		"--image",
		"busybox:1.36",
		"-f",
		file,
		"--no-autostart",
		"-o",
		"json",
	); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got, ok := postedPath(t, *posted, "readiness_probe.http.headers"); !ok || !jsonEqual(got, []any{}) {
		t.Errorf("readiness_probe.http.headers = %v (present %v), want []; body %s", got, ok, *posted)
	}
	// Nothing the file and flags did not set appears.
	if _, ok := postedPath(t, *posted, "country_codes"); ok {
		t.Errorf("country_codes sent though nothing set it: %s", *posted)
	}
}

// --from sends back the empty lists the API returned for the source.
func TestCreateFromKeepsSourceEmptyLists(t *testing.T) {
	posted := stubCreate(t)
	_, err := runRoot(
		t,
		"container-groups",
		"create",
		"new",
		"--from",
		"src",
		"--no-autostart",
		"-o",
		"json",
	)
	if err != nil {
		t.Fatalf("create --from: %v", err)
	}
	for _, path := range []string{"readiness_probe.http.headers", "country_codes"} {
		if got, ok := postedPath(t, *posted, path); !ok || !jsonEqual(got, []any{}) {
			t.Errorf("%s = %v (present %v), want []; body %s", path, got, ok, *posted)
		}
	}
}

// A create that connects a queue says on stderr that the image must run the
// worker, since nothing reports a missing one; one without a queue does not.
func TestCreateQueueNotesTheWorker(t *testing.T) {
	stubCreate(t)
	var err error
	errOut := captureStderr(t, func() {
		_, err = runRoot(t, "container-groups", "create", "new", "--image", "busybox:1.36", "--no-autostart",
			"--queue", "jobs", "--queue-port", "8000", "--queue-path", "/", "-o", "json")
	})
	if err != nil {
		t.Fatalf("create --queue: %v", err)
	}
	if !strings.Contains(errOut, `note: jobs from queue jobs reach`) || !strings.Contains(errOut, "job queue worker") ||
		!strings.Contains(errOut, queueWorkerDocs) {
		t.Errorf("stderr = %q, want the worker note", errOut)
	}

	resetFlags() // the first run's --queue would otherwise carry over
	stubCreate(t)
	errOut = captureStderr(t, func() {
		_, err = runRoot(
			t,
			"container-groups",
			"create",
			"new",
			"--image",
			"busybox:1.36",
			"--no-autostart",
			"-o",
			"json",
		)
	})
	if err != nil || strings.Contains(errOut, "job queue worker") {
		t.Errorf("create without a queue: err %v, stderr %q", err, errOut)
	}
}
