package cli

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// samePatch compares two JSON documents structurally.
func samePatch(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("patch is not JSON: %v\n%s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad expectation: %v", err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("patch = %s\nwant    %s", got, want)
	}
}

func TestBuildUpdatePatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		file    string
		flags   updateFlags
		current map[string]*string
		want    string
	}{
		{
			name:  "flags only",
			flags: updateFlags{Image: "ghcr.io/me/app:v2", ImageSet: true, Replicas: 4, ReplicasSet: true},
			want:  `{"container":{"image":"ghcr.io/me/app:v2"},"replicas":4}`,
		},
		{
			name: "flags win over the file; the rest of the file survives",
			file: `{"container":{"image":"old","resources":{"cpu":2}},"replicas":3}`,
			flags: updateFlags{
				Image:       "new",
				ImageSet:    true,
				Replicas:    5,
				ReplicasSet: true,
				Memory:      2048,
				MemorySet:   true,
			},
			want: `{"container":{"image":"new","resources":{"cpu":2,"memory":2048}},"replicas":5}`,
		},
		{
			name: "every flag lands on its patch field",
			flags: updateFlags{
				CPU:            4,
				CPUSet:         true,
				Priority:       "low",
				PrioritySet:    true,
				Port:           8080,
				PortSet:        true,
				DisplayName:    "App",
				DisplayNameSet: true,
			},
			want: `{"container":{"resources":{"cpu":4},"priority":"low"},` +
				`"networking":{"port":8080},"display_name":"App"}`,
		},
		{
			name:    "-e merges into the current variables and the file's changes",
			file:    `{"container":{"environment_variables":{"B":null,"C":"3"}}}`,
			flags:   updateFlags{Env: []string{"A=9", "D=4", "E=x=y"}},
			current: map[string]*string{"A": ptr.To("1"), "B": ptr.To("2"), "Z": ptr.To("26")},
			want:    `{"container":{"environment_variables":{"A":"9","C":"3","D":"4","E":"x=y","Z":"26"}}}`,
		},
		{
			name:    "without -e the file's variables are sent as written",
			file:    `{"container":{"environment_variables":{"X":"1"}}}`,
			flags:   updateFlags{Image: "i", ImageSet: true},
			current: map[string]*string{"OTHER": ptr.To("kept only by the server's own rule")},
			want:    `{"container":{"image":"i","environment_variables":{"X":"1"}}}`,
		},
		{
			name:  "explicit null and empty array in the file are preserved",
			file:  `{"container":{"command":[]},"display_name":null}`,
			flags: updateFlags{Image: "i", ImageSet: true},
			want:  `{"container":{"command":[],"image":"i"},"display_name":null}`,
		},
		{
			name: "--gpu names resolve, UUIDs pass through",
			flags: updateFlags{
				Gpus:       []string{"RTX 4090 (24 GB)", "11111111-1111-1111-1111-111111111111"},
				ResolveGpu: func(string) (string, *clierr.Error) { return "ed563892-aacd-40f5-80b7-90c9be6c759b", nil },
			},
			want: `{"container":{"resources":{"gpu_classes":` +
				`["ed563892-aacd-40f5-80b7-90c9be6c759b","11111111-1111-1111-1111-111111111111"]}}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var file []byte
			if tt.file != "" {
				file = []byte(tt.file)
			}
			patch, cerr := buildUpdatePatch(file, &tt.flags, tt.current)
			if cerr != nil {
				t.Fatalf("buildUpdatePatch: %v", cerr)
			}
			got := patch.wire
			samePatch(t, got, tt.want)
		})
	}
}

func TestValidateUpdateFlags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		flags updateFlags
		code  int // 0 = valid
	}{
		{"valid", updateFlags{Image: "i", ImageSet: true, Replicas: 0, ReplicasSet: true, Env: []string{"A=1"}}, 0},
		{"empty image", updateFlags{ImageSet: true}, clierr.CodeUsage},
		{"cpu 0", updateFlags{CPUSet: true}, clierr.CodeInvalidInput},
		{"memory 0", updateFlags{MemorySet: true}, clierr.CodeInvalidInput},
		{"replicas 501", updateFlags{Replicas: 501, ReplicasSet: true}, clierr.CodeInvalidInput},
		{"bad priority", updateFlags{Priority: "urgent", PrioritySet: true}, clierr.CodeInvalidInput},
		{"port out of range", updateFlags{Port: 70000, PortSet: true}, clierr.CodeInvalidInput},
		{"env without =", updateFlags{Env: []string{"NOEQUALS"}}, clierr.CodeUsage},
		{"env with an empty key", updateFlags{Env: []string{"=v"}}, clierr.CodeUsage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cerr := validateUpdateFlags(&tt.flags)
			switch {
			case tt.code == 0 && cerr != nil:
				t.Errorf("unexpected error: %v", cerr)
			case tt.code != 0 && (cerr == nil || cerr.Code != tt.code):
				t.Errorf("error = %v, want exit code %d", cerr, tt.code)
			}
		})
	}
}

// TestUpdateWithNothingToDoIsUsage: no -f and no flag is a usage error,
// before any client or API work.
func TestUpdateWithNothingToDoIsUsage(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	_, err := app.runRoot(t, "container-groups", "update", "my-app")
	var cerr *clierr.Error
	ok := errors.As(err, &cerr)
	if !ok || cerr.Code != clierr.CodeUsage {
		t.Fatalf("update with nothing to do = %v, want a usage error", err)
	}
}

// TestUpdateEnvRemovalIsNotReportedAsASideEffect: a file that removes a
// variable under -e is sent as a full map without it, and the post-write
// check must still see the removal as requested.
func TestUpdateEnvRemovalIsNotReportedAsASideEffect(t *testing.T) {
	t.Parallel()
	file := []byte(`{"container":{"environment_variables":{"A":null}}}`)
	flags := updateFlags{Env: []string{"C=3"}}
	patch, cerr := buildUpdatePatch(file, &flags, map[string]*string{"A": ptr.To("1")})
	if cerr != nil {
		t.Fatalf("buildUpdatePatch: %v", cerr)
	}
	wire, intent := patch.wire, patch.intent
	samePatch(t, wire, `{"container":{"environment_variables":{"C":"3"}}}`)
	samePatch(t, intent, `{"container":{"environment_variables":{"C":"3","A":null}}}`)

	var before, after shared.ContainerGroup
	beforeDoc := `{"name":"g","pending_change":false,"container":{"image":"i","environment_variables":{"A":"1"}}}`
	if err := json.Unmarshal([]byte(beforeDoc), &before); err != nil {
		t.Fatal(err)
	}
	afterDoc := `{"name":"g","pending_change":false,"container":{"image":"i","environment_variables":{"C":"3"}}}`
	if err := json.Unmarshal([]byte(afterDoc), &after); err != nil {
		t.Fatal(err)
	}
	if got := findUnrequestedChanges(intent, &before, &after); len(got) != 0 {
		t.Errorf("checked against the intent, the removal was reported as unrequested: %+v", got)
	}
	if got := findIgnoredFields(intent, &before, &after); len(got) != 0 {
		t.Errorf("checked against the intent, a field was reported as ignored: %+v", got)
	}
	// The wire patch alone is what produced the false warning live.
	if got := findUnrequestedChanges(wire, &before, &after); len(got) == 0 {
		t.Error("expected the wire patch alone to flag the removal; the test no longer covers the bug")
	}
	// Without a removal there is nothing to add: intent is the wire patch.
	patch, cerr = buildUpdatePatch(nil, &updateFlags{Env: []string{"B=2"}}, map[string]*string{"A": ptr.To("1")})
	if cerr != nil {
		t.Fatal(cerr)
	}
	w2, i2 := patch.wire, patch.intent
	if string(w2) != string(i2) {
		t.Errorf("intent %s differs from wire %s with nothing removed", i2, w2)
	}
}
