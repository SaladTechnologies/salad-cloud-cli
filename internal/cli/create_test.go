package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/organizationdata"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/util"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/input"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// testGpuClasses is the fixture class list for resolver tests.
func testGpuClasses() []organizationdata.GpuClass {
	mk := func(name, id string) organizationdata.GpuClass {
		return organizationdata.GpuClass{Name: new(name), Id: new(id)}
	}
	return []organizationdata.GpuClass{
		mk("RTX 3060 (12 GB)", "f51baccc-dc95-40fb-a5d1-6d0ee0db31d2"),
		mk("RTX 3060 Ti (8 GB)", "9998fe42-04a5-4807-b3a5-849943f16c38"),
		mk("RTX 4090 (24 GB)", "ed563892-aacd-40f5-80b7-90c9be6c759b"),
	}
}

// testResolver resolves against the fixture list, offline.
func testResolver(t *testing.T) func(string) (string, *clierr.Error) {
	t.Helper()
	classes := testGpuClasses()
	return func(value string) (string, *clierr.Error) {
		return resolveGpuClassName(value, classes)
	}
}

func TestValidateGroupName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		valid bool
	}{
		{"a", false},                           // too short
		{"Bad", false},                         // uppercase
		{"1abc", false},                        // leading digit
		{"abc-", false},                        // trailing hyphen
		{"bad_NAME", false},                    // underscore (and uppercase)
		{"-abc", false},                        // leading hyphen
		{"ab", true},                           // minimal valid
		{"my-app-2", true},                     // the common shape
		{"a" + strings.Repeat("b", 62), true},  // 63 chars: max length
		{"a" + strings.Repeat("b", 63), false}, // 64 chars: too long
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateGroupName(tt.name)
			if !tt.valid {
				// The message quotes the name.
				checkCreateInvalidInput(t, err, tt.name)
				return
			}
			if err != nil {
				t.Fatalf("validateGroupName(%q) = %v, want nil", tt.name, err)
			}
		})
	}
}

// TestValidateCreateRequestBadName pins that the name rule runs as part of
// create's pre-validation and surfaces as exit 2, offline.
func TestValidateCreateRequestBadName(t *testing.T) {
	t.Parallel()
	req := validCreateRequest()
	req.Name = new("bad_NAME")
	err := validateCreateRequest(&req)
	if err == nil {
		t.Fatal("validateCreateRequest = nil for an invalid name")
	}
	if err.Code != clierr.CodeInvalidInput {
		t.Errorf("code = %d, want %d", err.Code, clierr.CodeInvalidInput)
	}
}

func TestResolveGpuClassName(t *testing.T) {
	t.Parallel()
	classes := testGpuClasses()
	tests := []struct {
		name    string
		value   string
		wantID  string
		wantErr string // substring of the error message; "" = success
	}{
		{"exact", "RTX 3060 (12 GB)", "f51baccc-dc95-40fb-a5d1-6d0ee0db31d2", ""},
		{"exact case-insensitive", "rtx 3060 (12 gb)", "f51baccc-dc95-40fb-a5d1-6d0ee0db31d2", ""},
		// "RTX 3060 (12 GB)" is a substring match too, but the exact match
		// wins even though "RTX 3060" alone would be ambiguous.
		{"unique substring", "4090", "ed563892-aacd-40f5-80b7-90c9be6c759b", ""},
		{"ambiguous substring", "3060", "", "ambiguous"},
		{"none", "H100", "", "unknown GPU class"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			id, cerr := resolveGpuClassName(tt.value, classes)
			if tt.wantErr != "" {
				checkCreateInvalidInput(t, cerr, tt.wantErr)
				return
			}
			if cerr != nil {
				t.Fatalf("resolveGpuClassName(%q) error: %v", tt.value, cerr)
			}
			if id != tt.wantID {
				t.Errorf("resolveGpuClassName(%q) = %q, want %q", tt.value, id, tt.wantID)
			}
		})
	}
}

