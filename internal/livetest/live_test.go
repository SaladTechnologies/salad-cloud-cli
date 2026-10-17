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
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// prefix is unique per run so a previous run's reserved names cannot collide.
var prefix = fmt.Sprintf("lt-%06x", rand.Intn(1<<24))

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
// are assertions here, not accidents.
func runEnv(t *testing.T, extraEnv map[string]string, args ...string) result {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), binPath(t), args...)
	cmd.Env = os.Environ()
	for k, v := range extraEnv {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
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

// TestMain fails an unconfigured run instead of passing with no tests: the
// live suite is the check run before a release is tagged (see Releasing in
// AGENTS.md), so "ok" must mean it ran.
func TestMain(m *testing.M) {
	if os.Getenv("SALAD_API_KEY") == "" || os.Getenv("SALAD_ORGANIZATION") == "" {
		fmt.Println("livetest: SALAD_API_KEY and SALAD_ORGANIZATION must both be set; nothing ran")
		os.Exit(1)
	}
	m.Run()
	sweep()
}

// sweep deletes everything this run created, by prefix. Best-effort: a leaked
// stopped group costs nothing, but leaking is still untidy.
func sweep() {
	bin := os.Getenv("SALAD_CLI_TEST_BIN")
	if bin == "" {
		name := "saladcloud"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		bin = filepath.Join("..", "..", "dist", runtime.GOOS+"-"+runtime.GOARCH, name)
	}
	out, err := exec.Command(bin, "container-groups", "list", "-o", "json").Output()
	if err != nil {
		fmt.Printf("livetest sweep: list failed: %v\n", err)
		return
	}
	var listing struct {
		Items []struct{ Name string } `json:"items"`
	}
	if err = json.Unmarshal(out, &listing); err != nil {
		return
	}
	for _, it := range listing.Items {
		if strings.HasPrefix(it.Name, prefix) {
			fmt.Printf("livetest sweep: deleting %s\n", it.Name)
			_ = exec.Command(bin, "container-groups", "delete", it.Name).Run()
		}
	}
	// Queues after groups: a queue with a connected group may refuse deletion.
	out, err = exec.Command(bin, "queues", "list", "-o", "json").Output()
	if err != nil {
		return
	}
	var queues struct {
		Items []struct{ Name string } `json:"items"`
	}
	if json.Unmarshal(out, &queues) != nil {
		return
	}
	for _, it := range queues.Items {
		if strings.HasPrefix(it.Name, prefix) {
			fmt.Printf("livetest sweep: deleting queue %s\n", it.Name)
			_ = exec.Command(bin, "queues", "delete", it.Name).Run()
		}
	}
}

// TestPreflight: the environment is usable at all.
func TestPreflight(t *testing.T) {
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
			r := run(t, tc.args...)
			if r.Code != 0 {
				t.Fatalf("exit %d: %s", r.Code, r.Stderr)
			}
			for i, line := range strings.Split(strings.TrimSpace(r.Stdout), "\n") {
				if line == "" {
					continue
				}
				var entry struct {
					Resource struct {
						Labels map[string]string `json:"labels"`
					} `json:"resource"`
				}
				if err := json.Unmarshal([]byte(line), &entry); err != nil {
					t.Fatalf("line %d is not a complete JSON document: %s", i+1, line)
				}
				for label, want := range tc.want {
					if got := entry.Resource.Labels[label]; got != want {
						t.Errorf("line %d: %s = %q, want %q", i+1, label, got, want)
					}
				}
			}
		})
	}
}

