package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// TestValidateScaleReplicas pins the bounds and, importantly, that 0 is
// accepted: the API's minimum is 0 (drain to zero instances without
// deleting the group), so a naive "0 means unset" check would break it.
func TestValidateScaleReplicas(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		n       int64
		wantErr bool
	}{
		{"zero is legal", 0, false},
		{"one", 1, false},
		{"max", 500, false},
		{"negative", -1, true},
		{"over max", 501, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cerr := validateScaleReplicas(tt.n)
			if (cerr != nil) != tt.wantErr {
				t.Fatalf("validateScaleReplicas(%d) = %v, want an error: %v", tt.n, cerr, tt.wantErr)
			}
			if cerr == nil {
				return
			}
			if cerr.Code != clierr.CodeInvalidInput {
				t.Errorf("code = %d, want %d", cerr.Code, clierr.CodeInvalidInput)
			}
			// The message must name the flag and the range, not just fail.
			if !strings.Contains(cerr.Message, "--replicas") || !strings.Contains(cerr.Message, "0 and 500") {
				t.Errorf("message %q does not name --replicas and the accepted range", cerr.Message)
			}
		})
	}
}

// TestScaleResultJSONShape pins scale's -o json document. The "action" key
// is the point: start/stop/delete emit {name, action, ok} and scale used to
// omit action entirely, so a client switching on it silently found nothing.
// See TestActionResultsShareActionAndOK for the cross-command contract.
func TestScaleResultJSONShape(t *testing.T) {
	t.Parallel()
	b, err := json.Marshal(scaleResult{Name: "web", Action: "scaled", Replicas: 3, OK: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := map[string]any{
		"name":     "web",
		"action":   "scaled",
		"replicas": float64(3),
		"ok":       true,
	}
	if len(got) != len(want) {
		t.Errorf("got %d keys %v, want %d %v", len(got), got, len(want), want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
}

// TestScaleResultZeroReplicasStillSerializes guards against an omitempty
// creeping onto Replicas: `scale x --replicas 0` is a legitimate call and
// the response must still say so rather than dropping the field.
func TestScaleResultZeroReplicasStillSerializes(t *testing.T) {
	t.Parallel()
	b, err := json.Marshal(scaleResult{Name: "web", Action: "scaled", Replicas: 0, OK: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"replicas":0`) {
		t.Errorf("scale result %s dropped replicas=0", b)
	}
}

// TestScalePatchIsTheUpdatePatch pins that scale sends exactly the merge
// patch `update -f` would, so both ride applyPatch and get the same
// verification — including the hold-rule note on scheduled scaling.
func TestScalePatchIsTheUpdatePatch(t *testing.T) {
	t.Parallel()
	for _, n := range []int64{0, 1, 500} {
		var doc map[string]any
		if err := json.Unmarshal(scalePatch(n), &doc); err != nil {
			t.Fatalf("scalePatch(%d) is not JSON: %v", n, err)
		}
		if len(doc) != 1 || doc["replicas"] != float64(n) {
			t.Errorf("scalePatch(%d) = %s, want exactly {\"replicas\":%d}", n, scalePatch(n), n)
		}
	}
}

// TestShouldReenableSchedule: --keep-schedule acts only when the schedule was
// on before the write and the write put it on hold.
func TestShouldReenableSchedule(t *testing.T) {
	t.Parallel()
	on := &shared.ContainerGroup{ScheduledScalingEnabled: ptr.To(true)}
	off := &shared.ContainerGroup{ScheduledScalingEnabled: ptr.To(false)}
	if !shouldReenableSchedule(true, on, off) {
		t.Error("on -> off with --keep-schedule must re-enable")
	}
	if shouldReenableSchedule(false, on, off) {
		t.Error("without the flag nothing is re-enabled")
	}
	if shouldReenableSchedule(true, off, off) {
		t.Error("a schedule that was already off is not 'kept'")
	}
	if shouldReenableSchedule(true, on, on) {
		t.Error("nothing to do when the flag survived")
	}
	if shouldReenableSchedule(true, nil, off) || shouldReenableSchedule(true, on, nil) {
		t.Error("missing state must not trigger a write")
	}
	if string(reenableSchedulePatch()) != `{"scheduled-scaling-enabled":true}` {
		t.Errorf("re-enable patch = %s", reenableSchedulePatch())
	}
}