func TestResolveGpuClassNameAmbiguousListsCandidates(t *testing.T) {
	t.Parallel()
	_, cerr := resolveGpuClassName("3060", testGpuClasses())
	if cerr == nil {
		t.Fatal("expected an ambiguity error")
	}
	for _, want := range []string{"RTX 3060 (12 GB)", "RTX 3060 Ti (8 GB)"} {
		if !strings.Contains(cerr.Message, want) {
			t.Errorf("ambiguity message %q does not list %q", cerr.Message, want)
		}
	}
}

// TestResolveGpuClassNameTrimsStoredAndInput: the API has returned class
// names with a leading space (" RTX 5080 (16 GB)"); both the stored names
// and the user's input are trimmed, so an exact match still wins over a
// would-be-ambiguous substring match.
func TestResolveGpuClassNameTrimsStoredAndInput(t *testing.T) {
	t.Parallel()
	classes := []organizationdata.GpuClass{
		{Name: new(" RTX 5080 (16 GB)"), Id: new("id-5080")},
		{Name: new("RTX 5080 Ti (16 GB)"), Id: new("id-5080ti")},
	}
	tests := []struct {
		name   string
		value  string
		wantID string
	}{
		{"stored leading space, clean input", "RTX 5080 (16 GB)", "id-5080"},
		{"stored leading space, padded input", "  RTX 5080 (16 GB) ", "id-5080"},
		{"clean stored, padded input", " RTX 5080 Ti (16 GB)", "id-5080ti"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			id, cerr := resolveGpuClassName(tt.value, classes)
			if cerr != nil {
				t.Fatalf("resolveGpuClassName(%q) error: %v", tt.value, cerr)
			}
			if id != tt.wantID {
				t.Errorf("resolveGpuClassName(%q) = %q, want %q", tt.value, id, tt.wantID)
			}
		})
	}
	// The unknown-name hint lists trimmed names too.
	_, cerr := resolveGpuClassName("H100", classes)
	if cerr == nil {
		t.Fatal("expected an unknown-class error")
	}
	if !strings.Contains(cerr.HintText(), `"RTX 5080 (16 GB)"`) {
		t.Errorf("hint %q should list the trimmed name", cerr.HintText())
	}
}

// fileBase mimics a definition file: everything set, non-default values.
func fileBase() containergroups.ContainerGroupCreationRequest {
	rp := shared.CONTAINER_RESTART_POLICY_NEVER
	return containergroups.ContainerGroupCreationRequest{
		Name: new("from-file"),
		Container: &containergroups.ContainerConfiguration{
			Image: new("docker.io/file/image:1"),
			EnvironmentVariables: map[string]*string{
				"A": new("file-a"),
				"B": new("file-b"),
			},
			Resources: &containergroups.CreateContainerResourceRequirements{
				Cpu:    new(int64(4)),
				Memory: new(int64(2048)),
			},
		},
		Replicas:        new(int64(5)),
		RestartPolicy:   &rp,
		AutostartPolicy: new(false),
	}
}

func TestBuildCreateRequestFlagsOnlyMinimal(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	req, cerr := app.buildCreateRequest(&containergroups.ContainerGroupCreationRequest{}, &createFlags{
		Name:  "web",
		Image: "ghcr.io/me/app:latest",
	})
	if cerr != nil {
		t.Fatalf("buildCreateRequest error: %v", cerr)
	}
	checkCreateFields(t, []createFieldCheck{
		{"name", ptr.Deref(req.Name), "web"},
		{"image", ptr.Deref(req.Container.Image), "ghcr.io/me/app:latest"},
		// Defaults fill everything else.
		{"cpu (default)", ptr.Deref(req.Container.Resources.Cpu), int64(1)},
		{"memory (default)", ptr.Deref(req.Container.Resources.Memory), int64(1024)},
		{"replicas (default)", ptr.Deref(req.Replicas), int64(1)},
		{"restart_policy (default)", ptr.Deref(req.RestartPolicy), shared.CONTAINER_RESTART_POLICY_ALWAYS},
		{"autostart_policy (default)", ptr.Deref(req.AutostartPolicy), true},
		{"priority set (server default when unset)", req.Container.Priority != nil, false},
		{"networking set", req.Networking != nil, false},
	})
	if cerr := validateCreateRequest(&req); cerr != nil {
		t.Errorf("validateCreateRequest rejected the built request: %v", cerr)
	}
}

