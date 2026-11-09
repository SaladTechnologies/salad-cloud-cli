//go:build live

// Live smoke suite: end-to-end tests against the real API, driving the built
// binary rather than the packages, so exit codes and stream discipline are
// what get tested. Everything else about this CLI is covered by the offline
// unit tests; this tier exists only for the handful of things that cannot be
// faked — real capacity, real transcription, real quota, real name
// reservation.
//
//	go test -tags live ./internal/livetest/ -timeout 30m -v
//
// The live build tag keeps it out of a plain `go test ./...`, which never
// touches the network or spends money. With the tag, TestMain fails the run
// (exit 1) unless SALAD_API_KEY and SALAD_ORGANIZATION are both set, so a
// passing run always means the suite ran.
//
// It is deliberately not exhaustive: exploratory cases belong to a manual
// pass, and re-running them on a schedule would cost wall-clock time and
// money to re-derive what the unit tests already pin. What is here is the
// end-to-end spine plus the API behaviors that only show up live.
//
// Conventions this suite encodes:
//
//   - Every group name carries a per-run random suffix. Deleted names stay
//     reserved for well over a minute, so reusing one fails name_conflict even
//     though list/get report the group gone.
//   - Creates are spaced. A group counts against the replica quota during the
//     few seconds it sits pending after create, so a tight create loop trips
//     created_replicas_quota_exceeded even when every group ends up stopped.
//   - Updates settle first. Back-to-back patches are rejected with
//     pending_update_in_progress; `update` settles before returning by
//     default, which serializes them.
//   - Anything that runs uses `sleep 2147483647`, never `sleep infinity`:
//     busybox rejects the latter, the instance fails at start, and the
//     platform reallocates it, so the group loops across nodes, never
//     settles, and the wait times out.
//   - --wait timeouts are generous. Batch-priority capacity can take many
//     minutes to allocate.
//   - ssh needs the account's one public key registered in the portal, which
//     no API exposes, so `doctor` reports the local key as present with its
//     registration not checked.
//     The key must also be on the machine running the binary: SALAD_CLI_TEST_BIN
//     pointing at the linux build under WSL offers WSL's ~/.ssh key, not the
//     Windows host's registered one, and is denied every time. testSSH
//     therefore skips on a persistent publickey denial rather than failing —
//     it is an environment gap, not a CLI bug — but still fails on
//     anything else, so a broken relay or argv wrapper is caught.
package livetest

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// prefix is unique per run so a previous run's reserved names cannot collide: lt- and six random lowercase base32
// characters.
var prefix = "lt-" + strings.ToLower(rand.Text()[:6]) //nolint:gochecknoglobals // shared by the whole run

// binPath locates the CLI under test: $SALAD_CLI_TEST_BIN, else the dist build
// for this platform.
func binPath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("SALAD_CLI_TEST_BIN"); p != "" {
		return p
	}
	name := "saladcloud"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	p := filepath.Join("..", "..", "dist", runtime.GOOS+"-"+runtime.GOARCH, name)
	if _, err := os.Stat(p); err != nil {
		t.Skipf("no CLI binary at %s (run `just build`, or set SALAD_CLI_TEST_BIN)", p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("resolving %s: %v", p, err)
	}
	return abs
}

// result is one CLI invocation, with the streams kept apart — most of this
// CLI's contract is about which stream output lands on.
type result struct {
	Code   int
	Stdout string
	Stderr string
}

// run invokes the CLI. It never fails the test on a nonzero exit: exit codes
// are assertions here, not accidents. With SALAD_PROJECT unset the CLI is
// given the project the suite uses (liveProject), so it never warns about an
// assumed one and stderr carries only what a test checks.
func runEnv(t *testing.T, extraEnv map[string]string, args ...string) result {
	t.Helper()
	//nolint:gosec // G204, G702: runs the CLI under test with the test's own arguments
	cmd := exec.CommandContext(t.Context(), binPath(t), args...)
	cmd.Env = os.Environ()
	if os.Getenv("SALAD_PROJECT") == "" {
		cmd.Env = append(cmd.Env, "SALAD_PROJECT="+liveProject())
	}
	for k, v := range extraEnv {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running %v: %v", args, err)
	}
	t.Logf("$ saladcloud %s -> exit %d", strings.Join(args, " "), code)
	return result{Code: code, Stdout: out.String(), Stderr: errb.String()}
}

