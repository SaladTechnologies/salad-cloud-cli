package cli

// Tests for the invariants every command keeps, run through the whole-command
// harness: one error document, and stderr carries nothing else in json and
// yaml mode; no prompt or stdin wait without a terminal; credentials are never
// printed whole; and a config write names every variable that overrides what
// it wrote.

import (
	"bytes"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/agentdoc"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

// keyEnv is a working key, organization and project in the environment.
func keyEnv() map[string]string {
	return map[string]string{"SALAD_API_KEY": testAPIKey, "SALAD_ORGANIZATION": "my-org", "SALAD_PROJECT": "default"}
}

// with returns env plus the given name/value pairs.
func with(env map[string]string, kv ...string) map[string]string {
	out := make(map[string]string)
	maps.Copy(out, env)
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

// savedLogin is a config file after a login to my-org/default.
func savedLogin() *config.File {
	return &config.File{APIKey: testAPIKey, Organization: "my-org", Project: "default"}
}

// errDocCase is one command line the error-document invariant is checked on.
type errDocCase struct {
	name string
	s    session
}

func errDocCases() []errDocCase {
	noDefault := newFakeAPI()
	noDefault.orgs = map[string][]string{"my-org": {"staging"}}
	serverError := newFakeAPI()
	serverError.status["/quotas"] = http.StatusInternalServerError
	forbidden := newFakeAPI()
	forbidden.status["/gpu-classes"] = http.StatusForbidden
	return []errDocCase{
		{name: "unknown command", s: session{args: []string{"frobnicate"}}},
		{name: "unknown flag", s: session{args: []string{"cg", "list", "--bogus"}}},
		{name: "missing positional", s: session{args: []string{"cg", "get"}}},
		{name: "bare noun", s: session{args: []string{"gpus"}}},
		{name: "invalid value", s: session{args: []string{"cg", "create", "Bad_Name", "--image", "x"}, env: keyEnv()}},
		{
			name: "no API key",
			s:    session{args: []string{"cg", "list"}, env: map[string]string{"SALAD_ORGANIZATION": "my-org"}},
		},
		{
			name: "no organization",
			s:    session{args: []string{"quotas", "get"}, env: map[string]string{"SALAD_API_KEY": testAPIKey}},
		},
		{name: "not found", s: session{args: []string{"cg", "get", "missing"}, env: keyEnv()}},
		{
			name: "rejected key",
			s:    session{args: []string{"cg", "list"}, env: with(keyEnv(), "SALAD_API_KEY", testOtherAPIKey)},
		},
		{name: "forbidden", s: session{args: []string{"gpus", "list"}, env: keyEnv(), api: forbidden}},
		{name: "server error", s: session{args: []string{"quotas", "get"}, env: keyEnv(), api: serverError}},
		{
			name: "network error",
			s: session{
				args: []string{"quotas", "get"},
				env:  with(keyEnv(), "SALAD_CLI_API_BASE_URL", "http://127.0.0.1:1"),
			},
		},
		{
			name: "assumed project missing",
			s:    session{args: []string{"cg", "list"}, env: with(keyEnv(), "SALAD_PROJECT", ""), api: noDefault},
		},
		{
			name: "assumed project warning",
			s:    session{args: []string{"cg", "list"}, env: with(keyEnv(), "SALAD_PROJECT", "")},
		},
		{name: "login, no key", s: session{args: []string{"login", "--org", "my-org"}}},
		{
			name: "login, rejected flag key",
			s:    session{args: []string{"login", "--api-key", testOtherAPIKey, "--org", "my-org"}},
		},
		{
			name: "login at a terminal",
			s:    session{args: []string{"login", "--org", "my-org"}, tty: []string{testAPIKey, ""}},
		},
		{name: "ai-gateway login, no key", s: session{args: []string{"ai-gateway", "login"}}},
		{
			name: "ai-gateway login at a terminal",
			s:    session{args: []string{"ai-gateway", "login"}, tty: []string{testGatewayKey}},
		},
		{
			name: "use, organization not found",
			s:    session{args: []string{"use", "nope"}, env: map[string]string{"SALAD_API_KEY": testAPIKey}},
		},
		{
			name: "use",
			s:    session{args: []string{"use", "my-org/staging"}, env: map[string]string{"SALAD_API_KEY": testAPIKey}},
		},
		{
			name: "config set",
			s:    session{args: []string{"config", "set", "organization", "acme-prod"}, saved: savedLogin()},
		},
		{name: "config set, unknown key", s: session{args: []string{"config", "set", "color", "blue"}}},
		{name: "config unset", s: session{args: []string{"config", "unset", "organization"}, saved: savedLogin()}},
		{name: "logout", s: session{args: []string{"logout"}, saved: savedLogin()}},
		{name: "whoami", s: session{args: []string{"whoami"}, saved: savedLogin()}},
		{name: "doctor, failing", s: session{args: []string{"doctor"}}},
		{name: "ssh-key setup, no terminal", s: session{args: []string{"ssh-key", "setup"}}},
		{
			name: "ssh-key setup at a terminal",
			// tty: []string{} is a terminal with no answers; nil would be no terminal.
			s: session{args: []string{"ssh-key", "setup"}, tty: []string{}, sshKey: true}, //nolint:revive // non-nil tty
		},
	}
}

// TestErrorDocumentInvariant: in json and yaml mode stderr is only warning:
// and note: lines and, on failure, one error document with all five keys; in
// table mode a failure is one saladcloud: error: line and at most one hint:
// line.
func TestErrorDocumentInvariant(t *testing.T) {
	t.Parallel()
	for _, tc := range errDocCases() {
		for _, mode := range []string{"table", "json", "yaml"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				t.Parallel()
				s := tc.s
				s.args = append(append([]string(nil), s.args...), "-o", mode)
				o := s.run(t)
				checkNoPasteInHints(t, &o)
				if mode == "table" {
					checkTableError(t, &o)
					return
				}
				checkMachineStderr(t, o)
			})
		}
	}
}

