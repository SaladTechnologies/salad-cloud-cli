//go:build live

// The container-group write paths: create, read back, patch, scale, copy, and the name and command rules.

package livetest

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestGroupLifecycle is the spine: create stopped, read it back, patch it,
// scale it, then delete. Nothing here runs, so it costs nothing.
func TestGroupLifecycle(t *testing.T) { //nolint:paralleltest // writes to the live account, one at a time
	g := name("life")
	lifecycleCreate(t, g)
	lifecycleGet(t, g)

	// A freshly created group is pending; a patch during that window is
	// rejected, so wait it out before writing again.
	settle(t, g)

	lifecycleWritableRoundTrip(t, g)
	sched := lifecycleScaleHolds(t, g)
	lifecycleKeepSchedule(t, g, sched)

	if r := run(t, "container-groups", "delete", g, "-o", "json"); r.Code != 0 {
		t.Fatalf("delete exit %d: %s", r.Code, r.Stderr)
	}
}

// lifecycleCreate creates TestGroupLifecycle's group g stopped with two replicas and checks create's document.
func lifecycleCreate(t *testing.T, g string) {
	t.Helper()
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
}

// lifecycleGet checks that get round-trips the same shape as create, every key present.
func lifecycleGet(t *testing.T, g string) {
	t.Helper()
	r := run(t, "container-groups", "get", g, "-o", "json")
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
}

// lifecycleWritableRoundTrip patches g, then feeds get --writable back through update -f.
func lifecycleWritableRoundTrip(t *testing.T, g string) {
	t.Helper()
	// update settles by default, so the next write is accepted.
	patch := liveTempFile(t, "p.json", `{"replicas":3}`)
	if r := run(t, "container-groups", "update", g, "-f", patch); r.Code != 0 {
		t.Fatalf("update exit %d: %s", r.Code, r.Stderr)
	}
	// get --writable must round-trip through update -f with no rejection
	// and no warning (notes are fine). Done before scheduled scaling is
	// enabled, so the replicas in the document do not put a schedule on hold.
	wr := run(t, "container-groups", "get", g, "--writable")
	if wr.Code != 0 {
		t.Fatalf("get --writable exit %d: %s", wr.Code, wr.Stderr)
	}
	wdoc := liveTempFile(t, "writable.json", wr.Stdout)
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
}

// lifecycleScaleHolds enables scheduled scaling on g, scales it straight after, and checks that the schedule went on
// hold with a note. It returns the schedule patch file for lifecycleKeepSchedule.
func lifecycleScaleHolds(t *testing.T, g string) string {
	t.Helper()
	// The point of settling: an immediate second write must not be rejected
	// with pending_update_in_progress. Enable scheduled scaling first so the
	// scale below exercises the hold rule: setting replicas puts the schedule
	// on hold, and scale — the replicas verb — must say so (as a note, not a
	// warning: it is a documented platform rule).
	sched := liveTempFile(
		t,
		"sched.json",
		`{"scheduled-scaling-enabled":true,"scaling-actions":[{"replicas":1,"schedule":"0 9 * * 1-5"}]}`,
	)
	if r := run(t, "container-groups", "update", g, "-f", sched); r.Code != 0 {
		t.Fatalf("enable scheduled scaling exit %d: %s", r.Code, r.Stderr)
	}
	r := run(t, "container-groups", "scale", g, "--replicas", "1", "-o", "json")
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
	if held["scheduled-scaling-enabled"] != any(false) {
		t.Errorf("scheduled-scaling-enabled after scale = %v, want false (on hold)", held["scheduled-scaling-enabled"])
	}
	return sched
}

