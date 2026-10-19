package cli

import (
	"maps"
	"net/http"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

// AI Gateway keys the fake answers with 402 (out of credits) and 403 (no
// access); testGatewayKey works and testOtherGatewayKey is rejected.
const (
	keyNoCredits = "sk-gw-NoCreditsGatewayKey00CRDT" // ****...CRDT
	keyNoAccess  = "sk-gw-NoAccessGatewayKey000NOAC" // ****...NOAC
)

func gatewayFake() *fakeAPI {
	f := newFakeAPI()
	f.gatewayKeys[keyNoCredits] = http.StatusPaymentRequired
	f.gatewayKeys[keyNoAccess] = http.StatusForbidden
	return f
}

func TestGatewayLoginBranches(t *testing.T) {
	k, w := testGatewayKey, testOtherGatewayKey
	gw := func(args ...string) []string { return append([]string{"ai-gateway", "login"}, args...) }
	piped := gw("--api-key", "-")
	saved := func(key string) *config.File { return &config.File{GatewayAPIKey: key} }
	errLine := func(msg, hint string) string { return "saladcloud: error: " + msg + "\nhint: " + hint + "\n" }
	const (
		dGet      = "Get your AI Gateway key at https://portal.salad.com/ai-gateway\n"
		eTry      = "hint: to try it, run saladcloud ai-gateway test (sends one billed request)\n"
		oSaved    = "AI Gateway key saved\nConfig at: <path>\n"
		dInvalidG = "That AI Gateway key is invalid. Check that you copied all of it, and that it isn't your " +
			"SaladCloud API key (that one goes in saladcloud login).\n"
		eWarnW = "warning: commands will use SALAD_AI_GATEWAY_API_KEY (****...WXYZ) instead of the AI Gateway key " +
			"you just saved (****...EFGH)"
	)
	cancelAtCheck := gatewayFake()
	cancelAtCheck.cancelOn = "/models"
	docSaved := map[string]any{
		"models":                    1,
		"ai_gateway_api_key":        "****...EFGH",
		"ai_gateway_api_key_source": "config",
		"ai_gateway_api_key_saved":  true,
		"credits_available":         true,
		"env_overrides":             "[]",
		"config_path":               "<path>",
		"action":                    "logged_in",
		"ok":                        true,
	}
	with := func(m map[string]any, kv ...any) map[string]any {
		out := make(map[string]any)
		maps.Copy(out, m)
		for i := 0; i+1 < len(kv); i += 2 {
			out[kv[i].(string)] = kv[i+1]
		}
		return out
	}
	runBranches(t, []branchCase{
		{
			id:   "corrupt config",
			s:    session{args: piped, stdin: k, rawConfig: "{"},
			code: 1,
			kind: "api_error",
			stderr: errLine(
				"cannot read the config file <path>: unexpected end of JSON input",
				"fix or delete it, then run saladcloud ai-gateway login again",
			),
		},
		{
			id: "empty --api-key", s: session{args: gw("--api-key=")}, code: 2, kind: "usage",
			stderr: errLine("--api-key is empty", "check that the variable you passed is set"),
		},
		{
			id: "--api-key",
			s:  session{args: gw("--api-key", k), api: gatewayFake()},
			stderr: "warning: --api-key exposes the key on the command line; pipe it instead\nAI Gateway key: " +
				"****...EFGH (from --api-key)\n" + eTry,
			stdout: oSaved,
			after:  saved(k),
			doc:    docSaved,
		},
		{
			id: "--api-key -", s: session{args: piped, stdin: k + "\n", api: gatewayFake()},
			stderr: "AI Gateway key: ****...EFGH\n" + eTry, stdout: oSaved, after: saved(k), doc: docSaved,
		},
		{
			id: "--api-key -, nothing on stdin", s: session{args: piped}, code: 2, kind: "usage",
			stderr: errLine("--api-key - got no key on stdin", "pipe the key in, or set SALAD_AI_GATEWAY_API_KEY"),
		},
		{
			id:   "no key, no terminal",
			s:    session{args: gw()},
			code: 3,
			kind: "auth",
			stderr: errLine(
				"no AI Gateway key provided",
				"run saladcloud ai-gateway login in a terminal, or set SALAD_AI_GATEWAY_API_KEY",
			),
		},
		{
			id: "key from SALAD_AI_GATEWAY_API_KEY",
			s:  session{args: gw(), env: map[string]string{"SALAD_AI_GATEWAY_API_KEY": k}, api: gatewayFake()},
			stderr: "No AI Gateway key provided, using SALAD_AI_GATEWAY_API_KEY (****...EFGH).\nhint: to save it, " +
				"pipe it to saladcloud ai-gateway login --api-key -\n",
			stdout: "The AI Gateway key works. It is not saved, so it works only where SALAD_AI_GATEWAY_API_KEY is " +
				"set.\n",
			doc: with(docSaved, "ai_gateway_api_key_saved", false, "ai_gateway_api_key_source", "env"),
		},
		{
			id:     "at a terminal",
			s:      session{args: gw(), tty: []string{k}, api: gatewayFake()},
			device: dGet + dPaste,
			stderr: "AI Gateway key: ****...EFGH\n" + eTry,
			stdout: oSaved,
			after:  saved(k),
			doc:    docSaved,
		},
		{
			id: "at a terminal, a variable set and a key saved",
			s: session{
				args: gw(), tty: []string{"", k}, api: gatewayFake(),
				env: map[string]string{"SALAD_AI_GATEWAY_API_KEY": w}, saved: saved(w),
			},
			device: "SALAD_AI_GATEWAY_API_KEY is set in this shell (****...WXYZ); commands will use it instead of " +
				"what ai-gateway login saves.\n" +
				"Saved AI Gateway key: ****...WXYZ (Ctrl-C to keep it)\n" + dGet + dPaste + "Opening https://portal.salad.com/ai-gateway in your browser.\n" + dPaste,
			opened: 1,
			stderr: "AI Gateway key: ****...EFGH\n" + eTry + eWarnW + ". To remove the variable:\n" +
				"  PowerShell:  Remove-Item Env:SALAD_AI_GATEWAY_API_KEY\n  cmd:         set " +
				"SALAD_AI_GATEWAY_API_KEY=\nIf it comes back in a new terminal, remove it permanently:\n  " +
				"[Environment]::SetEnvironmentVariable('SALAD_AI_GATEWAY_API_KEY', $null, 'User')\n" +
				"Then open a new terminal (restart VS Code if you use its terminal).\n",
			stdout: oSaved,
			after:  saved(k),
			doc:    with(docSaved, "ai_gateway_api_key_source", "env", "env_overrides", "[SALAD_AI_GATEWAY_API_KEY]"),
		},
		{
			id: "Ctrl-C", s: session{args: gw(), tty: []string{keyCtrlC}}, code: 1, kind: "interrupted",
			device: dGet + dPaste, stderr: "saladcloud: error: ai-gateway login canceled, nothing changed\n",
		},
		{
			id:     "end of input, a key saved",
			s:      session{args: gw(), tty: []string{keyEOF}, saved: saved(w)},
			code:   1,
			kind:   "interrupted",
			device: "Saved AI Gateway key: ****...WXYZ (Ctrl-C to keep it)\n" + dGet + dPaste,
			stderr: "saladcloud: error: ai-gateway login canceled, your saved AI Gateway key (****...WXYZ) is " +
				"unchanged\n",
		},
		{
			id:     "401, pasted",
			s:      session{args: gw(), tty: []string{w, k}, api: gatewayFake()},
			device: dGet + dPaste + dInvalidG + dPaste,
			stderr: "AI Gateway key: ****...WXYZ\nAI Gateway key: ****...EFGH\n" + eTry,
			stdout: oSaved,
			after:  saved(k),
			doc:    docSaved,
		},
		{
			id: "401, pasted in quotes",
			s:  session{args: gw(), tty: []string{`"` + k + `"`, k}, api: gatewayFake()},
			device: dGet + dPaste + "That AI Gateway key is invalid. Remove the quotes around it and paste it " +
				"again.\n" + dPaste,
			stderr: "AI Gateway key: ****...FGH\"\nAI Gateway key: ****...EFGH\n" + eTry,
			stdout: oSaved,
			after:  saved(k),
			doc:    docSaved,
		},
		{
			id:     "401, --api-key",
			status: 401,
			s:      session{args: gw("--api-key", w), api: gatewayFake()},
			code:   3,
			kind:   "auth",
			stderr: "warning: --api-key exposes the key on the command line; pipe it instead\n" +
				errLine(
					"AI Gateway key ****...WXYZ (from --api-key) is invalid",
					"get one at https://portal.salad.com/ai-gateway",
				),
		},
		{
			id:     "401, stdin",
			status: 401,
			s:      session{args: piped, stdin: w, api: gatewayFake()},
			code:   3,
			kind:   "auth",
			stderr: errLine(
				"AI Gateway key ****...WXYZ (from stdin) is invalid",
				"get one at https://portal.salad.com/ai-gateway",
			),
		},
		{
			id:     "401, the variable, another key saved",
			status: 401,
			s: session{
				args:  gw(),
				env:   map[string]string{"SALAD_AI_GATEWAY_API_KEY": w},
				saved: saved(k),
				api:   gatewayFake(),
			},
			code: 3,
			kind: "auth",
			stderr: errLine(
				"AI Gateway key ****...WXYZ (from SALAD_AI_GATEWAY_API_KEY) is invalid",
				"remove SALAD_AI_GATEWAY_API_KEY to use the saved AI Gateway key (****...EFGH), or set it to a "+
					"working key",
			),
		},
		{
			id:     "401, the variable, none saved",
			status: 401,
			s:      session{args: gw(), env: map[string]string{"SALAD_AI_GATEWAY_API_KEY": w}, api: gatewayFake()},
			code:   3,
			kind:   "auth",
			stderr: errLine(
				"AI Gateway key ****...WXYZ (from SALAD_AI_GATEWAY_API_KEY) is invalid",
				"set SALAD_AI_GATEWAY_API_KEY to a working key, or remove it and run saladcloud ai-gateway login",
			),
		},
		{
			id:     "401, in quotes",
			status: 401,
			s:      session{args: piped, stdin: "'" + k + "'", api: gatewayFake()},
			code:   3,
			kind:   "auth",
			stderr: errLine("AI Gateway key ****...FGH' (from stdin) is invalid", "remove the quotes around it"),
		},
		{
			id: "402",
			s:  session{args: piped, stdin: keyNoCredits, api: gatewayFake()},
			stderr: "AI Gateway key: ****...CRDT\nwarning: the key's organization is out of credits, so AI Gateway " +
				"requests are rejected until you add some\n" +
				"hint: add credits from the organization's AI Gateway page: https://portal.salad.com/ai-gateway\n",
			stdout: oSaved,
			after:  saved(keyNoCredits),
			doc:    with(docSaved, "ai_gateway_api_key", "****...CRDT", "credits_available", false),
		},
		{
			id:     "403",
			status: 403,
			s:      session{args: piped, stdin: keyNoAccess, api: gatewayFake()},
			code:   3,
			kind:   "auth",
			stderr: errLine(
				"AI Gateway key ****...NOAC (from stdin) has no AI Gateway access",
				"check that you are still a member of its organization; if you are, contact support",
			),
		},
		{
			id: "the variable differs",
			s: session{
				args:  piped,
				stdin: k,
				env:   map[string]string{"SALAD_AI_GATEWAY_API_KEY": w},
				api:   gatewayFake(),
			},
			stderr: "AI Gateway key: ****...EFGH\n" + eTry + eWarnW + "\n",
			stdout: oSaved,
			after:  saved(k),
			doc:    with(docSaved, "ai_gateway_api_key_source", "env", "env_overrides", "[SALAD_AI_GATEWAY_API_KEY]"),
		},
		{
			id:   "a key no key can be",
			s:    session{args: piped, stdin: "sk-gw-café-0000EFGH"},
			code: 3,
			kind: "auth",
			stderr: errLine(
				"AI Gateway key ****...EFGH (from stdin) contains characters an AI Gateway key cannot have",
				"check for quotes or stray characters in it",
			),
		},
		{
			id:     "Ctrl-C during the check",
			s:      session{args: piped, stdin: k, api: cancelAtCheck},
			code:   1,
			kind:   "interrupted",
			stderr: "saladcloud: error: ai-gateway login canceled, nothing changed\n",
		},
	})
}

func TestGatewayLogoutBranches(t *testing.T) {
	k, w := testGatewayKey, testOtherGatewayKey
	both := &config.File{APIKey: testAPIKey, Organization: "my-org", GatewayAPIKey: k}
	runBranches(t, []branchCase{
		{
			id: "a key saved", s: session{args: []string{"ai-gateway", "logout"}, saved: both},
			stdout: "Logged out: removed the saved AI Gateway key (****...EFGH)\n",
			after:  &config.File{APIKey: testAPIKey, Organization: "my-org"},
			doc:    map[string]any{"removed": true, "env_overrides": "[]", "action": "logged_out", "ok": true},
		},
		{
			id: "nothing saved", s: session{args: []string{"ai-gateway", "logout"}},
			stdout: "No AI Gateway key was saved; nothing to remove\n",
			doc:    map[string]any{"removed": false, "env_overrides": "[]"},
		},
		{
			id: "the variable set, at a terminal", s: session{
				args: []string{"ai-gateway", "logout"}, saved: both, tty: []string{},
				env: map[string]string{"SALAD_AI_GATEWAY_API_KEY": w},
			},
			stdout: "Logged out: removed the saved AI Gateway key (****...EFGH)\n",
			stderr: "warning: commands still use SALAD_AI_GATEWAY_API_KEY (****...WXYZ). To remove the variable:\n" +
				"  PowerShell:  Remove-Item Env:SALAD_AI_GATEWAY_API_KEY\n  cmd:         set " +
				"SALAD_AI_GATEWAY_API_KEY=\nIf it comes back in a new terminal, remove it permanently:\n  " +
				"[Environment]::SetEnvironmentVariable('SALAD_AI_GATEWAY_API_KEY', $null, 'User')\n" +
				"Then open a new terminal (restart VS Code if you use its terminal).\n",
			after: &config.File{APIKey: testAPIKey, Organization: "my-org"},
			doc:   map[string]any{"removed": true, "env_overrides": "[SALAD_AI_GATEWAY_API_KEY]"},
		},
		{
			id: "the variable set, no terminal",
			s: session{
				args: []string{"ai-gateway", "logout"},
				env:  map[string]string{"SALAD_AI_GATEWAY_API_KEY": w},
			},
			stdout: "No AI Gateway key was saved; nothing to remove\n",
			stderr: "warning: commands still use SALAD_AI_GATEWAY_API_KEY (****...WXYZ)\n",
			doc:    map[string]any{"removed": false, "env_overrides": "[SALAD_AI_GATEWAY_API_KEY]"},
		},
	})
}