// checkTableError: a failure is one saladcloud: error: line, then at most
// one hint: line and nothing else.
func checkTableError(t *testing.T, o *outcome) {
	t.Helper()
	ls := lines(o.stderr)
	at := -1
	for i, l := range ls {
		if strings.HasPrefix(l, "saladcloud: error: ") {
			if at >= 0 {
				t.Errorf("two saladcloud: error: lines:\n%s", o.stderr)
			}
			at = i
		}
	}
	if o.code == 0 {
		if at >= 0 {
			t.Errorf("exit 0 with an error line:\n%s", o.stderr)
		}
		return
	}
	if at < 0 {
		t.Fatalf("exit %d without a saladcloud: error: line; stderr:\n%s", o.code, o.stderr)
	}
	if rest := ls[at+1:]; len(rest) > 1 || len(rest) == 1 && !strings.HasPrefix(rest[0], "hint: ") {
		t.Errorf("after the error line stderr has %q; want at most one hint: line", rest)
	}
}

// checkMachineStderr: json/yaml stderr is warning: and note: lines, plus the
// error document as the last line when the command failed.
func checkMachineStderr(t *testing.T, o outcome) {
	t.Helper()
	checkMachineStderrLines(t, &o)
	if o.code == 0 {
		return
	}
	doc, err := o.errorDocument()
	if err != nil {
		t.Fatalf("exit %d, but the last stderr line is not the error document (%v):\n%s", o.code, err, o.stderr)
	}
	checkErrorDocumentValues(t, &o, doc)
	t.Run("five keys", func(t *testing.T) {
		t.Parallel()
		keys := make([]string, 0, len(doc))
		for k := range doc {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		if got, want := strings.Join(keys, ","), "exit_code,hint,http_status,kind,message"; got != want {
			t.Errorf("error document keys = %s, want %s", got, want)
		}
	})
}

// checkMachineStderrLines checks that every stderr line of o is a warning: or a note:, except the last line of a
// failed run, which may be the error document.
func checkMachineStderrLines(t *testing.T, o *outcome) {
	t.Helper()
	ls := lines(o.stderr)
	for i, l := range ls {
		switch {
		case strings.HasPrefix(l, "warning: "), strings.HasPrefix(l, "note: "):
		case o.code != 0 && i == len(ls)-1 && strings.HasPrefix(l, `{"error":`):
		default:
			t.Errorf("stderr line %d is not a warning:, a note: or the error document: %q", i+1, l)
		}
	}
}

// checkErrorDocumentValues checks the error document of the failed run o: its exit_code is the process's, its kind
// one of clierr.Kinds, and its message one non-empty line.
func checkErrorDocumentValues(t *testing.T, o *outcome, doc map[string]any) {
	t.Helper()
	if code := asType[float64](doc["exit_code"]); int(code) != o.code {
		t.Errorf("exit_code %v, process exited %d", doc["exit_code"], o.code)
	}
	kind := asType[string](doc["kind"])
	known := false
	for _, k := range clierr.Kinds() {
		known = known || string(k) == kind
	}
	if !known {
		t.Errorf("kind %q is not in clierr.Kinds", kind)
	}
	if msg := asType[string](doc["message"]); msg == "" || strings.Contains(msg, "\n") {
		t.Errorf("message %q: want one non-empty line", msg)
	}
}

// checkNoPasteInHints: no hint asks for a key's value.
func checkNoPasteInHints(t *testing.T, o *outcome) {
	t.Helper()
	for _, l := range lines(o.stderr) {
		if strings.HasPrefix(l, "hint: ") && strings.Contains(strings.ToLower(l), "paste") {
			t.Errorf("hint mentions paste: %q", l)
		}
	}
	if doc, err := o.errorDocument(); err == nil {
		if h := asType[string](doc["hint"]); strings.Contains(strings.ToLower(h), "paste") {
			t.Errorf("hint mentions paste: %q", h)
		}
	}
}

// TestLoginNeverWaitsOnStdin: without --api-key - and without a terminal,
// the logins return without reading a stdin that never closes.
func TestLoginNeverWaitsOnStdin(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"login, no key anywhere", []string{"login", "--org", "my-org"}, nil},
		{"login, SALAD_API_KEY " +
			"set", []string{"login", "--org", "my-org"}, map[string]string{"SALAD_API_KEY": testAPIKey}},
		{"ai-gateway login, no key anywhere", []string{"ai-gateway", "login"}, nil},
		{"ai-gateway login, SALAD_AI_GATEWAY_API_KEY " +
			"set", []string{"ai-gateway", "login"}, map[string]string{"SALAD_AI_GATEWAY_API_KEY": testGatewayKey}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := session{args: tc.args, env: tc.env, openStdin: true}.run(t)
			if o.hung {
				t.Fatalf("still reading stdin after %s", stdinPatience)
			}
		})
	}
}

