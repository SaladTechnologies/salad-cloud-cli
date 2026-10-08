package cli

import (
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// validCreateRequest builds a request that passes validateCreateRequest;
// each test case knocks one required field out.
func validCreateRequest() containergroups.ContainerGroupCreationRequest {
	policy := shared.CONTAINER_RESTART_POLICY_ALWAYS
	return containergroups.ContainerGroupCreationRequest{
		Name: ptr.To("demo"),
		Container: &containergroups.ContainerConfiguration{
			Image: ptr.To("docker.io/library/busybox:1.36"),
			Resources: &containergroups.CreateContainerResourceRequirements{
				Cpu:    ptr.To(int64(1)),
				Memory: ptr.To(int64(1024)),
			},
		},
		Replicas:        ptr.To(int64(1)),
		RestartPolicy:   &policy,
		AutostartPolicy: ptr.To(true),
	}
}

func TestValidateCreateRequestValid(t *testing.T) {
	req := validCreateRequest()
	if err := validateCreateRequest(&req); err != nil {
		t.Fatalf("validateCreateRequest returned %v for a valid request", err)
	}
}

func TestValidateCreateRequestMissingFields(t *testing.T) {
	tests := []struct {
		field  string // the field named in the error message
		mutate func(*containergroups.ContainerGroupCreationRequest)
	}{
		{"name", func(r *containergroups.ContainerGroupCreationRequest) { r.Name = nil }},
		{"name", func(r *containergroups.ContainerGroupCreationRequest) { r.Name = ptr.To("") }},
		{"container", func(r *containergroups.ContainerGroupCreationRequest) { r.Container = nil }},
		{"container.image", func(r *containergroups.ContainerGroupCreationRequest) { r.Container.Image = nil }},
		{"container.image", func(r *containergroups.ContainerGroupCreationRequest) { r.Container.Image = ptr.To("") }},
		{"container.resources", func(r *containergroups.ContainerGroupCreationRequest) { r.Container.Resources = nil }},
		{"container.resources.cpu", func(r *containergroups.ContainerGroupCreationRequest) { r.Container.Resources.Cpu = nil }},
		{"container.resources.memory", func(r *containergroups.ContainerGroupCreationRequest) { r.Container.Resources.Memory = nil }},
		{"replicas", func(r *containergroups.ContainerGroupCreationRequest) { r.Replicas = nil }},
		{"restart_policy", func(r *containergroups.ContainerGroupCreationRequest) { r.RestartPolicy = nil }},
		{"autostart_policy", func(r *containergroups.ContainerGroupCreationRequest) { r.AutostartPolicy = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			req := validCreateRequest()
			tt.mutate(&req)
			err := validateCreateRequest(&req)
			if err == nil {
				t.Fatalf("validateCreateRequest returned nil with %s missing", tt.field)
			}
			if err.Code != clierr.CodeInvalidInput {
				t.Errorf("code = %d, want %d", err.Code, clierr.CodeInvalidInput)
			}
			if !strings.Contains(err.Message, tt.field) {
				t.Errorf("message %q does not name field %q", err.Message, tt.field)
			}
		})
	}
}

// scalingAction builds one fixture action.
func scalingAction(replicas int64, schedule string) shared.ContainerGroupScalingAction {
	return shared.ContainerGroupScalingAction{Replicas: ptr.To(replicas), Schedule: ptr.To(schedule)}
}

func TestScheduledScalingString(t *testing.T) {
	weekdayPair := []shared.ContainerGroupScalingAction{
		scalingAction(2, "0 9 * * 1-5"),
		scalingAction(0, "0 17 * * 1-5"),
	}
	tests := []struct {
		name    string
		enabled *bool
		actions []shared.ContainerGroupScalingAction
		want    string
	}{
		{
			"enabled with actions", ptr.To(true), weekdayPair,
			`enabled (2 actions: 2 replicas @ "0 9 * * 1-5"; 0 replicas @ "0 17 * * 1-5")`,
		},
		{
			"enabled with one action", ptr.To(true),
			[]shared.ContainerGroupScalingAction{scalingAction(5, "0 0 * * *")},
			`enabled (1 action: 5 replicas @ "0 0 * * *")`,
		},
		{
			"enabled with four actions caps at three", ptr.To(true),
			[]shared.ContainerGroupScalingAction{
				scalingAction(1, "0 1 * * *"), scalingAction(2, "0 2 * * *"),
				scalingAction(3, "0 3 * * *"), scalingAction(4, "0 4 * * *"),
			},
			`enabled (4 actions: 1 replicas @ "0 1 * * *"; 2 replicas @ "0 2 * * *"; 3 replicas @ "0 3 * * *"; and 1 more)`,
		},
		{"enabled without actions", ptr.To(true), nil, "enabled (no actions)"},
		{"disabled with actions", ptr.To(false), weekdayPair, "disabled (2 actions configured)"},
		{
			"nil flag with actions counts as disabled", nil,
			[]shared.ContainerGroupScalingAction{scalingAction(2, "0 9 * * 1-5")},
			"disabled (1 action configured)",
		},
		{"neither means no row", nil, nil, ""},
		{"disabled without actions means no row", ptr.To(false), nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &shared.ContainerGroup{
				ScheduledScalingEnabled: tt.enabled,
				ScalingActions:          tt.actions,
			}
			if got := scheduledScalingString(g); got != tt.want {
				t.Errorf("scheduledScalingString = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestWriteGroupDetailScheduledScalingRow pins the row's presence contract:
// shown when the group has the flag or actions, absent otherwise.
func TestWriteGroupDetailScheduledScalingRow(t *testing.T) {
	with := &shared.ContainerGroup{
		Name:                    ptr.To("demo"),
		ScheduledScalingEnabled: ptr.To(true),
		ScalingActions:          []shared.ContainerGroupScalingAction{scalingAction(2, "0 9 * * 1-5")},
	}
	without := &shared.ContainerGroup{Name: ptr.To("demo")}

	var buf strings.Builder
	writeGroupDetail(&buf, with)
	if !strings.Contains(buf.String(), "Scheduled scaling\tenabled (1 action: 2 replicas @ \"0 9 * * 1-5\")") {
		t.Errorf("detail lacks the Scheduled scaling row:\n%s", buf.String())
	}

	buf.Reset()
	writeGroupDetail(&buf, without)
	if strings.Contains(buf.String(), "Scheduled scaling") {
		t.Errorf("detail has a Scheduled scaling row for a group without any:\n%s", buf.String())
	}
}

// detailGroup builds a minimal group for writeGroupDetail tests.
func detailGroup(status shared.ContainerGroupStatus, autostart bool) *shared.ContainerGroup {
	return &shared.ContainerGroup{
		Name:            ptr.To("demo"),
		AutostartPolicy: ptr.To(autostart),
		CurrentState:    &shared.ContainerGroupState{Status: &status},
	}
}

func TestWriteGroupDetailPendingAutostartNote(t *testing.T) {
	note := `settles to "stopped" shortly (created without autostart)`

	tests := []struct {
		name     string
		group    *shared.ContainerGroup
		wantNote bool
	}{
		{"pending without autostart gets the note", detailGroup(shared.CONTAINER_GROUP_STATUS_PENDING, false), true},
		{"pending with autostart gets no note", detailGroup(shared.CONTAINER_GROUP_STATUS_PENDING, true), false},
		{"stopped without autostart gets no note", detailGroup(shared.CONTAINER_GROUP_STATUS_STOPPED, false), false},
		{"nil autostart policy gets no note", &shared.ContainerGroup{
			Name: ptr.To("demo"),
			CurrentState: func() *shared.ContainerGroupState {
				s := shared.CONTAINER_GROUP_STATUS_PENDING
				return &shared.ContainerGroupState{Status: &s}
			}(),
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			writeGroupDetail(&buf, tt.group)
			got := strings.Contains(buf.String(), note)
			if got != tt.wantNote {
				t.Errorf("note present = %t, want %t\noutput:\n%s", got, tt.wantNote, buf.String())
			}
		})
	}
}

// TestScheduledScalingResetStillWarns pins that replacing the hand-coded
// scheduled-scaling warning with the generic unrequested-change diff kept
// every case it used to handle: an unrelated merge patch can reset
// scheduled-scaling-enabled to false while leaving the actions configured,
// so a schedule silently stops firing.
func TestScheduledScalingResetStillWarns(t *testing.T) {
	group := func(enabled bool) *shared.ContainerGroup {
		return &shared.ContainerGroup{
			Name:                    ptr.To("demo"),
			ScheduledScalingEnabled: ptr.To(enabled),
		}
	}
	tests := []struct {
		name     string
		before   *shared.ContainerGroup
		after    *shared.ContainerGroup
		patch    string
		wantWarn bool
	}{
		{"unrelated patch flips the flag off", group(true), group(false), `{"replicas": 3}`, true},
		{"patch that names the field is deliberate", group(true), group(false), `{"scheduled-scaling-enabled": false}`, false},
		{"flag stayed on", group(true), group(true), `{"replicas": 3}`, false},
		{"was already off", group(false), group(false), `{"replicas": 3}`, false},
		{"pre-state unknown", nil, group(false), `{"replicas": 3}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			warnUnrequestedChanges(&buf, findUnrequestedChanges([]byte(tt.patch), tt.before, tt.after))
			got := strings.Contains(buf.String(), "scheduled-scaling-enabled")
			if got != tt.wantWarn {
				t.Errorf("warned = %t, want %t; output: %s", got, tt.wantWarn, buf.String())
			}
			// The remediation that made the old hand-coded warning useful
			// must survive the generalization.
			if tt.wantWarn && !strings.Contains(buf.String(), "to re-enable it") {
				t.Errorf("warning lost its remediation: %s", buf.String())
			}
		})
	}
}
