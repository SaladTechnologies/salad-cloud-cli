package cli

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

// Login's test keys: testAPIKey (****...H13m) and keyRotated (****...9QxZ)
// work; testOtherAPIKey (****...ABCD) is rejected.
const keyRotated = "salad_cloud_user_RotatedApiKey000009QxZ"

// Lines the terminal shows (a prompt is followed by what was typed: nothing
// for a hidden one).
const (
	dGet      = "Get your API key at https://portal.salad.com/api-key\n"
	dPaste    = "Paste it here, hidden (press Enter to open the page in a browser): \n"
	dSavedKey = "Saved API key: ****...ABCD (Ctrl-C to keep it)\nhint: to switch organization or project instead, run saladcloud use <org>/<project>\n"
	dMenu     = "  1  acme-prod   (Acme Production)\n  2  my-org      (My Org)\n"
	dProject  = "Type a project name [default]: \n"
	dInvalid  = "That API key is invalid. Check that you copied all of it, and that it isn't an AI Gateway key (those go in saladcloud ai-gateway login).\n"
	eNext     = "Next, check that everything works:\n  saladcloud doctor\n"
	eKeyH13m  = "API key: ****...H13m\n"
	oLoggedIn = "Logged in: my-org/default\nConfig at: <path>\n"
)

// loginCase is one path through login, from where the key comes from to
// what is saved.
type loginCase struct {
	id     string
	s      session
	code   int
	kind   string // the error document's kind; "" on success
	stdout string // table mode; <path> is the config file
	stderr string
	device string
	opened int
	after  *config.File // the config file afterward; nil: unchanged
	// json: env_overrides, and whether the key was saved (success only)
	overrides  []string
	keyUnsaved bool
	// status is the error document's http_status; 0 means null.
	status int
}

func withOrgs(orgs map[string][]string) *fakeAPI {
	f := newFakeAPI()
	f.orgs = orgs
	return f
}

