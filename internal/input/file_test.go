package input

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

type demo struct {
	Name     string `json:"name"`
	Replicas int64  `json:"replicas"`
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadYAML(t *testing.T) {
	p := writeTemp(t, "demo.yaml", "name: hello\nreplicas: 3\n")
	var d demo
	if err := Load(p, &d); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if d.Name != "hello" || d.Replicas != 3 {
		t.Errorf("Load got %+v, want {hello 3}", d)
	}
}

func TestLoadJSON(t *testing.T) {
	p := writeTemp(t, "demo.json", `{"name": "hello", "replicas": 3}`)
	var d demo
	if err := Load(p, &d); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if d.Name != "hello" || d.Replicas != 3 {
		t.Errorf("Load got %+v, want {hello 3}", d)
	}
}

func TestLoadStdin(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("name: piped\nreplicas: 1\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	orig := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = orig; f.Close() }()

	var d demo
	if cerr := Load("-", &d); cerr != nil {
		t.Fatalf("Load returned error: %v", cerr)
	}
	if d.Name != "piped" || d.Replicas != 1 {
		t.Errorf("Load got %+v, want {piped 1}", d)
	}
}

func TestLoadMissingFile(t *testing.T) {
	var d demo
	err := Load(filepath.Join(t.TempDir(), "no-such-file.yaml"), &d)
	if err == nil {
		t.Fatal("Load returned nil for a missing file")
	}
	if err.Code != clierr.CodeInvalidInput {
		t.Errorf("Load code = %d, want %d", err.Code, clierr.CodeInvalidInput)
	}
	if !strings.Contains(err.Message, "no-such-file.yaml") {
		t.Errorf("Load message %q does not name the file", err.Message)
	}
}

func TestLoadMalformedYAML(t *testing.T) {
	p := writeTemp(t, "bad.yaml", "name: [unclosed\n")
	var d demo
	err := Load(p, &d)
	if err == nil {
		t.Fatal("Load returned nil for malformed YAML")
	}
	if err.Code != clierr.CodeInvalidInput {
		t.Errorf("Load code = %d, want %d", err.Code, clierr.CodeInvalidInput)
	}
	if !strings.Contains(err.Message, "bad.yaml") {
		t.Errorf("Load message %q does not name the file", err.Message)
	}
}

// TestLoadRawPreservesEmptyArray pins the raw-passthrough contract that
// update -f depends on: an explicit empty array (merge-patch for "clear
// this field") must survive into the returned JSON document, in both YAML
// and JSON forms. Re-marshaling the struct would drop it via omitempty.
func TestLoadRawPreservesEmptyArray(t *testing.T) {
	tests := []struct {
		name, file, content string
	}{
		{"yaml", "patch.yaml", "scaling-actions: []\n"},
		{"json", "patch.json", `{"scaling-actions": []}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := writeTemp(t, tt.file, tt.content)
			var patch containergroups.ContainerGroupPatch
			raw, cerr := LoadRaw(p, &patch)
			if cerr != nil {
				t.Fatalf("LoadRaw returned error: %v", cerr)
			}
			if !strings.Contains(string(raw), `"scaling-actions":[]`) {
				t.Errorf("raw = %s, want the explicit empty array preserved", raw)
			}
			// The struct half parsed it too (validation path).
			if patch.ScalingActions == nil || len(patch.ScalingActions) != 0 {
				t.Errorf("struct ScalingActions = %v, want an empty (non-nil) slice", patch.ScalingActions)
			}
		})
	}
}

func TestLoadRawMissingFile(t *testing.T) {
	var d demo
	raw, err := LoadRaw(filepath.Join(t.TempDir(), "no-such-file.yaml"), &d)
	if err == nil {
		t.Fatal("LoadRaw returned nil for a missing file")
	}
	if raw != nil {
		t.Errorf("raw = %s, want nil on error", raw)
	}
	if err.Code != clierr.CodeInvalidInput {
		t.Errorf("LoadRaw code = %d, want %d", err.Code, clierr.CodeInvalidInput)
	}
}

// TestLoadScheduledScalingExampleRoundTrip pins examples/scheduled-scaling.yaml:
// both kebab-case fields must parse into the creation request AND survive
// LoadRaw's raw output with their kebab-case spelling intact.
func TestLoadScheduledScalingExampleRoundTrip(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "scheduled-scaling.yaml")
	var req containergroups.ContainerGroupCreationRequest
	raw, cerr := LoadRaw(path, &req)
	if cerr != nil {
		t.Fatalf("LoadRaw returned error: %v", cerr)
	}
	if got := ptr.Deref(req.Name); got != "scheduled-demo" {
		t.Errorf("name = %q, want %q", got, "scheduled-demo")
	}
	if req.ScheduledScalingEnabled == nil || !*req.ScheduledScalingEnabled {
		t.Errorf("scheduled-scaling-enabled = %v, want true", req.ScheduledScalingEnabled)
	}
	if len(req.ScalingActions) != 2 {
		t.Fatalf("scaling-actions has %d entries, want 2", len(req.ScalingActions))
	}
	up, down := req.ScalingActions[0], req.ScalingActions[1]
	if ptr.Deref(up.Replicas) != 2 || ptr.Deref(up.Schedule) != "0 9 * * 1-5" {
		t.Errorf("action[0] = %d @ %q, want 2 @ \"0 9 * * 1-5\"", ptr.Deref(up.Replicas), ptr.Deref(up.Schedule))
	}
	if ptr.Deref(down.Replicas) != 0 || ptr.Deref(down.Schedule) != "0 17 * * 1-5" {
		t.Errorf("action[1] = %d @ %q, want 0 @ \"0 17 * * 1-5\"", ptr.Deref(down.Replicas), ptr.Deref(down.Schedule))
	}
	for _, key := range []string{`"scheduled-scaling-enabled":true`, `"scaling-actions":[`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("raw output lacks %s", key)
		}
	}
}

// TestLoadExampleRoundTrip pins examples/hello-cpu.yaml: it must unmarshal
// into a ContainerGroupCreationRequest with every demo-critical field set.
func TestLoadExampleRoundTrip(t *testing.T) {
	var req containergroups.ContainerGroupCreationRequest
	if err := Load(filepath.Join("..", "..", "examples", "hello-cpu.yaml"), &req); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := ptr.Deref(req.Name); got != "saladcloud-demo" {
		t.Errorf("name = %q, want %q", got, "saladcloud-demo")
	}
	if got := ptr.Deref(req.Replicas); got != 1 {
		t.Errorf("replicas = %d, want 1", got)
	}
	if req.RestartPolicy == nil || string(*req.RestartPolicy) != "always" {
		t.Errorf("restart_policy = %v, want always", req.RestartPolicy)
	}
	if req.AutostartPolicy == nil || !*req.AutostartPolicy {
		t.Errorf("autostart_policy = %v, want true", req.AutostartPolicy)
	}
	c := req.Container
	if c == nil {
		t.Fatal("container is nil")
	}
	if got := ptr.Deref(c.Image); got != "docker.io/library/busybox:1.36" {
		t.Errorf("container.image = %q, want busybox", got)
	}
	if c.Priority == nil || c.Priority.IsNull || string(c.Priority.Value) != "batch" {
		t.Errorf("container.priority = %v, want batch", c.Priority)
	}
	if c.Resources == nil {
		t.Fatal("container.resources is nil")
	}
	if got := ptr.Deref(c.Resources.Cpu); got != 1 {
		t.Errorf("container.resources.cpu = %d, want 1", got)
	}
	if got := ptr.Deref(c.Resources.Memory); got != 1024 {
		t.Errorf("container.resources.memory = %d, want 1024", got)
	}
	if c.Command == nil || c.Command.IsNull {
		t.Fatal("container.command is not set")
	}
	if len(c.Command.Value) != 3 || c.Command.Value[0] != "sh" || c.Command.Value[1] != "-c" {
		t.Errorf("container.command = %v, want a 3-element sh -c loop", c.Command.Value)
	}
	if !strings.Contains(c.Command.Value[2], "while true") {
		t.Errorf("container.command[2] = %q, want a shell loop", c.Command.Value[2])
	}
	// The loop's echo contains " #$i", which in an unquoted YAML scalar starts
	// a comment and silently truncates the command mid-quote — the container
	// then crash-loops with "unterminated quoted string". The example shipped
	// that way once; pin the full text so a YAML edit cannot reintroduce it.
	if !strings.Contains(c.Command.Value[2], "#$i") || !strings.HasSuffix(c.Command.Value[2], "sleep 5; done") {
		t.Errorf("container.command[2] = %q, want the full loop through \"sleep 5; done\" (a YAML comment truncated it before)", c.Command.Value[2])
	}
}

// TestLoadSleepExampleRoundTrip pins examples/sleep-cpu.json (pure JSON —
// it doubles as the "definition files are just the API create payload"
// exhibit in the README and docs/guide.md).
func TestLoadSleepExampleRoundTrip(t *testing.T) {
	var req containergroups.ContainerGroupCreationRequest
	if err := Load(filepath.Join("..", "..", "examples", "sleep-cpu.json"), &req); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := ptr.Deref(req.Name); got != "sleep-demo" {
		t.Errorf("name = %q, want %q", got, "sleep-demo")
	}
	if got := ptr.Deref(req.Replicas); got != 1 {
		t.Errorf("replicas = %d, want 1", got)
	}
	if req.RestartPolicy == nil || string(*req.RestartPolicy) != "always" {
		t.Errorf("restart_policy = %v, want always", req.RestartPolicy)
	}
	if req.AutostartPolicy == nil || !*req.AutostartPolicy {
		t.Errorf("autostart_policy = %v, want true", req.AutostartPolicy)
	}
	c := req.Container
	if c == nil {
		t.Fatal("container is nil")
	}
	if got := ptr.Deref(c.Image); got != "docker.io/library/busybox:1.36" {
		t.Errorf("container.image = %q, want busybox", got)
	}
	// "2147483647", not "infinity": busybox's sleep takes an integer, and an
	// invalid command fails the instance at start, so the platform reallocates
	// it and the group loops across nodes instead of settling — the example has
	// to stay copy-pasteable.
	if c.Command == nil || len(c.Command.Value) != 2 ||
		c.Command.Value[0] != "sleep" || c.Command.Value[1] != "2147483647" {
		t.Errorf("container.command = %v, want [sleep 2147483647]", c.Command)
	}
	if c.Priority == nil || c.Priority.IsNull || string(c.Priority.Value) != "batch" {
		t.Errorf("container.priority = %v, want batch", c.Priority)
	}
	if c.Resources == nil {
		t.Fatal("container.resources is nil")
	}
	if got := ptr.Deref(c.Resources.Cpu); got != 1 {
		t.Errorf("container.resources.cpu = %d, want 1", got)
	}
	if got := ptr.Deref(c.Resources.Memory); got != 1024 {
		t.Errorf("container.resources.memory = %d, want 1024", got)
	}
}

// TestLoadGpuExampleRoundTrip pins examples/gpu-small.yaml.
func TestLoadGpuExampleRoundTrip(t *testing.T) {
	var req containergroups.ContainerGroupCreationRequest
	if err := Load(filepath.Join("..", "..", "examples", "gpu-small.yaml"), &req); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := ptr.Deref(req.Name); got != "gpu-demo" {
		t.Errorf("name = %q, want %q", got, "gpu-demo")
	}
	if got := ptr.Deref(req.Replicas); got != 1 {
		t.Errorf("replicas = %d, want 1", got)
	}
	if req.RestartPolicy == nil || string(*req.RestartPolicy) != "always" {
		t.Errorf("restart_policy = %v, want always", req.RestartPolicy)
	}
	if req.AutostartPolicy == nil || !*req.AutostartPolicy {
		t.Errorf("autostart_policy = %v, want true", req.AutostartPolicy)
	}
	c := req.Container
	if c == nil {
		t.Fatal("container is nil")
	}
	if got := ptr.Deref(c.Image); got != "docker.io/nvidia/cuda:12.4.1-base-ubuntu22.04" {
		t.Errorf("container.image = %q, want nvidia/cuda base", got)
	}
	if c.Priority == nil || c.Priority.IsNull || string(c.Priority.Value) != "batch" {
		t.Errorf("container.priority = %v, want batch", c.Priority)
	}
	if c.Resources == nil {
		t.Fatal("container.resources is nil")
	}
	if got := ptr.Deref(c.Resources.Cpu); got != 2 {
		t.Errorf("container.resources.cpu = %d, want 2", got)
	}
	if got := ptr.Deref(c.Resources.Memory); got != 4096 {
		t.Errorf("container.resources.memory = %d, want 4096", got)
	}
	if len(c.Resources.GpuClasses) != 1 ||
		c.Resources.GpuClasses[0] != "f51baccc-dc95-40fb-a5d1-6d0ee0db31d2" {
		t.Errorf("container.resources.gpu_classes = %v, want the RTX 3060 id", c.Resources.GpuClasses)
	}
}
