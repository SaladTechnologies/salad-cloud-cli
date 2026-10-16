package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

func TestMaskShowsTheLastFourInASCII(t *testing.T) {
	for _, tc := range []struct{ key, want string }{
		{"", "(not set)"},
		{"12345678", "****"},
		{"123456789", "****...6789"},
		{"salad_cloud_user_abcdH13m", "****...H13m"},
		{"ключ-ключ-ключ", "****...????"},
		{"key-with-tab-end\tabc", "****...?abc"},
		{"0000000000abéd", "****...ab?d"},
	} {
		if got := maskAPIKey(tc.key); got != tc.want {
			t.Errorf("maskAPIKey(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

func TestFromTag(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{"flag", " (from --org)"}, {"env", " (from SALAD_ORGANIZATION)"}, {"config", " (from config)"}, {"default", ""}, {"", ""},
	} {
		if got := fromTag(originOf(tc.source, "--org", "SALAD_ORGANIZATION")); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.source, got, tc.want)
		}
	}
	if got := fromTag("stdin"); got != " (from stdin)" {
		t.Errorf("stdin: %q", got)
	}
}

// TestNameCheck covers every branch of the name check, as an error from a
// flag or variable and as a retry line at a prompt.
func TestNameCheck(t *testing.T) {
	for _, tc := range []struct {
		kind, name, origin string
		msg, hint, retry   string
	}{
		{"organization", "my-org", "--org", "", "", ""},
		{"organization", "ab", "--org", "", "", ""},
		{
			"organization", "Acme Prod", "--org",
			`invalid organization name "Acme Prod" (from --org)`,
			"it looks like a display name; use the name from the portal URL, the part after /organizations/",
			`"Acme Prod" is not a valid organization name. It looks like a display name; type the name from the portal URL, the part after /organizations/.`,
		},
		{
			"organization", "My-Org", "--org",
			`invalid organization name "My-Org" (from --org)`, `did you mean "my-org"?`,
			`"My-Org" is not a valid organization name. Did you mean "my-org"?`,
		},
		{
			"organization", "1org", "SALAD_ORGANIZATION",
			`invalid organization name "1org" (from SALAD_ORGANIZATION)`,
			"names are 2-63 characters: lowercase letters, digits and hyphens, starting with a letter and ending with a letter or digit",
			`"1org" is not a valid organization name. Names are 2-63 characters: lowercase letters, digits and hyphens, starting with a letter and ending with a letter or digit.`,
		},
		{
			"organization", "org-", "config",
			`invalid organization name "org-" (from config)`,
			"names are 2-63 characters: lowercase letters, digits and hyphens, starting with a letter and ending with a letter or digit", "",
		},
		{
			"organization", "a", "--org", `invalid organization name "a" (from --org)`,
			"names are 2-63 characters: lowercase letters, digits and hyphens, starting with a letter and ending with a letter or digit", "",
		},
		{
			"organization", "acme_prod", "--org", `invalid organization name "acme_prod" (from --org)`,
			"it looks like a display name; use the name from the portal URL, the part after /organizations/", "",
		},
		{
			"project", "Staging Env", "SALAD_PROJECT",
			`invalid project name "Staging Env" (from SALAD_PROJECT)`,
			"it looks like a display name; use the name from the portal URL, the part after /projects/",
			`"Staging Env" is not a valid project name. It looks like a display name; type the name from the portal URL, the part after /projects/.`,
		},
		{"project", "STAGING", "--project", `invalid project name "STAGING" (from --project)`, `did you mean "staging"?`, ""},
	} {
		t.Run(tc.kind+" "+tc.name, func(t *testing.T) {
			err := checkName(tc.kind, tc.name, tc.origin)
			if tc.msg == "" {
				if err != nil {
					t.Fatalf("valid name rejected: %v", err)
				}
				return
			}
			if err == nil || err.Code != clierr.CodeInvalidInput || err.Kind != clierr.KindInvalidInput ||
				err.Message != tc.msg ||
				err.HintText() != tc.hint {
				t.Errorf("got %+v hint %q\nwant %q / %q", err, err.HintText(), tc.msg, tc.hint)
			}
			if tc.retry != "" {
				if got := nameRetry(tc.kind, tc.name, "some-key"); got != tc.retry {
					t.Errorf("retry line:\n got %q\nwant %q", got, tc.retry)
				}
			}
		})
	}
}

// TestNameRetryNeverQuotesAKey: a prompt answer that is the key just read,
// or too long to be a name, is not quoted back.
func TestNameRetryNeverQuotesAKey(t *testing.T) {
	key := "salad_cloud_user_SecretKeyValue000H13m"
	if got := nameRetry(
		"organization",
		key,
		key,
	); got != "That looks like the API key you just pasted, not an organization name." {
		t.Errorf("key: %q", got)
	}
	if got := nameRetry(
		"project",
		key,
		key,
	); got != "That looks like the API key you just pasted, not a project name." {
		t.Errorf("key, project: %q", got)
	}
	long := strings.Repeat("a", 64)
	if got := nameRetry(
		"organization",
		long,
		key,
	); got != "That is not a valid organization name: names are at most 63 characters." {
		t.Errorf("long: %q", got)
	}
}

func TestNormalizeKey(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
		ok        bool
	}{
		{"  abc123  ", "abc123", true},
		{"abc123\r\n", "abc123", true},
		{"\xef\xbb\xbfabc123", "abc123", true},
		{"\x1b[200~abc123\x1b[201~", "abc123", true},
		{`"abc123"`, `"abc123"`, true},
		{"abc 123", "abc 123", true},
		{"abc\t123", "abc\t123", false},
		{"abcé123", "abcé123", false},
		{"abc\x1b[A", "abc\x1b[A", false},
	} {
		got, ok := normalizeKey(tc.raw)
		if got != tc.want || ok != tc.ok {
			t.Errorf("normalizeKey(%q) = %q, %t; want %q, %t", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
	for _, tc := range []struct {
		key  string
		want bool
	}{{`"abc"`, true}, {`'abc'`, true}, {`"abc'`, false}, {`abc"`, false}, {`"`, false}, {"abc", false}} {
		if got := quotedKey(tc.key); got != tc.want {
			t.Errorf("quotedKey(%q) = %t", tc.key, got)
		}
	}
}

func TestMalformedKey(t *testing.T) {
	err := malformedKey(labelAPIKey, "salad_cloud_user_000000000éDTb9", "SALAD_API_KEY")
	if err.Code != clierr.CodeAuth || err.Kind != clierr.KindAuth ||
		err.Message != "API key ****...DTb9 (from SALAD_API_KEY) contains characters an API key cannot have" ||
		err.HintText() != "check for quotes or stray characters in it" {
		t.Errorf("got %+v hint %q", err, err.HintText())
	}
	if got := malformedKeyRetry(
		labelAPIKey,
	); got != "That API key contains characters an API key cannot have. Check for quotes or stray characters in it." {
		t.Errorf("retry: %q", got)
	}
	if got := malformedKey(
		labelGatewayKey,
		"sk-é-0000000EFGH",
		"stdin",
	).Message; got != "AI Gateway key ****...EFGH (from stdin) contains characters an AI Gateway key cannot have" {
		t.Errorf("gateway: %q", got)
	}
}

func TestOrgsClause(t *testing.T) {
	names := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("org-%02d", i+1)
		}
		return out
	}
	atATerminal(t, true, "linux", "table")
	for _, tc := range []struct {
		names []string
		want  string
	}{
		{[]string{"my-org", "acme-prod"}, "your organizations: my-org, acme-prod"},
		{names(10), "your organizations: org-01, org-02, org-03, org-04, org-05, org-06, org-07, org-08, org-09, org-10"},
		{names(22), "your organizations: org-01, org-02, org-03, org-04, org-05, org-06, org-07, org-08, org-09, org-10 and 12 more; to list all, run saladcloud orgs list"},
		{nil, ""},
		{[]string{}, ""},
	} {
		if got := orgsClause(tc.names); got != tc.want {
			t.Errorf("%d names at a terminal:\n got %q\nwant %q", len(tc.names), got, tc.want)
		}
	}
	for _, mode := range []string{"table", "json"} {
		atATerminal(t, false, "linux", mode)
		for _, n := range [][]string{names(2), names(22), nil} {
			if got := orgsClause(n); got != "to list yours, run saladcloud orgs list" {
				t.Errorf("no terminal, %s mode, %d names: %q", mode, len(n), got)
			}
		}
	}
	if got := hintClauses(
		orgsClause([]string{"my-org"}),
		"fix or remove SALAD_ORGANIZATION",
	); got != "to list yours, run saladcloud orgs list; fix or remove SALAD_ORGANIZATION" {
		t.Errorf("hintClauses: %q", got)
	}
}

func TestConfigReady(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SALAD_CLI_CONFIG_DIR", dir)
	if err := configReady("login"); err != nil {
		t.Fatalf("no file yet: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("the check left %d files behind", len(entries))
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ command, hint string }{
		{"login", "fix or delete it, then run saladcloud login again"},
		{"use", "fix or delete it, then run saladcloud use again"},
		{"ai-gateway login", "fix or delete it, then run saladcloud ai-gateway login again"},
	} {
		err := configReady(tc.command)
		if err == nil || err.Code != clierr.CodeGeneric || err.Kind != clierr.KindAPIError ||
			!strings.HasPrefix(
				err.Message,
				"cannot read the config file "+path+": invalid character",
			) || err.HintText() != tc.hint {
			t.Errorf("corrupt, %s: %+v hint %q", tc.command, err, err.HintText())
		}
	}
	if b, _ := os.ReadFile(path); string(b) != "{not json" {
		t.Errorf("the corrupt file was changed: %q", b)
	}

	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("folder permissions do not stop this user from writing")
	}
	ro := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) }) //nolint:gosec // a directory: TempDir cleanup needs its x bit back
	t.Setenv("SALAD_CLI_CONFIG_DIR", ro)
	for _, tc := range []struct{ command, hint string }{
		{"login", "set SALAD_CLI_CONFIG_DIR to a writable folder, or set SALAD_API_KEY, SALAD_ORGANIZATION and SALAD_PROJECT instead of logging in"},
		{"ai-gateway login", "set SALAD_CLI_CONFIG_DIR to a writable folder, or set SALAD_AI_GATEWAY_API_KEY instead of logging in"},
		{"use", "set SALAD_CLI_CONFIG_DIR to a writable folder, then run saladcloud use again"},
	} {
		err := configReady(tc.command)
		want := "cannot write the config file " + filepath.Join(ro, "config.json") + ": permission denied"
		if err == nil || err.Message != want || err.HintText() != tc.hint {
			t.Errorf("read-only, %s: %+v hint %q", tc.command, err, err.HintText())
		}
	}
}