func loginCases() []loginCase {
	K, O := testAPIKey, testOtherAPIKey
	stdinK := K + "\n"
	piped := []string{"login", "--api-key", "-"}
	pipedOrg := []string{"login", "--api-key", "-", "--org", "my-org"}
	saved := func(key, org, project string) *config.File {
		return &config.File{APIKey: key, Organization: org, Project: project}
	}
	oneOrg := withOrgs(map[string][]string{"my-org": {"default"}})
	noDefault := withOrgs(map[string][]string{"my-org": {"staging"}})
	listing := func(status int) *fakeAPI {
		f := newFakeAPI()
		f.listing = status
		return f
	}
	cancelAtOrg := newFakeAPI()
	cancelAtOrg.cancelOn = "/quotas"
	noOrgs := withOrgs(make(map[string][]string))
	empty := "saladcloud: error: %s\nhint: %s\n"
	errLine := func(msg, hint string) string { return fmt.Sprintf(empty, msg, hint) }
	noHint := func(msg string) string { return "saladcloud: error: " + msg + "\n" }
	return []loginCase{
		// Before step 1.
		{
			id:   "corrupt config",
			s:    session{args: pipedOrg, stdin: stdinK, rawConfig: "{"},
			code: 1,
			kind: "api_error",
			stderr: errLine(
				"cannot read the config file <path>: unexpected end of JSON input",
				"fix or delete it, then run saladcloud login again",
			),
		},
		{
			id: "empty --api-key", s: session{args: []string{"login", "--api-key="}}, code: 2, kind: "usage",
			stderr: errLine("--api-key is empty", "check that the variable you passed is set"),
		},
		{
			id: "empty --org", s: session{args: []string{"login", "--org="}}, code: 2, kind: "usage",
			stderr: errLine("--org is empty", "check that the variable you passed is set"),
		},
		{
			id: "empty --project", s: session{args: []string{"login", "--project="}}, code: 2, kind: "usage",
			stderr: errLine("--project is empty", "check that the variable you passed is set"),
		},
		{
			id:   "--org with /",
			s:    session{args: []string{"login", "--org", "acme-prod/staging"}},
			code: 2,
			kind: "usage",
			stderr: errLine(
				"--org takes an organization only",
				"pass the project separately, for example saladcloud login --org acme-prod --project staging",
			),
		},

		// Step 1: where the key comes from.
		{
			id:     "key from --api-key",
			s:      session{args: []string{"login", "--api-key", K, "--org", "my-org"}},
			stderr: "warning: --api-key exposes the key on the command line; pipe it instead\nAPI key: ****...H13m (from --api-key)\nOrganization: my-org (from --org)\nProject: default\n",
			stdout: oLoggedIn,
			after:  saved(K, "my-org", "default"),
		},
		{
			id:     "key from stdin",
			s:      session{args: pipedOrg, stdin: stdinK},
			stderr: eKeyH13m + "Organization: my-org (from --org)\nProject: default\n",
			stdout: oLoggedIn,
			after:  saved(K, "my-org", "default"),
		},
		{
			id: "--api-key - with nothing on stdin", s: session{args: pipedOrg}, code: 2, kind: "usage",
			stderr: errLine("--api-key - got no key on stdin", "pipe the key in, or set SALAD_API_KEY"),
		},
		{
			id: "key from stdin at a terminal", s: session{args: pipedOrg, tty: []string{K, ""}},
			device: "\n" + dProject, stderr: eKeyH13m + "Organization: my-org (from --org)\nProject: default\n" + eNext,
			stdout: oLoggedIn, after: saved(K, "my-org", "default"),
		},
		{
			id: "key from SALAD_API_KEY",
			s: session{
				args: []string{"login"},
				env:  map[string]string{"SALAD_API_KEY": K, "SALAD_ORGANIZATION": "my-org"},
			},
			stderr: "No API key provided, using SALAD_API_KEY (****...H13m).\nOrganization: my-org (from SALAD_ORGANIZATION)\nProject: default\n" +
				"The API key is not saved, so it works only where SALAD_API_KEY is set.\nhint: to save it, pipe it to saladcloud login --api-key -\n",
			stdout:     oLoggedIn,
			after:      saved("", "my-org", "default"),
			keyUnsaved: true,
		},
		{
			id: "no key and no terminal", s: session{args: []string{"login"}}, code: 3, kind: "auth",
			stderr: errLine("no API key provided", "run saladcloud login in a terminal, or set SALAD_API_KEY"),
		},
		{
			id:   "no new key, one already saved",
			s:    session{args: []string{"login"}, saved: saved(K, "my-org", "default")},
			code: 2,
			kind: "usage",
			stderr: errLine(
				"no API key provided, and a key is already saved",
				"to switch organization or project, run saladcloud use <org>/<project>",
			),
		},
		{
			id: "a key no key can be",
			s: session{
				args: []string{"login", "--org", "my-org"},
				env:  map[string]string{"SALAD_API_KEY": "salad_cloud_user_caf\u00e9000DTb9"},
			},
			code: 3,
			kind: "auth",
			stderr: errLine(
				"API key ****...DTb9 (from SALAD_API_KEY) contains characters an API key cannot have",
				"check for quotes or stray characters in it",
			),
		},
		{
			id: "a pasted key no key can be",
			s: session{
				args: []string{"login", "--org", "my-org"},
				tty:  []string{"salad_cloud_user_caf\u00e9000DTb9", K, ""},
			},
			device: dGet + dPaste + "That API key contains characters an API key cannot have. Check for quotes or stray characters in it.\n" + dPaste + dProject,
			stderr: eKeyH13m + "Organization: my-org (from --org)\nProject: default\n" + eNext,
			stdout: oLoggedIn,
			after:  saved(K, "my-org", "default"),
		},

		// Step 2: the key prompt.
		{
			id: "an overriding variable named at the key prompt",
			s: session{
				args: []string{"login", "--org", "my-org"},
				tty:  []string{K, ""},
				env:  map[string]string{"SALAD_ORGANIZATION": "acme-prod"},
			},
			device: "SALAD_ORGANIZATION is set in this shell (acme-prod); commands will use it instead of what login saves.\n" + dGet + dPaste + dProject,
			stderr: eKeyH13m + "Organization: my-org (from --org)\nProject: default\n" +
				"warning: commands will use SALAD_ORGANIZATION (acme-prod) instead of the organization you just saved (my-org). To remove the variable:\n" +
				"  PowerShell:  Remove-Item Env:SALAD_ORGANIZATION\n  cmd:         set SALAD_ORGANIZATION=\n" +
				"If it comes back in a new terminal, remove it permanently:\n  [Environment]::SetEnvironmentVariable('SALAD_ORGANIZATION', $null, 'User')\n" +
				"Then open a new terminal (restart VS Code if you use its terminal).\n" + eNext,
			stdout:    oLoggedIn,
			after:     saved(K, "my-org", "default"),
			overrides: []string{"SALAD_ORGANIZATION"},
		},
		{
			id:     "Enter at the key prompt opens the browser",
			s:      session{args: []string{"login", "--org", "my-org"}, tty: []string{"", K, ""}},
			device: dGet + dPaste + "Opening https://portal.salad.com/api-key in your browser.\n" + dPaste + dProject,
			opened: 1,
			stderr: eKeyH13m + "Organization: my-org (from --org)\nProject: default\n" + eNext,
			stdout: oLoggedIn,
			after:  saved(K, "my-org", "default"),
		},
		{
			id:     "Enter at the key prompt with no display",
			s:      session{args: []string{"login", "--org", "my-org"}, tty: []string{"", K, ""}, os: "linux"},
			device: dGet + "Paste it here, hidden: \nPaste it here, hidden: \n" + dProject,
			stderr: eKeyH13m + "Organization: my-org (from --org)\nProject: default\n" + eNext,
			stdout: oLoggedIn,
			after:  saved(K, "my-org", "default"),
		},
		{
			id:     "Ctrl-C at the key prompt",
			s:      session{args: []string{"login"}, tty: []string{keyCtrlC}},
			code:   1,
			kind:   "interrupted",
			device: dGet + dPaste,
			stderr: noHint("login cancelled, nothing changed"),
		},
		{
			id:     "end of input at the key prompt, a key saved",
			s:      session{args: []string{"login"}, tty: []string{keyEOF}, saved: saved(O, "my-org", "default")},
			code:   1,
			kind:   "interrupted",
			device: dSavedKey + dGet + dPaste,
			stderr: noHint("login cancelled, your saved API key (****...ABCD) is unchanged"),
		},

		// Step 3: is the key good?
		{
			id:     "a rejected key at the prompt, then a good one",
			s:      session{args: []string{"login", "--org", "my-org"}, tty: []string{O, K, ""}},
			device: dGet + dPaste + dInvalid + dPaste + dProject,
			stderr: "API key: ****...ABCD\n" + eKeyH13m + "Organization: my-org (from --org)\nProject: default\n" + eNext,
			stdout: oLoggedIn,
			after:  saved(K, "my-org", "default"),
		},
		{
			id:     "a quoted key at the prompt",
			s:      session{args: []string{"login", "--org", "my-org"}, tty: []string{`"` + K + `"`, K, ""}},
			device: dGet + dPaste + "That API key is invalid. Remove the quotes around it and paste it again.\n" + dPaste + dProject,
			stderr: "API key: ****...13m\"\n" + eKeyH13m + "Organization: my-org (from --org)\nProject: default\n" + eNext,
			stdout: oLoggedIn,
			after:  saved(K, "my-org", "default"),
		},
		{
			id:     "a rejected key from --api-key",
			status: 401,
			s:      session{args: []string{"login", "--api-key", O, "--org", "my-org"}},
			code:   3,
			kind:   "auth",
			stderr: "warning: --api-key exposes the key on the command line; pipe it instead\n" +
				errLine(
					"API key ****...ABCD (from --api-key) is invalid",
					"check that you copied all of it, and that it isn't an AI Gateway key",
				),
		},
		{
			id:     "a rejected key from stdin",
			status: 401,
			s:      session{args: pipedOrg, stdin: O},
			code:   3,
			kind:   "auth",
			stderr: errLine(
				"API key ****...ABCD (from stdin) is invalid",
				"check that you copied all of it, and that it isn't an AI Gateway key",
			),
		},
		{
			id:     "a rejected SALAD_API_KEY, another key saved",
			status: 401,
			s: session{
				args:  []string{"login", "--org", "my-org"},
				env:   map[string]string{"SALAD_API_KEY": O},
				saved: saved(K, "my-org", "default"),
			},
			code: 3,
			kind: "auth",
			stderr: errLine(
				"API key ****...ABCD (from SALAD_API_KEY) is invalid",
				"remove SALAD_API_KEY to use the saved API key (****...H13m), or set it to a working key",
			),
		},
		{
			id:     "a rejected SALAD_API_KEY, none saved",
			status: 401,
			s:      session{args: []string{"login", "--org", "my-org"}, env: map[string]string{"SALAD_API_KEY": O}},
			code:   3,
			kind:   "auth",
			stderr: errLine(
				"API key ****...ABCD (from SALAD_API_KEY) is invalid",
				"set SALAD_API_KEY to a working key, or remove it and run saladcloud login",
			),
		},
		{
			id:     "a quoted key from stdin",
			status: 401,
			s:      session{args: pipedOrg, stdin: "'" + K + "'"},
			code:   3,
			kind:   "auth",
			stderr: errLine("API key ****...13m' (from stdin) is invalid", "remove the quotes around it"),
		},
		{
			id:     "the organization listing fails",
			status: 500,
			s:      session{args: pipedOrg, stdin: stdinK, api: listing(500)},
			code:   1,
			kind:   "api_error",
			stderr: errLine("HTTP 500", "nothing was saved; run saladcloud login again"),
		},
		{
			id:     "no organization listing",
			s:      session{args: pipedOrg, stdin: stdinK, api: listing(403)},
			stderr: eKeyH13m + "Organization: my-org (from --org)\nProject: default\n",
			stdout: oLoggedIn,
			after:  saved(K, "my-org", "default"),
		},
		{
			id:     "Ctrl-C during a request",
			s:      session{args: pipedOrg, stdin: stdinK, api: cancelAtOrg},
			code:   1,
			kind:   "interrupted",
			stderr: eKeyH13m + noHint("login cancelled, nothing changed"),
		},

		// Step 4: which organization?
		{
			id:     "organization from SALAD_ORGANIZATION",
			s:      session{args: piped, stdin: stdinK, env: map[string]string{"SALAD_ORGANIZATION": "acme-prod"}},
			stderr: eKeyH13m + "Organization: acme-prod (from SALAD_ORGANIZATION)\nProject: default\n",
			stdout: "Logged in: acme-prod/default\nConfig at: <path>\n",
			after:  saved(K, "acme-prod", "default"),
		},
		{
			id:     "organization from --org not found",
			status: 404,
			s:      session{args: []string{"login", "--api-key", "-", "--org", "acme-stagng"}, stdin: stdinK},
			code:   4,
			kind:   "not_found",
			stderr: eKeyH13m + errLine(
				`organization "acme-stagng" (from --org) not found`,
				"to list yours, run saladcloud orgs list",
			),
		},
		{
			id:     "organization from --org not found at a terminal",
			status: 404,
			s:      session{args: []string{"login", "--org", "acme-stagng"}, tty: []string{K}},
			code:   4,
			kind:   "not_found",
			device: dGet + dPaste,
			stderr: eKeyH13m + errLine(
				`organization "acme-stagng" (from --org) not found`,
				"your organizations: acme-prod, my-org",
			),
		},
		{
			id:     "saved organization",
			s:      session{args: piped, stdin: stdinK, saved: saved("", "my-org", "")},
			stderr: eKeyH13m + "Organization: my-org\nProject: default\n",
			stdout: oLoggedIn,
			after:  saved(K, "my-org", "default"),
		},
		{
			id:     "saved organization, your only one",
			s:      session{args: []string{"login"}, tty: []string{K, ""}, saved: saved("", "my-org", ""), api: oneOrg},
			device: dGet + dPaste + dProject,
			stderr: eKeyH13m + "Organization: my-org\nProject: default\n" + eNext,
			stdout: oLoggedIn,
			after:  saved(K, "my-org", "default"),
		},
		{
			id:     "organization menu, the saved one as default",
			s:      session{args: []string{"login"}, tty: []string{K, "", ""}, saved: saved("", "acme-prod", "")},
			device: dGet + dPaste + dMenu + "Select an organization by number or name [acme-prod]: \n" + dProject,
			stderr: eKeyH13m + "Organization: acme-prod\nProject: default\n" + eNext,
			stdout: "Logged in: acme-prod/default\nConfig at: <path>\n",
			after:  saved(K, "acme-prod", "default"),
		},
		{
			id:     "saved organization not found, then the menu",
			s:      session{args: []string{"login"}, tty: []string{K, "2", ""}, saved: saved("", "old-org", "")},
			device: dGet + dPaste + "Saved organization \"old-org\" not found.\n" + dMenu + "Select an organization by number or name: 2\n" + dProject,
			stderr: eKeyH13m + "Organization: my-org\nProject: default\n" + eNext,
			stdout: oLoggedIn,
			after:  saved(K, "my-org", "default"),
		},
		{
			id:     "saved organization not found, no terminal",
			status: 404,
			s:      session{args: piped, stdin: stdinK, saved: saved("", "old-org", "")},
			code:   4,
			kind:   "not_found",
			stderr: eKeyH13m + errLine(
				`saved organization "old-org" not found`,
				"pass --org or set SALAD_ORGANIZATION; to list yours, run saladcloud orgs list",
			),
		},
		{
			id:   "no organization and no terminal",
			s:    session{args: piped, stdin: stdinK},
			code: 2,
			kind: "usage",
			stderr: eKeyH13m + errLine(
				"no organization set",
				"pass --org or set SALAD_ORGANIZATION; to list yours, run saladcloud orgs list",
			),
		},
		{
			id:     "your only organization",
			s:      session{args: []string{"login"}, tty: []string{K, ""}, api: oneOrg},
			device: dGet + dPaste + dProject,
			stderr: eKeyH13m + "Organization: my-org\nProject: default\n" + eNext,
			stdout: oLoggedIn,
			after:  saved(K, "my-org", "default"),
		},
		{
			id:     "organization menu, picked by number",
			s:      session{args: []string{"login"}, tty: []string{K, "2", ""}},
			device: dGet + dPaste + dMenu + "Select an organization by number or name: 2\n" + dProject,
			stderr: eKeyH13m + "Organization: my-org\nProject: default\n" + eNext,
			stdout: oLoggedIn,
			after:  saved(K, "my-org", "default"),
		},
		{
			id: "organization menu, invalid answers",
			s:  session{args: []string{"login"}, tty: []string{K, "3", "acme-stagng", "Acme Prod", K, "1", ""}},
			device: dGet + dPaste + dMenu +
				"Select an organization by number or name: 3\n\"3\" isn't on the list; pick 1-2 or type a name.\n" +
				"Select an organization by number or name: acme-stagng\n\"acme-stagng\" not found; pick 1-2 or type a name.\n" +
				"Select an organization by number or name: Acme Prod\n\"Acme Prod\" is not a valid organization name. It looks like a display name; type the name from the portal URL, the part after /organizations/.\n" +
				"Select an organization by number or name: " + K + "\nThat looks like the API key you just pasted, not an organization name.\n" +
				"Select an organization by number or name: 1\n" + dProject,
			stderr: eKeyH13m + "Organization: acme-prod\nProject: default\n" + eNext,
			stdout: "Logged in: acme-prod/default\nConfig at: <path>\n",
			after:  saved(K, "acme-prod", "default"),
		},
		{
			id: "no listing, organization typed",
			s:  session{args: []string{"login"}, tty: []string{K, "acme-stagng", "my-org", ""}, api: listing(404)},
			device: dGet + dPaste + "Type an organization name: acme-stagng\n" +
				"Organization \"acme-stagng\" not found. Visit https://portal.salad.com/organizations to see your organizations, create one or accept an invitation.\n" +
				"Type an organization name: my-org\n" + dProject,
			stderr: eKeyH13m + "Organization: my-org\nProject: default\n" + eNext,
			stdout: oLoggedIn,
			after:  saved(K, "my-org", "default"),
		},
		{
			id: "no listing, the saved organization as default",
			s: session{
				args:  []string{"login"},
				tty:   []string{K, "", ""},
				api:   listing(404),
				saved: saved("", "my-org", ""),
			},
			device: dGet + dPaste + "Type an organization name [my-org]: \n" + dProject,
			stderr: eKeyH13m + "Organization: my-org\nProject: default\n" + eNext,
			stdout: oLoggedIn,
			after:  saved(K, "my-org", "default"),
		},
		{
			id:     "no listing, the key rejected by the organization check",
			s:      session{args: []string{"login"}, tty: []string{O, "my-org", K, "my-org", ""}, api: listing(404)},
			device: dGet + dPaste + "Type an organization name: my-org\n" + dInvalid + dPaste + "Type an organization name: my-org\n" + dProject,
			stderr: "API key: ****...ABCD\n" + eKeyH13m + "Organization: my-org\nProject: default\n" + eNext,
			stdout: oLoggedIn,
			after:  saved(K, "my-org", "default"),
		},
		{
			id:   "no organizations",
			s:    session{args: pipedOrg, stdin: stdinK, api: noOrgs},
			code: 4,
			kind: "not_found",
			stderr: eKeyH13m + errLine(
				"you have no organizations",
				"create one, or accept an invitation, at https://portal.salad.com/organizations",
			),
		},
		{
			id:   "a display name from --org",
			s:    session{args: []string{"login", "--org", "Acme Prod"}},
			code: 6,
			kind: "invalid_input",
			stderr: errLine(
				`invalid organization name "Acme Prod" (from --org)`,
				"it looks like a display name; use the name from the portal URL, the part after /organizations/",
			),
		},
		{
			id:     "capitals from SALAD_ORGANIZATION",
			s:      session{args: []string{"login"}, env: map[string]string{"SALAD_ORGANIZATION": "My-Org"}},
			code:   6,
			kind:   "invalid_input",
			stderr: errLine(`invalid organization name "My-Org" (from SALAD_ORGANIZATION)`, `did you mean "my-org"?`),
		},
		{
			id:   "a name against the rule from --project",
			s:    session{args: []string{"login", "--project", "1proj"}},
			code: 6,
			kind: "invalid_input",
			stderr: errLine(
				`invalid project name "1proj" (from --project)`,
				"names are 2-63 characters: lowercase letters, digits and hyphens, starting with a letter and ending with a letter or digit",
			),
		},

		// Step 5: which project?
		{
			id: "project from --project",
			s: session{
				args:  []string{"login", "--api-key", "-", "--org", "my-org", "--project", "staging"},
				stdin: stdinK,
			},
			stderr: eKeyH13m + "Organization: my-org (from --org)\nProject: staging (from --project)\n",
			stdout: "Logged in: my-org/staging\nConfig at: <path>\n",
			after:  saved(K, "my-org", "staging"),
		},
		{
			id:     "project from SALAD_PROJECT",
			s:      session{args: pipedOrg, stdin: stdinK, env: map[string]string{"SALAD_PROJECT": "staging"}},
			stderr: eKeyH13m + "Organization: my-org (from --org)\nProject: staging (from SALAD_PROJECT)\n",
			stdout: "Logged in: my-org/staging\nConfig at: <path>\n",
			after:  saved(K, "my-org", "staging"),
		},
		{
			id:     "project from --project not found",
			status: 404,
			s: session{
				args:  []string{"login", "--api-key", "-", "--org", "my-org", "--project", "stagng"},
				stdin: stdinK,
			},
			code: 4,
			kind: "not_found",
			stderr: eKeyH13m + "Organization: my-org (from --org)\n" +
				errLine(
					`project "stagng" (from --project) not found in my-org`,
					"create one at https://portal.salad.com/organizations/my-org/projects/new",
				),
		},
		{
			id:     "the saved project as the prompt's default",
			s:      session{args: []string{"login"}, tty: []string{K, "", ""}, saved: saved("", "my-org", "staging")},
			device: dGet + dPaste + dMenu + "Select an organization by number or name [my-org]: \nType a project name [staging]: \n",
			stderr: eKeyH13m + "Organization: my-org\nProject: staging\n" + eNext,
			stdout: "Logged in: my-org/staging\nConfig at: <path>\n",
			after:  saved(K, "my-org", "staging"),
		},
		{
			id:     "saved project",
			s:      session{args: piped, stdin: stdinK, saved: saved("", "my-org", "staging")},
			stderr: eKeyH13m + "Organization: my-org\nProject: staging\n",
			stdout: "Logged in: my-org/staging\nConfig at: <path>\n",
			after:  saved(K, "my-org", "staging"),
		},
		{
			id:     "saved project not found",
			s:      session{args: piped, stdin: stdinK, saved: saved("", "my-org", "old-proj")},
			stderr: eKeyH13m + "Organization: my-org\nSaved project \"old-proj\" not found in my-org. Create one at https://portal.salad.com/organizations/my-org/projects/new.\nProject: default\n",
			stdout: oLoggedIn,
			after:  saved(K, "my-org", "default"),
		},
		{
			id: "saved project not found at a terminal",
			s: session{
				args:  []string{"login", "--org", "my-org"},
				tty:   []string{K, ""},
				saved: saved("", "my-org", "old-proj"),
			},
			device: dGet + dPaste + "Saved project \"old-proj\" not found in my-org. Create one at https://portal.salad.com/organizations/my-org/projects/new.\n" + dProject,
			stderr: eKeyH13m + "Organization: my-org (from --org)\nProject: default\n" + eNext,
			stdout: oLoggedIn,
			after:  saved(K, "my-org", "default"),
		},
		{
			id:     "no default project, skipped",
			s:      session{args: []string{"login"}, tty: []string{K, ""}, api: noDefault},
			device: dGet + dPaste + "Project \"default\" not found in my-org. Create one later if needed.\nType a project name, or press Enter to skip: \n",
			stderr: eKeyH13m + "Organization: my-org\n" + eNext,
			stdout: "Logged in: my-org, no project set\nConfig at: <path>\n",
			after:  saved(K, "my-org", ""),
		},
		{
			id:     "no default project, a name typed",
			s:      session{args: []string{"login"}, tty: []string{K, "staging"}, api: noDefault},
			device: dGet + dPaste + "Project \"default\" not found in my-org. Create one later if needed.\nType a project name, or press Enter to skip: staging\n",
			stderr: eKeyH13m + "Organization: my-org\nProject: staging\n" + eNext,
			stdout: "Logged in: my-org/staging\nConfig at: <path>\n",
			after:  saved(K, "my-org", "staging"),
		},
		{
			id: "no default project, no terminal", s: session{args: pipedOrg, stdin: stdinK, api: noDefault},
			stderr: eKeyH13m + "Organization: my-org (from --org)\n",
			stdout: "Logged in: my-org, no project set\nConfig at: <path>\n", after: saved(K, "my-org", ""),
		},
		{
			id: "project prompt, invalid answers",
			s:  session{args: []string{"login"}, tty: []string{K, "stagng", "Staging Env", "staging"}, api: noDefault},
			device: dGet + dPaste + "Project \"default\" not found in my-org. Create one later if needed.\n" +
				"Type a project name, or press Enter to skip: stagng\nProject \"stagng\" not found in my-org. Create one at https://portal.salad.com/organizations/my-org/projects/new.\n" +
				"Type a project name, or press Enter to skip: Staging Env\n\"Staging Env\" is not a valid project name. It looks like a display name; type the name from the portal URL, the part after /projects/.\n" +
				"Type a project name, or press Enter to skip: staging\n",
			stderr: eKeyH13m + "Organization: my-org\nProject: staging\n" + eNext,
			stdout: "Logged in: my-org/staging\nConfig at: <path>\n",
			after:  saved(K, "my-org", "staging"),
		},
		{
			id:     "a project saved for another organization",
			s:      session{args: pipedOrg, stdin: stdinK, saved: saved("", "acme-prod", "staging")},
			stderr: eKeyH13m + "Organization: my-org (from --org)\nProject: default\n",
			stdout: oLoggedIn,
			after:  saved(K, "my-org", "default"),
		},

		// Step 6: save.
		{
			id: "several variables override what was saved",
			s: session{
				args:  pipedOrg,
				stdin: stdinK,
				env:   map[string]string{"SALAD_API_KEY": O, "SALAD_ORGANIZATION": "acme-prod"},
			},
			stderr: eKeyH13m + "Organization: my-org (from --org)\nProject: default\n" +
				"warning: commands will use these environment variables instead of what you just saved:\n" +
				"warning:   SALAD_API_KEY       ****...ABCD  (saved: ****...H13m)\n" +
				"warning:   SALAD_ORGANIZATION  acme-prod    (saved: my-org)\n",
			stdout:    oLoggedIn,
			after:     saved(K, "my-org", "default"),
			overrides: []string{"SALAD_API_KEY", "SALAD_ORGANIZATION"},
		},
		{
			id: "key from SALAD_API_KEY, another key saved",
			s: session{
				args:  []string{"login"},
				env:   map[string]string{"SALAD_API_KEY": K, "SALAD_ORGANIZATION": "my-org"},
				saved: saved(O, "", ""),
			},
			stderr: "No API key provided, using SALAD_API_KEY (****...H13m).\nOrganization: my-org (from SALAD_ORGANIZATION)\nProject: default\n" +
				"The API key is not saved, so it works only where SALAD_API_KEY is set.\n" +
				"Where SALAD_API_KEY is not set, commands use the saved API key (****...ABCD).\nhint: to save it, pipe it to saladcloud login --api-key -\n",
			stdout:     oLoggedIn,
			after:      saved(O, "my-org", "default"),
			keyUnsaved: true,
		},
		{
			id:     "Ctrl-C at the organization prompt, a key saved",
			s:      session{args: []string{"login"}, tty: []string{K, keyCtrlC}, saved: saved(O, "", "")},
			code:   1,
			kind:   "interrupted",
			device: dSavedKey + dGet + dPaste + dMenu + "Select an organization by number or name: \n",
			stderr: eKeyH13m + noHint("login cancelled, your saved API key (****...ABCD) is unchanged"),
		},
	}
}

