package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"

	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// grp builds a group from a JSON literal, so the fixtures read like the wire
// documents the diff actually sees (including the kebab-case fields).
func grp(t *testing.T, doc string) *shared.ContainerGroup {
	t.Helper()
	var g shared.ContainerGroup
	if err := json.Unmarshal([]byte(doc), &g); err != nil {
		t.Fatalf("fixture %s: %v", doc, err)
	}
	return &g
}

func TestFindIgnoredFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		patch  string
		before string
		after  string
		want   []string // expected paths, in order
	}{
		{
			// The real case: CPU-only group, priority validated then dropped.
			// Note the request/response asymmetry — the patch says
			// container.priority, the response says priority — so this also
			// covers the requestToResponsePath alias.
			name:   "dropped nested write is reported",
			patch:  `{"container":{"priority":"low"}}`,
			before: `{"name":"g","priority":"batch"}`,
			after:  `{"name":"g","priority":"batch"}`,
			want:   []string{"container.priority"},
		},
		{
			// The false positive the alias exists to prevent: on a GPU group
			// the priority patch DOES apply, but it lands at top-level
			// `priority` while the patch names `container.priority`. Without
			// the alias both lookups miss and a successful write is reported
			// as dropped.
			name:   "applied priority on a GPU group is silent",
			patch:  `{"container":{"priority":"low"}}`,
			before: `{"name":"g","priority":"batch"}`,
			after:  `{"name":"g","priority":"low"}`,
			want:   nil,
		},
		{
			// A field the response does not expose at all is unobservable;
			// guessing would mean warning on every write to it.
			name:   "field absent from the response is skipped",
			patch:  `{"container":{"registry_authentication":{"basic":{"username":"u","password":"p"}}}}`,
			before: `{"name":"g","container":{"image":"x"}}`,
			after:  `{"name":"g","container":{"image":"x"}}`,
			want:   nil,
		},
		{
			name:   "applied write is silent",
			patch:  `{"replicas":3}`,
			before: `{"name":"g","replicas":1}`,
			after:  `{"name":"g","replicas":3}`,
			want:   nil,
		},
		{
			// Asking for a value the field already holds is a no-op, not a
			// dropped write — this is what keeps re-sent env maps quiet.
			name:   "requesting the current value is not a warning",
			patch:  `{"replicas":3}`,
			before: `{"name":"g","replicas":3}`,
			after:  `{"name":"g","replicas":3}`,
			want:   nil,
		},
		{
			// Server normalization: display_name null yields the group name.
			// A null request answered with a fresh non-null value is a
			// default being filled in, not a dropped write; stays quiet.
			name:   "normalized value is not a warning",
			patch:  `{"display_name":null}`,
			before: `{"name":"g","display_name":"Renamed"}`,
			after:  `{"name":"g","display_name":"g"}`,
			want:   nil,
		},
		{
			// The hold rule, from the requester's side: asking to keep the
			// flag true while setting replicas ends with it false. The field
			// MOVED (true -> false), which the old "unchanged only" condition
			// skipped entirely, with no report.
			name:   "field moved to something other than requested is reported",
			patch:  `{"replicas":2,"scheduled-scaling-enabled":true}`,
			before: `{"name":"g","replicas":1,"scheduled-scaling-enabled":true}`,
			after:  `{"name":"g","replicas":2,"scheduled-scaling-enabled":false}`,
			want:   []string{"scheduled-scaling-enabled"},
		},
		{
			name:   "non-null request answered with a third value is reported",
			patch:  `{"display_name":"A"}`,
			before: `{"name":"g","display_name":"B"}`,
			after:  `{"name":"g","display_name":"C"}`,
			want:   []string{"display_name"},
		},
		{
			name:   "clearing a field that stays put is reported",
			patch:  `{"country_codes":null}`,
			before: `{"name":"g","country_codes":["us"]}`,
			after:  `{"name":"g","country_codes":["us"]}`,
			want:   []string{"country_codes"},
		},
		{
			// image_caching: verified write-ignored on CPU and GPU alike.
			name:   "image_caching drop is reported",
			patch:  `{"container":{"image_caching":false}}`,
			before: `{"name":"g","container":{"image":"x","image_caching":true}}`,
			after:  `{"name":"g","container":{"image":"x","image_caching":true}}`,
			want:   []string{"container.image_caching"},
		},
		{
			// A mixed patch must isolate the dropped leaf, not blame the
			// whole container object because a sibling did change.
			name:   "mixed patch reports only the dropped leaf",
			patch:  `{"container":{"image":"y","image_caching":false}}`,
			before: `{"name":"g","container":{"image":"x","image_caching":true}}`,
			after:  `{"name":"g","container":{"image":"y","image_caching":true}}`,
			want:   []string{"container.image_caching"},
		},
		{
			name:   "several drops are all reported, sorted",
			patch:  `{"container":{"priority":"low","image_caching":false}}`,
			before: `{"name":"g","priority":"batch","container":{"image":"x","image_caching":true}}`,
			after:  `{"name":"g","priority":"batch","container":{"image":"x","image_caching":true}}`,
			want:   []string{"container.image_caching", "container.priority"},
		},
		{
			// Arrays are leaves in merge-patch (they replace wholesale).
			name:   "array replacement that applied is silent",
			patch:  `{"country_codes":["us","gb"]}`,
			before: `{"name":"g","country_codes":["us"]}`,
			after:  `{"name":"g","country_codes":["us","gb"]}`,
			want:   nil,
		},
		{
			name:   "empty scaling-actions that applied is silent",
			patch:  `{"scaling-actions":[]}`,
			before: `{"name":"g","scaling-actions":[{"replicas":2,"schedule":"0 9 * * 1-5"}]}`,
			after:  `{"name":"g","scaling-actions":[]}`,
			want:   nil,
		},
		{
			name:   "deep path is reported with its full dotted location",
			patch:  `{"container":{"resources":{"shm_size":256}}}`,
			before: `{"name":"g","container":{"resources":{"cpu":1,"shm_size":64}}}`,
			after:  `{"name":"g","container":{"resources":{"cpu":1,"shm_size":64}}}`,
			want:   []string{"container.resources.shm_size"},
		},
		{
			name:   "empty object in a patch reports nothing",
			patch:  `{"container":{}}`,
			before: `{"name":"g","container":{"image":"x"}}`,
			after:  `{"name":"g","container":{"image":"x"}}`,
			want:   nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := findIgnoredFields([]byte(tt.patch), grp(t, tt.before), grp(t, tt.after))
			paths := make([]string, 0, len(got))
			for _, f := range got {
				paths = append(paths, f.Path)
			}
			if strings.Join(paths, ",") != strings.Join(tt.want, ",") {
				t.Errorf("paths = %v, want %v", paths, tt.want)
			}
		})
	}
}