// run executes the CLI under test with the inherited environment.
func run(t *testing.T, args ...string) result {
	t.Helper()
	return runEnv(t, nil, args...)
}

// oneDoc parses stdout as exactly one JSON document.
func oneDoc(t *testing.T, r result) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(r.Stdout), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, r.Stdout)
	}
	return doc
}

// name returns a run-unique group name.
func name(suffix string) string { return prefix + "-" + suffix }

// settle polls until the group has no pending change. A freshly created
// group is pending for a few seconds, and `update` cannot help with that:
// it settles *after* its own patch, so a patch issued during the create
// window is rejected outright with pending_update_in_progress. Every write
// that follows a create needs this.
func settle(t *testing.T, group string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		r := run(t, "container-groups", "get", group, "-o", "json")
		if r.Code != 0 {
			t.Fatalf("settle: get %s exit %d: %s", group, r.Code, r.Stderr)
		}
		var doc struct {
			PendingChange bool `json:"pending_change"`
		}
		if err := json.Unmarshal([]byte(r.Stdout), &doc); err != nil {
			t.Fatalf("settle: %v", err)
		}
		if !doc.PendingChange {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("settle: %s still pending after 2m", group)
}

// createSpacing keeps a create loop from tripping the pending-window quota.
const createSpacing = 3 * time.Second

// fprintf writes the suite's own messages (TestMain's and the sweep's) to
// stdout, where go test shows them; a failed write leaves nothing to tell.
func fprintf(format string, a ...any) {
	if _, err := fmt.Fprintf(os.Stdout, format, a...); err != nil {
		return
	}
}

// fprintln is fprintf for one line.
func fprintln(line string) { fprintf("%s\n", line) }

// TestMain fails an unconfigured run instead of passing with no tests: the
// live suite is the check run before a release is tagged (see Releasing in
// AGENTS.md), so "ok" must mean it ran.
func TestMain(m *testing.M) {
	if os.Getenv("SALAD_API_KEY") == "" || os.Getenv("SALAD_ORGANIZATION") == "" {
		fprintln("livetest: SALAD_API_KEY and SALAD_ORGANIZATION must both be set; nothing ran")
		os.Exit(1) //nolint:forbidigo // an unconfigured run must fail, not pass with no tests
	}
	m.Run()
	sweep(context.Background()) //nolint:forbidigo // TestMain is the test binary's main, and no test is left to ask
}

// sweep deletes everything this run created, by prefix. Best-effort: a leaked
// stopped group costs nothing, but leaking is still untidy.
func sweep(ctx context.Context) {
	bin := sweepBin()
	out, err := sweepCommand(ctx, bin, "container-groups", "list", "-o", "json").Output()
	if err != nil {
		fprintf("livetest sweep: list failed: %v\n", err)
		return
	}
	if !sweepDelete(ctx, bin, "container-groups", "", out) {
		return
	}
	// Queues after groups: a queue with a connected group may refuse deletion.
	out, err = sweepCommand(ctx, bin, "queues", "list", "-o", "json").Output()
	if err != nil {
		return
	}
	sweepDelete(ctx, bin, "queues", "queue ", out)
}

// sweepCommand is the CLI under test at bin, run by the sweep with args.
func sweepCommand(ctx context.Context, bin string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, bin, args...) //nolint:gosec // G204: the CLI under test, with the sweep's own args
}

// sweepBin is binPath without a test to skip: $SALAD_CLI_TEST_BIN, else the dist build for this platform.
func sweepBin() string {
	if bin := os.Getenv("SALAD_CLI_TEST_BIN"); bin != "" {
		return bin
	}
	name := "saladcloud"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join("..", "..", "dist", runtime.GOOS+"-"+runtime.GOARCH, name)
}

// sweepListing is the part of a list document the sweep reads: each item's name.
type sweepListing struct {
	Items []sweepItem `json:"items"`
}

// sweepItem is one item of a sweepListing.
type sweepItem struct {
	Name string `json:"name"`
}

