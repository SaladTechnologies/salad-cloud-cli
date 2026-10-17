package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// TestValidateCommand: the words after -- reach the node unchanged, where
// the first runs as the program in place of the image's ENTRYPOINT and CMD.
func TestValidateCommand(t *testing.T) {
	tests := []struct {
		name     string
		words    []string
		given    bool
		wantCode int // 0: accepted
		wantMsg  string
	}{
		{"no --", nil, false, 0, ""},
		{"a plain command", []string{"sleep", "infinity"}, true, 0, ""},
		{
			"a shell script with nested quotes",
			[]string{"bash", "-c", `echo "hi there"; exec python app.py --port "$PORT"`},
			true,
			0,
			"",
		},
		{"padded words pass (the node trims them)", []string{"bash", "-c", " echo hi\n"}, true, 0, ""},
		{"nothing after --", []string{}, true, clierr.CodeUsage, "nothing after --"},
		{"an empty word", []string{"python", "app.py", "--name", ""}, true, clierr.CodeInvalidInput, "word 4 is empty"},
		{"docker-style args", []string{"--port", "8080"}, true, clierr.CodeInvalidInput, "start it with a program"},
		{"too many words", make101(), true, clierr.CodeInvalidInput, "the API allows at most 100"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cerr := validateCommand(tt.words, tt.given)
			if tt.wantCode == 0 {
				if cerr != nil {
					t.Fatalf("rejected: %v", cerr)
				}
				return
			}
			if cerr == nil || cerr.Code != tt.wantCode || !strings.Contains(errText(cerr), tt.wantMsg) {
				t.Fatalf("got %v, want exit %d containing %q", cerr, tt.wantCode, tt.wantMsg)
			}
		})
	}
}

// TestValidateCommandGitBash: rewritten words are caught only under Git Bash,
// and never inside a script word, whose text is literal.
func TestValidateCommandGitBash(t *testing.T) {
	rewritten := []string{"C:/Program-Files/Git/usr/bin/bash", "-c", "x"}
	t.Setenv("MSYSTEM", "")
	if cerr := validateCommand(rewritten, true); cerr != nil {
		t.Errorf("outside Git Bash: %v", cerr)
	}
	t.Setenv("MSYSTEM", "MINGW64")
	for _, words := range [][]string{rewritten, {"configure", "--prefix=C:/Program-Files/Git/usr"}} {
		if cerr := validateCommand(
			words,
			true,
		); cerr == nil || cerr.Code != clierr.CodeUsage ||
			!strings.Contains(cerr.Message, "rewritten by Git Bash") {
			t.Errorf("%q under Git Bash: %v", words, cerr)
		}
	}
	if cerr := validateCommand([]string{"bash", "-c", "P=a:/opt; ls $P"}, true); cerr != nil {
		t.Errorf("a script word was checked: %v", cerr)
	}
}

// TestWarnCommand: a spaced first word and saladcloud's own flags after --
// draw a warning; ordinary commands do not.
func TestWarnCommand(t *testing.T) {
	prepareTree()
	create, _, err := rootCmd.Find([]string{"container-groups", "create"})
	if err != nil {
		t.Fatal(err)
	}
	warn := func(words ...string) string {
		var b strings.Builder
		warnCommand(&b, create, words)
		return b.String()
	}
	if got := warn("sleep", "2147483647"); got != "" {
		t.Errorf("plain command warned: %q", got)
	}
	if got := warn("python", "app.py", "--port", "8080", "-e", "x", "-f", "cfg", "--gpu", "0", "-v"); got != "" {
		t.Errorf("program flags warned: %q", got)
	}
	if got := warn("sleep 2147483647"); !strings.Contains(got, "contains a space") {
		t.Errorf("spaced first word: %q", got)
	}
	got := warn("sleep", "1", "-o", "json", "--wait", "--replicas=3")
	for _, want := range []string{`"-o"`, `"--wait"`, `"--replicas=3"`, "put saladcloud's own flags before --"} {
		if !strings.Contains(got, want) {
			t.Errorf("own flags: %q missing %q", got, want)
		}
	}
}

func make101() []string {
	w := make([]string, 101)
	for i := range w {
		w[i] = "x"
	}
	return w
}

// TestCreateSendsCommand: the words after -- become container.command,
// exactly as given, and a positional after them is not taken for the name.
func TestCreateSendsCommand(t *testing.T) {
	posted := stubCreate(t)
	script := `echo "hi there"; sleep 5`
	_, err := runRoot(t, "container-groups", "create", "new", "--image", "busybox:1.36", "--no-autostart",
		"-o", "json", "--", "sh", "-c", script)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, ok := postedPath(t, *posted, "container.command")
	if !ok || !jsonEqual(got, []any{"sh", "-c", script}) {
		t.Errorf("container.command = %v (present %v); body %s", got, ok, *posted)
	}
	if name, _ := postedPath(t, *posted, "name"); name != "new" {
		t.Errorf("name = %v, want new", name)
	}
}

// TestCreateCommandArgs: one positional before --, any number after.
func TestCreateCommandArgs(t *testing.T) {
	_, err := runRoot(t, "container-groups", "create", "a", "b", "--image", "x", "--", "sleep", "1")
	var cerr *clierr.Error
	ok := errors.As(err, &cerr)
	if !ok || cerr.Code != clierr.CodeUsage || !strings.Contains(cerr.Message, `unexpected argument "b"`) {
		t.Errorf("two positionals: %v", err)
	}
	resetFlags()
	_, err = runRoot(t, "container-groups", "update", "a", "--")
	if !errors.As(err, &cerr) || !strings.Contains(cerr.Message, "nothing after --") {
		t.Errorf("update with nothing after --: %v", err)
	}
}

// TestUpdatePatchCommand: update -- <words> puts container.command in the
// patch, over a file's.
func TestUpdatePatchCommand(t *testing.T) {
	wire, _, cerr := buildUpdatePatch([]byte(`{"container":{"command":["old"],"image":"i"}}`),
		updateFlags{Command: []string{"python", "app.py"}}, nil)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if want := `{"container":{"command":["python","app.py"],"image":"i"}}`; string(wire) != want {
		t.Errorf("patch = %s, want %s", wire, want)
	}
	if !(updateFlags{Command: []string{"x"}}).any() {
		t.Error("a command alone should count as something to update")
	}
}