// TestFindIgnoredFieldsSkipsPending is the guard that matters most: a
// container change settles asynchronously and the response lags a version,
// so diffing mid-flight would report a queued write as dropped.
func TestFindIgnoredFieldsSkipsPending(t *testing.T) {
	t.Parallel()
	before := grp(t, `{"name":"g","container":{"image":"x"}}`)
	after := grp(t, `{"name":"g","container":{"image":"x"}}`)
	after.PendingChange = ptr.To(true)
	if got := findIgnoredFields([]byte(`{"container":{"image":"y"}}`), before, after); got != nil {
		t.Errorf("findIgnoredFields while pending = %v, want nil", got)
	}
	// Same inputs, settled: now it is a genuine dropped write.
	after.PendingChange = ptr.To(false)
	if got := findIgnoredFields([]byte(`{"container":{"image":"y"}}`), before, after); len(got) != 1 {
		t.Errorf("findIgnoredFields settled = %v, want 1 finding", got)
	}
}

func TestFindIgnoredFieldsMissingStateOrBadPatch(t *testing.T) {
	t.Parallel()
	g := grp(t, `{"name":"g","replicas":1}`)
	// A failed pre-state GET must not produce warnings.
	if got := findIgnoredFields([]byte(`{"replicas":3}`), nil, g); got != nil {
		t.Errorf("nil before = %v, want nil", got)
	}
	if got := findIgnoredFields([]byte(`{"replicas":3}`), g, nil); got != nil {
		t.Errorf("nil after = %v, want nil", got)
	}
	if got := findIgnoredFields([]byte(`not json`), g, g); got != nil {
		t.Errorf("unparseable patch = %v, want nil", got)
	}
}