func TestBuildCreateRequestDefaultsOnlyWhenUnset(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	// A fully-specified base with no flags must come through untouched.
	base := fileBase()
	req, cerr := app.buildCreateRequest(&base, &createFlags{HasBase: true})
	if cerr != nil {
		t.Fatalf("buildCreateRequest error: %v", cerr)
	}
	if got := ptr.Deref(req.Name); got != "from-file" {
		t.Errorf("name = %q, want from-file", got)
	}
	if got := ptr.Deref(req.Container.Resources.Cpu); got != 4 {
		t.Errorf("cpu = %d, want 4 (file value, not default)", got)
	}
	if got := ptr.Deref(req.Container.Resources.Memory); got != 2048 {
		t.Errorf("memory = %d, want 2048", got)
	}
	if got := ptr.Deref(req.Replicas); got != 5 {
		t.Errorf("replicas = %d, want 5", got)
	}
	if *req.RestartPolicy != shared.CONTAINER_RESTART_POLICY_NEVER {
		t.Errorf("restart_policy = %v, want never", *req.RestartPolicy)
	}
	if ptr.Deref(req.AutostartPolicy) {
		t.Error("autostart_policy = true, want false (file value)")
	}
}

func TestBuildCreateRequestFileWithOverrides(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	base := fileBase()
	req, cerr := app.buildCreateRequest(&base, &createFlags{
		HasBase:          true,
		Name:             "renamed",
		Image:            "ghcr.io/me/other:2",
		CPU:              8,
		CPUSet:           true,
		Replicas:         2,
		ReplicasSet:      true,
		RestartPolicy:    "on_failure",
		RestartPolicySet: true,
		Priority:         "batch",
		DisplayName:      "Renamed App",
	})
	if cerr != nil {
		t.Fatalf("buildCreateRequest error: %v", cerr)
	}
	if req.Container.Priority == nil {
		t.Fatal("priority is unset, want batch")
	}
	checkCreateFields(t, []createFieldCheck{
		{"name (positional overrides file)", ptr.Deref(req.Name), "renamed"},
		{"display_name", ptr.Deref(req.DisplayName), "Renamed App"},
		{"image (the flag value)", ptr.Deref(req.Container.Image), "ghcr.io/me/other:2"},
		{"cpu (flag overrides file)", ptr.Deref(req.Container.Resources.Cpu), int64(8)},
		{"memory (file value kept — flag not set)", ptr.Deref(req.Container.Resources.Memory), int64(2048)},
		{"replicas", ptr.Deref(req.Replicas), int64(2)},
		{"restart_policy", ptr.Deref(req.RestartPolicy), shared.CONTAINER_RESTART_POLICY_ON_FAILURE},
		{"priority is null", req.Container.Priority.IsNull, false},
		{"priority", req.Container.Priority.Value, shared.CONTAINER_GROUP_PRIORITY_BATCH},
	})
}

// createFieldCheck is one field of a built request and the value a test
// wants in it; got and want must be of the same comparable type.
type createFieldCheck struct {
	field     string
	got, want any
}

// checkCreateFields reports every check whose field does not hold the value
// it wants.
func checkCreateFields(t *testing.T, checks []createFieldCheck) {
	t.Helper()
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.field, c.got, c.want)
		}
	}
}

// checkCreateInvalidInput fails t unless cerr is an invalid-input error
// whose message contains fragment.
func checkCreateInvalidInput(t *testing.T, cerr *clierr.Error, fragment string) {
	t.Helper()
	if cerr == nil {
		t.Fatalf("error = nil, want an invalid-input error containing %q", fragment)
	}
	if cerr.Code != clierr.CodeInvalidInput || cerr.Kind != clierr.KindInvalidInput {
		t.Errorf("error = (%d, %s), want (%d, invalid_input)", cerr.Code, cerr.Kind, clierr.CodeInvalidInput)
	}
	checkCreateTextHas(t, cerr.Message, fragment)
}

// checkCreateTextHas reports each of wants that text does not contain.
func checkCreateTextHas(t *testing.T, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Errorf("message %q missing %q", text, want)
		}
	}
}