// credentialCase is one run a credential travels through.
type credentialCase struct {
	name string
	s    session
}

func credentialCases() []credentialCase {
	other := &config.File{
		APIKey:        testOtherAPIKey,
		Organization:  "my-org",
		Project:       "default",
		GatewayAPIKey: testOtherGatewayKey,
	}
	//nolint:prealloc // built by several appends; a count would only drift
	var cases []credentialCase
	for _, key := range []string{testAPIKey, testOtherAPIKey} { // accepted, rejected
		cases = append(
			cases,
			credentialCase{
				name: "login, flag",
				s:    session{args: []string{"login", "--api-key", key, "--org", "my-org"}},
			},
			credentialCase{
				name: "login, stdin",
				s:    session{args: []string{"login", "--api-key", "-", "--org", "my-org"}, stdin: key + "\n"},
			},
			credentialCase{
				name: "login, variable",
				s:    session{args: []string{"login", "--org", "my-org"}, env: map[string]string{"SALAD_API_KEY": key}},
			},
			credentialCase{
				name: "login, pasted",
				s:    session{args: []string{"login", "--org", "my-org"}, tty: []string{key, ""}},
			},
			credentialCase{
				name: "login, variable over a saved key",
				s: session{
					args:  []string{"login", "--api-key", "-", "--org", "my-org"},
					stdin: key + "\n",
					saved: other,
					env:   map[string]string{"SALAD_API_KEY": testOtherAPIKey},
				},
			},
		)
	}
	for _, key := range []string{testGatewayKey, testOtherGatewayKey} {
		cases = append(
			cases,
			credentialCase{
				name: "ai-gateway login, flag",
				s:    session{args: []string{"ai-gateway", "login", "--api-key", key}},
			},
			credentialCase{
				name: "ai-gateway login, stdin",
				s:    session{args: []string{"ai-gateway", "login", "--api-key", "-"}, stdin: key + "\n"},
			},
			credentialCase{
				name: "ai-gateway login, variable",
				s: session{
					args: []string{"ai-gateway", "login"},
					env:  map[string]string{"SALAD_AI_GATEWAY_API_KEY": key},
				},
			},
			credentialCase{
				name: "ai-gateway login, pasted",
				s:    session{args: []string{"ai-gateway", "login"}, tty: []string{key}},
			},
		)
	}
	varKeys := map[string]string{"SALAD_API_KEY": testAPIKey, "SALAD_AI_GATEWAY_API_KEY": testGatewayKey}
	cases = append(
		cases,
		credentialCase{name: "use", s: session{args: []string{"use", "my-org"}, env: varKeys, saved: other}},
		credentialCase{name: "whoami", s: session{args: []string{"whoami"}, env: varKeys, saved: other}},
		credentialCase{name: "doctor", s: session{args: []string{"doctor"}, env: varKeys, saved: other}},
		credentialCase{name: "logout", s: session{args: []string{"logout"}, env: varKeys, saved: other}},
		credentialCase{
			name: "ai-gateway logout",
			s:    session{args: []string{"ai-gateway", "logout"}, env: varKeys, saved: other},
		},
		credentialCase{
			name: "config set, argument",
			s:    session{args: []string{"config", "set", "api-key", testAPIKey}},
		},
		credentialCase{
			name: "config set, stdin",
			s:    session{args: []string{"config", "set", "ai-gateway-api-key", "-"}, stdin: testGatewayKey + "\n"},
		},
		credentialCase{name: "config get", s: session{args: []string{"config", "get", "api-key"}, saved: other}},
		credentialCase{name: "config list", s: session{args: []string{"config", "list"}, saved: other}},
		credentialCase{
			name: "config unset",
			s:    session{args: []string{"config", "unset", "api-key"}, saved: other, env: varKeys},
		},
		credentialCase{
			name: "a rejected key at run time",
			s:    session{args: []string{"cg", "list"}, env: with(keyEnv(), "SALAD_API_KEY", testOtherAPIKey)},
		},
		credentialCase{name: "ai-gateway test", s: session{args: []string{"ai-gateway", "test"}, env: varKeys}},
		credentialCase{
			name: "ai-gateway test, rejected key",
			s: session{
				args: []string{"ai-gateway", "test"},
				env:  map[string]string{"SALAD_AI_GATEWAY_API_KEY": testOtherGatewayKey},
			},
		},
	)
	return cases
}