func TestWarnIgnoredFieldsOutput(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	warnIgnoredFields(&buf, nil)
	if buf.Len() != 0 {
		t.Errorf("no findings should print nothing, got %q", buf.String())
	}

	buf.Reset()
	warnIgnoredFields(&buf, []ignoredField{
		{Path: "container.priority", Requested: `"low"`, Actual: `"batch"`},
	})
	out := buf.String()
	for _, want := range []string{"did not apply 1 field", "container.priority", `requested "low"`, `still "batch"`} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}

	buf.Reset()
	warnIgnoredFields(&buf, []ignoredField{
		{Path: "a", Requested: "1", Actual: "2"},
		{Path: "b", Requested: "3", Actual: "4"},
	})
	if !strings.Contains(buf.String(), "did not apply 2 fields") {
		t.Errorf("plural form missing: %q", buf.String())
	}
}

// TestFindUnrequestedChanges is the inverse direction: side effects the
// patch never asked for. The exclusions matter more than the detections —
// a group document churns on its own, so a naive diff would cry wolf on
// every update.
func TestFindUnrequestedChanges(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		patch  string
		before string
		after  string
		want   []string
	}{
		{
			// The hold rule: setting replicas puts scheduled scaling on hold.
			// Still detected (it is a change the patch did not name); its
			// severity is a note — see TestHoldRuleIsANote.
			name:   "scheduled-scaling flag put on hold by a replicas write",
			patch:  `{"replicas":3}`,
			before: `{"name":"g","replicas":1,"scheduled-scaling-enabled":true}`,
			after:  `{"name":"g","replicas":3,"scheduled-scaling-enabled":false}`,
			want:   []string{"scheduled-scaling-enabled"},
		},
		{
			// env is replace-not-merge server-side: naming B drops A.
			name:   "env key lost to replace-not-merge",
			patch:  `{"container":{"environment_variables":{"B":"2"}}}`,
			before: `{"name":"g","container":{"environment_variables":{"A":"1","B":"1"}}}`,
			after:  `{"name":"g","container":{"environment_variables":{"B":"2"}}}`,
			want:   []string{"container.environment_variables.A"},
		},
		{
			name:   "a patch that only does what it says is silent",
			patch:  `{"replicas":3}`,
			before: `{"name":"g","replicas":1}`,
			after:  `{"name":"g","replicas":3}`,
			want:   nil,
		},
		{
			// Server-managed churn must never be reported: none of these
			// are settable by any request schema.
			name:  "server-managed churn is excluded",
			patch: `{"replicas":3}`,
			before: `{"name":"g","replicas":1,"version":1,"update_time":"t1","pending_change":false,
			          "current_state":{"status":"stopped"},"container":{"image":"x","hash":"h1","size":1}}`,
			after: `{"name":"g","replicas":3,"version":2,"update_time":"t2","pending_change":false,
			          "current_state":{"status":"running"},"container":{"image":"x","hash":"h2","size":2}}`,
			want: nil,
		},
		{
			// The assigned DNS name appears on first networking write.
			name:   "assigned dns is excluded",
			patch:  `{"networking":{"port":9100}}`,
			before: `{"name":"g","networking":{"port":9000}}`,
			after:  `{"name":"g","networking":{"port":9100,"dns":"x.salad.cloud"}}`,
			want:   nil,
		},
		{
			// Normalization of a field the patch DID name is the forward
			// direction's business, not this one.
			name:   "named field is never reported here",
			patch:  `{"display_name":null}`,
			before: `{"name":"g","display_name":"Renamed"}`,
			after:  `{"name":"g","display_name":"g"}`,
			want:   nil,
		},
		{
			name:   "several side effects are sorted",
			patch:  `{"replicas":3}`,
			before: `{"name":"g","replicas":1,"scheduled-scaling-enabled":true,"country_codes":["us"]}`,
			after:  `{"name":"g","replicas":3,"scheduled-scaling-enabled":false,"country_codes":["us","ca"]}`,
			want:   []string{"country_codes", "scheduled-scaling-enabled"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := findUnrequestedChanges([]byte(tt.patch), grp(t, tt.before), grp(t, tt.after))
			paths := make([]string, 0, len(got))
			for _, c := range got {
				paths = append(paths, c.Path)
			}
			if strings.Join(paths, ",") != strings.Join(tt.want, ",") {
				t.Errorf("paths = %v, want %v", paths, tt.want)
			}
		})
	}
}