// sweepDelete deletes every item in noun's list document out whose name carries this run's prefix, naming each
// as label plus its name. It reports false when out is not a list document, which ends the sweep.
func sweepDelete(ctx context.Context, bin, noun, label string, out []byte) bool {
	var listing sweepListing
	if json.Unmarshal(out, &listing) != nil {
		return false
	}
	for _, it := range listing.Items {
		if strings.HasPrefix(it.Name, prefix) {
			fprintf("livetest sweep: deleting %s%s\n", label, it.Name)
			if runErr := sweepCommand(ctx, bin, noun, "delete", it.Name).Run(); runErr != nil {
				fprintf("livetest sweep: deleting %s%s failed: %v\n", label, it.Name, runErr)
			}
		}
	}
	return true
}

// TestPreflight: the environment is usable at all.
func TestPreflight(t *testing.T) { //nolint:paralleltest // runs first so a bad environment fails fast
	if r := run(t, "doctor"); r.Code != 0 {
		t.Fatalf("doctor exit %d: %s", r.Code, r.Stderr)
	}
	r := run(t, "container-groups", "list", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("list exit %d: %s", r.Code, r.Stderr)
	}
	if !strings.Contains(r.Stdout, "items") {
		t.Errorf("list -o json should emit an items document: %s", r.Stdout)
	}
	// A working key belongs to at least its own organization, so the
	// listing is an array; null would mean the listing was unavailable.
	who := oneDoc(t, run(t, "whoami", "-o", "json"))
	if orgs, ok := who["organizations"].([]any); !ok || len(orgs) == 0 {
		t.Errorf("whoami organizations = %v, want a non-empty array", who["organizations"])
	}
}

// TestLogScopes: logs with no container group searches the organization,
// and an explicit --project narrows it to that project. Every entry must
// carry the matching labels. Read-only; passes with no logs in the window.
func TestLogScopes(t *testing.T) {
	t.Parallel()
	org := os.Getenv("SALAD_ORGANIZATION")
	project := os.Getenv("SALAD_PROJECT")
	if project == "" {
		project = "default"
	}
	for _, tc := range []struct {
		name string
		args []string
		want map[string]string // label -> value every entry must have
	}{
		{
			"organization",
			[]string{"logs", "--since", "6h", "-o", "json"},
			map[string]string{"organization_name": org},
		},
		{
			"project",
			[]string{"logs", "--project", project, "--since", "6h", "-o", "json"},
			map[string]string{"organization_name": org, "project_name": project},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := run(t, tc.args...)
			if r.Code != 0 {
				t.Fatalf("exit %d: %s", r.Code, r.Stderr)
			}
			checkLogLabels(t, r.Stdout, tc.want)
		})
	}
}

// checkLogLabels checks that every NDJSON line of stdout is a complete log entry carrying each label in want with
// its value.
func checkLogLabels(t *testing.T, stdout string, want map[string]string) {
	t.Helper()
	for i, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if line == "" {
			continue
		}
		var entry logLabelsEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("line %d is not a complete JSON document: %s", i+1, line)
		}
		for label, value := range want {
			if got := entry.Resource.Labels[label]; got != value {
				t.Errorf("line %d: %s = %q, want %q", i+1, label, got, value)
			}
		}
	}
}

// logLabelsEntry is the part of a log entry checkLogLabels reads.
type logLabelsEntry struct {
	Resource logLabelsResource `json:"resource"`
}

// logLabelsResource is a log entry's resource, with its labels.
type logLabelsResource struct {
	Labels map[string]string `json:"labels"`
}

// liveTempFile writes body to a file called name in a fresh temporary folder and returns its path.
func liveTempFile(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestErrorContract covers the exit codes and stream discipline that scripts
// depend on. All client-side or 404 paths, so nothing is created.
func TestErrorContract(t *testing.T) {
	t.Parallel()
	cases := []errorContractCase{
		{
			"not found is 4",
			[]string{"container-groups", "get", prefix + "-nope", "-o", "json"},
			4,
			"not_found",
			"404",
			true,
		},
		{
			"bad name is client-side 6",
			[]string{"container-groups", "create", "Bad_Name", "--image", "x", "--no-autostart", "-o", "json"},
			6,
			"invalid_input",
			"null",
			true,
		},
		{
			"missing --image is 2",
			[]string{"container-groups", "create", name("x"), "-o", "json"},
			2,
			"usage",
			"null",
			true,
		},
		{
			"an invalid -o cannot itself report in json",
			[]string{"container-groups", "list", "-o", "xml"},
			2,
			"",
			"",
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := run(t, tc.args...)
			if r.Code != tc.wantCode {
				t.Errorf("exit = %d, want %d (stderr: %s)", r.Code, tc.wantCode, r.Stderr)
			}
			// The contract: errors leave stdout empty and put one JSON
			// document on stderr.
			if strings.TrimSpace(r.Stdout) != "" {
				t.Errorf("stdout must be empty on error, got: %s", r.Stdout)
			}
			line := strings.TrimSpace(r.Stderr)
			if !tc.jsonError {
				// Table-mode error: one "saladcloud: error: ..." line.
				if !strings.HasPrefix(line, "saladcloud: error:") {
					t.Errorf("table-mode error should start with the CLI prefix: %s", line)
				}
				return
			}
			checkErrorContractDoc(t, &tc, line)
		})
	}
}