// secretWords are the distinctive middles of the test credentials: no output
// may hold one, even cut short.
func secretWords() []string {
	return []string{"SentinelApiKey", "OtherApiKey", "SentinelGatewayKey", "OtherGatewayKey"}
}

// TestCredentialsNeverPrinted: an API key and an AI Gateway key, given by
// flag, stdin, variable or the prompt, through every setup command, never
// appear whole in stdout, stderr or on the terminal, only as ****...ABCD; no
// hint asks for a key's value.
func TestCredentialsNeverPrinted(t *testing.T) {
	t.Parallel()
	for _, tc := range credentialCases() {
		for _, mode := range []string{"table", "json"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				t.Parallel()
				s := tc.s
				s.args = append(append([]string(nil), s.args...), "-o", mode)
				o := s.run(t)
				all := o.all()
				checkNoCredentialIn(t, all)
				checkNoPasteInHints(t, &o)
				t.Run("mask", func(t *testing.T) {
					t.Parallel()
					if strings.Contains(all, "****\u2026") {
						t.Errorf("a key is masked as ****\u2026, not ****...:\n%s", all)
					}
				})
			})
		}
	}
}

// checkNoCredentialIn checks that all, everything a run showed, holds no test credential short of its last four
// characters and none of their distinctive middles.
func checkNoCredentialIn(t *testing.T, all string) {
	t.Helper()
	for _, k := range testSecrets() {
		if strings.Contains(all, k[:len(k)-4]) {
			t.Errorf("output holds the key %s:\n%s", k, all)
		}
	}
	for _, w := range secretWords() {
		if strings.Contains(all, w) {
			t.Errorf("output holds part of a key (%s):\n%s", w, all)
		}
	}
}