// TestFindUnrequestedChangesGuards: same two guards as the forward direction.
func TestFindUnrequestedChangesGuards(t *testing.T) {
	t.Parallel()
	before := grp(t, `{"name":"g","replicas":1,"scheduled-scaling-enabled":true}`)
	after := grp(t, `{"name":"g","replicas":3,"scheduled-scaling-enabled":false}`)
	after.PendingChange = ptr.To(true)
	if got := findUnrequestedChanges([]byte(`{"replicas":3}`), before, after); got != nil {
		t.Errorf("pending = %v, want nil", got)
	}
	after.PendingChange = ptr.To(false)
	if got := findUnrequestedChanges([]byte(`{"replicas":3}`), nil, after); got != nil {
		t.Errorf("nil before = %v, want nil", got)
	}
	if got := findUnrequestedChanges([]byte(`{"replicas":3}`), before, after); len(got) != 1 {
		t.Errorf("settled = %v, want 1 finding", got)
	}
}

func TestWarnUnrequestedChangesOutput(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	warnUnrequestedChanges(&buf, nil)
	if buf.Len() != 0 {
		t.Errorf("no changes should print nothing, got %q", buf.String())
	}

	// Remediation is printed once per cause, not once per affected key.
	buf.Reset()
	warnUnrequestedChanges(&buf, []unrequestedChange{
		{Path: "container.environment_variables.A", Before: `"1"`, After: "null"},
		{Path: "container.environment_variables.C", Before: `"3"`, After: "null"},
	})
	out := buf.String()
	if n := strings.Count(out, "replaces the whole environment_variables map"); n != 1 {
		t.Errorf("remediation printed %d times, want 1: %s", n, out)
	}
	for _, want := range []string{"also changed 2 fields", "container.environment_variables.A"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
	// Keys first, then the hint: a hint wedged between two keys read as two
	// separate warnings for one cause.
	if strings.Index(out, "environment_variables.C") > strings.Index(out, "replaces the whole") {
		t.Errorf("hint printed before the last affected key:\n%s", out)
	}
}

// TestHoldRuleIsANote: a replicas write putting scheduled scaling on hold is
// a documented platform rule (thermostat model), so it is classified Note
// and printed as "note:", while an unrelated side effect in the same diff
// stays a warning.
func TestHoldRuleIsANote(t *testing.T) {
	t.Parallel()
	changes := findUnrequestedChanges(
		[]byte(`{"replicas":3}`),
		grp(t, `{"name":"g","replicas":1,"scheduled-scaling-enabled":true,"country_codes":["us"]}`),
		grp(t, `{"name":"g","replicas":3,"scheduled-scaling-enabled":false,"country_codes":["us","ca"]}`),
	)
	if len(changes) != 2 {
		t.Fatalf("changes = %v, want 2", changes)
	}
	byPath := make(map[string]unrequestedChange)
	for _, c := range changes {
		byPath[c.Path] = c
	}
	if !byPath["scheduled-scaling-enabled"].Note {
		t.Error("hold rule not classified as a note")
	}
	if byPath["country_codes"].Note {
		t.Error("unrelated change classified as a note")
	}
	var buf bytes.Buffer
	warnUnrequestedChanges(&buf, changes)
	out := buf.String()
	for _, want := range []string{
		// country_codes, as a warning
		"warning: this update also changed 1 field", "warning:   country_codes",
		// the hold rule, as a note with the re-enable hint
		"note: scheduled-scaling-enabled: true -> false", "on hold", "to re-enable it",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"warning:   scheduled-scaling-enabled", "known API bug"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("hold rule still reported as a warning / bug (%q):\n%s", unwanted, out)
		}
	}
}