func TestBuildCreateRequestEnvMerge(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	base := fileBase()
	req, cerr := app.buildCreateRequest(&base, &createFlags{
		HasBase: true,
		Env:     []string{"B=flag-b", "C=flag-c", "D=has=equals"},
	})
	if cerr != nil {
		t.Fatalf("buildCreateRequest error: %v", cerr)
	}
	want := map[string]string{
		"A": "file-a",     // kept from the file
		"B": "flag-b",     // flag wins per key
		"C": "flag-c",     // added by flag
		"D": "has=equals", // only the first '=' splits
	}
	env := req.Container.EnvironmentVariables
	if len(env) != len(want) {
		t.Errorf("env has %d entries, want %d: %v", len(env), len(want), env)
	}
	for k, v := range want {
		if got := ptr.Deref(env[k]); got != v {
			t.Errorf("env[%s] = %q, want %q", k, got, v)
		}
	}
}

func TestBuildCreateRequestPortAndAuth(t *testing.T) {
	t.Parallel()
	// fileNetworking is a base that already has networking.
	fileNetworking := func() containergroups.ContainerGroupCreationRequest {
		base := fileBase()
		proto := shared.CONTAINER_NETWORKING_PROTOCOL_HTTP
		base.Networking = &containergroups.CreateContainerGroupNetworking{
			Protocol: &proto, Port: new(int64(80)), Auth: new(true),
		}
		return base
	}
	tests := []struct {
		name     string
		base     containergroups.ContainerGroupCreationRequest
		opts     createFlags
		wantPort int64
		wantAuth bool
	}{
		{
			// --port alone: http, auth on by default.
			name:     "port",
			opts:     createFlags{Image: "ghcr.io/me/app", Port: 8080, PortSet: true},
			wantPort: 8080,
			wantAuth: true,
		},
		{
			// --no-auth with --port flips auth off.
			name:     "port with no-auth",
			opts:     createFlags{Image: "ghcr.io/me/app", Port: 8080, PortSet: true, NoAuth: true},
			wantPort: 8080,
			wantAuth: false,
		},
		{
			// --no-auth against a file that already has networking is fine
			// too, and keeps the file's port.
			name:     "no-auth over file networking",
			base:     fileNetworking(),
			opts:     createFlags{HasBase: true, NoAuth: true},
			wantPort: 80,
			wantAuth: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req, cerr := newTestRun(t).buildCreateRequest(&tt.base, &tt.opts)
			if cerr != nil {
				t.Fatalf("buildCreateRequest error: %v", cerr)
			}
			n := req.Networking
			if n == nil {
				t.Fatal("networking is nil")
			}
			checkCreateFields(t, []createFieldCheck{
				{"port", ptr.Deref(n.Port), tt.wantPort},
				{"protocol", ptr.Deref(n.Protocol), shared.CONTAINER_NETWORKING_PROTOCOL_HTTP},
				{"auth", ptr.Deref(n.Auth), tt.wantAuth},
			})
		})
	}
}

func TestBuildCreateRequestGpuFlag(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	uuid := "ed563892-aacd-40f5-80b7-90c9be6c759b"
	req, cerr := app.buildCreateRequest(&containergroups.ContainerGroupCreationRequest{}, &createFlags{
		Image: "ghcr.io/me/app",
		Gpus:  []string{uuid, "RTX 3060 (12 GB)"},
		ResolveGpu: func(value string) (string, *clierr.Error) {
			if value == uuid {
				t.Errorf("resolver called for a UUID value %q", value)
			}
			return resolveGpuClassName(value, testGpuClasses())
		},
	})
	if cerr != nil {
		t.Fatalf("buildCreateRequest error: %v", cerr)
	}
	got := req.Container.Resources.GpuClasses
	want := []string{uuid, "f51baccc-dc95-40fb-a5d1-6d0ee0db31d2"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("gpu_classes = %v, want %v", got, want)
	}
}