// TestNoHelpAsksForAKey: no --help text and not the agent block asks for a
// key's value (the word "paste").
func TestNoHelpAsksForAKey(t *testing.T) {
	t.Parallel()
	parent := newTestRun(t)
	if strings.Contains(strings.ToLower(agentdoc.Body()), "paste") {
		t.Error("the agent block mentions paste")
	}
	var all []*cobra.Command
	walkTree(parent.root, func(c *cobra.Command) { all = append(all, c) })
	for _, c := range all {
		path := c.CommandPath()
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			app := parent.clone(t)
			var out bytes.Buffer
			_, root := app.fresh()
			root.SetOut(&out)
			root.SetArgs(append(strings.Fields(path)[1:], "--help"))
			if err := root.ExecuteContext(t.Context()); err != nil {
				t.Fatalf("--help: %v", err)
			}
			if out.Len() == 0 {
				t.Fatal("--help printed nothing")
			}
			if strings.Contains(strings.ToLower(out.String()), "paste") {
				t.Errorf("--help mentions paste:\n%s", out.String())
			}
		})
	}
}

// overrideCase is one config write, with the variables that differ from what
// it writes.
type overrideCase struct {
	name      string
	s         session
	overrides []string // the variables the warning and env_overrides name
}

func overrideCases() []overrideCase {
	saved := &config.File{APIKey: testAPIKey, Organization: "my-org", Project: "default", GatewayAPIKey: testGatewayKey}
	key := map[string]string{"SALAD_API_KEY": testAPIKey}
	return []overrideCase{
		{
			name: "login",
			s:    session{args: []string{"login", "--api-key", "-", "--org", "my-org"}, stdin: testAPIKey + "\n"},
		},
		{name: "login, one variable", s: session{
			args: []string{"login", "--api-key", "-", "--org", "my-org"}, stdin: testAPIKey + "\n",
			env: map[string]string{"SALAD_API_KEY": testOtherAPIKey},
		}, overrides: []string{"SALAD_API_KEY"}},
		{name: "login, two variables", s: session{
			args: []string{"login", "--api-key", "-", "--org", "my-org"}, stdin: testAPIKey + "\n",
			env: map[string]string{"SALAD_API_KEY": testOtherAPIKey, "SALAD_ORGANIZATION": "acme-prod"},
		}, overrides: []string{"SALAD_API_KEY", "SALAD_ORGANIZATION"}},
		{
			name: "ai-gateway login",
			s:    session{args: []string{"ai-gateway", "login", "--api-key", "-"}, stdin: testGatewayKey + "\n"},
		},
		{name: "ai-gateway login, one variable", s: session{
			args: []string{"ai-gateway", "login", "--api-key", "-"}, stdin: testGatewayKey + "\n",
			env: map[string]string{"SALAD_AI_GATEWAY_API_KEY": testOtherGatewayKey},
		}, overrides: []string{"SALAD_AI_GATEWAY_API_KEY"}},
		{name: "use", s: session{args: []string{"use", "acme-prod"}, env: key}},
		{
			name:      "use, one variable",
			s:         session{args: []string{"use", "acme-prod"}, env: with(key, "SALAD_ORGANIZATION", "my-org")},
			overrides: []string{"SALAD_ORGANIZATION"},
		},
		{name: "config set", s: session{args: []string{"config", "set", "organization", "acme-prod"}, saved: saved}},
		{name: "config set, one variable", s: session{
			args: []string{"config", "set", "organization", "acme-prod"}, saved: saved,
			env: map[string]string{"SALAD_ORGANIZATION": "my-org"},
		}, overrides: []string{"SALAD_ORGANIZATION"}},
		{name: "config unset", s: session{args: []string{"config", "unset", "organization"}, saved: saved}},
		{name: "config unset, one variable", s: session{
			args: []string{"config", "unset", "organization"}, saved: saved,
			env: map[string]string{"SALAD_ORGANIZATION": "my-org"},
		}, overrides: []string{"SALAD_ORGANIZATION"}},
		{name: "logout", s: session{args: []string{"logout"}, saved: saved}},
		{
			name:      "logout, one variable",
			s:         session{args: []string{"logout"}, saved: saved, env: key},
			overrides: []string{"SALAD_API_KEY"},
		},
		{name: "ai-gateway logout", s: session{args: []string{"ai-gateway", "logout"}, saved: saved}},
		{name: "ai-gateway logout, one variable", s: session{
			args: []string{"ai-gateway", "logout"}, saved: saved,
			env: map[string]string{"SALAD_AI_GATEWAY_API_KEY": testGatewayKey},
		}, overrides: []string{"SALAD_AI_GATEWAY_API_KEY"}},
	}
}

