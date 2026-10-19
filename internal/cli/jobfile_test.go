package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/inferenceendpoints"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/queues"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// TestLoadJobFileShapeHint: a job file holding the input document alone
// fails on its first field (`unknown field "prompt"`), so the error also
// says what a job file looks like, without changing its exit code or kind.
func TestLoadJobFileShapeHint(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tests := []struct {
		name     string
		body     string
		wantErr  bool
		wantHint bool
	}{
		{"bare input document", `{"prompt": "a lighthouse"}`, true, true},
		{"bare input as yaml", "prompt: a lighthouse\nsteps: 25\n", true, true},
		{"input plus a typo", `{"input": {"prompt": "x"}, "webhok": "https://example.com"}`, true, false},
		{"valid job", `{"input": {"prompt": "x"}, "metadata": {"k": "v"}}`, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var proto queues.QueueJobPrototype
			cerr := app.loadJobFile(write(strings.ReplaceAll(tt.name, " ", "-")+".yaml", tt.body), &proto)
			if (cerr != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", cerr, tt.wantErr)
			}
			if cerr == nil {
				return
			}
			if cerr.Code != clierr.CodeInvalidInput || cerr.Kind != clierr.KindUnknownField {
				t.Errorf(
					"code/kind = %d/%s, want %d/%s",
					cerr.Code,
					cerr.Kind,
					clierr.CodeInvalidInput,
					clierr.KindUnknownField,
				)
			}
			if !strings.HasPrefix(cerr.Message, "cannot use ") || !strings.Contains(cerr.Message, "unknown field") {
				t.Errorf("message start changed: %q", cerr.Message)
			}
			if got := strings.Contains(cerr.HintText(), `put your fields under "input"`); got != tt.wantHint {
				t.Errorf("hint = %v, want %v: %q", got, tt.wantHint, errText(cerr))
			}
		})
	}

	// A duplicated key is a parse error, as with every other file the CLI
	// reads strictly; YAML-to-JSON alone would keep the last silently.
	var dup queues.QueueJobPrototype
	if cerr := app.loadJobFile(write("dup.json", `{"input": {"a": 1}, "input": {"b": 2}}`), &dup); cerr == nil ||
		cerr.Code != clierr.CodeInvalidInput || !strings.Contains(cerr.Message, "cannot parse") {
		t.Errorf("duplicate input key: %v", cerr)
	}

	// inference jobs create loads its prototype the same way.
	var inf inferenceendpoints.InferenceEndpointJobPrototype
	if cerr := app.loadJobFile(
		write("inf-ok.json", `{"input": {"url": "https://example.com/a.wav"}}`),
		&inf,
	); cerr != nil ||
		inf.Input == nil {
		t.Errorf("valid inference job: err %v, input %v", cerr, inf.Input)
	}
	cerr := app.loadJobFile(write("inf-bare.json", `{"url": "https://example.com/a.wav"}`), &inf)
	if cerr == nil || cerr.Kind != clierr.KindUnknownField ||
		!strings.Contains(cerr.HintText(), `put your fields under "input"`) {
		t.Errorf("bare inference input: %v", cerr)
	}
}