// TestGroupLifecycle is the spine: create stopped, read it back, patch it,
// scale it, then delete. Nothing here runs, so it costs nothing.
func TestGroupLifecycle(t *testing.T) {
	g := name("life")
	r := run(t, "container-groups", "create", g, "--image", "docker.io/library/busybox:1.36",
		"--no-autostart", "--replicas", "2", "-e", "A=1", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("create exit %d: %s", r.Code, r.Stderr)
	}
	doc := oneDoc(t, r)
	if doc["name"] != g {
		t.Errorf("created name = %v, want %s", doc["name"], g)
	}
	if doc["replicas"] != float64(2) {
		t.Errorf("replicas = %v, want 2", doc["replicas"])
	}

	// get must round-trip the same shape as create.
	r = run(t, "container-groups", "get", g, "-o", "json")
	if r.Code != 0 {
		t.Fatalf("get exit %d: %s", r.Code, r.Stderr)
	}
	got := oneDoc(t, r)
	if got["name"] != g {
		t.Errorf("get name = %v, want %s", got["name"], g)
	}
	// Every key is present although this group sets none of these: absent
	// objects and scalars are null, absent arrays [].
	for _, k := range []string{"readme", "queue_connection", "queue_autoscaler", "liveness_probe", "startup_probe"} {
		if v, ok := got[k]; !ok || v != nil {
			t.Errorf("get %s = %v (present %t), want null", k, v, ok)
		}
	}
	if _, ok := got["country_codes"].([]any); !ok {
		t.Errorf("get country_codes = %v, want an array", got["country_codes"])
	}

	// A freshly created group is pending; a patch during that window is
	// rejected, so wait it out before writing again.
	settle(t, g)

	// update settles by default, so the next write is accepted.
	patch := filepath.Join(t.TempDir(), "p.json")
	if err := os.WriteFile(patch, []byte(`{"replicas":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if r = run(t, "container-groups", "update", g, "-f", patch); r.Code != 0 {
		t.Fatalf("update exit %d: %s", r.Code, r.Stderr)
	}
	// get --writable must round-trip through update -f with no rejection
	// and no warning (notes are fine). Done before scheduled scaling is
	// enabled, so the replicas in the document do not put a schedule on hold.
	wr := run(t, "container-groups", "get", g, "--writable")
	if wr.Code != 0 {
		t.Fatalf("get --writable exit %d: %s", wr.Code, wr.Stderr)
	}
	wdoc := filepath.Join(t.TempDir(), "writable.json")
	if err := os.WriteFile(wdoc, []byte(wr.Stdout), 0o600); err != nil {
		t.Fatal(err)
	}
	rt := run(
		t,
		"container-groups",
		"update",
		g,
		"-f",
		wdoc,
	)
	if rt.Code != 0 ||
		strings.Contains(rt.Stderr, "warning:") {
		t.Errorf("get --writable | update -f round trip: exit %d, stderr %q", rt.Code, rt.Stderr)
	}

	// The point of settling: an immediate second write must not be rejected
	// with pending_update_in_progress. Enable scheduled scaling first so the
	// scale below exercises the hold rule: setting replicas puts the schedule
	// on hold, and scale — the replicas verb — must say so (as a note, not a
	// warning: it is a documented platform rule).
	sched := filepath.Join(t.TempDir(), "sched.json")
	if err := os.WriteFile(
		sched,
		[]byte(`{"scheduled-scaling-enabled":true,"scaling-actions":[{"replicas":1,"schedule":"0 9 * * 1-5"}]}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if r = run(t, "container-groups", "update", g, "-f", sched); r.Code != 0 {
		t.Fatalf("enable scheduled scaling exit %d: %s", r.Code, r.Stderr)
	}
	r = run(t, "container-groups", "scale", g, "--replicas", "1", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("scale straight after update exit %d: %s", r.Code, r.Stderr)
	}
	if !strings.Contains(r.Stderr, "note:") || !strings.Contains(r.Stderr, "on hold") {
		t.Errorf("scale on a scheduled group did not print the hold note; stderr %q", r.Stderr)
	}
	if strings.Contains(r.Stderr, "warning:") {
		t.Errorf("the hold rule must be a note, not a warning; stderr %q", r.Stderr)
	}
	held := oneDoc(
		t,
		run(t, "container-groups", "get", g, "-o", "json"),
	)
	if held["scheduled-scaling-enabled"] != false {
		t.Errorf("scheduled-scaling-enabled after scale = %v, want false (on hold)", held["scheduled-scaling-enabled"])
	}
	// --keep-schedule: re-enable, scale again with the flag, and the schedule
	// must survive (two writes under the hood).
	if r = run(t, "container-groups", "update", g, "-f", sched); r.Code != 0 {
		t.Fatalf("re-enable scheduled scaling exit %d: %s", r.Code, r.Stderr)
	}
	if r = run(t, "container-groups", "scale", g, "--replicas", "2", "--keep-schedule"); r.Code != 0 {
		t.Fatalf("scale --keep-schedule exit %d: %s", r.Code, r.Stderr)
	} else if !strings.Contains(r.Stderr, "re-enabled") {
		t.Errorf("scale --keep-schedule did not report the re-enable; stderr %q", r.Stderr)
	}
	kept := oneDoc(
		t,
		run(t, "container-groups", "get", g, "-o", "json"),
	)
	if kept["scheduled-scaling-enabled"] != true ||
		kept["replicas"] != float64(2) {
		t.Errorf(
			"after scale --keep-schedule: enabled=%v replicas=%v, want true and 2",
			kept["scheduled-scaling-enabled"],
			kept["replicas"],
		)
	}
	act := oneDoc(t, r)
	if act["action"] != "scaled" {
		t.Errorf("scale action = %v, want scaled (the shared action shape)", act["action"])
	}
	if act["ok"] != true {
		t.Errorf("scale ok = %v, want true", act["ok"])
	}

	if r := run(t, "container-groups", "delete", g, "-o", "json"); r.Code != 0 {
		t.Fatalf("delete exit %d: %s", r.Code, r.Stderr)
	}
}

// TestDeletedNameStaysTaken pins the uniqueness rule: names must be unique
// within the organization and project, including against deleted groups —
// deletion is a soft delete and never releases the name, which is why every
// run here uses unique names. This is deliberate platform behavior
// (docs/notes.md). If a deleted name ever becomes reusable,
// the rule itself has changed and the naming convention can be relaxed.
func TestDeletedNameStaysTaken(t *testing.T) {
	g := name("resv")
	if r := run(
		t,
		"container-groups",
		"create",
		g,
		"--image",
		"docker.io/library/busybox:1.36",
		"--no-autostart",
	); r.Code != 0 {
		t.Fatalf("create exit %d: %s", r.Code, r.Stderr)
	}
	if r := run(t, "container-groups", "delete", g); r.Code != 0 {
		t.Fatalf("delete exit %d: %s", r.Code, r.Stderr)
	}
	if r := run(t, "container-groups", "get", g); r.Code != 4 {
		t.Errorf("get after delete = exit %d, want 4 (not found)", r.Code)
	}
	r := run(t, "container-groups", "create", g, "--image", "docker.io/library/busybox:1.36", "--no-autostart")
	if r.Code == 0 {
		t.Log("NOTE: the deleted name was reusable — the uniqueness rule may have changed; " +
			"update docs/notes.md and consider relaxing the unique-name convention")
		_ = run(t, "container-groups", "delete", g)
		return
	}
	if !strings.Contains(r.Stderr, "already exists") {
		t.Errorf("re-create error should explain the name conflict, got: %s", r.Stderr)
	}
}

// TestErrorContract covers the exit codes and stream discipline that scripts
// depend on. All client-side or 404 paths, so nothing is created.
func TestErrorContract(t *testing.T) {
	cases := []struct {
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
	}{
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
			var errDoc struct {
				Error struct {
					ExitCode   int             `json:"exit_code"`
					Kind       string          `json:"kind"`
					HTTPStatus json.RawMessage `json:"http_status"`
				} `json:"error"`
			}
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
		})
	}
}