var varName = regexp.MustCompile(`\bSALAD_[A-Z_]+\b`)

// TestConfigWritesNameOverridingVariables: every config writer prints one
// warning naming exactly the variables in its env_overrides (one warning:
// row each when there are several), and none when env_overrides is [].
func TestConfigWritesNameOverridingVariables(t *testing.T) {
	t.Parallel()
	for _, tc := range overrideCases() {
		for _, mode := range []string{"table", "json"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				t.Parallel()
				s := tc.s
				s.args = append(append([]string(nil), s.args...), "-o", mode)
				o := s.run(t)
				if o.code != 0 {
					t.Fatalf("exit %d:\n%s", o.code, o.stderr)
				}
				want := slices.Clone(tc.overrides)
				slices.Sort(want)
				if mode == "json" {
					checkEnvOverridesDocument(t, &o, want)
				}
				checkOverrideWarning(t, o.stderr, want)
			})
		}
	}
}

// checkEnvOverridesDocument checks that the json run o's env_overrides lists exactly the variables in want (sorted).
func checkEnvOverridesDocument(t *testing.T, o *outcome, want []string) {
	t.Helper()
	raw, ok := o.document(t)["env_overrides"].([]any)
	if !ok {
		t.Fatalf("env_overrides missing or not a list:\n%s", o.stdout)
	}
	got := make([]string, 0, len(raw))
	for _, v := range raw {
		s := asType[string](v)
		got = append(got, s)
	}
	slices.Sort(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("env_overrides = %v, want %v", got, want)
	}
}

// checkOverrideWarning checks that the warning: lines in stderr name exactly the variables in want (sorted), under
// one "warning: commands" line when there are any and none when there are not.
func checkOverrideWarning(t *testing.T, stderr string, want []string) {
	t.Helper()
	got, firsts := overrideWarnings(stderr)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the warnings name %v, want %v:\n%s", got, want, stderr)
	}
	if wantFirsts := min(len(want), 1); firsts != wantFirsts {
		t.Errorf("%d warnings open with \"warning: commands\", want %d:\n%s", firsts, wantFirsts, stderr)
	}
}

// overrideWarnings reads the override warning in stderr: the variables its warning: lines name, sorted, and how
// many lines open a warning ("warning: commands ...").
func overrideWarnings(stderr string) ([]string, int) {
	named, firsts := make(map[string]bool), 0
	for _, l := range lines(stderr) {
		if strings.HasPrefix(l, "warning: commands ") {
			firsts++ // the warning's first line, with one variable or several
		}
		if !strings.HasPrefix(l, "warning: ") || !varName.MatchString(l) {
			continue
		}
		for _, v := range varName.FindAllString(l, -1) {
			named[v] = true
		}
	}
	got := make([]string, 0, len(named))
	for v := range named {
		got = append(got, v)
	}
	slices.Sort(got)
	return got, firsts
}

// stdinPatience is how long a run that must not read stdin gets before the
// harness calls it stuck.
const stdinPatience = 10 * time.Second