func TestBuildCreateRequestGpuResolverErrors(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	for _, value := range []string{"3060", "H100"} {
		_, cerr := app.buildCreateRequest(&containergroups.ContainerGroupCreationRequest{}, &createFlags{
			Image:      "ghcr.io/me/app",
			Gpus:       []string{value},
			ResolveGpu: testResolver(t),
		})
		if cerr == nil {
			t.Fatalf("buildCreateRequest accepted --gpu %q", value)
		}
		if cerr.Code != clierr.CodeInvalidInput {
			t.Errorf("--gpu %q code = %d, want %d", value, cerr.Code, clierr.CodeInvalidInput)
		}
	}
}

func TestBuildCreateRequestNoAutostart(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	req, cerr := app.buildCreateRequest(&containergroups.ContainerGroupCreationRequest{}, &createFlags{
		Image:       "ghcr.io/me/app",
		NoAutostart: true,
	})
	if cerr != nil {
		t.Fatalf("buildCreateRequest error: %v", cerr)
	}
	if req.AutostartPolicy == nil || *req.AutostartPolicy {
		t.Errorf("autostart_policy = %v, want false", req.AutostartPolicy)
	}
}

func TestBuildCreateRequestValidationFailures(t *testing.T) {
	t.Parallel()
	parent := newTestRun(t)
	parent.setenv("MSYSTEM", "MINGW64") // the Git Bash check runs only under Git Bash
	tests := []struct {
		name     string
		base     containergroups.ContainerGroupCreationRequest
		opts     createFlags
		wantCode int
		wantMsg  string
	}{
		{
			name:     "image required without base",
			opts:     createFlags{Name: "web"},
			wantCode: clierr.CodeUsage,
			wantMsg:  "--image",
		},
		{
			name:     "env without equals",
			opts:     createFlags{Image: "x", Env: []string{"NOEQUALS"}},
			wantCode: clierr.CodeUsage,
			wantMsg:  "--env",
		},
		{
			name: "queue path Git Bash rewrote",
			opts: createFlags{
				Image:        "x",
				Queue:        "q",
				QueuePort:    8000,
				QueuePortSet: true,
				QueuePath:    "C:/Program Files/Git/",
			},
			wantCode: clierr.CodeUsage,
			wantMsg:  "rewritten by Git Bash",
		},
		{
			name:     "env empty key",
			opts:     createFlags{Image: "x", Env: []string{"=v"}},
			wantCode: clierr.CodeUsage,
			wantMsg:  "--env",
		},
		{
			name:     "invalid priority",
			opts:     createFlags{Image: "x", Priority: "urgent"},
			wantCode: clierr.CodeInvalidInput,
			wantMsg:  "--priority",
		},
		{
			name:     "invalid restart policy",
			opts:     createFlags{Image: "x", RestartPolicy: "sometimes", RestartPolicySet: true},
			wantCode: clierr.CodeInvalidInput,
			wantMsg:  "--restart-policy",
		},
		{
			name:     "no-auth without port or networking",
			opts:     createFlags{Image: "x", NoAuth: true},
			wantCode: clierr.CodeUsage,
			wantMsg:  "--no-auth",
		},
		{
			name:     "port out of range",
			opts:     createFlags{Image: "x", Port: 70000, PortSet: true},
			wantCode: clierr.CodeInvalidInput,
			wantMsg:  "--port",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			app := parent.clone(t)
			_, cerr := app.buildCreateRequest(&tt.base, &tt.opts)
			if cerr == nil {
				t.Fatal("buildCreateRequest returned nil error")
			}
			if cerr.Code != tt.wantCode {
				t.Errorf("code = %d, want %d (message: %s)", cerr.Code, tt.wantCode, cerr.Message)
			}
			if !strings.Contains(cerr.Message, tt.wantMsg) {
				t.Errorf("message %q does not contain %q", cerr.Message, tt.wantMsg)
			}
		})
	}
}

func TestBuildCreateRequestPriorityNullable(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	// The priority flag must land as a non-null Nullable on the container.
	req, cerr := app.buildCreateRequest(&containergroups.ContainerGroupCreationRequest{}, &createFlags{
		Image:    "x",
		Priority: "high",
	})
	if cerr != nil {
		t.Fatalf("buildCreateRequest error: %v", cerr)
	}
	p := req.Container.Priority
	var want util.Nullable[shared.ContainerGroupPriority]
	want.Value = shared.CONTAINER_GROUP_PRIORITY_HIGH
	if p == nil || p.IsNull || p.Value != want.Value {
		t.Errorf("priority = %v, want high", p)
	}
}