// TestLoginBranches drives every path through login's six steps in table
// mode (stdout, stderr, the terminal, the config file afterward) and in json
// mode (the error document or the login document, and nothing else on
// stderr but warnings).
func TestLoginBranches(t *testing.T) {
	for _, tc := range loginCases() {
		t.Run(tc.id, func(t *testing.T) {
			before := config.File{}
			if tc.s.saved != nil {
				before = *tc.s.saved
			}
			want := before
			if tc.after != nil {
				want = *tc.after
			}

			o := withMode(tc.s, "table").run(t)
			at := func(s string) string { return strings.ReplaceAll(s, o.configPath, "<path>") }
			if o.code != tc.code {
				t.Errorf("table: exit %d, want %d\nstderr:\n%s", o.code, tc.code, o.stderr)
			}
			if got := at(o.stdout); got != tc.stdout {
				t.Errorf("table stdout:\n%s\nwant:\n%s", got, tc.stdout)
			}
			if got := at(o.stderr); got != tc.stderr {
				t.Errorf("table stderr:\n%s\nwant:\n%s", got, tc.stderr)
			}
			if o.device != tc.device {
				t.Errorf("terminal:\n%s\nwant:\n%s", o.device, tc.device)
			}
			if len(o.opened) != tc.opened {
				t.Errorf("opened %v, want %d pages", o.opened, tc.opened)
			}
			if tc.s.rawConfig == "" && o.config != want {
				t.Errorf("config file %+v, want %+v", o.config, want)
			}

			j := withMode(tc.s, "json").run(t)
			if j.code != tc.code || j.device != tc.device {
				t.Errorf("json: exit %d, terminal %q; table mode had %d, %q", j.code, j.device, tc.code, tc.device)
			}
			checkMachineStderr(t, j)
			if tc.code != 0 {
				doc, err := j.errorDocument()
				if err != nil {
					t.Fatalf("json: %v\n%s", err, j.stderr)
				}
				msg, hint := tableError(at(o.stderr))
				atJ := func(s string) string { return strings.ReplaceAll(s, j.configPath, "<path>") }
				if doc["kind"] != tc.kind || atJ(fmt.Sprint(doc["message"])) != msg ||
					atJ(fmt.Sprint(doc["hint"])) != hint {
					t.Errorf("json error %v; want kind %s, message %q, hint %q", doc, tc.kind, msg, hint)
				}
				var status any
				if tc.status != 0 {
					status = float64(tc.status)
				}
				if doc["http_status"] != status {
					t.Errorf("json http_status = %v, want %v", doc["http_status"], status)
				}
				return
			}
			doc := j.document(t)
			wantOverrides := tc.overrides
			if wantOverrides == nil {
				wantOverrides = []string{}
			}
			var gotOverrides []string
			for _, v := range doc["env_overrides"].([]any) {
				gotOverrides = append(gotOverrides, v.(string))
			}
			slices.Sort(gotOverrides)
			project := any(nil)
			if want.Project != "" {
				project = want.Project
			}
			if doc["organization"] != want.Organization || doc["project"] != project ||
				doc["api_key"] != "****...H13m" ||
				doc["api_key_saved"] != !tc.keyUnsaved ||
				doc["action"] != "logged_in" ||
				doc["ok"] != true ||
				fmt.Sprint(gotOverrides) != fmt.Sprint(wantOverrides) ||
				doc["config_path"] != j.configPath {
				t.Errorf("json document %v", doc)
			}
		})
	}
}