// TestGpuConfigCreatesStopped exercises GPU class resolution against the real
// catalog. Created stopped, so no GPU is ever allocated and it costs nothing.
func TestGpuConfigCreatesStopped(t *testing.T) {
	time.Sleep(createSpacing)
	g := name("gpu")
	r := run(t, "container-groups", "create", g, "--image", "docker.io/library/busybox:1.36",
		"--no-autostart", "--gpu", "rtx 3060 (12 gb)", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("create with --gpu exit %d: %s", r.Code, r.Stderr)
	}
	doc := oneDoc(t, r)
	container, _ := doc["container"].(map[string]any)
	resources, _ := container["resources"].(map[string]any)
	classes, _ := resources["gpu_classes"].([]any)
	if len(classes) != 1 {
		t.Errorf("gpu_classes = %v, want exactly one resolved id", classes)
	}
	// Ambiguity must be a clean client-side error, not an API round-trip.
	amb := run(t, "container-groups", "create", name("amb"), "--image", "docker.io/library/busybox:1.36",
		"--no-autostart", "--gpu", "3060")
	if amb.Code != 6 || !strings.Contains(amb.Stderr, "ambiguous") {
		t.Errorf("ambiguous --gpu = exit %d, stderr %q; want 6 and 'ambiguous'", amb.Code, amb.Stderr)
	}
	_ = run(t, "container-groups", "delete", g)
}