// TestReadKeys: the raw-mode reader ends a line on Enter, edits with
// Backspace, cancels on Ctrl-C and ends on Ctrl-D or Ctrl-Z at the start of
// a line, echoing only what is visible.
func TestReadKeys(t *testing.T) {
	for _, tc := range []struct {
		name, keys, want, echo string
		err                    error
	}{
		{"enter", "my-org\r", "my-org", "my-org", nil},
		{"newline", "my-org\n", "my-org", "my-org", nil},
		{"backspace", "my-orx\x7fg\r", "my-org", "my-orx\b \bg", nil},
		{"ctrl-h", "ab\x08c\r", "ac", "ab\b \bc", nil},
		{"trimmed", "  my-org  \r", "my-org", "  my-org  ", nil},
		{"unicode", "café\r", "café", "café", nil},
		{"ctrl-c", "my-o\x03", "", "my-o", errInterrupt},
		{"ctrl-d at the start", "\x04", "", "", io.EOF},
		{"ctrl-z at the start", "\x1a", "", "", io.EOF},
		{"ctrl-d later does nothing", "ab\x04c\r", "abc", "abc", nil},
		{"end of input", "", "", "", io.EOF},
		{"end of input after text", "abc", "abc", "abc", nil},
		{"escape kept, not echoed", "ab\x1b[Ac\r", "ab\x1b[Ac", "ab[Ac", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var echo strings.Builder
			got, err := (&keyReader{r: strings.NewReader(tc.keys)}).readLine(&echo)
			if got != tc.want || !errors.Is(err, tc.err) && (err != nil || tc.err != nil) || echo.String() != tc.echo {
				t.Errorf("got %q, %v, echo %q; want %q, %v, echo %q", got, err, echo.String(), tc.want, tc.err, tc.echo)
			}
		})
	}
}