// TestFlagFlipWithoutReplicasIsNotTheHoldRule: the same flip as
// TestHoldRuleIsANote, but the patch did not touch replicas, so it is not
// the rule and stays a warning.
func TestFlagFlipWithoutReplicasIsNotTheHoldRule(t *testing.T) {
	t.Parallel()
	other := findUnrequestedChanges(
		[]byte(`{"display_name":"x"}`),
		grp(t, `{"name":"g","display_name":"y","scheduled-scaling-enabled":true}`),
		grp(t, `{"name":"g","display_name":"x","scheduled-scaling-enabled":false}`),
	)
	if len(other) != 1 || other[0].Note {
		t.Errorf("flip without a replicas write = %v, want one non-note change", other)
	}
}

// TestWarnIgnoredFieldsMovedWording: a field that moved to the wrong value
// reads "got", one that stayed put reads "still".
func TestWarnIgnoredFieldsMovedWording(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	warnIgnoredFields(&buf, []ignoredField{
		{Path: "scheduled-scaling-enabled", Requested: "true", Actual: "false", Moved: true},
		{Path: "container.image_caching", Requested: "false", Actual: "true"},
	})
	out := buf.String()
	if !strings.Contains(out, "scheduled-scaling-enabled: requested true, got false") {
		t.Errorf("moved field wording wrong:\n%s", out)
	}
	if !strings.Contains(out, "container.image_caching: requested false, still true") {
		t.Errorf("unchanged field wording wrong:\n%s", out)
	}
}

// TestFindUnrequestedChangesCoversAncestors is a live-caught false positive:
// setting a whole object names only its leaf paths, so when the field was
// previously absent the before/after trees diverge at the object itself.
// Without ancestors of named leaves counting as requested, deliberately
// adding a probe was reported as an unrequested side effect.
func TestFindUnrequestedChangesCoversAncestors(t *testing.T) {
	t.Parallel()
	patch := `{"liveness_probe":{"tcp":{"port":8080},"initial_delay_seconds":5}}`
	before := grp(t, `{"name":"g"}`)
	after := grp(t, `{"name":"g","liveness_probe":{"tcp":{"port":8080},"initial_delay_seconds":5}}`)
	if got := findUnrequestedChanges([]byte(patch), before, after); got != nil {
		t.Errorf("adding a probe reported as unrequested: %v", got)
	}
	// The sibling-key case must still work: naming one env key and losing
	// another is a genuine side effect, even though the map itself is an
	// ancestor of a named path.
	patch = `{"container":{"environment_variables":{"B":"2"}}}`
	before = grp(t, `{"name":"g","container":{"environment_variables":{"A":"1","B":"1"}}}`)
	after = grp(t, `{"name":"g","container":{"environment_variables":{"B":"2"}}}`)
	got := findUnrequestedChanges([]byte(patch), before, after)
	if len(got) != 1 || got[0].Path != "container.environment_variables.A" {
		t.Errorf("env key loss = %v, want container.environment_variables.A", got)
	}
}

// TestFindUnrequestedChangesProbeSwitchIsSameShapeAsEnvLoss pins the
// tradeoff documented at the top of the inverse section: replacing a probe
// handler and losing an env key are structurally identical (a sibling key
// vanishes under a parent the patch named), so either both are reported or
// neither is. Both are reported, because the env case is the valuable one.
func TestFindUnrequestedChangesProbeSwitchIsSameShapeAsEnvLoss(t *testing.T) {
	t.Parallel()
	probe := findUnrequestedChanges(
		[]byte(`{"startup_probe":{"grpc":{"port":8080},"period_seconds":3}}`),
		grp(t, `{"name":"g","startup_probe":{"http":{"path":"/h","port":8080},"period_seconds":3}}`),
		grp(t, `{"name":"g","startup_probe":{"grpc":{"port":8080},"period_seconds":3}}`),
	)
	if len(probe) != 1 || probe[0].Path != "startup_probe.http" {
		t.Errorf("probe switch = %v, want startup_probe.http reported", probe)
	}
	env := findUnrequestedChanges(
		[]byte(`{"container":{"environment_variables":{"B":"2"}}}`),
		grp(t, `{"name":"g","container":{"environment_variables":{"A":"1","B":"1"}}}`),
		grp(t, `{"name":"g","container":{"environment_variables":{"B":"2"}}}`),
	)
	if len(env) != 1 || env[0].Path != "container.environment_variables.A" {
		t.Errorf("env loss = %v, want container.environment_variables.A reported", env)
	}
}