// TestContainerCommand: words after -- set container.command on create and
// replace it on update, reaching the API as the shell split them.
func TestContainerCommand(t *testing.T) {
	time.Sleep(createSpacing)
	g := name("cmd")
	script := `echo "hi there"; sleep 2147483647`
	r := run(t, "container-groups", "create", g, "--image", "docker.io/library/busybox:1.36", "--no-autostart",
		"-o", "json", "--", "sh", "-c", script)
	if r.Code != 0 {
		t.Fatalf("create exit %d: %s", r.Code, r.Stderr)
	}
	defer run(t, "container-groups", "delete", g)
	command := func(doc string) []string {
		var got struct {
			Container struct {
				Command []string `json:"command"`
			} `json:"container"`
		}
		if err := json.Unmarshal([]byte(doc), &got); err != nil {
			t.Fatalf("stdout: %v\n%s", err, doc)
		}
		return got.Container.Command
	}
	if got := command(r.Stdout); strings.Join(got, "\x00") != strings.Join([]string{"sh", "-c", script}, "\x00") {
		t.Errorf("created command = %q", got)
	}
	if strings.Contains(r.Stderr, "warning:") {
		t.Errorf("create warned: %q", r.Stderr)
	}

	settle(t, g)
	r = run(t, "container-groups", "update", g, "-o", "json", "--", "sleep", "2147483647")
	if r.Code != 0 {
		t.Fatalf("update exit %d: %s", r.Code, r.Stderr)
	}
	if got := command(r.Stdout); strings.Join(got, " ") != "sleep 2147483647" {
		t.Errorf("updated command = %q", got)
	}

	// The documented way back to the image's own command: an empty list in a
	// patch file, with no drift warning on the read-back.
	clearDoc := filepath.Join(t.TempDir(), "clear.json")
	if err := os.WriteFile(clearDoc, []byte(`{"container":{"command":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r = run(t, "container-groups", "update", g, "-f", clearDoc, "-o", "json")
	if r.Code != 0 {
		t.Fatalf("clear exit %d: %s", r.Code, r.Stderr)
	}
	if got := command(r.Stdout); len(got) != 0 {
		t.Errorf("cleared command = %q, want []", got)
	}
	if strings.Contains(r.Stderr, "warning:") {
		t.Errorf("clear warned: %q", r.Stderr)
	}

	// Client-side checks, before any write.
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"container-groups", "update", g, "--"}, 2},
		{[]string{"container-groups", "update", g, "--", "--port", "8080"}, 6},
		{[]string{"container-groups", "update", g, "--", "sleep", ""}, 6},
	} {
		if r := run(t, tc.args...); r.Code != tc.code {
			t.Errorf("%q = exit %d, want %d: %s", tc.args, r.Code, tc.code, r.Stderr)
		}
	}
}

// TestCreateFrom copies a stopped group whose settings are all non-default,
// so a field that fails to come across shows up as the default.
func TestCreateFrom(t *testing.T) {
	time.Sleep(createSpacing)
	src, cp := name("from-src"), name("from-copy")
	// An HTTP probe with no headers: the API rejects the probe unless the
	// empty headers list is sent, from the file here and from the source on
	// the copy below.
	probe := filepath.Join(t.TempDir(), "probe.yaml")
	if err := os.WriteFile(probe, []byte("readiness_probe:\n  http: {path: /, port: 8080, scheme: http, headers: []}\n"+
		"  initial_delay_seconds: 0\n  period_seconds: 5\n  timeout_seconds: 2\n  success_threshold: 1\n  "+
		"failure_threshold: 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := run(t, "container-groups", "create", src, "--image", "docker.io/library/busybox:1.36", "-f", probe,
		"--restart-policy", "never", "--port", "8080", "--no-auth", "-e", "MODE=prod",
		"--priority", "high", "--replicas", "2", "--no-autostart"); r.Code != 0 {
		t.Fatalf("create source exit %d: %s", r.Code, r.Stderr)
	}
	defer run(t, "container-groups", "delete", src)

	// Usage errors, before any write.
	if r := run(t, "container-groups", "create", "--from", src); r.Code != 2 {
		t.Errorf("same-project copy without a name = exit %d, want 2", r.Code)
	}
	if r := run(t, "container-groups", "create", cp, "--from", "other-org/default/"+src); r.Code != 2 {
		t.Errorf("cross-organization --from = exit %d, want 2", r.Code)
	}

	time.Sleep(createSpacing)
	r := run(t, "container-groups", "create", cp, "--from", src, "-e", "EXTRA=1", "--no-autostart", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("create --from exit %d: %s", r.Code, r.Stderr)
	}
	defer run(t, "container-groups", "delete", cp)
	// The source image is on Docker Hub, a public registry: no credentials
	// note (one prints only for an image the CLI cannot place as public).
	if strings.Contains(r.Stderr, "registry credentials") {
		t.Errorf("registry-credentials note for a public image: %q", r.Stderr)
	}
	// The copy sends back the source's empty lists; none may read as a drop.
	if strings.Contains(r.Stderr, "warning:") {
		t.Errorf("create --from warned: %q", r.Stderr)
	}
	var got struct {
		Name          string `json:"name"`
		DisplayName   string `json:"display_name"`
		RestartPolicy string `json:"restart_policy"`
		Priority      string `json:"priority"`
		Replicas      int    `json:"replicas"`
		Networking    struct {
			Port int  `json:"port"`
			Auth bool `json:"auth"`
		} `json:"networking"`
		Container struct {
			Env map[string]string `json:"environment_variables"`
		} `json:"container"`
		ReadinessProbe *struct {
			HTTP *struct {
				Path string `json:"path"`
			} `json:"http"`
		} `json:"readiness_probe"`
	}
	if err := json.Unmarshal([]byte(r.Stdout), &got); err != nil {
		t.Fatalf("stdout: %v\n%s", err, r.Stdout)
	}
	if got.ReadinessProbe == nil || got.ReadinessProbe.HTTP == nil || got.ReadinessProbe.HTTP.Path != "/" {
		t.Errorf("readiness_probe not copied: %s", r.Stdout)
	}
	if got.Name != cp || got.DisplayName != cp {
		t.Errorf("name/display_name = %q/%q, want both %q", got.Name, got.DisplayName, cp)
	}
	if got.RestartPolicy != "never" || got.Priority != "high" || got.Replicas != 2 {
		t.Errorf(
			"restart_policy/priority/replicas = %q/%q/%d, want never/high/2",
			got.RestartPolicy,
			got.Priority,
			got.Replicas,
		)
	}
	if got.Networking.Port != 8080 || got.Networking.Auth {
		t.Errorf("networking = %+v, want port 8080 with auth off", got.Networking)
	}
	if got.Container.Env["MODE"] != "prod" || got.Container.Env["EXTRA"] != "1" {
		t.Errorf("env = %v, want MODE=prod copied and EXTRA=1 added", got.Container.Env)
	}
}

// TestRunningLifecycle is the only test here that runs a workload: three
// 1-vCPU batch replicas of busybox, stopped and deleted immediately. Three
// rather than one because --wait returns as soon as the group is running,
// and three allocation attempts in parallel reach a first running instance
// far sooner than one (a single batch-priority replica can wait many
// minutes). Cost is still cents. This is what cannot be faked: real
// capacity, real instance states, real logs.
func TestRunningLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: skipping the workload that actually runs")
	}
	time.Sleep(createSpacing)
	g := name("run")
	def := filepath.Join(t.TempDir(), "run.json")
	// sleep 2147483647, never "infinity": busybox rejects it, the instance
	// fails at start and the platform reallocates it, so the group loops
	// across nodes and --wait would just time out.
	body := `{"autostart_policy":true,"restart_policy":"always","replicas":3,
	  "container":{"image":"docker.io/library/busybox:1.36","priority":"batch",
	  "command":["sleep","2147483647"],"resources":{"cpu":1,"memory":1024}}}`
	if err := os.WriteFile(def, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = run(t, "container-groups", "delete", g) }()

	// Generous timeout regardless: batch capacity can still be slow.
	r := run(t, "container-groups", "create", g, "-f", def, "--wait", "--timeout", "15m", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("create --wait exit %d: %s", r.Code, r.Stderr)
	}
	doc := oneDoc(t, r)
	state, _ := doc["current_state"].(map[string]any)
	if state["status"] != "running" {
		t.Errorf("--wait -o json emitted status %v, want running (the settled document)", state["status"])
	}

	// Logs must be NDJSON: every line a complete document.
	lg := run(t, "container-groups", "logs", g, "--since", "20m", "-o", "json")
	if lg.Code != 0 {
		t.Fatalf("logs exit %d: %s", lg.Code, lg.Stderr)
	}
	for i, line := range strings.Split(strings.TrimSpace(lg.Stdout), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Errorf("logs line %d is not a complete JSON document: %s", i+1, line)
		}
	}

	// Instance prefix resolution, against a real instance id.
	inst := run(t, "container-groups", "instances", "list", "-c", g, "-o", "json")
	if inst.Code != 0 {
		t.Fatalf("instances list exit %d: %s", inst.Code, inst.Stderr)
	}
	// The instance document's key is "id" (not "instance_id"); an earlier
	// revision decoded the wrong name, got "", and the length guard below
	// silently skipped this whole check on every run.
	var instances struct {
		Instances []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(inst.Stdout), &instances); err != nil {
		t.Fatalf("instances list: %v", err)
	}
	if len(instances.Instances) == 0 || len(instances.Instances[0].ID) < 4 {
		t.Fatalf("instances list returned no usable instance id: %s", inst.Stdout)
	}
	id := instances.Instances[0].ID
	if got := run(t, "container-groups", "instances", "get", id[:4], "-c", g); got.Code != 0 {
		t.Errorf("instances get by 4-char prefix exit %d: %s", got.Code, got.Stderr)
	}
	// deletion_cost is the one writable instance field; update returns the
	// instance carrying it.
	upd := run(t, "container-groups", "instances", "update", id[:4], "-c", g, "--deletion-cost", "7", "-o", "json")
	if upd.Code != 0 {
		t.Errorf("instances update exit %d: %s", upd.Code, upd.Stderr)
	} else if doc := oneDoc(t, upd); doc["deletion_cost"] != float64(7) || doc["id"] != id {
		t.Errorf("instances update = deletion_cost %v id %v, want 7 and %s", doc["deletion_cost"], doc["id"], id)
	}

	// Remote access, while we have a running instance to talk to.
	t.Run("ssh", func(t *testing.T) { testSSH(t, g) })

	if r := run(t, "container-groups", "stop", g, "--wait", "--timeout", "5m"); r.Code != 0 {
		t.Errorf("stop --wait exit %d: %s", r.Code, r.Stderr)
	}
}

// testSSH covers the remote-access surface: ssh/cp is the part of this CLI a
// user is most likely to reach for interactively and the part least covered
// by anything offline.
//
// It runs inside TestRunningLifecycle's group to avoid paying for a second
// running workload; see runningGroup.
//
// Auth depends on the account's single portal-registered key, so a
// publickey denial skips rather than fails. Everything else — the relay
// endpoint resolving, the sh -c argv wrapping, --config rendering, and the
// documented exit-code caveat — is asserted.
func testSSH(t *testing.T, g string) { //nolint:thelper // a subtest body, not a helper: failures point into it
	// --config opens no session, so it works even without a usable key and
	// proves the relay endpoint was resolved.
	cfg := run(t, "container-groups", "ssh", g, "--config")
	if cfg.Code != 0 {
		t.Errorf("ssh --config exit %d: %s", cfg.Code, cfg.Stderr)
	} else {
		for _, want := range []string{"Host ", "HostName ", "Port "} {
			if !strings.Contains(cfg.Stdout, want) {
				t.Errorf("ssh --config output missing %q; got:\n%s", want, cfg.Stdout)
			}
		}
	}

	// One-shot command. The relay does not propagate exit codes, so assert
	// on stdout rather than the code — that caveat is itself the contract.
	// A publickey denial here is almost always environmental, not a CLI bug:
	// the registered key must be on the machine running the binary. Running
	// the linux build under WSL offers WSL's ~/.ssh key, not the Windows
	// host's registered one, and is denied every time, which can look like a
	// platform fault. Point SALAD_CLI_TEST_BIN at a binary whose host holds
	// the registered key.
	//
	// A couple of retries still guard against a genuine transient without
	// masking a real failure for long.
	var r result
	for attempt := 1; attempt <= 3; attempt++ {
		r = run(t, "container-groups", "ssh", g, "--", "echo", "hello-from-instance")
		if !strings.Contains(r.Stderr, "publickey") {
			break
		}
		if attempt < 3 {
			t.Logf("ssh attempt %d denied on publickey; retrying", attempt)
			time.Sleep(10 * time.Second)
		}
	}
	if strings.Contains(r.Stderr, "publickey") {
		// SALAD_CLI_TEST_REQUIRE_SSH=1 (set for the run before a release is
		// tagged) makes the environment gap a failure, so ssh and the cp
		// round trip after it cannot be skipped into a passing run.
		skip := t.Skipf
		if os.Getenv("SALAD_CLI_TEST_REQUIRE_SSH") == "1" {
			skip = t.Fatalf
		}
		skip(
			"ssh denied on publickey after 3 attempts. Three causes cover nearly every case: the key on this machine "+
				"is not "+
				"registered on the account that owns the API key (keys are per user); this shell has a different "+
				"~/.ssh "+
				"(WSL/container); "+
				"or the key is passphrase-protected and not in ssh-add (one-shot ssh runs in BatchMode). "+
				"`saladcloud ssh-key get` reports which key this build offers. stderr: %s",
			strings.TrimSpace(r.Stderr),
		)
	}
	if !strings.Contains(r.Stdout, "hello-from-instance") {
		t.Errorf("ssh one-shot did not echo through the relay; stdout %q, stderr %q", r.Stdout, r.Stderr)
	}
	// The sh -c wrapping is what makes shell syntax work at all through a
	// relay that execs raw argv.
	if sh := run(
		t,
		"container-groups",
		"ssh",
		g,
		"--",
		"echo",
		"a",
		"&&",
		"echo",
		"b",
	); !strings.Contains(sh.Stdout, "a") ||
		!strings.Contains(sh.Stdout, "b") {
		t.Errorf("shell operators did not survive the sh -c wrapping; stdout %q, stderr %q", sh.Stdout, sh.Stderr)
	}

	// cp: push a file and read it back through the same relay. The group
	// runs several replicas and "first running instance" can change between
	// calls as instances come and go, so pin one instance for the whole
	// round trip — otherwise the write and the read could land on different
	// containers and fail for a reason that has nothing to do with the CLI.
	inst := firstRunningInstance(t, g)
	local := filepath.Join(t.TempDir(), "up.txt")
	payload := "livetest-" + prefix
	if err := os.WriteFile(local, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	if up := run(t, "container-groups", "cp", local, g+"/"+inst+":/tmp/up.txt"); up.Code != 0 {
		t.Errorf("cp up exit %d: %s", up.Code, up.Stderr)
	}
	if back := run(
		t,
		"container-groups",
		"ssh",
		g,
		"--instance",
		inst,
		"--",
		"cat",
		"/tmp/up.txt",
	); !strings.Contains(
		back.Stdout,
		payload,
	) {
		t.Errorf("uploaded file did not read back; stdout %q, stderr %q", back.Stdout, back.Stderr)
	}
	down := filepath.Join(t.TempDir(), "down.txt")
	if dl := run(t, "container-groups", "cp", g+"/"+inst+":/tmp/up.txt", down); dl.Code != 0 {
		t.Errorf("cp down exit %d: %s", dl.Code, dl.Stderr)
	} else if b, err := os.ReadFile(down); err != nil || !strings.Contains(string(b), payload) {
		t.Errorf("round-tripped file = %q (err %v), want %q", b, err, payload)
	}
}

// firstRunningInstance returns the id of one running instance of g, so a
// multi-step check can address the same container throughout.
func firstRunningInstance(t *testing.T, g string) string {
	t.Helper()
	r := run(t, "container-groups", "instances", "list", "-c", g, "-o", "json")
	if r.Code != 0 {
		t.Fatalf("instances list exit %d: %s", r.Code, r.Stderr)
	}
	var doc struct {
		Instances []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(r.Stdout), &doc); err != nil {
		t.Fatalf("instances list: %v", err)
	}
	for _, i := range doc.Instances {
		if i.State == "running" && i.ID != "" {
			return i.ID
		}
	}
	if len(doc.Instances) > 0 && doc.Instances[0].ID != "" {
		return doc.Instances[0].ID
	}
	t.Fatalf("no usable instance id in %s: %s", g, r.Stdout)
	return ""
}

// TestWaitTimeoutContract: a deliberately short --wait must exit 1 and leave
// stdout empty. This is the regression that mattered most in live testing —
// scripts reading stdout would otherwise act on a group that is not running.
func TestWaitTimeoutContract(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: skipping the workload that actually runs")
	}
	time.Sleep(createSpacing)
	g := name("to")
	def := filepath.Join(t.TempDir(), "to.json")
	body := `{"autostart_policy":true,"restart_policy":"always","replicas":1,
	  "container":{"image":"docker.io/library/busybox:1.36","priority":"batch",
	  "command":["sleep","2147483647"],"resources":{"cpu":1,"memory":1024}}}`
	if err := os.WriteFile(def, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = run(t, "container-groups", "delete", g) }()

	r := run(t, "container-groups", "create", g, "-f", def, "--wait", "--timeout", "15s", "-o", "json")
	if r.Code != 1 {
		t.Errorf("short --wait = exit %d, want 1", r.Code)
	}
	if strings.TrimSpace(r.Stdout) != "" {
		t.Errorf("stdout must be empty when a wait times out, got: %s", r.Stdout)
	}
	if !strings.Contains(r.Stderr, "wait_timeout") {
		t.Errorf("stderr should carry kind wait_timeout: %s", r.Stderr)
	}
}

// TestTranscribe covers the inference path end to end. Cost is per audio
// hour, so a few seconds of speech is negligible.
//
// A transcription job can fail transiently, with no error detail in the job
// document, so a single failure is retried once before the test is believed.
func TestTranscribe(t *testing.T) {
	audio := os.Getenv("SALAD_CLI_TEST_AUDIO")
	if audio == "" {
		t.Skip("SALAD_CLI_TEST_AUDIO unset (path to a short wav); skipping transcription")
	}
	phrase := os.Getenv("SALAD_CLI_TEST_PHRASE")
	if phrase == "" {
		phrase = "quick brown fox"
	}
	var r result
	for attempt := 1; attempt <= 2; attempt++ {
		r = run(t, "inference", "transcribe", audio)
		if r.Code == 0 {
			break
		}
		t.Logf("transcribe attempt %d failed (exit %d): %s", attempt, r.Code, r.Stderr)
	}
	if r.Code != 0 {
		t.Fatalf("transcribe failed twice: %s", r.Stderr)
	}
	if !strings.Contains(strings.ToLower(r.Stdout), strings.ToLower(phrase)) {
		t.Errorf("transcript %q does not contain %q", r.Stdout, phrase)
	}

	// --srt must produce an actual SRT document, not the plain transcript.
	srt := run(t, "inference", "transcribe", audio, "--srt")
	if srt.Code == 0 && !strings.Contains(srt.Stdout, "-->") {
		t.Errorf("--srt produced no cue timestamps: %s", srt.Stdout)
	}

	// A failed transcription must exit nonzero with stdout empty — the API
	// reports such jobs as "succeeded" with the error inside the payload.
	bad := run(t, "inference", "transcribe", "https://example.com/nope.mp3")
	if bad.Code == 0 {
		t.Errorf("an undownloadable URL exited 0; stdout: %s", bad.Stdout)
	}
	if strings.TrimSpace(bad.Stdout) != "" {
		t.Errorf("failed transcription must leave stdout empty, got: %s", bad.Stdout)
	}

	// -o json never hands out the upload's signed URL: the token is removed
	// (and the upload itself deleted) once the transcript is back.
	js := run(t, "inference", "transcription-lite", audio, "-o", "json")
	if js.Code == 0 {
		in, _ := oneDoc(t, js)["input"].(map[string]any)
		if u, _ := in["url"].(string); strings.Contains(u, "?") {
			t.Errorf("input.url kept its signed query string: %s", u)
		}
	}

	// The lite tier is its own porcelain command, named after its endpoint.
	lite := run(t, "inference", "transcription-lite", audio)
	if lite.Code == 0 && !strings.Contains(strings.ToLower(lite.Stdout), strings.ToLower(phrase)) {
		t.Errorf("transcription-lite transcript %q does not contain %q", lite.Stdout, phrase)
	}
}

// TestTranscribeLanguageAndStream pins two behaviors only a live run can
// confirm: language_code omitted or null detects the language (it is not
// 'en'), and audio_stream_index is honored. It needs a non-English clip,
// because English audio cannot tell detection from a forced en. A
// single-track clip makes stream 1 invalid.
func TestTranscribeLanguageAndStream(t *testing.T) {
	audio := os.Getenv("SALAD_CLI_TEST_AUDIO_FOREIGN")
	if audio == "" {
		t.Skip("SALAD_CLI_TEST_AUDIO_FOREIGN unset (path to a short single-track non-English clip); skipping")
	}
	// Transient job failures are retried once, as in TestTranscribe.
	doc := func(args ...string) map[string]any {
		var r result
		for attempt := 1; attempt <= 2; attempt++ {
			r = run(t, append([]string{"inference", "transcribe", audio, "-o", "json"}, args...)...)
			if r.Code == 0 {
				return oneDoc(t, r)
			}
			t.Logf("transcribe %v attempt %d failed (exit %d): %s", args, attempt, r.Code, r.Stderr)
		}
		t.Fatalf("transcribe %v failed twice: %s", args, r.Stderr)
		return nil
	}
	text := func(d map[string]any) string {
		out, _ := d["output"].(map[string]any)
		s, _ := out["text"].(string)
		return strings.TrimSpace(s)
	}

	omitted := doc()
	null := doc("--input-extra", `{"language_code": null}`)
	forced := doc("--language-code", "en")

	if in, _ := null["input"].(map[string]any); in == nil {
		t.Errorf("null job has no input: %v", null)
	} else if v, has := in["language_code"]; !has || v != nil {
		t.Errorf("null was not preserved in the echoed input: %v", in)
	}
	if text(omitted) == "" {
		t.Fatalf("omitted language_code returned no transcript: %v", omitted)
	}
	if text(null) != text(omitted) {
		t.Errorf("null and omitted language_code differ:\nnull:    %s\nomitted: %s", text(null), text(omitted))
	}
	if text(forced) == text(omitted) {
		t.Errorf("forcing en changed nothing, so omitted may default to en, or the clip is English: %s", text(forced))
	}

	// A stream the file lacks fails the job, which proves the field is read.
	bad := run(t, "inference", "transcribe", audio, "--audio-stream-index", "1")
	if bad.Code == 0 {
		t.Errorf("--audio-stream-index 1 on a single-track clip exited 0; stdout: %s", bad.Stdout)
	}
	if !strings.Contains(bad.Stderr, "invalid_audio_stream") {
		t.Errorf("stderr should carry invalid_audio_stream: %s", bad.Stderr)
	}
	if strings.TrimSpace(bad.Stdout) != "" {
		t.Errorf("failed transcription must leave stdout empty, got: %s", bad.Stdout)
	}
}

// TestGateway smoke-tests the AI Gateway commands. It needs a gateway key,
// which is a separate credential, so it skips without one.
func TestGateway(t *testing.T) {
	if os.Getenv("SALAD_AI_GATEWAY_API_KEY") == "" {
		t.Skip("SALAD_AI_GATEWAY_API_KEY unset; skipping the AI Gateway smoke test")
	}
	m := run(t, "ai-gateway", "models", "list", "-o", "json")
	if m.Code != 0 {
		t.Fatalf("gateway models list exit %d: %s", m.Code, m.Stderr)
	}
	doc := oneDoc(t, m)
	data, _ := doc["items"].([]any)
	if len(data) == 0 {
		t.Fatalf("gateway models listed nothing: %s", m.Stdout)
	}
	r := run(t, "ai-gateway", "test", "--prompt", "Reply with the single word: pong", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("gateway test exit %d: %s", r.Code, r.Stderr)
	}
	res := oneDoc(t, r)
	if res["ok"] != true || res["reply"] == "" {
		t.Errorf("gateway test = %v", res)
	}
	// A wrong key is an auth failure with empty stdout. The model list is
	// public and accepts any key, so the check is test's completion, which
	// the gateway rejects before it runs (not billed).
	bad := runEnv(
		t,
		map[string]string{"SALAD_AI_GATEWAY_API_KEY": "not-a-real-key"},
		"ai-gateway",
		"test",
		"-o",
		"json",
	)
	if bad.Code != 3 || strings.TrimSpace(bad.Stdout) != "" {
		t.Errorf("wrong gateway key: exit %d stdout %q, want 3 and empty", bad.Code, bad.Stdout)
	}
	var errDoc struct {
		Error struct {
			Kind       string `json:"kind"`
			Message    string `json:"message"`
			HTTPStatus *int   `json:"http_status"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(bad.Stderr)), &errDoc); err != nil {
		t.Fatalf("wrong gateway key: stderr is not one error document: %s", bad.Stderr)
	}
	if want := "AI Gateway key ****...-key (from SALAD_AI_GATEWAY_API_KEY) was rejected"; errDoc.Error.Kind != "auth" ||
		errDoc.Error.Message != want || errDoc.Error.HTTPStatus == nil || *errDoc.Error.HTTPStatus != 401 {
		t.Errorf("wrong gateway key: %s; want kind auth, %q, http_status 401", bad.Stderr, want)
	}
}