// lifecycleKeepSchedule re-enables g's schedule from sched and checks that scale --keep-schedule keeps it.
func lifecycleKeepSchedule(t *testing.T, g, sched string) {
	t.Helper()
	// --keep-schedule: re-enable, scale again with the flag, and the schedule
	// must survive (two writes under the hood).
	r := run(t, "container-groups", "update", g, "-f", sched)
	if r.Code != 0 {
		t.Fatalf("re-enable scheduled scaling exit %d: %s", r.Code, r.Stderr)
	}
	if r = run(t, "container-groups", "scale", g, "--replicas", "2", "--keep-schedule", "-o", "json"); r.Code != 0 {
		t.Fatalf("scale --keep-schedule exit %d: %s", r.Code, r.Stderr)
	} else if !strings.Contains(r.Stderr, "re-enabled") {
		t.Errorf("scale --keep-schedule did not report the re-enable; stderr %q", r.Stderr)
	}
	kept := oneDoc(
		t,
		run(t, "container-groups", "get", g, "-o", "json"),
	)
	if kept["scheduled-scaling-enabled"] != any(true) ||
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
	if act["ok"] != any(true) {
		t.Errorf("scale ok = %v, want true", act["ok"])
	}
}

// TestDeletedNameStaysTaken pins the uniqueness rule: names must be unique
// within the organization and project, including against deleted groups —
// deletion is a soft delete and never releases the name, which is why every
// run here uses unique names. This is deliberate platform behavior
// (docs/notes.md). If a deleted name ever becomes reusable,
// the rule itself has changed and the naming convention can be relaxed.
func TestDeletedNameStaysTaken(t *testing.T) { //nolint:paralleltest // writes to the live account, one at a time
	g := name("resv")
	created := run(t, "container-groups", "create", g, "--image", "docker.io/library/busybox:1.36", "--no-autostart")
	if created.Code != 0 {
		t.Fatalf("create exit %d: %s", created.Code, created.Stderr)
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

// TestGpuConfigCreatesStopped exercises GPU class resolution against the real
// catalog. Created stopped, so no GPU is ever allocated and it costs nothing.
func TestGpuConfigCreatesStopped(t *testing.T) { //nolint:paralleltest // writes to the live account, one at a time
	time.Sleep(createSpacing)
	g := name("gpu")
	r := run(t, "container-groups", "create", g, "--image", "docker.io/library/busybox:1.36",
		"--no-autostart", "--gpu", "rtx 3060 (12 gb)", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("create with --gpu exit %d: %s", r.Code, r.Stderr)
	}
	doc := oneDoc(t, r)
	container := valueAs[map[string]any](doc["container"])
	resources := valueAs[map[string]any](container["resources"])
	classes := valueAs[[]any](resources["gpu_classes"])
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
func TestContainerCommand(t *testing.T) { //nolint:paralleltest // writes to the live account, one at a time
	time.Sleep(createSpacing)
	g := name("cmd")
	script := `echo "hi there"; sleep 2147483647`
	r := run(t, "container-groups", "create", g, "--image", "docker.io/library/busybox:1.36", "--no-autostart",
		"-o", "json", "--", "sh", "-c", script)
	if r.Code != 0 {
		t.Fatalf("create exit %d: %s", r.Code, r.Stderr)
	}
	defer run(t, "container-groups", "delete", g)
	got := containerCommand(t, r.Stdout)
	if strings.Join(got, "\x00") != strings.Join([]string{"sh", "-c", script}, "\x00") {
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
	if got := containerCommand(t, r.Stdout); strings.Join(got, " ") != "sleep 2147483647" {
		t.Errorf("updated command = %q", got)
	}

	containerCommandClear(t, g)

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

// containerCommand is container.command in doc, a group document on stdout.
func containerCommand(t *testing.T, doc string) []string {
	t.Helper()
	var got containerCommandDoc
	if err := json.Unmarshal([]byte(doc), &got); err != nil {
		t.Fatalf("stdout: %v\n%s", err, doc)
	}
	return got.Container.Command
}

// containerCommandDoc is the part of a group document containerCommand reads.
type containerCommandDoc struct {
	Container containerCommandField `json:"container"`
}

// containerCommandField is a containerCommandDoc's container, with its command.
type containerCommandField struct {
	Command []string `json:"command"`
}

// containerCommandClear clears g's command the documented way back to the image's own: an empty list in a patch
// file, with no drift warning on the read-back.
func containerCommandClear(t *testing.T, g string) {
	t.Helper()
	clearDoc := liveTempFile(t, "clear.json", `{"container":{"command":[]}}`)
	r := run(t, "container-groups", "update", g, "-f", clearDoc, "-o", "json")
	if r.Code != 0 {
		t.Fatalf("clear exit %d: %s", r.Code, r.Stderr)
	}
	if got := containerCommand(t, r.Stdout); len(got) != 0 {
		t.Errorf("cleared command = %q, want []", got)
	}
	if strings.Contains(r.Stderr, "warning:") {
		t.Errorf("clear warned: %q", r.Stderr)
	}
}

// TestCreateFrom copies a stopped group whose settings are all non-default,
// so a field that fails to come across shows up as the default.
func TestCreateFrom(t *testing.T) { //nolint:paralleltest // writes to the live account, one at a time
	time.Sleep(createSpacing)
	src, cp := name("from-src"), name("from-copy")
	// An HTTP probe with no headers: the API rejects the probe unless the
	// empty headers list is sent, from the file here and from the source on
	// the copy below.
	probe := liveTempFile(
		t,
		"probe.yaml",
		"readiness_probe:\n  http: {path: /, port: 8080, scheme: http, headers: []}\n"+
			"  initial_delay_seconds: 0\n  period_seconds: 5\n  timeout_seconds: 2\n  success_threshold: 1\n  "+
			"failure_threshold: 3\n",
	)
	created := run(t, "container-groups", "create", src, "--image", "docker.io/library/busybox:1.36", "-f", probe,
		"--restart-policy", "never", "--port", "8080", "--no-auth", "-e", "MODE=prod",
		"--priority", "high", "--replicas", "2", "--no-autostart")
	if created.Code != 0 {
		t.Fatalf("create source exit %d: %s", created.Code, created.Stderr)
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
	checkCreateFromCopy(t, r, cp)
}

// checkCreateFromCopy checks that create --from's document r carries every non-default setting of
// TestCreateFrom's source, under the copy's own name cp.
func checkCreateFromCopy(t *testing.T, r result, cp string) {
	t.Helper()
	var got createFromDoc
	if err := json.Unmarshal([]byte(r.Stdout), &got); err != nil {
		t.Fatalf("stdout: %v\n%s", err, r.Stdout)
	}
	if got.ReadinessProbe == nil || got.ReadinessProbe.HTTP == nil || got.ReadinessProbe.HTTP.Path != "/" {
		t.Errorf("readiness_probe not copied: %s", r.Stdout)
	}
	if got.Name != cp || got.DisplayName != cp {
		t.Errorf("name/display_name = %q/%q, want both %q", got.Name, got.DisplayName, cp)
	}
	checkCreateFromSettings(t, &got)
}

// createFromDoc is the part of create --from's document TestCreateFrom checks.
type createFromDoc struct {
	Name           string                    `json:"name"`
	DisplayName    string                    `json:"display_name"`
	RestartPolicy  string                    `json:"restart_policy"`
	Priority       string                    `json:"priority"`
	Replicas       int                       `json:"replicas"`
	Networking     createFromNetworking      `json:"networking"`
	Container      createFromContainer       `json:"container"`
	ReadinessProbe *createFromReadinessProbe `json:"readiness_probe"`
}

// createFromNetworking is a createFromDoc's networking.
type createFromNetworking struct {
	Port int  `json:"port"`
	Auth bool `json:"auth"`
}

// createFromContainer is a createFromDoc's container, with its environment.
type createFromContainer struct {
	Env map[string]string `json:"environment_variables"`
}

// createFromReadinessProbe is a createFromDoc's readiness probe.
type createFromReadinessProbe struct {
	HTTP *createFromHTTPProbe `json:"http"`
}

// createFromHTTPProbe is a createFromReadinessProbe's HTTP check, with its path.
type createFromHTTPProbe struct {
	Path string `json:"path"`
}

// checkCreateFromSettings checks the copied restart policy, priority, replicas, networking and environment, with the
// copy's own EXTRA variable added.
func checkCreateFromSettings(t *testing.T, got *createFromDoc) {
	t.Helper()
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