func TestIsUUID(t *testing.T) {
	t.Parallel()
	if !isUUID("f51baccc-dc95-40fb-a5d1-6d0ee0db31d2") {
		t.Error("isUUID rejected a valid UUID")
	}
	for _, s := range []string{
		"RTX 3060", "f51baccc", "f51baccc-dc95-40fb-a5d1-6d0ee0db31d", "g51baccc-dc95-40fb-a5d1-6d0ee0db31d2",
	} {
		if isUUID(s) {
			t.Errorf("isUUID accepted %q", s)
		}
	}
}

// TestBuildCreateRequestEnvLastFlagWins covers `-e A=1 -e A=2` — repeated
// keys within the flag slice, as opposed to TestBuildCreateRequestEnvMerge's
// file-vs-flag precedence. Verified live: the last occurrence wins.
func TestBuildCreateRequestEnvLastFlagWins(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	req, cerr := app.buildCreateRequest(&containergroups.ContainerGroupCreationRequest{}, &createFlags{
		Image: "x",
		Env:   []string{"A=1", "B=keep", "A=2"},
	})
	if cerr != nil {
		t.Fatalf("buildCreateRequest error: %v", cerr)
	}
	env := req.Container.EnvironmentVariables
	if got := ptr.Deref(env["A"]); got != "2" {
		t.Errorf("env[A] = %q, want %q (last -e wins)", got, "2")
	}
	if got := ptr.Deref(env["B"]); got != "keep" {
		t.Errorf("env[B] = %q, want %q", got, "keep")
	}
	if len(env) != 2 {
		t.Errorf("env has %d entries, want 2: %v", len(env), env)
	}
}

// TestBuildCreateRequestReplicasZeroSurvivesDefaulting pins `--replicas 0`.
// The API's minimum is 0, and the defaulting pass only fills replicas when
// it is nil — so an explicit 0 must reach the request as 0, not get
// "helpfully" replaced by the default of 1. A refactor to
// `if req.Replicas == nil || *req.Replicas == 0` would silently break this.
func TestBuildCreateRequestReplicasZeroSurvivesDefaulting(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	req, cerr := app.buildCreateRequest(&containergroups.ContainerGroupCreationRequest{}, &createFlags{
		Image:       "x",
		Replicas:    0,
		ReplicasSet: true,
	})
	if cerr != nil {
		t.Fatalf("buildCreateRequest error: %v", cerr)
	}
	if req.Replicas == nil {
		t.Fatal("replicas is nil, want an explicit 0")
	}
	if got := *req.Replicas; got != 0 {
		t.Errorf("replicas = %d, want 0", got)
	}
}

// TestBuildCreateRequestReplicasDefaultsWhenUnset is the other half: with
// the flag untouched, the default of 1 still applies.
func TestBuildCreateRequestReplicasDefaultsWhenUnset(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	req, cerr := app.buildCreateRequest(&containergroups.ContainerGroupCreationRequest{}, &createFlags{Image: "x"})
	if cerr != nil {
		t.Fatalf("buildCreateRequest error: %v", cerr)
	}
	if got := ptr.Deref(req.Replicas); got != 1 {
		t.Errorf("replicas = %d, want the default 1", got)
	}
}

// TestValidateWaitAutostart covers the pre-flight conflict: a group created
// with --no-autostart never reaches "running", so --wait could only time
// out. Exit code must be usage (2), and the check must run before any file
// read or API call.
func TestValidateWaitAutostart(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		wait        bool
		noAutostart bool
		wantErr     bool
	}{
		{"neither", false, false, false},
		{"wait alone", true, false, false},
		{"no-autostart alone", false, true, false},
		{"both conflict", true, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cerr := validateWaitAutostart(&createOptions{
				wait:  waitOptions{wait: tt.wait},
				flags: createFlags{NoAutostart: tt.noAutostart},
			})
			if (cerr != nil) != tt.wantErr {
				t.Fatalf("validateWaitAutostart(%v, %v) = %v, want an error: %v",
					tt.wait, tt.noAutostart, cerr, tt.wantErr)
			}
			if cerr == nil {
				return
			}
			if cerr.Code != clierr.CodeUsage {
				t.Errorf("code = %d, want %d (usage)", cerr.Code, clierr.CodeUsage)
			}
			checkCreateTextHas(t, cerr.Message, "--wait", "--no-autostart")
		})
	}
}