// TestReadKeysCRLF: a line ending in \r\n is one Enter, so the next prompt
// does not read an empty answer; a \n on its own still ends a line.
func TestReadKeysCRLF(t *testing.T) {
	k := &keyReader{r: strings.NewReader("key\r\nmy-org\r\n\nnext\r")}
	var got []string
	for range 4 {
		line, err := k.readLine(io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, line)
	}
	if fmt.Sprintf("%q", got) != `["key" "my-org" "" "next"]` {
		t.Errorf("lines %q", got)
	}
}

func TestBrowserOffer(t *testing.T) {
	for _, tc := range []struct {
		os  string
		env []string
		ok  bool
	}{
		{"windows", nil, true},
		{"darwin", nil, true},
		{"linux", []string{"DISPLAY", ":0"}, true},
		{"linux", []string{"WAYLAND_DISPLAY", "wayland-0"}, true},
		{"linux", nil, false},
		{"linux", []string{"DISPLAY", ":0", "SSH_CONNECTION", "1.2.3.4 22 5.6.7.8 22"}, false},
		{"darwin", []string{"SSH_TTY", "/dev/ttys001"}, false},
		{"windows", []string{"SSH_CONNECTION", "x"}, false},
	} {
		old := goos
		goos = tc.os
		for _, v := range []string{"DISPLAY", "WAYLAND_DISPLAY", "SSH_CONNECTION", "SSH_TTY"} {
			t.Setenv(v, "")
		}
		for i := 0; i+1 < len(tc.env); i += 2 {
			t.Setenv(tc.env[i], tc.env[i+1])
		}
		want := "Paste it here, hidden:"
		if tc.ok {
			want = "Paste it here, hidden (press Enter to open the page in a browser):"
		}
		if got := keyPrompt(); browserVisible() != tc.ok || got != want {
			t.Errorf("%s %v: visible %t, prompt %q", tc.os, tc.env, browserVisible(), got)
		}
		goos = old
	}
}

