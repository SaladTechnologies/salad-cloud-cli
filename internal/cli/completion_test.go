package cli

import (
	"bytes"
	"strings"
	"testing"
)

// runComplete runs cobra's hidden __complete command, as the shell scripts
// do, and returns its stdout and stderr: cobra's writer and the process's
// own stderr, where the CLI's notes and warnings go.
func (app *testRun) runComplete(t *testing.T, args ...string) (stdout, stderr string) {
	t.Helper()
	var out, errBuf, cliErr bytes.Buffer
	a, root := app.fresh()
	a.Stderr = &cliErr
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(append([]string{"__complete"}, args...))
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("__complete %v: %v", args, err)
	}
	return out.String(), errBuf.String() + cliErr.String()
}

// cliStderr drops cobra's own "Completion ended with directive" line, which
// the shell scripts discard; anything left came from the CLI.
func cliStderr(stderr string) string {
	var kept []string
	for l := range strings.SplitSeq(strings.TrimSpace(stderr), "\n") {
		if l != "" && !strings.HasPrefix(l, "Completion ended with directive") {
			kept = append(kept, l)
		}
	}
	return strings.Join(kept, "\n")
}

func TestCompleteInstanceIDsSilently(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	app.stubAPI(t, map[string]string{
		"/containers/my-app/instances": `{"instances":[{"id":"8a2f13ba-01c2-4a29-9c4d-52c7e5a44e37"},{"id":"1111aaaa-01c2-4a29-9c4d-52c7e5a44e37"}]}`,
	})
	out, stderr := app.runComplete(t, "container-groups", "instances", "get", "-c", "my-app", "")
	if !strings.Contains(out, "8a2f13ba-01c2-4a29-9c4d-52c7e5a44e37") || !strings.Contains(out, "1111aaaa") {
		t.Errorf("candidates = %q, want both instance ids", out)
	}
	if s := cliStderr(stderr); s != "" {
		t.Errorf("completion wrote to stderr: %q", s)
	}
}

// With no API key, completion offers nothing and still says nothing.
func TestCompleteWithoutKeyIsEmptyAndSilent(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	app.setenv("SALAD_CLI_CONFIG_DIR", t.TempDir())
	app.setenv("SALAD_API_KEY", "")
	out, stderr := app.runComplete(t, "container-groups", "get", "")
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 1 || !strings.HasPrefix(lines[0], ":") {
		t.Errorf("candidates = %q, want none (only the directive line)", out)
	}
	if s := cliStderr(stderr); s != "" {
		t.Errorf("completion wrote to stderr: %q", s)
	}
}

func TestCompleteEnumFlags(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	out, _ := app.runComplete(t, "container-groups", "create", "x", "--priority", "")
	for _, want := range []string{"high", "medium", "low", "batch"} {
		if !strings.Contains(out, want) {
			t.Errorf("--priority candidates = %q, want %s", out, want)
		}
	}
	out, _ = app.runComplete(t, "gpus", "list", "-o", "y")
	if !strings.Contains(out, "yaml") || strings.Contains(out, "json") {
		t.Errorf("-o y candidates = %q, want yaml only", out)
	}
}

// The project fallback and other notes stay off stderr during completion,
// where the shell would print them under the prompt.
func TestCompleteWithProjectFallbackIsSilent(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	app.stubAPI(t, map[string]string{"/containers": `{"items":[{"name":"my-app"}]}`})
	app.setenv("SALAD_PROJECT", "")
	out, stderr := app.runComplete(t, "container-groups", "get", "")
	if !strings.Contains(out, "my-app") {
		t.Errorf("candidates = %q, want my-app", out)
	}
	if s := cliStderr(stderr); s != "" {
		t.Errorf("completion wrote to stderr: %q", s)
	}
}
