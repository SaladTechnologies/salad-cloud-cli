package cli

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// runComplete runs cobra's hidden __complete command, as the shell scripts
// do, and returns its stdout and stderr: cobra's writer and the process's
// own stderr, where the CLI's notes and warnings go.
func runComplete(t *testing.T, args ...string) (stdout, stderr string) {
	t.Helper()
	registerCompletions(rootCmd)
	var out, errBuf bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errBuf)
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		// cobra adds __complete to the tree for the run that calls it and
		// leaves it there; a real process exits after one run.
		if c, _, err := rootCmd.Find([]string{"__complete"}); err == nil && c != rootCmd {
			rootCmd.RemoveCommand(c)
		}
		resetRun()
	})
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = w
	osErr := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		osErr <- string(b)
	}()
	rootCmd.SetArgs(append([]string{"__complete"}, args...))
	execErr := rootCmd.Execute()
	w.Close()
	os.Stderr = oldStderr
	written := <-osErr
	if execErr != nil {
		t.Fatalf("__complete %v: %v", args, execErr)
	}
	return out.String(), errBuf.String() + written
}

// cliStderr drops cobra's own "Completion ended with directive" line, which
// the shell scripts discard; anything left came from the CLI.
func cliStderr(stderr string) string {
	var kept []string
	for _, l := range strings.Split(strings.TrimSpace(stderr), "\n") {
		if l != "" && !strings.HasPrefix(l, "Completion ended with directive") {
			kept = append(kept, l)
		}
	}
	return strings.Join(kept, "\n")
}

func TestCompleteInstanceIDsSilently(t *testing.T) {
	stubAPI(t, map[string]string{
		"/containers/my-app/instances": `{"instances":[{"id":"8a2f13ba-01c2-4a29-9c4d-52c7e5a44e37"},{"id":"1111aaaa-01c2-4a29-9c4d-52c7e5a44e37"}]}`,
	})
	out, stderr := runComplete(t, "container-groups", "instances", "get", "-c", "my-app", "")
	if !strings.Contains(out, "8a2f13ba-01c2-4a29-9c4d-52c7e5a44e37") || !strings.Contains(out, "1111aaaa") {
		t.Errorf("candidates = %q, want both instance ids", out)
	}
	if s := cliStderr(stderr); s != "" {
		t.Errorf("completion wrote to stderr: %q", s)
	}
}

// With no API key, completion offers nothing and still says nothing.
func TestCompleteWithoutKeyIsEmptyAndSilent(t *testing.T) {
	t.Setenv("SALAD_CLI_CONFIG_DIR", t.TempDir())
	t.Setenv("SALAD_API_KEY", "")
	out, stderr := runComplete(t, "container-groups", "get", "")
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 1 || !strings.HasPrefix(lines[0], ":") {
		t.Errorf("candidates = %q, want none (only the directive line)", out)
	}
	if s := cliStderr(stderr); s != "" {
		t.Errorf("completion wrote to stderr: %q", s)
	}
}

func TestCompleteEnumFlags(t *testing.T) {
	out, _ := runComplete(t, "container-groups", "create", "x", "--priority", "")
	for _, want := range []string{"high", "medium", "low", "batch"} {
		if !strings.Contains(out, want) {
			t.Errorf("--priority candidates = %q, want %s", out, want)
		}
	}
	out, _ = runComplete(t, "gpus", "list", "-o", "y")
	if !strings.Contains(out, "yaml") || strings.Contains(out, "json") {
		t.Errorf("-o y candidates = %q, want yaml only", out)
	}
}

// The project fallback and other notes stay off stderr during completion,
// where the shell would print them under the prompt.
func TestCompleteWithProjectFallbackIsSilent(t *testing.T) {
	stubAPI(t, map[string]string{"/containers": `{"items":[{"name":"my-app"}]}`})
	t.Setenv("SALAD_PROJECT", "")
	out, stderr := runComplete(t, "container-groups", "get", "")
	if !strings.Contains(out, "my-app") {
		t.Errorf("candidates = %q, want my-app", out)
	}
	if s := cliStderr(stderr); s != "" {
		t.Errorf("completion wrote to stderr: %q", s)
	}
}
