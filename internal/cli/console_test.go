package cli

import (
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// TestAtTerminal: SALAD_CLI_NO_PROMPT, or CI set to anything but empty,
// false or 0, makes a terminal count as none.
func TestAtTerminal(t *testing.T) {
	app := newTestRun(t)
	old := app.Console
	t.Cleanup(func() { app.Console = old })
	for _, tc := range []struct {
		terminal     bool
		noPrompt, ci string
		want         bool
	}{
		{true, "", "", true},
		{false, "", "", false},
		{true, "1", "", false},
		{true, "anything", "", false},
		{true, "", "true", false},
		{true, "", "1", false},
		{true, "", "yes", false},
		{true, "", "false", true},
		{true, "", "FALSE", true},
		{true, "", "0", true},
		{false, "", "false", false},
	} {
		app.setenv("SALAD_CLI_NO_PROMPT", tc.noPrompt)
		app.setenv("CI", tc.ci)
		app.Console = &fakeConsole{terminal: tc.terminal}
		if got := app.atTerminal(); got != tc.want {
			t.Errorf(
				"terminal=%t SALAD_CLI_NO_PROMPT=%q CI=%q: atTerminal() = %t, want %t",
				tc.terminal,
				tc.noPrompt,
				tc.ci,
				got,
				tc.want,
			)
		}
	}
}

// TestErrorHint: a hint prints as a hint: line under the error in table
// mode and as the hint key in the error document, which is ASCII.
func TestErrorHint(t *testing.T) {
	app := newTestRun(t)
	var buf strings.Builder
	old := app.Stderr
	app.Stderr = &buf
	t.Cleanup(func() { app.Stderr = old; app.setOutputMode(t, "table") })
	e := clierr.New(clierr.CodeUsage, clierr.KindUsage, "invalid organization name %q", "Café").
		WithHint("did you mean %q?", "cafe")
	app.printError(e)
	if got, want := buf.String(), "saladcloud: error: invalid organization name \"Café\"\nhint: did you mean "+
		"\"cafe\"?\n"; got != want {
		t.Errorf("table mode:\n%q\nwant\n%q", got, want)
	}
	buf.Reset()
	app.setOutputMode(t, "json")
	app.printError(e)
	want := `{"error":{"exit_code":2,"kind":"usage","message":"invalid organization name \"Caf\u00e9\"","hint":"did ` +
		`you mean \"cafe\"?","http_status":null}}` + "\n"
	if got := buf.String(); got != want {
		t.Errorf("json mode:\n%s\nwant\n%s", got, want)
	}
}