// TestGrammar covers the command-grammar rules against the real API: a bare
// resource noun is a usage error, every list is an items envelope, and the
// read verbs of the nouns answer. Read-only.
func TestGrammar(t *testing.T) {
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
	if r := run(t, "inference", "get", "transcribe"); r.Code != 0 || !strings.Contains(r.Stdout, "Inputs") {
		t.Errorf("inference get transcribe exit %d: %s", r.Code, r.Stderr)
	}
	if r := run(t, "inference", "jobs", "list", "--endpoint", "transcribe", "--limit", "1", "-o", "json"); r.Code != 0 {
		t.Errorf("inference jobs list --endpoint exit %d: %s", r.Code, r.Stderr)
	}
	if r := run(
		t,
		"create",
	); r.Code != 2 ||
		!strings.Contains(r.Stderr, "did you mean saladcloud container-groups create") {
		t.Errorf("bare create: exit %d stderr %q, want 2 with did-you-mean", r.Code, r.Stderr)
	}
}

// TestAvailability asks once per class, as the command is meant to be used.
func TestAvailability(t *testing.T) {
	one := run(t, "availability", "--gpu", "rtx 3060 (12 gb)", "-o", "json")
	if one.Code != 0 {
		t.Fatalf("availability exit %d: %s", one.Code, one.Stderr)
	}
	doc := oneDoc(t, one)
	items, _ := doc["items"].([]any)
	if len(items) != 1 {
		t.Errorf("availability one class = %s, want one item", one.Stdout)
	}
	// Two classes: one row each plus the combined query, never a sum.
	two := run(t, "availability", "--gpu", "rtx 3060 (12 gb)", "--gpu", "rtx 3090 (24 gb)", "-o", "json")
	if two.Code != 0 {
		t.Fatalf("availability with two classes exit %d: %s", two.Code, two.Stderr)
	}
	d2 := oneDoc(t, two)
	if items, _ := d2["items"].([]any); len(items) != 2 {
		t.Errorf("two classes = %d items, want 2", len(items))
	}
	if c, _ := d2["combined"].(map[string]any); c == nil || len(c["gpu_classes"].([]any)) != 2 {
		t.Errorf("combined = %v, want the combined query for both classes", d2["combined"])
	}
	cpu := run(t, "availability", "--cpu", "2", "--memory", "2048", "-o", "json")
	if cpu.Code != 0 {
		t.Fatalf("CPU availability exit %d: %s", cpu.Code, cpu.Stderr)
	}
	if items, _ := oneDoc(t, cpu)["items"].([]any); len(items) != 1 {
		t.Errorf("CPU-only availability = %s, want one item", cpu.Stdout)
	}
}

