//go:build live

// The one running workload: wait, logs, instances, ssh and cp, and the --wait timeout contract.

package livetest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestRunningLifecycle is the only test here that runs a workload: three
// 1-vCPU batch replicas of busybox, stopped and deleted immediately. Three
// rather than one because --wait returns as soon as the group is running,
// and three allocation attempts in parallel reach a first running instance
// far sooner than one (a single batch-priority replica can wait many
// minutes). Cost is still cents. This is what cannot be faked: real
// capacity, real instance states, real logs.
func TestRunningLifecycle(t *testing.T) { //nolint:paralleltest // writes to the live account, one at a time
	if testing.Short() {
		t.Skip("-short: skipping the workload that actually runs")
	}
	time.Sleep(createSpacing)
	g := name("run")
	// sleep 2147483647, never "infinity": busybox rejects it, the instance
	// fails at start and the platform reallocates it, so the group loops
	// across nodes and --wait would just time out.
	body := `{"autostart_policy":true,"restart_policy":"always","replicas":3,
	  "container":{"image":"docker.io/library/busybox:1.36","priority":"batch",
	  "command":["sleep","2147483647"],"resources":{"cpu":1,"memory":1024}}}`
	def := liveTempFile(t, "run.json", body)
	defer func() { _ = run(t, "container-groups", "delete", g) }()

	// Generous timeout regardless: batch capacity can still be slow.
	r := run(t, "container-groups", "create", g, "-f", def, "--wait", "--timeout", "15m", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("create --wait exit %d: %s", r.Code, r.Stderr)
	}
	doc := oneDoc(t, r)
	state := valueAs[map[string]any](doc["current_state"])
	if state["status"] != "running" {
		t.Errorf("--wait -o json emitted status %v, want running (the settled document)", state["status"])
	}

	runningLogsAreNDJSON(t, g)
	runningLogsLimit(t, g)
	runningInstancePrefix(t, g)

	// Remote access, while we have a running instance to talk to.
	t.Run("ssh", func(t *testing.T) { testSSH(t, g) })

	// After ssh: the new instance this rolls out to can take a minute to get its SSH endpoint.
	runningWaitReady(t, g)

	if r := run(t, "container-groups", "stop", g, "--wait", "--timeout", "5m"); r.Code != 0 {
		t.Errorf("stop --wait exit %d: %s", r.Code, r.Stderr)
	}
}

// runningLogsAreNDJSON checks that g's logs are NDJSON: every line a complete document.
func runningLogsAreNDJSON(t *testing.T, g string) {
	t.Helper()
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
}