// errorContractCase is one TestErrorContract command line and the error it must report.
type errorContractCase struct {
	name     string
	args     []string
	wantCode int
	wantKind string
	// wantHTTP is the error document's http_status: "null" for a
	// client-side failure, the status otherwise.
	wantHTTP string
	// jsonError is false when the failure is the output mode itself: the
	// CLI never reached json mode, so it correctly reports in table form.
	jsonError bool
}

// errorContractDoc is the part of an error document checkErrorContractDoc reads.
type errorContractDoc struct {
	Error errorContractBody `json:"error"`
}

// errorContractBody is an errorContractDoc's error, with http_status kept raw so null and missing differ.
type errorContractBody struct {
	ExitCode   int             `json:"exit_code"`
	Kind       string          `json:"kind"`
	HTTPStatus json.RawMessage `json:"http_status"`
}

// checkErrorContractDoc checks that line, the trimmed stderr, is one JSON error document matching tc.
func checkErrorContractDoc(t *testing.T, tc *errorContractCase, line string) {
	t.Helper()
	var errDoc errorContractDoc
	if err := json.Unmarshal([]byte(line), &errDoc); err != nil {
		t.Fatalf("stderr is not one JSON error document: %s", line)
	}
	if errDoc.Error.ExitCode != tc.wantCode {
		t.Errorf("error.exit_code = %d, want %d", errDoc.Error.ExitCode, tc.wantCode)
	}
	if tc.wantKind != "" && errDoc.Error.Kind != tc.wantKind {
		t.Errorf("error.kind = %q, want %q", errDoc.Error.Kind, tc.wantKind)
	}
	// Every key is present: http_status is null, not missing, when
	// no HTTP response caused the error.
	if got := string(errDoc.Error.HTTPStatus); got != tc.wantHTTP {
		t.Errorf("error.http_status = %q, want %s (stderr: %s)", got, tc.wantHTTP, line)
	}
}

// TestGrammar covers the command-grammar rules against the real API: a bare
// resource noun is a usage error, every list is an items envelope, and the
// read verbs of the nouns answer. Read-only.
func TestGrammar(t *testing.T) {
	t.Parallel()
	grammarBareNouns(t)
	grammarListEnvelopes(t)
	grammarGets(t)
	if r := run(t, "inference", "get", "transcribe"); r.Code != 0 || !strings.Contains(r.Stdout, "Inputs") {
		t.Errorf("inference get transcribe exit %d: %s", r.Code, r.Stderr)
	}
	if r := run(t, "inference", "jobs", "list", "--endpoint", "transcribe", "--limit", "1", "-o", "json"); r.Code != 0 {
		t.Errorf("inference jobs list --endpoint exit %d: %s", r.Code, r.Stderr)
	}
	bare := run(t, "create")
	if bare.Code != 2 || !strings.Contains(bare.Stderr, "did you mean saladcloud container-groups create") {
		t.Errorf("bare create: exit %d stderr %q, want 2 with did-you-mean", bare.Code, bare.Stderr)
	}
}

// grammarBareNouns checks that a bare resource noun is a usage error with empty stdout.
func grammarBareNouns(t *testing.T) {
	t.Helper()
	for _, noun := range []string{"gpus", "orgs", "quotas", "queues", "inference"} {
		r := run(t, noun, "-o", "json")
		if r.Code != 2 || strings.TrimSpace(r.Stdout) != "" || !strings.Contains(r.Stderr, "needs a verb") {
			t.Errorf(
				"bare %s: exit %d stdout %q stderr %q, want 2, empty, needs a verb",
				noun,
				r.Code,
				r.Stdout,
				r.Stderr,
			)
		}
	}
}

