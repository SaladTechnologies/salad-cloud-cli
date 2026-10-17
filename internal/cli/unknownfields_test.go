package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/input"
)

// The wording contract for strict rejections: no escape hatch for a field
// with a did-you-mean, no repetition in the single-field message, a get
// response gets only its diagnosis, and schema facts are stated as facts
// about this build.

func TestRejectUnknownsNoHatchForTypos(t *testing.T) {
	cerr := rejectUnknowns([]input.Unknown{
		{
			Desc: `unknown field "replica" at the top level (did you mean "replicas"?)`,
			Path: "replica",
			Hint: "replicas",
		},
	})
	if cerr == nil {
		t.Fatal("nil")
	}
	if strings.Contains(errText(cerr), "--allow-unknown-fields") {
		t.Errorf("escape hatch offered for a typo: %s", errText(cerr))
	}
	if strings.Count(errText(cerr), "replica") != 2 { // once in the field name, once in the hint
		t.Errorf("single-field message should name the field once plus the hint: %s", errText(cerr))
	}
	if strings.Contains(errText(cerr), "unrecognized (") {
		t.Errorf("single-field message repeats itself: %s", errText(cerr))
	}
}

func TestRejectUnknownsHatchNamesOnlyNovelFields(t *testing.T) {
	cerr := rejectUnknowns([]input.Unknown{
		{
			Desc: `unknown field "replica" at the top level (did you mean "replicas"?)`,
			Path: "replica",
			Hint: "replicas",
		},
		{Desc: `unknown field "gpu_sharing" in "container"`, Path: "container.gpu_sharing"},
	})
	if cerr == nil {
		t.Fatal("nil")
	}
	if !strings.Contains(errText(cerr), "--allow-unknown-fields") {
		t.Errorf("hatch missing for the novel field: %s", errText(cerr))
	}
	if !strings.Contains(errText(cerr), "container.gpu_sharing") || strings.Contains(errText(cerr), "(replica)") {
		t.Errorf("hatch clause should list only the novel path: %s", errText(cerr))
	}
}

func TestRejectUnknownsSingleNovelDoesNotRepeat(t *testing.T) {
	cerr := rejectUnknowns([]input.Unknown{
		{Desc: `unknown field "gpu_sharing" in "container"`, Path: "container.gpu_sharing"},
	})
	if cerr == nil {
		t.Fatal("nil")
	}
	if strings.Contains(errText(cerr), "unrecognized (") {
		t.Errorf("single novel field message repeats the path: %s", errText(cerr))
	}
	if !strings.Contains(errText(cerr), "re-run with --allow-unknown-fields") {
		t.Errorf("hatch missing: %s", errText(cerr))
	}
}

func TestRejectUnknownsGetResponseTellLeads(t *testing.T) {
	paths := []string{
		"id", "create_time", "update_time", "version", "pending_change", "current_state", "organization_name",
		"project_name", "container.hash", "container.size", "name", "autostart_policy", "restart_policy", "priority",
	}
	us := make([]input.Unknown, 0, len(paths))
	for _, p := range paths {
		us = append(us, input.Unknown{Desc: `unknown field "` + p + `"`, Path: p})
	}
	// The diagnosis and the command to start from are the whole message.
	cerr := rejectUnknowns(us)
	if cerr == nil {
		t.Fatal("nil")
	}
	want := "this document is a get response, not a patch (10 of its fields only responses carry); " +
		"to get a document update -f accepts, run saladcloud container-groups get <group> --writable; --debug lists " +
		"the fields"
	if errText(cerr) != want || cerr.Kind != clierr.KindUnknownField || cerr.Code != clierr.CodeInvalidInput {
		t.Errorf("(%d, %s) %s\nwant (6, unknown_field) %s", cerr.Code, cerr.Kind, errText(cerr), want)
	}

	// --debug adds the per-field list on stderr.
	api.SetDebug(api.DebugRequests)
	t.Cleanup(func() { api.SetDebug(api.DebugOff) })
	stderr := captureStderr(t, func() { rejectUnknowns(us) })
	for _, want := range []string{"debug: 14 unknown fields", "not in this CLI's update schema", "response-only in this CLI's API model", `"priority" belongs at "container.priority"`} {
		if !strings.Contains(stderr, want) {
			t.Errorf("--debug output lacks %q: %s", want, stderr)
		}
	}
	if strings.Contains(stderr, "--allow-unknown-fields") {
		t.Errorf("hatch offered for a get document with nothing novel: %s", stderr)
	}
}

