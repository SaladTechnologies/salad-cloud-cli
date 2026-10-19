package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// runExecute runs Run with args through the real command tree, capturing
// stdout and stderr. It resets the run's state afterward because cobra keeps
// flag values between executions in one process.
func runExecute(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	t.Cleanup(func() { resetRun(t) })
	d := app.Deps
	d.Args, d.Stdout, d.Stderr = args, &out, &errBuf
	code = Run(t.Context(), d)
	return code, out.String(), errBuf.String()
}

// TestErrorDocumentIsNotHTMLEscaped: a placeholder in a message reads as
// typed, not as the HTML-safe escape json.Marshal would write.
func TestErrorDocumentIsNotHTMLEscaped(t *testing.T) {
	_, _, stderr := runExecute(t, "container-groups", "scale", "sr-ex2", "-o", "json")
	escaped := `\` + "u003c"
	if !strings.Contains(stderr, "<n>") || strings.Contains(stderr, escaped) {
		t.Errorf("error document = %s, want <n> unescaped", stderr)
	}
}

// TestCommandLineErrorsHonourJSONMode: cobra validates positional arguments
// before PersistentPreRunE sets the output mode, so Execute must still report
// an argument error as the one-line JSON error document under -o json.
func TestCommandLineErrorsHonourJSONMode(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"missing positional argument", []string{"container-groups", "get", "-o", "json"}},
		{"unknown flag after -o", []string{"container-groups", "get", "-o", "json", "--bogus"}},
		{"unknown flag before -o", []string{"container-groups", "get", "--bogus", "-o", "json"}},
		{"unknown command", []string{"-o", "json", "frobnicate"}},
		{"did-you-mean", []string{"create", "-o", "json"}},
		{"unknown command with --output=json", []string{"transcribe", "x.wav", "--output=json"}},
		{"-ojson", []string{"cg", "list", "--bogus", "-ojson"}},
		{"bare completion", []string{"completion", "-o", "json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runExecute(t, tc.args...)
			if code != clierr.CodeUsage {
				t.Errorf("exit = %d, want %d", code, clierr.CodeUsage)
			}
			if stdout != "" {
				t.Errorf("stdout must be empty on error, got %q", stdout)
			}
			lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
			if len(lines) != 1 {
				t.Fatalf("stderr = %q, want exactly one line", stderr)
			}
			var doc struct {
				Error map[string]json.RawMessage `json:"error"`
			}
			if err := json.Unmarshal([]byte(lines[0]), &doc); err != nil {
				t.Fatalf("stderr is not a JSON error document: %v\n%s", err, stderr)
			}
			for _, k := range []string{"exit_code", "kind", "message", "http_status"} {
				if _, ok := doc.Error[k]; !ok {
					t.Errorf("error key %q missing: %s", k, stderr)
				}
			}
			if got := string(doc.Error["exit_code"]); got != "2" {
				t.Errorf("exit_code = %s, want 2", got)
			}
			if got := string(doc.Error["kind"]); got != `"`+string(clierr.KindUsage)+`"` {
				t.Errorf("kind = %s, want %q", got, clierr.KindUsage)
			}
			if got := string(doc.Error["http_status"]); got != "null" {
				t.Errorf("http_status = %s, want null", got)
			}
		})
	}
}

// TestCommandLineErrorsInTableMode: without a usable -o json, command-line
// errors stay in table form — including an invalid -o, which cannot itself
// select json.
func TestCommandLineErrorsInTableMode(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"no -o", []string{"container-groups", "get"}},
		{"invalid -o", []string{"container-groups", "get", "-o", "xml"}},
		{"unknown command, invalid -o", []string{"frobnicate", "-o", "xml"}},
		{"-o json after --", []string{"frobnicate", "--", "-o", "json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runExecute(t, tc.args...)
			if code != clierr.CodeUsage {
				t.Errorf("exit = %d, want %d", code, clierr.CodeUsage)
			}
			if stdout != "" {
				t.Errorf("stdout must be empty on error, got %q", stdout)
			}
			// One error line, and at most one hint line under it.
			first, rest, _ := strings.Cut(strings.TrimRight(stderr, "\n"), "\n")
			if !strings.HasPrefix(first, "saladcloud: error: ") ||
				rest != "" && (!strings.HasPrefix(rest, "hint: ") || strings.Contains(rest, "\n")) {
				t.Errorf("stderr = %q, want one table-mode error line and at most one hint", stderr)
			}
		})
	}
}

// Errors stay the one-line JSON document under -o yaml, so a script parses
// one error shape in every machine mode.
func TestErrorsAreJSONUnderYAML(t *testing.T) {
	code, stdout, stderr := runExecute(t, "container-groups", "get", "-o", "yaml")
	if code != clierr.CodeUsage || stdout != "" {
		t.Fatalf("exit %d stdout %q", code, stdout)
	}
	var doc map[string]map[string]any
	err := json.Unmarshal(
		[]byte(strings.TrimSpace(stderr)),
		&doc,
	)
	if err != nil ||
		doc["error"]["kind"] != string(clierr.KindUsage) {
		t.Errorf("stderr = %q, want the JSON error document", stderr)
	}
}

// --version is 'version': the same document under -o json, wherever -o sits.
func TestVersionFlag(t *testing.T) {
	for _, args := range [][]string{{"--version", "-o", "json"}, {"-o", "json", "--version"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, stdout, stderr := runExecute(t, args...)
			var doc map[string]any
			if code != clierr.CodeOK || json.Unmarshal([]byte(stdout), &doc) != nil {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
			}
			if _, ok := doc["version"]; !ok {
				t.Errorf("document lacks version: %s", stdout)
			}
		})
	}
	t.Run("table", func(t *testing.T) {
		code, stdout, _ := runExecute(t, "--version")
		if code != clierr.CodeOK || !strings.HasPrefix(stdout, "saladcloud") {
			t.Errorf("exit %d, stdout %q", code, stdout)
		}
	})
}