// TestQueues drives the queue plumbing without a worker: a job submitted to
// a queue with no container group stays pending, so it can be read back and
// cancelled. Queues cost nothing.
func TestQueues(t *testing.T) {
	q := name("q")
	r := run(t, "queues", "create", q, "--display-name", "Live test queue", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("queues create exit %d: %s", r.Code, r.Stderr)
	}
	t.Cleanup(func() { _ = run(t, "queues", "delete", q) })
	if doc := oneDoc(t, r); doc["name"] != q {
		t.Errorf("created queue name = %v", doc["name"])
	}
	if g := run(t, "queues", "get", q, "-o", "json"); g.Code != 0 {
		t.Errorf("queues get exit %d: %s", g.Code, g.Stderr)
	}
	u := run(
		t,
		"queues",
		"update",
		q,
		"--description",
		"updated",
		"-o",
		"json",
	)
	if u.Code != 0 ||
		oneDoc(t, u)["description"] != "updated" {
		t.Errorf("queues update exit %d: %s %s", u.Code, u.Stdout, u.Stderr)
	}

	job := run(t, "queues", "jobs", "create", "--queue", q, "--input", `{"n": 1}`, "-o", "json")
	if job.Code != 0 {
		t.Fatalf("queues jobs create exit %d: %s", job.Code, job.Stderr)
	}
	id, _ := oneDoc(t, job)["id"].(string)
	if id == "" {
		t.Fatalf("queue job has no id: %s", job.Stdout)
	}
	if g := run(t, "queues", "jobs", "get", id, "--queue", q, "-o", "json"); g.Code != 0 {
		t.Errorf("queues jobs get exit %d: %s", g.Code, g.Stderr)
	}
	l := run(
		t,
		"queues",
		"jobs",
		"list",
		"--queue",
		q,
		"-o",
		"json",
	)
	if l.Code != 0 ||
		!strings.Contains(l.Stdout, id) {
		t.Errorf("queues jobs list exit %d does not list %s: %s", l.Code, id, l.Stdout)
	}
	if d := run(t, "queues", "jobs", "cancel", id, "--queue", q, "-o", "json"); d.Code != 0 {
		t.Errorf("queues jobs cancel (before dispatch) exit %d: %s", d.Code, d.Stderr)
	}

	// create --queue checks the queue exists before creating anything.
	missing := run(t, "container-groups", "create", name("qmiss"), "--image", "docker.io/library/busybox:1.36",
		"--no-autostart", "--queue", prefix+"-no-such-queue", "--queue-port", "8080", "--queue-path", "/job")
	if missing.Code != 4 {
		t.Errorf("create --queue <missing> exit %d, want 4: %s", missing.Code, missing.Stderr)
	}
	time.Sleep(createSpacing)
	g := name("qcg")
	auto := filepath.Join(t.TempDir(), "auto.json")
	err := os.WriteFile(
		auto,
		[]byte(`{"queue_autoscaler":{"min_replicas":0,"max_replicas":1,"desired_queue_length":1}}`),
		0o600,
	)
	if err != nil {
		t.Fatal(err)
	}
	cg := run(t, "container-groups", "create", g, "--image", "docker.io/library/busybox:1.36", "--no-autostart",
		"--queue", q, "--queue-port", "8080", "--queue-path", "/job", "-f", auto, "-o", "json")
	if cg.Code != 0 {
		t.Fatalf("create --queue exit %d: %s", cg.Code, cg.Stderr)
	}
	conn, _ := oneDoc(t, cg)["queue_connection"].(map[string]any)
	if conn["queue_name"] != q || conn["port"] != float64(8080) || conn["path"] != "/job" {
		t.Errorf("queue_connection = %v", conn)
	}
	// Nothing reports an image without the worker, so create says it.
	if !strings.Contains(cg.Stderr, "job queue worker") {
		t.Errorf("create --queue stderr has no worker note: %q", cg.Stderr)
	}
	// The API can drop queue_autoscaler on create; either it kept it, or
	// create said it did not.
	if oneDoc(t, cg)["queue_autoscaler"] == nil && !strings.Contains(cg.Stderr, "queue_autoscaler") {
		t.Errorf("queue_autoscaler dropped without a warning: %q", cg.Stderr)
	}
	_ = run(t, "container-groups", "delete", g)
}

// TestWebhookSecretGet reads the organization's webhook secret. It never
// rotates: that would break every receiver verifying with the current key.
func TestWebhookSecretGet(t *testing.T) {
	r := run(t, "webhook-secret", "get", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("webhook-secret get exit %d: %s", r.Code, r.Stderr)
	}
	if k, _ := oneDoc(t, r)["secret_key"].(string); len(k) < 44 {
		t.Errorf("secret_key is %d characters, want at least 44", len(k))
	}
}