// TestDriftWarningsNeverPrintSecrets: environment-variable values, header
// values and credential fields never reach a warning; the key is named and
// the change is described instead. The sentinel must not appear.
func TestDriftWarningsNeverPrintSecrets(t *testing.T) {
	t.Parallel()
	const sentinel = "sentinel-s3cret-value"
	before := grp(
		t,
		`{"name":"g","replicas":1,"container":{"image":"x","environment_variables":{`+
			`"KEEP":"`+sentinel+`","DROP":"`+sentinel+`","SWAP":"`+sentinel+`"}}}`,
	)
	after := grp(
		t,
		`{"name":"g","replicas":1,"container":{"image":"x","environment_variables":{`+
			`"KEEP":"`+sentinel+`","SWAP":"other-`+sentinel+`","NEW":"`+sentinel+`"}}}`,
	)

	var out bytes.Buffer
	warnUnrequestedChanges(&out, findUnrequestedChanges([]byte(`{"replicas":1}`), before, after))
	warnIgnoredFields(
		&out,
		findIgnoredFields(
			[]byte(`{"container":{"environment_variables":{"KEEP":"requested-`+sentinel+`"}}}`),
			before,
			after,
		),
	)
	got := out.String()
	if strings.Contains(got, sentinel) {
		t.Fatalf("a warning printed a secret value:\n%s", got)
	}
	for _, want := range []string{
		"container.environment_variables.DROP: (removed)",
		"container.environment_variables.SWAP: (changed)",
		"container.environment_variables.NEW: (set)",
		"requested (hidden)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("warnings lack %q:\n%s", want, got)
		}
	}

	// Credentials nested in an object or array are redacted the way
	// --debug=body redacts them; other values stay visible.
	nested := map[string]any{
		"datadog": map[string]any{"host": "h", "api_key": sentinel},
		"http": map[string]any{"headers": []any{
			map[string]any{"name": "Authorization", "value": "Bearer " + sentinel},
		}},
	}
	shown := shownValue("container.logging", nested)
	if strings.Contains(shown, sentinel) || !strings.Contains(shown, `"host":"h"`) ||
		!strings.Contains(shown, `"Authorization"`) {
		t.Errorf("shownValue = %s, want credentials redacted and the rest kept", shown)
	}
}

// create reports a top-level object the API dropped whole (queue_autoscaler
// on create), once, and nothing for what the response never shows or
// for empty lists it echoes.
func TestFindUnappliedCreateFieldsDroppedObject(t *testing.T) {
	t.Parallel()
	created := grp(t, `{"name":"g","container":{"image":"x","command":[]},"country_codes":[],
		"readiness_probe":{"http":{"path":"/","port":8000,"scheme":"http","headers":[]},"period_seconds":5}}`)
	req := `{"name":"g","container":{"image":"x","registry_authentication":{"basic":{"username":"u","password":"p"}}},
		"country_codes":[],"liveness_probe":null,
		"readiness_probe":{"http":{"path":"/","port":8000,"scheme":"http","headers":[]},"period_seconds":5},
		"queue_autoscaler":{"min_replicas":0,"max_replicas":3,"desired_queue_length":1}}`
	got := findUnappliedCreateFields([]byte(req), created)
	if len(got) != 1 || got[0].Path != "queue_autoscaler" || got[0].Actual != "null" ||
		!strings.Contains(got[0].Requested, `"max_replicas":3`) {
		t.Fatalf("got %+v, want only queue_autoscaler dropped", got)
	}

	// A dropped object's credentials are not printed.
	got = findUnappliedCreateFields([]byte(`{"liveness_probe":{"http":{"path":"/","port":1,"scheme":"http",
		"headers":[{"name":"Authorization","value":"Bearer s3cret"}]}}}`), grp(t, `{"name":"g"}`))
	if len(got) != 1 || strings.Contains(got[0].Requested, "s3cret") {
		t.Errorf("got %+v, want liveness_probe reported with its header value hidden", got)
	}
}