// errText is an error's message and hint, as one string to search.
func errText(e *clierr.Error) string {
	if e == nil {
		return ""
	}
	return e.Message + "; " + e.HintText()
}

// captureStderr runs fn with os.Stderr sent to a file and returns what it wrote.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = f
	fn()
	os.Stderr = old
	f.Close() // before TempDir's cleanup: Windows cannot remove an open file
	b, _ := os.ReadFile(f.Name())
	return string(b)
}

func TestRejectUnknownsHedgesSchemaFacts(t *testing.T) {
	for _, tt := range []struct{ path, want, mustNot string }{
		{"restart_policy", "not in this CLI's update schema (create accepts it)", "cannot be changed by update"},
		{"readme", "response-only in this CLI's API model", "is a response-only field and"},
	} {
		cerr := rejectUnknowns(
			[]input.Unknown{{Desc: `unknown field "` + tt.path + `" at the top level`, Path: tt.path}},
		)
		if cerr == nil || !strings.Contains(errText(cerr), tt.want) {
			t.Errorf("%s: message %v lacks %q", tt.path, cerr, tt.want)
		}
		if cerr != nil && strings.Contains(errText(cerr), tt.mustNot) {
			t.Errorf("%s: message still asserts an API fact: %s", tt.path, errText(cerr))
		}
	}
}

func TestWarnUnknownsLenientTypo(t *testing.T) {
	var buf bytes.Buffer
	warnUnknowns(&buf, []input.Unknown{
		{
			Desc: `unknown field "replica" at the top level (did you mean "replicas"?)`,
			Path: "replica",
			Hint: "replicas",
		},
	}, false)
	out := buf.String()
	if !strings.Contains(out, `did you mean "replicas"?`) ||
		!strings.Contains(out, "sent to the API anyway (--allow-unknown-fields)") {
		t.Errorf("lenient warning for a typo should carry the hint and the plain sent-anyway text: %s", out)
	}
	if strings.Contains(out, "looks like a typo") || strings.Contains(out, "ignore") {
		t.Errorf("lenient warning should not editorialize beyond the hint: %s", out)
	}
}

// queue_connection is create-only, and update says why and what to do
// instead of offering the escape hatch.
func TestRejectUnknownsExplainsQueueConnection(t *testing.T) {
	cerr := rejectUnknowns([]input.Unknown{
		{Desc: `unknown field "queue_connection" at the top level`, Path: "queue_connection"},
	})
	if cerr == nil {
		t.Fatal("nil")
	}
	for _, want := range []string{"fixed when the group is created", "--from", "--queue"} {
		if !strings.Contains(errText(cerr), want) {
			t.Errorf("message lacks %q: %s", want, errText(cerr))
		}
	}
	if strings.Contains(errText(cerr), "--allow-unknown-fields") {
		t.Errorf("escape hatch offered for a create-only field: %s", errText(cerr))
	}
}

// TestUpdateNamesTheGroupToStartFrom: update knows the group, so a get
// response given as its patch gets the exact command to start from.
func TestUpdateNamesTheGroupToStartFrom(t *testing.T) {
	doc := filepath.Join(t.TempDir(), "g.json")
	if err := os.WriteFile(
		doc,
		[]byte(`{"id":"x","create_time":"t","update_time":"t","version":1,"current_state":{}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	stubAPI(t, make(map[string]string))
	_, err := runRoot(t, "cg", "update", "my-app", "-f", doc)
	var cerr *clierr.Error
	ok := errors.As(err, &cerr)
	if !ok || cerr.Kind != clierr.KindUnknownField ||
		!strings.Contains(cerr.HintText(), "run saladcloud container-groups get my-app --writable") {
		t.Errorf("update -f <get response> = %v, want the --writable command for my-app", err)
	}
}
