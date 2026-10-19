package cli

import (
	"net/http"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

const removeOrgSteps = "  PowerShell:  Remove-Item Env:SALAD_ORGANIZATION\n  cmd:         set SALAD_ORGANIZATION=\n" +
	"If it comes back in a new terminal, remove it permanently:\n  " +
	"[Environment]::SetEnvironmentVariable('SALAD_ORGANIZATION', $null, 'User')\n" +
	"Then open a new terminal (restart VS Code if you use its terminal).\n"

func errLines(msg, hint string) string { return "saladcloud: error: " + msg + "\nhint: " + hint + "\n" }

// TestUseBranches covers use: a malformed target, the organization and
// project checks, rejected keys, a variable that overrides what was saved,
// and failed or canceled requests.
func TestUseBranches(t *testing.T) {
	t.Parallel()
	k := testAPIKey
	saved := &config.File{APIKey: k, Organization: "my-org", Project: "default"}
	use := func(target string) []string { return []string{"use", target} }
	noDefault := withOrgs(map[string][]string{"my-org": {"staging"}})
	serverError := newFakeAPI()
	serverError.status["/quotas"] = http.StatusInternalServerError
	cancelAtOrg := newFakeAPI()
	cancelAtOrg.cancelOn = "/quotas"
	target := "the target is <org> or <org>/<project>, for example saladcloud use acme-prod/staging"
	runBranches(t, []branchCase{
		{
			id:   "corrupt config",
			s:    session{args: use("my-org"), rawConfig: "{"},
			code: 1,
			kind: "api_error",
			stderr: errLines(
				"cannot read the config file <path>: unexpected end of JSON input",
				"fix or delete it, then run saladcloud use again",
			),
		},
		{
			id: "trailing slash", s: session{args: use("acme-prod/"), saved: saved}, code: 2, kind: "usage",
			stderr: errLines(`invalid target "acme-prod/"`, target),
		},
		{
			id: "leading slash", s: session{args: use("/staging"), saved: saved}, code: 2, kind: "usage",
			stderr: errLines(`invalid target "/staging"`, target),
		},
		{
			id: "two slashes", s: session{args: use("a/b/c"), saved: saved}, code: 2, kind: "usage",
			stderr: errLines(`invalid target "a/b/c"`, target),
		},
		{
			id: "no API key", s: session{args: use("my-org")}, code: 3, kind: "auth",
			stderr: errLines("no API key set", "run saladcloud login in a terminal, or set SALAD_API_KEY"),
		},
		{
			id:     "organization not found",
			status: 404,
			s:      session{args: use("acme-stagng"), saved: saved},
			code:   4,
			kind:   "not_found",
			stderr: errLines(`organization "acme-stagng" not found`, "to list yours, run saladcloud orgs list"),
		},
		{
			id:     "organization not found at a terminal",
			status: 404,
			s:      session{args: use("acme-stagng"), saved: saved, tty: []string{}},
			code:   4,
			kind:   "not_found",
			stderr: errLines(`organization "acme-stagng" not found`, "your organizations: acme-prod, my-org"),
		},
		{
			id:   "a display name",
			s:    session{args: use("Acme Prod"), saved: saved},
			code: 6,
			kind: "invalid_input",
			stderr: errLines(
				`invalid organization name "Acme Prod"`,
				"it looks like a display name; use the name from the portal URL, the part after /organizations/",
			),
		},
		{
			id:     "a project in capitals",
			s:      session{args: use("my-org/Staging"), saved: saved},
			code:   6,
			kind:   "invalid_input",
			stderr: errLines(`invalid project name "Staging"`, `did you mean "staging"?`),
		},
		{
			id:     "project not found",
			status: 404,
			s:      session{args: use("my-org/stagng"), saved: saved},
			code:   4,
			kind:   "not_found",
			stderr: errLines(
				`project "stagng" not found in my-org`,
				"create one at https://portal.salad.com/organizations/my-org/projects/new",
			),
		},
		{
			id:     "no default project",
			s:      session{args: use("my-org"), saved: saved, api: noDefault},
			stderr: "Project \"default\" not found in my-org. Create one later if needed.\n",
			stdout: "Switched to my-org, no project set\n",
			after:  &config.File{APIKey: k, Organization: "my-org"},
			doc: map[string]any{
				"organization":  "my-org",
				"project":       nil,
				"env_overrides": "[]",
				"action":        "switched",
				"ok":            true,
			},
		},
		{
			id: "organization alone, default project",
			s: session{
				args:  use("acme-prod"),
				saved: &config.File{APIKey: k, Organization: "my-org", Project: "staging"},
			},
			stdout: "Switched to acme-prod/default\n",
			stderr: "hint: no project named, so \"default\" is used; for another, run saladcloud use " +
				"acme-prod/<project>\n",
			after: &config.File{APIKey: k, Organization: "acme-prod", Project: "default"},
			doc:   map[string]any{"organization": "acme-prod", "project": "default", "env_overrides": "[]"},
		},
		{
			id:     "organization and project",
			s:      session{args: use("my-org/staging"), saved: saved},
			stdout: "Switched to my-org/staging\n",
			after:  &config.File{APIKey: k, Organization: "my-org", Project: "staging"},
			doc:    map[string]any{"organization": "my-org", "project": "staging"},
		},
		{
			id: "a variable overrides the organization",
			s: session{
				args:  use("acme-prod/default"),
				saved: saved,
				env:   map[string]string{"SALAD_ORGANIZATION": "my-org"},
			},
			stdout: "Switched to acme-prod/default\n",
			stderr: "warning: commands will use SALAD_ORGANIZATION (my-org) instead of the organization you just " +
				"saved (acme-prod)\n",
			after: &config.File{APIKey: k, Organization: "acme-prod", Project: "default"},
			doc:   map[string]any{"env_overrides": "[SALAD_ORGANIZATION]"},
		},
		{
			id: "a variable overrides the organization, at a terminal",
			s: session{
				args:  use("acme-prod/default"),
				saved: saved,
				tty:   []string{},
				env:   map[string]string{"SALAD_ORGANIZATION": "my-org"},
			},
			stdout: "Switched to acme-prod/default\n",
			stderr: "warning: commands will use SALAD_ORGANIZATION (my-org) instead of the organization you just " +
				"saved (acme-prod). To remove the variable:\n" + removeOrgSteps,
			after: &config.File{APIKey: k, Organization: "acme-prod", Project: "default"},
			doc:   map[string]any{"env_overrides": "[SALAD_ORGANIZATION]"},
		},
		{
			id:     "a rejected key from config",
			status: 401,
			s:      session{args: use("my-org"), saved: &config.File{APIKey: testOtherAPIKey}},
			code:   3,
			kind:   "auth",
			stderr: errLines("API key ****...ABCD (from config) is invalid", "run saladcloud login to save a new key"),
		},
		{
			id:     "a rejected SALAD_API_KEY, another key saved",
			status: 401,
			s: session{
				args:  use("my-org"),
				saved: saved,
				env:   map[string]string{"SALAD_API_KEY": testOtherAPIKey},
			},
			code: 3,
			kind: "auth",
			stderr: errLines(
				"API key ****...ABCD (from SALAD_API_KEY) is invalid",
				"remove SALAD_API_KEY to use the saved API key (****...H13m), or set it to a working key",
			),
		},
		{
			id:     "a rejected SALAD_API_KEY, none saved",
			status: 401,
			s:      session{args: use("my-org"), env: map[string]string{"SALAD_API_KEY": testOtherAPIKey}},
			code:   3,
			kind:   "auth",
			stderr: errLines(
				"API key ****...ABCD (from SALAD_API_KEY) is invalid",
				"set SALAD_API_KEY to a working key, or remove it and run saladcloud login",
			),
		},
		{
			id:     "server error",
			status: 500,
			s:      session{args: use("my-org"), saved: saved, api: serverError},
			code:   1,
			kind:   "api_error",
			stderr: errLines("HTTP 500", "nothing was changed; run saladcloud use again"),
		},
		{
			id:     "Ctrl-C during a request",
			s:      session{args: use("my-org"), saved: saved, api: cancelAtOrg},
			code:   1,
			kind:   "interrupted",
			stderr: "saladcloud: error: use canceled, nothing changed\n",
		},
	})
}

// TestConfigSetUnsetBranches covers config set and unset: masked keys, a key
// from stdin, a variable that overrides what was written, and a saved project
// carried over to another organization.
func TestConfigSetUnsetBranches(t *testing.T) {
	t.Parallel()
	saved := &config.File{Organization: "my-org", Project: "staging"}
	cfg := func(args ...string) []string { return append([]string{"config"}, args...) }
	runBranches(t, []branchCase{
		{
			id:     "set organization, a saved project carried over",
			s:      session{args: cfg("set", "organization", "acme-prod"), saved: saved},
			stdout: "Set organization to acme-prod\nConfig at: <path>\n",
			stderr: "warning: the saved project \"staging\" was saved with my-org and now applies to " +
				"acme-prod\nhint: to switch both, run saladcloud use acme-prod/<project>\n",
			after: &config.File{Organization: "acme-prod", Project: "staging"},
			doc: map[string]any{
				"key":           "organization",
				"value":         "acme-prod",
				"env_overrides": "[]",
				"config_path":   "<path>",
				"action":        "set",
				"ok":            true,
			},
		},
		{
			id:     "set api-key as an argument",
			s:      session{args: cfg("set", "api-key", keyRotated)},
			stdout: "Set api-key to ****...9QxZ\nConfig at: <path>\n",
			stderr: "warning: a key passed as an argument is exposed on the command line; pipe it to saladcloud " +
				"config set api-key - instead\n",
			after: &config.File{APIKey: keyRotated},
			doc:   map[string]any{"value": "****...9QxZ"},
		},
		{
			id:     "set ai-gateway-api-key from stdin",
			s:      session{args: cfg("set", "ai-gateway-api-key", "-"), stdin: testGatewayKey + "\n"},
			stdout: "Set ai-gateway-api-key to ****...EFGH\nConfig at: <path>\n",
			after:  &config.File{GatewayAPIKey: testGatewayKey},
			doc:    map[string]any{"value": "****...EFGH"},
		},
		{
			id: "set api-key -, nothing on stdin", s: session{args: cfg("set", "api-key", "-")}, code: 2, kind: "usage",
			stderr: errLines("no api-key value on stdin", "pipe the key in"),
		},
		{
			id: "set, a variable differs",
			s: session{
				args: cfg("set", "organization", "acme-prod"),
				env:  map[string]string{"SALAD_ORGANIZATION": "my-org"},
			},
			stdout: "Set organization to acme-prod\nConfig at: <path>\n",
			stderr: "warning: commands will use SALAD_ORGANIZATION (my-org) instead of the organization you just " +
				"saved (acme-prod)\n",
			after: &config.File{
				Organization: "acme-prod",
			},
			doc: map[string]any{"env_overrides": "[SALAD_ORGANIZATION]"},
		},
		{
			id:   "set, an unknown key",
			s:    session{args: cfg("set", "color", "blue")},
			code: 2,
			kind: "usage",
			stderr: errLines(
				`unknown config key "color"`,
				"the keys are api-key, organization, project and ai-gateway-api-key",
			),
		},
		{
			id:   "set, a corrupt config file",
			s:    session{args: cfg("set", "organization", "x"), rawConfig: "{"},
			code: 1,
			kind: "api_error",
			stderr: errLines(
				"cannot read the config file <path>: unexpected end of JSON input",
				"fix or delete it, then run saladcloud config set again",
			),
		},
		{
			id: "unset", s: session{args: cfg("unset", "organization"), saved: saved},
			stdout: "Unset organization\nConfig at: <path>\n", after: &config.File{Project: "staging"},
			doc: map[string]any{"key": "organization", "env_overrides": "[]", "action": "unset", "ok": true},
		},
		{
			id: "unset, the variable still set, at a terminal",
			s: session{
				args: cfg("unset", "organization"), saved: saved, tty: []string{},
				env: map[string]string{"SALAD_ORGANIZATION": "acme-prod"},
			},
			stdout: "Unset organization\nConfig at: <path>\n",
			stderr: "warning: commands still use SALAD_ORGANIZATION (acme-prod). To remove the " +
				"variable:\n" + removeOrgSteps,
			after: &config.File{Project: "staging"},
			doc:   map[string]any{"env_overrides": "[SALAD_ORGANIZATION]"},
		},
		{
			id: "unset, the variable still set, no terminal", s: session{
				args: cfg("unset", "organization"), saved: saved,
				env: map[string]string{"SALAD_ORGANIZATION": "acme-prod"},
			},
			stdout: "Unset organization\nConfig at: <path>\n",
			stderr: "warning: commands still use SALAD_ORGANIZATION (acme-prod)\n",
			after:  &config.File{Project: "staging"}, doc: map[string]any{"env_overrides": "[SALAD_ORGANIZATION]"},
		},
	})
}