func TestOpener(t *testing.T) {
	const url = "https://portal.salad.com/api-key"
	t.Setenv("WSL_DISTRO_NAME", "")
	old := goos
	t.Cleanup(func() { goos = old })
	for _, tc := range []struct {
		os, wsl, want string
	}{
		{"windows", "", "rundll32 url.dll,FileProtocolHandler " + url},
		{"darwin", "", "open " + url},
		{"linux", "", "xdg-open " + url},
		{"linux", "Ubuntu", "cmd.exe /c start  " + url},
	} {
		goos = tc.os
		t.Setenv("WSL_DISTRO_NAME", tc.wsl)
		name, args := opener(url)
		if got := name + " " + strings.Join(args, " "); got != tc.want {
			t.Errorf("%s (WSL %q): %q, want %q", tc.os, tc.wsl, got, tc.want)
		}
	}
}

func TestOpenOffered(t *testing.T) {
	const url = "https://portal.salad.com/api-key"
	fc := &fakeConsole{terminal: true}
	oldCon, oldOpen := con, openPage
	t.Cleanup(func() { con, openPage = oldCon, oldOpen })
	con = fc
	openPage = func(string) error { return nil }
	openOffered(url)
	openPage = func(string) error { return errors.New("no opener") }
	openOffered(url)
	want := "Opening https://portal.salad.com/api-key in your browser.\nCould not open a browser; open https://portal.salad.com/api-key yourself.\n"
	if got := fc.device.String(); got != want {
		t.Errorf("device:\n%s\nwant:\n%s", got, want)
	}
}

// TestAskUntil: prompts and retry lines go to the terminal device, answers
// are re-asked until accepted, and Ctrl-C or end of input end the exchange.
func TestAskUntil(t *testing.T) {
	old := con
	t.Cleanup(func() { con = old })
	fc := &fakeConsole{terminal: true, answers: []string{"Acme Prod", "my-org"}}
	con = fc
	got, err := askUntil("Type an organization name:", false, func(a string) (bool, string, *clierr.Error) {
		if !validName(a) {
			return false, nameRetry("organization", a, ""), nil
		}
		return true, "", nil
	})
	if got != "my-org" || err != nil {
		t.Fatalf("got %q, %v", got, err)
	}
	want := "Type an organization name: Acme Prod\n" +
		`"Acme Prod" is not a valid organization name. It looks like a display name; type the name from the portal URL, the part after /organizations/.` + "\n" +
		"Type an organization name: my-org\n"
	if fc.device.String() != want {
		t.Errorf("device:\n%s\nwant:\n%s", fc.device.String(), want)
	}
	for _, end := range []string{keyCtrlC, keyEOF} {
		con = &fakeConsole{terminal: true, answers: []string{end}}
		if _, err := askUntil(
			"Paste it here, hidden:",
			true,
			func(string) (bool, string, *clierr.Error) { return true, "", nil },
		); err == nil {
			t.Errorf("%q ended nothing", end)
		}
	}
}