// grammarListEnvelopes checks that every list is an items envelope.
func grammarListEnvelopes(t *testing.T) {
	t.Helper()
	for _, args := range [][]string{{"gpus", "list"}, {"orgs", "list"}, {"queues", "list"}, {"inference", "list"}} {
		r := run(t, append(args, "-o", "json")...)
		if r.Code != 0 {
			t.Errorf("%v exit %d: %s", args, r.Code, r.Stderr)
			continue
		}
		if _, ok := oneDoc(t, r)["items"].([]any); !ok {
			t.Errorf("%v: no items array: %s", args, r.Stdout)
		}
	}
}

// grammarGets checks that the get verbs of quotas, orgs and gpus answer, and an unknown organization is not found.
func grammarGets(t *testing.T) {
	t.Helper()
	if r := run(t, "quotas", "get", "-o", "json"); r.Code != 0 || oneDoc(t, r)["container_groups_quotas"] == nil {
		t.Errorf("quotas get exit %d: %s", r.Code, r.Stderr)
	}
	org := os.Getenv("SALAD_ORGANIZATION")
	if r := run(t, "orgs", "get", org, "-o", "json"); r.Code != 0 || oneDoc(t, r)["organization_name"] != org {
		t.Errorf("orgs get %s exit %d: %s %s", org, r.Code, r.Stdout, r.Stderr)
	}
	if r := run(t, "orgs", "get", prefix+"-no-such-org"); r.Code != 4 {
		t.Errorf("orgs get of an unknown org exit %d, want 4", r.Code)
	}
	if r := run(t, "gpus", "get", "rtx 3060 (12 gb)", "-o", "json"); r.Code != 0 || oneDoc(t, r)["prices"] == nil {
		t.Errorf("gpus get exit %d: %s", r.Code, r.Stderr)
	}
}

// TestAvailability asks once per class, as the command is meant to be used.
func TestAvailability(t *testing.T) {
	t.Parallel()
	one := run(t, "availability", "--gpu", "rtx 3060 (12 gb)", "-o", "json")
	if one.Code != 0 {
		t.Fatalf("availability exit %d: %s", one.Code, one.Stderr)
	}
	doc := oneDoc(t, one)
	items := valueAs[[]any](doc["items"])
	if len(items) != 1 {
		t.Errorf("availability one class = %s, want one item", one.Stdout)
	}
	// Two classes: one row each plus the combined query, never a sum.
	two := run(t, "availability", "--gpu", "rtx 3060 (12 gb)", "--gpu", "rtx 3090 (24 gb)", "-o", "json")
	if two.Code != 0 {
		t.Fatalf("availability with two classes exit %d: %s", two.Code, two.Stderr)
	}
	d2 := oneDoc(t, two)
	if items := valueAs[[]any](d2["items"]); len(items) != 2 {
		t.Errorf("two classes = %d items, want 2", len(items))
	}
	if c := valueAs[map[string]any](d2["combined"]); c == nil || len(valueAs[[]any](c["gpu_classes"])) != 2 {
		t.Errorf("combined = %v, want the combined query for both classes", d2["combined"])
	}
	cpu := run(t, "availability", "--cpu", "2", "--memory", "2048", "-o", "json")
	if cpu.Code != 0 {
		t.Fatalf("CPU availability exit %d: %s", cpu.Code, cpu.Stderr)
	}
	if items := valueAs[[]any](oneDoc(t, cpu)["items"]); len(items) != 1 {
		t.Errorf("CPU-only availability = %s, want one item", cpu.Stdout)
	}
}

// TestWebhookSecretGet reads the organization's webhook secret. It never
// rotates: that would break every receiver verifying with the current key.
func TestWebhookSecretGet(t *testing.T) {
	t.Parallel()
	r := run(t, "webhook-secret", "get", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("webhook-secret get exit %d: %s", r.Code, r.Stderr)
	}
	if k := valueAs[string](oneDoc(t, r)["secret_key"]); len(k) < 44 {
		t.Errorf("secret_key is %d characters, want at least 44", len(k))
	}
}