// runningLogsLimit checks --limit, read newest first, against the same fixed window read whole (--limit 0, oldest
// first): it prints the window's newest entries, oldest first, and notes on stderr when it left older ones out.
func runningLogsLimit(t *testing.T, g string) {
	t.Helper()
	const limit = 3
	until := time.Now().UTC().Add(-30 * time.Second)
	window := []string{
		"--since", until.Add(-20 * time.Minute).Format(time.RFC3339),
		"--until", until.Format(time.RFC3339),
		"-o", "json",
	}
	all := run(t, append([]string{"container-groups", "logs", g, "--limit", "0"}, window...)...)
	limited := run(t, append([]string{"container-groups", "logs", g, "--limit", strconv.Itoa(limit)}, window...)...)
	if all.Code != 0 || limited.Code != 0 {
		t.Fatalf("logs exit %d (--limit 0) and %d (--limit %d): %s %s",
			all.Code, limited.Code, limit, all.Stderr, limited.Stderr)
	}
	whole, got := ndjsonLines(all.Stdout), ndjsonLines(limited.Stdout)
	want := whole[max(0, len(whole)-limit):]
	// Entries that share an emission time may come back in either order, so compare as sets, then check the order.
	if !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
		t.Errorf("--limit %d printed\n%s\nwant the newest %d of the whole window:\n%s",
			limit, strings.Join(got, "\n"), limit, strings.Join(want, "\n"))
	}
	var prev string
	for i, line := range got {
		var entry struct {
			Time string `json:"time"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil || entry.Time < prev {
			t.Errorf("--limit line %d is out of order or not JSON (%v): %s", i+1, err, line)
		}
		prev = entry.Time
	}
	noted := strings.Contains(limited.Stderr, "note: showing the newest "+strconv.Itoa(limit)+" log entries")
	if noted != (len(whole) > limit) {
		t.Errorf("--limit %d with %d entries in the window: note on stderr %t; stderr: %s",
			limit, len(whole), noted, limited.Stderr)
	}
}

// ndjsonLines is out's non-empty lines.
func ndjsonLines(out string) []string {
	var lines []string
	for line := range strings.Lines(out) {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// runningInstancePrefix checks instance prefix resolution against a real instance id of g, through instances get
// and instances update.
func runningInstancePrefix(t *testing.T, g string) {
	t.Helper()
	inst := run(t, "container-groups", "instances", "list", "-c", g, "-o", "json")
	if inst.Code != 0 {
		t.Fatalf("instances list exit %d: %s", inst.Code, inst.Stderr)
	}
	// The instance document's key is "id" (not "instance_id"); an earlier
	// revision decoded the wrong name, got "", and the length guard below
	// silently skipped this whole check on every run.
	var instances runningInstanceList
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
	sshConfig(t, g)

	// One-shot command. The relay does not propagate exit codes, so assert
	// on stdout rather than the code — that caveat is itself the contract.
	r := sshOneShot(t, g)
	if !strings.Contains(r.Stdout, "hello-from-instance") {
		t.Errorf("ssh one-shot did not echo through the relay; stdout %q, stderr %q", r.Stdout, r.Stderr)
	}
	// The sh -c wrapping is what makes shell syntax work at all through a
	// relay that execs raw argv.
	sh := run(t, "container-groups", "ssh", g, "--", "echo", "a", "&&", "echo", "b")
	if !strings.Contains(sh.Stdout, "a") || !strings.Contains(sh.Stdout, "b") {
		t.Errorf("shell operators did not survive the sh -c wrapping; stdout %q, stderr %q", sh.Stdout, sh.Stderr)
	}

	sshCopyRoundTrip(t, g)
}

// sshConfig checks ssh --config. It opens no session, so it works even without a usable key and proves the relay
// endpoint was resolved.
func sshConfig(t *testing.T, g string) {
	t.Helper()
	cfg := run(t, "container-groups", "ssh", g, "--config")
	if cfg.Code != 0 {
		t.Errorf("ssh --config exit %d: %s", cfg.Code, cfg.Stderr)
		return
	}
	for _, want := range []string{"Host ", "HostName ", "Port "} {
		if !strings.Contains(cfg.Stdout, want) {
			t.Errorf("ssh --config output missing %q; got:\n%s", want, cfg.Stdout)
		}
	}
}

// sshOneShot runs one echo through ssh on g and returns its result, skipping the test (failing it under
// SALAD_CLI_TEST_REQUIRE_SSH=1) when every attempt is denied on publickey.
//
// A publickey denial here is almost always environmental, not a CLI bug:
// the registered key must be on the machine running the binary. Running
// the linux build under WSL offers WSL's ~/.ssh key, not the Windows
// host's registered one, and is denied every time, which can look like a
// platform fault. Point SALAD_CLI_TEST_BIN at a binary whose host holds
// the registered key.
//
// A couple of retries still guard against a genuine transient without
// masking a real failure for long.
func sshOneShot(t *testing.T, g string) result {
	t.Helper()
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
	return r
}

// sshCopyRoundTrip pushes a file to g with cp and reads it back through the same relay, with ssh and with cp. The
// group runs several replicas and "first running instance" can change between calls as instances come and go, so
// it pins one instance for the whole round trip — otherwise the write and the read could land on different
// containers and fail for a reason that has nothing to do with the CLI.
func sshCopyRoundTrip(t *testing.T, g string) {
	t.Helper()
	inst := firstRunningInstance(t, g)
	payload := "livetest-" + prefix
	local := liveTempFile(t, "up.txt", payload)
	if up := run(t, "container-groups", "cp", local, g+"/"+inst+":/tmp/up.txt"); up.Code != 0 {
		t.Errorf("cp up exit %d: %s", up.Code, up.Stderr)
	}
	back := run(t, "container-groups", "ssh", g, "--instance", inst, "--", "cat", "/tmp/up.txt")
	if !strings.Contains(back.Stdout, payload) {
		t.Errorf("uploaded file did not read back; stdout %q, stderr %q", back.Stdout, back.Stderr)
	}
	down := filepath.Join(t.TempDir(), "down.txt")
	if dl := run(t, "container-groups", "cp", g+"/"+inst+":/tmp/up.txt", down); dl.Code != 0 {
		t.Errorf("cp down exit %d: %s", dl.Code, dl.Stderr)
		return
	}
	b, err := os.ReadFile(down) //nolint:gosec // G304: down is in the test's own temp folder
	if err != nil || !strings.Contains(string(b), payload) {
		t.Errorf("round-tripped file = %q (err %v), want %q", b, err, payload)
	}
}

// runningInstanceList is the part of an instances list document the running tests read.
type runningInstanceList struct {
	Instances []runningInstance `json:"items"`
}

// runningInstance is one instance of a runningInstanceList.
type runningInstance struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Version int64  `json:"version"`
	Ready   bool   `json:"ready"`
}

// runningWaitReady: update --wait-ready returns only once the replicas run the new version and are ready, and the
// update says the instances move onto it. An environment change makes the new version; one replica keeps batch
// capacity from deciding how long it takes.
func runningWaitReady(t *testing.T, g string) {
	t.Helper()
	r := run(t, "container-groups", "update", g, "--replicas", "1", "-e", "SALAD_CLI_LIVE_READY=1",
		"--wait-ready", "--timeout", "15m", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("update --wait-ready exit %d: %s", r.Code, r.Stderr)
	}
	if !strings.Contains(r.Stderr, "is now version") {
		t.Errorf("no new-version note on stderr: %s", r.Stderr)
	}
	version := int64(valueAs[float64](oneDoc(t, r)["version"]))
	r = run(t, "container-groups", "instances", "list", "-c", g, "-o", "json")
	if r.Code != 0 {
		t.Fatalf("instances list exit %d: %s", r.Code, r.Stderr)
	}
	var doc runningInstanceList
	if err := json.Unmarshal([]byte(r.Stdout), &doc); err != nil {
		t.Fatalf("instances list: %v", err)
	}
	for _, i := range doc.Instances {
		if i.Version >= version && i.State == "running" && i.Ready {
			return
		}
	}
	t.Errorf("--wait-ready returned, but no instance runs version %d and is ready: %s", version, r.Stdout)
}

// firstRunningInstance returns the id of one running instance of g, so a
// multi-step check can address the same container throughout.
func firstRunningInstance(t *testing.T, g string) string {
	t.Helper()
	r := run(t, "container-groups", "instances", "list", "-c", g, "-o", "json")
	if r.Code != 0 {
		t.Fatalf("instances list exit %d: %s", r.Code, r.Stderr)
	}
	var doc runningInstanceList
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
func TestWaitTimeoutContract(t *testing.T) { //nolint:paralleltest // writes to the live account, one at a time
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