// withMode is s with -o mode on its command line.
func withMode(s session, mode string) session {
	s.args = append(append([]string(nil), s.args...), "-o", mode)
	return s
}

// tableError reads the error and its hint from table-mode stderr; the hint
// is "<nil>" when there is none, as the error document prints null.
func tableError(stderr string) (msg, hint string) {
	hint = "<nil>"
	for _, l := range lines(stderr) {
		if m, ok := strings.CutPrefix(l, "saladcloud: error: "); ok {
			msg = m
		} else if h, ok := strings.CutPrefix(l, "hint: "); ok && msg != "" {
			hint = h
		}
	}
	return msg, hint
}

// TestLoginWholeSessions checks whole login sessions line for line: what a
// person sees, prompts and answers, stdout and stderr in the order they were
// written.
func TestLoginWholeSessions(t *testing.T) {
	oneOrg := withOrgs(map[string][]string{"my-org": {"default"}})
	twoOrgs := newFakeAPI()
	twoOrgs.keys[keyRotated] = true
	twoOrgs.orgOrder = []string{"my-org", "acme-prod"}
	const windowsPath = `C:\Users\you\AppData\Roaming\saladcloud\config.json`
	const ciPath = "/home/ci/.config/saladcloud/config.json"
	for _, tc := range []struct {
		name, path string
		s          session
		want       string
	}{
		{
			"new user, one organization, key on the clipboard", windowsPath,
			session{args: []string{"login"}, tty: []string{testAPIKey, ""}, api: oneOrg},
			`Get your API key at https://portal.salad.com/api-key
Paste it here, hidden (press Enter to open the page in a browser):
API key: ****...H13m
Organization: my-org
Type a project name [default]:
Project: default
Logged in: my-org/default
Config at: C:\Users\you\AppData\Roaming\saladcloud\config.json
Next, check that everything works:
  saladcloud doctor
`,
		},
		{
			"rotating a key with the old one still in the environment, two organizations, one saved", windowsPath,
			session{
				args: []string{"login"}, tty: []string{"", keyRotated, "", ""}, api: twoOrgs,
				env:   map[string]string{"SALAD_API_KEY": testOtherAPIKey},
				saved: &config.File{APIKey: testOtherAPIKey, Organization: "my-org", Project: "default"},
			},
			`SALAD_API_KEY is set in this shell (****...ABCD); commands will use it instead of what login saves.
Saved API key: ****...ABCD (Ctrl-C to keep it)
hint: to switch organization or project instead, run saladcloud use <org>/<project>
Get your API key at https://portal.salad.com/api-key
Paste it here, hidden (press Enter to open the page in a browser):
Opening https://portal.salad.com/api-key in your browser.
Paste it here, hidden (press Enter to open the page in a browser):
API key: ****...9QxZ
  1  my-org      (My Org)
  2  acme-prod   (Acme Production)
Select an organization by number or name [my-org]:
Organization: my-org
Type a project name [default]:
Project: default
Logged in: my-org/default
Config at: C:\Users\you\AppData\Roaming\saladcloud\config.json
warning: commands will use SALAD_API_KEY (****...ABCD) instead of the API key you just saved (****...9QxZ). To remove the variable:
  PowerShell:  Remove-Item Env:SALAD_API_KEY
  cmd:         set SALAD_API_KEY=
If it comes back in a new terminal, remove it permanently:
  [Environment]::SetEnvironmentVariable('SALAD_API_KEY', $null, 'User')
Then open a new terminal (restart VS Code if you use its terminal).
`,
		},
		{
			"a script", ciPath,
			session{args: []string{"login", "--api-key", "-", "--org", "my-org"}, stdin: testAPIKey, os: "linux"},
			`API key: ****...H13m
Organization: my-org (from --org)
Project: default
Logged in: my-org/default
Config at: /home/ci/.config/saladcloud/config.json
`,
		},
		{
			"a script with only the environment", ciPath,
			session{args: []string{"login"}, os: "linux", env: map[string]string{"SALAD_API_KEY": testAPIKey, "SALAD_ORGANIZATION": "my-org"}},
			`No API key provided, using SALAD_API_KEY (****...H13m).
Organization: my-org (from SALAD_ORGANIZATION)
Project: default
Logged in: my-org/default
Config at: /home/ci/.config/saladcloud/config.json
The API key is not saved, so it works only where SALAD_API_KEY is set.
hint: to save it, pipe it to saladcloud login --api-key -
`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := tc.s.run(t)
			if o.code != 0 {
				t.Fatalf("exit %d:\n%s", o.code, o.stderr)
			}
			got := strings.ReplaceAll(o.transcript, o.configPath, tc.path)
			var trimmed []string
			for l := range strings.SplitSeq(got, "\n") {
				trimmed = append(trimmed, strings.TrimRight(l, " "))
			}
			if got = strings.Join(trimmed, "\n"); got != tc.want {
				t.Errorf("session:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}