// TestRejectUnknownsOffersHatchOnlyForNovelFields is the fix for a live
// finding: the escape-hatch hint used to be attached to every unknown field,
// including ones that are real but simply not writable here. Following it
// sent the field, the API discarded it, and the caller got exit 0 believing
// the write landed — the exact silent no-op strict checking exists to stop.
func TestRejectUnknownsOffersHatchOnlyForNovelFields(t *testing.T) {
	t.Parallel()
	const hatch = "--allow-unknown-fields"
	tests := []struct {
		name      string
		path      string
		wantHatch bool
		wantText  []string
	}{
		{
			// Genuinely unknown: could be a typo or a newer API field.
			name:      "novel field offers the hatch",
			path:      "totally_made_up",
			wantHatch: true,
		},
		{
			// networking carries all seven fields on create; the patch
			// schema carries only port.
			name:      "create-only field explains itself instead",
			path:      "networking.auth",
			wantHatch: false,
			wantText:  []string{"not in this CLI's update schema", "recreate the group to change it"},
		},
		{
			// autostart_policy is create-time; start/stop change the state.
			name:      "another create-only field",
			path:      "autostart_policy",
			wantHatch: false,
			wantText:  []string{"update schema"},
		},
		{
			// Pure response field — the get | edit | update round-trip case.
			name:      "response-only field says so",
			path:      "create_time",
			wantHatch: false,
			wantText:  []string{"is response-only in this CLI's API model and cannot be sent"},
		},
		{
			// Shape asymmetry: responses return priority top-level, requests
			// take container.priority. Point at the request location.
			name:      "relocated field names the request path",
			path:      "priority",
			wantHatch: false,
			wantText:  []string{"container.priority", "belongs at"},
		},
		{
			// A probe nested under container is a likely mistake; the hatch
			// would send a field the API ignores there.
			name:      "top-level field nested under container says where it goes",
			path:      "container.readiness_probe",
			wantHatch: false,
			wantText:  []string{`"container.readiness_probe" belongs at "readiness_probe"`},
		},
		{
			name:      "container field put at the top level says where it goes",
			path:      "image",
			wantHatch: false,
			wantText:  []string{`"image" belongs at "container.image"`},
		},
		{
			// A deeper slip is a different mistake: no swap is suggested,
			// least of all an entrypoint override or the group's name.
			name:      "nested command gets no belongs-at hint",
			path:      "readiness_probe.command",
			wantHatch: true,
		},
		{
			name:      "nested name gets no belongs-at hint",
			path:      "queue_connection.name",
			wantHatch: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			app := newTestRun(t)
			cerr := app.rejectUnknowns([]input.Unknown{
				{Desc: `unknown field "x"`, Path: tt.path},
			})
			if cerr == nil {
				t.Fatal("rejectUnknowns returned nil")
			}
			if got := strings.Contains(errText(cerr), hatch); got != tt.wantHatch {
				t.Errorf("hatch offered = %v, want %v (message: %s)", got, tt.wantHatch, errText(cerr))
			}
			checkCreateTextHas(t, errText(cerr), tt.wantText...)
			if cerr.Kind != clierr.KindUnknownField {
				t.Errorf("kind = %q, want %q", cerr.Kind, clierr.KindUnknownField)
			}
		})
	}
}

// TestRejectUnknownsListsEveryField covers the other half of that finding:
// only the first offender used to be named, so cleaning a document piped
// from `get -o json` took one round-trip per field.
func TestRejectUnknownsListsEveryField(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	cerr := app.rejectUnknowns([]input.Unknown{
		{Desc: `unknown field "id" at the top level`, Path: "id"},
		{Desc: `unknown field "create_time" at the top level`, Path: "create_time"},
		{Desc: `unknown field "gpu_sharing" in "container"`, Path: "container.gpu_sharing"},
	})
	if cerr == nil {
		t.Fatal("rejectUnknowns returned nil")
	}
	for _, want := range []string{"3 unknown fields", "id", "create_time", "gpu_sharing"} {
		if !strings.Contains(cerr.Message, want) {
			t.Errorf("message %q missing %q", cerr.Message, want)
		}
	}
	// Advice must be grouped, not repeated per field.
	if n := strings.Count(cerr.HintText(), "response-only in this CLI's API model and cannot be sent"); n != 1 {
		t.Errorf("advice repeated %d times, want grouped into 1: %s", n, errText(cerr))
	}
	// Three or more response-only fields is the get-round-trip tell, which
	// replaces the list.
	tell := app.rejectUnknowns([]input.Unknown{
		{Desc: `unknown field "id" at the top level`, Path: "id"},
		{Desc: `unknown field "create_time" at the top level`, Path: "create_time"},
		{Desc: `unknown field "version" at the top level`, Path: "version"},
	})
	if !strings.HasPrefix(tell.Message, "this document is a get response, not a patch") ||
		strings.Contains(tell.Message, "unknown fields:") {
		t.Errorf("get-round-trip message = %q", tell.Message)
	}
	if app.rejectUnknowns(nil) != nil {
		t.Error("rejectUnknowns(nil) should be nil")
	}
}

// TestWarnUnknownsFlagsIgnoredFields: when a create-only field IS forced
// through with the flag, the warning must say it will be ignored rather than
// just "sent to the API anyway".
func TestWarnUnknownsFlagsIgnoredFields(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	warnUnknowns(&buf, []input.Unknown{
		{Desc: `unknown field "auth" in "networking"`, Path: "networking.auth"},
	})
	out := buf.String()
	for _, want := range []string{"expect it to be ignored", "update schema"} {
		if !strings.Contains(out, want) {
			t.Errorf("warning %q missing %q", out, want)
		}
	}
}

// TestWarnUnknowns: create mode flags array-nested fields as dropped; update
// mode passes everything through.
func TestWarnUnknowns(t *testing.T) {
	t.Parallel()
	unknowns := []input.Unknown{
		{Desc: `unknown field "a" at the top level`},
		{Desc: `unknown field "b" in "scaling-actions[0]"`, InArray: true},
	}
	var createBuf strings.Builder
	warnCreateUnknowns(&createBuf, unknowns)
	if got := createBuf.String(); !strings.Contains(got, "sent to the API anyway") ||
		!strings.Contains(got, "; dropped, because") {
		t.Errorf("create-mode warnings wrong:\n%s", got)
	}
	var updateBuf strings.Builder
	warnUnknowns(&updateBuf, unknowns)
	if got := updateBuf.String(); strings.Contains(got, "dropped") ||
		strings.Count(got, "sent to the API anyway") != 2 {
		t.Errorf("update-mode warnings wrong:\n%s", got)
	}
}

// TestBuildCreateRequestAutostartTrueBaseSurvives: a definition file with
// autostart_policy:true, no --no-autostart, plus unrelated flags
// (gpu/priority/replicas). The true must survive the
// build AND the JSON marshal that goes on the wire — a stopped group created
// from a true file would be the defect.
func TestBuildCreateRequestAutostartTrueBaseSurvives(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	base := fileBase()
	base.AutostartPolicy = new(true) // file said autostart: true
	req, cerr := app.buildCreateRequest(&base, &createFlags{
		HasBase:  true,
		Gpus:     []string{"11111111-1111-1111-1111-111111111111"},
		Priority: "high",
		Replicas: 10, ReplicasSet: true,
		// NoAutostart deliberately false — no --no-autostart passed.
	})
	if cerr != nil {
		t.Fatalf("buildCreateRequest error: %v", cerr)
	}
	if !ptr.Deref(req.AutostartPolicy) {
		t.Errorf("built autostart_policy = %v, want true (file value must survive)", req.AutostartPolicy)
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"autostart_policy":true`) {
		t.Errorf("wire bytes lack autostart_policy:true:\n%s", b)
	}
}
