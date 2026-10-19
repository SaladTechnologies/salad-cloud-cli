package cli

import (
	"bufio"
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
)

const (
	gatewayKeyPage = "https://portal.salad.com/ai-gateway"
	envGatewayKey  = "SALAD_AI_GATEWAY_API_KEY"

	invalidPastedGatewayKey = "That AI Gateway key is invalid. Check that you copied all of it, and that it isn't your SaladCloud API key (that one goes in saladcloud login)."
	quotedPastedGatewayKey  = "That AI Gateway key is invalid. Remove the quotes around it and paste it again."
)

func gatewayLoginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Save an AI Gateway key",
		Long: `Save an AI Gateway key to the config file, checked against the gateway first.
The check sends one billed request (a fraction of a cent, to the key's
organization): the gateway lists its models for any key, so a completion is
the only way to tell a working one.

At a terminal, ai-gateway login asks for the key with the input hidden (Enter
at the prompt opens ` + gatewayKeyPage + `, which lists your
organizations and links to each one's key page). Without a terminal it never
prompts: the key comes from --api-key - (the first line of stdin), --api-key,
or SALAD_AI_GATEWAY_API_KEY, which is checked but not saved.

The AI Gateway key is not the SaladCloud API key that saladcloud login saves.
A key whose organization is out of credits is saved anyway, with a warning:
requests are rejected until credits are added.

SALAD_CLI_NO_PROMPT, or CI set to anything but empty, false or 0, makes it
behave as if there is no terminal.`,
		Example: `  # At a terminal
  saladcloud ai-gateway login

  # Scripted, bash or zsh (only the first line of stdin is read)
  printf '%s' "$GATEWAY_KEY" | saladcloud ai-gateway login --api-key -

  # Scripted, PowerShell
  $env:GATEWAY_KEY | saladcloud ai-gateway login --api-key -`,
		Args: cobra.NoArgs,
		RunE: runGatewayLogin,
	}
	cmd.Flags().
		String("api-key", "", "the AI Gateway key; - reads it from the first line of stdin (a key typed here is exposed on the command line)")
	return cmd
}

// gatewayLoginResult is ai-gateway login's JSON contract: the key checked
// (masked), where commands read it from now, whether it was saved, whether
// its organization has credits, and the variable that overrides it.
type gatewayLoginResult struct {
	Models           int      `json:"models"`
	GatewayKey       string   `json:"ai_gateway_api_key"`
	KeySource        string   `json:"ai_gateway_api_key_source"`
	KeySaved         bool     `json:"ai_gateway_api_key_saved"`
	CreditsAvailable bool     `json:"credits_available"`
	EnvOverrides     []string `json:"env_overrides"`
	ConfigPath       string   `json:"config_path"`
	Action           string   `json:"action"` // "logged_in"
	OK               bool     `json:"ok"`
}

// gatewayLogin is one run of ai-gateway login.
type gatewayLogin struct {
	ctx      context.Context
	saved    config.File
	terminal bool
	key      string
	keyFrom  string // keyFromFlag, keyFromStdin, envGatewayKey, or "" (pasted)
	models   int
	credits  bool // false after a 402
}

func runGatewayLogin(cmd *cobra.Command, _ []string) error {
	keyFlag, _ := cmd.Flags().GetString("api-key")
	if cmd.Flags().Changed("api-key") && keyFlag == "" {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "%s is empty", "--api-key").
			WithHint("check that the variable you passed is set")
	}
	if cerr := configReady("ai-gateway login"); cerr != nil {
		return cerr
	}
	g := &gatewayLogin{ctx: cmd.Context(), terminal: atTerminal(), credits: true}
	g.saved, _ = config.Load() // configReady has read it

	switch {
	case keyFlag == "-":
		var line string
		if con.Terminal() { // hidden whenever stdin is a terminal, prompts or not
			answer, err := con.ReadHidden()
			if err != nil {
				return g.canceled()
			}
			line = answer
		} else {
			line, _ = readLine(bufio.NewReader(os.Stdin))
		}
		if strings.TrimSpace(line) == "" {
			return clierr.New(clierr.CodeUsage, clierr.KindUsage, "--api-key - got no key on stdin").
				WithHint("pipe the key in, or set SALAD_AI_GATEWAY_API_KEY")
		}
		g.key, g.keyFrom = line, keyFromStdin
	case keyFlag != "":
		fprintln(errw(), "warning: --api-key exposes the key on the command line; pipe it instead")
		g.key, g.keyFrom = keyFlag, keyFromFlag
	case g.terminal:
		if cerr := g.askForKey(); cerr != nil {
			return cerr
		}
		return g.save()
	case os.Getenv(envGatewayKey) != "":
		g.key, g.keyFrom = os.Getenv(envGatewayKey), envGatewayKey
	default:
		return clierr.New(clierr.CodeAuth, clierr.KindAuth, "no AI Gateway key provided").
			WithHint("run saladcloud ai-gateway login in a terminal, or set SALAD_AI_GATEWAY_API_KEY")
	}

	key, ok := normalizeKey(g.key)
	if !ok {
		return malformedKey(labelGatewayKey, key, g.keyFrom)
	}
	g.key = key
	if cerr := g.check(); cerr != nil {
		if rejected(cerr) {
			return g.keyInvalid()
		}
		return g.failed(cerr)
	}
	if g.keyFrom == envGatewayKey {
		return g.reportUnsaved()
	}
	if g.keyFrom == keyFromStdin {
		say("AI Gateway key: %s", maskAPIKey(g.key)) // a piped key's source shows only in its error
	} else {
		say("AI Gateway key: %s%s", maskAPIKey(g.key), fromTag(g.keyFrom))
	}
	return g.save()
}

// canceled is the error for Ctrl-C or end of input at the prompt, or
// Ctrl-C during the check.
func (g *gatewayLogin) canceled() *clierr.Error {
	if g.saved.GatewayAPIKey != "" {
		return clierr.New(
			clierr.CodeGeneric,
			clierr.KindInterrupted,
			"ai-gateway login canceled, your saved AI Gateway key (%s) is unchanged",
			maskAPIKey(g.saved.GatewayAPIKey),
		)
	}
	return clierr.New(clierr.CodeGeneric, clierr.KindInterrupted, "ai-gateway login canceled, nothing changed")
}

// failed is a check that failed for another reason than the key: Ctrl-C
// reads as canceled, a 403 is the key's missing access, anything else says
// nothing was saved.
func (g *gatewayLogin) failed(cerr *clierr.Error) *clierr.Error {
	switch {
	case cerr.IsCancel():
		return g.canceled()
	case cerr.HTTPStatus != nil && *cerr.HTTPStatus == http.StatusForbidden:
		return clierr.New(clierr.CodeAuth, clierr.KindAuth, "AI Gateway key %s%s has no AI Gateway access", maskAPIKey(g.key), fromTag(g.keyFrom)).
			WithStatus(http.StatusForbidden).
			WithHint("check that you are still a member of its organization; if you are, contact support")
	case cerr.Code == clierr.CodeRateLimited:
		return cerr.WithHint("nothing was saved; run saladcloud ai-gateway login again in a minute")
	}
	return cerr.WithHint("nothing was saved; run saladcloud ai-gateway login again")
}

// check runs the key check (the model list, then one billed completion): a
// 402 is a working key whose organization is out of credits, which is
// saved.
func (g *gatewayLogin) check() *clierr.Error {
	gw, cerr := api.NewGateway(config.Resolved{GatewayAPIKey: g.key})
	if cerr != nil {
		return cerr
	}
	models, cerr := gw.CheckKey(g.ctx)
	if models != nil {
		g.models = len(models.Data)
	}
	if cerr != nil && cerr.HTTPStatus != nil && *cerr.HTTPStatus == http.StatusPaymentRequired {
		g.credits = false
		return nil
	}
	return cerr
}

// keyInvalid is the error for a named key the gateway rejected, with the
// fix for where it came from.
func (g *gatewayLogin) keyInvalid() *clierr.Error {
	e := clierr.New(clierr.CodeAuth, clierr.KindAuth, "AI Gateway key %s%s is invalid", maskAPIKey(g.key), fromTag(g.keyFrom)).
		WithStatus(http.StatusUnauthorized)
	switch {
	case quotedKey(g.key):
		return e.WithHint("remove the quotes around it")
	case g.keyFrom != envGatewayKey:
		return e.WithHint("get one at %s", gatewayKeyPage)
	case g.saved.GatewayAPIKey != "" && g.saved.GatewayAPIKey != g.key:
		return e.WithHint(
			"remove SALAD_AI_GATEWAY_API_KEY to use the saved AI Gateway key (%s), or set it to a working key",
			maskAPIKey(g.saved.GatewayAPIKey),
		)
	}
	return e.WithHint("set SALAD_AI_GATEWAY_API_KEY to a working key, or remove it and run saladcloud ai-gateway login")
}

// askForKey is the key prompt at a terminal, until a key the gateway
// accepts, Ctrl-C or end of input; a pasted key is read back before it is
// checked.
func (g *gatewayLogin) askForKey() *clierr.Error {
	if line := beforePrompt("ai-gateway login", stillSupplied(envGatewayKey)); line != "" {
		tell("%s", line)
	}
	if g.saved.GatewayAPIKey != "" {
		tell("Saved AI Gateway key: %s (Ctrl-C to keep it)", maskAPIKey(g.saved.GatewayAPIKey))
	}
	tell("Get your AI Gateway key at %s", gatewayKeyPage)
	for {
		answer, err := ask(keyPrompt(), true)
		if err != nil {
			return g.canceled()
		}
		if answer == "" {
			if browserVisible() {
				openOffered(gatewayKeyPage)
			}
			continue
		}
		key, ok := normalizeKey(answer)
		if !ok {
			tell("%s", malformedKeyRetry(labelGatewayKey))
			continue
		}
		say("AI Gateway key: %s", maskAPIKey(key))
		g.key, g.keyFrom = key, ""
		cerr := g.check()
		if rejected(cerr) {
			if quotedKey(key) {
				tell("%s", quotedPastedGatewayKey)
			} else {
				tell("%s", invalidPastedGatewayKey)
			}
			continue
		}
		if cerr != nil {
			return g.failed(cerr)
		}
		return nil
	}
}

// result is the JSON document for this run.
func (g *gatewayLogin) result(saved bool, path string, ovs []override) gatewayLoginResult {
	source := "config"
	if os.Getenv(envGatewayKey) != "" {
		source = "env"
	}
	return gatewayLoginResult{
		Models:           g.models,
		GatewayKey:       maskAPIKey(g.key),
		KeySource:        source,
		KeySaved:         saved,
		CreditsAvailable: g.credits,
		EnvOverrides:     overrideNames(ovs),
		ConfigPath:       path,
		Action:           "logged_in",
		OK:               true,
	}
}

// warnNoCredits is the warning and hint for a working key whose
// organization is out of credits; it replaces the try-it hint.
func (*gatewayLogin) warnNoCredits() {
	fprintln(
		errw(),
		"warning: the key's organization is out of credits, so AI Gateway requests are rejected until you add some",
	)
	hintLine("add credits from the organization's AI Gateway page: %s", gatewayKeyPage)
}

// save writes the key and reports it.
func (g *gatewayLogin) save() error {
	f := g.saved
	f.GatewayAPIKey = g.key
	path, err := config.Path()
	if err == nil {
		err = config.Save(f)
	}
	if err != nil {
		return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot write the config file %s: %s", path, config.Reason(err)).
			WithHint("%s", unwritableHint("ai-gateway login"))
	}
	ovs := overridesOf(f, envGatewayKey)
	if err := output.Emit(g.result(true, path, ovs), func(w *output.Writer) {
		w.Println("AI Gateway key saved")
		w.Printf("Config at: %s\n", path)
	}); err != nil {
		return wrapErr(err)
	}
	if g.credits {
		hintLine("to try it, run saladcloud ai-gateway test (sends one billed request)")
	}
	warnOverrides(errw(), afterSave, ovs)
	if !g.credits {
		g.warnNoCredits()
	}
	return nil
}

// reportUnsaved is the result for a key from SALAD_AI_GATEWAY_API_KEY with
// no terminal: checked, not saved.
func (g *gatewayLogin) reportUnsaved() error {
	path, _ := config.Path()
	say("No AI Gateway key provided, using SALAD_AI_GATEWAY_API_KEY (%s).", maskAPIKey(g.key))
	err := output.Emit(g.result(false, path, nil), func(w *output.Writer) {
		fprintln(
			w,
			"The AI Gateway key works. It is not saved, so it works only where SALAD_AI_GATEWAY_API_KEY is set.",
		)
	})
	if err != nil {
		return wrapErr(err)
	}
	hintLine("to save it, pipe it to saladcloud ai-gateway login --api-key -")
	if !g.credits {
		g.warnNoCredits()
	}
	return nil
}

func gatewayLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove the saved AI Gateway key",
		Long: `Remove the AI Gateway key from the config file. When SALAD_AI_GATEWAY_API_KEY
is set, commands still use it, and logout says so.`,
		Args: cobra.NoArgs,
		RunE: runGatewayLogout,
	}
}

// gatewayLogoutResult is ai-gateway logout's JSON contract: whether a key
// was removed, the variable that still supplies one, and the action shape.
type gatewayLogoutResult struct {
	Removed      bool     `json:"removed"`
	EnvOverrides []string `json:"env_overrides"`
	Action       string   `json:"action"`
	OK           bool     `json:"ok"`
}

func runGatewayLogout(_ *cobra.Command, _ []string) error {
	path, err := config.Path()
	if err != nil {
		return clierr.New(
			clierr.CodeGeneric,
			clierr.KindAPIError,
			"cannot find a folder for the config file: %s",
			config.Reason(err),
		)
	}
	f, err := config.Load()
	if err != nil {
		return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot read the config file %s: %s", path, readReason(err)).
			WithHint("fix or delete it, then run saladcloud ai-gateway logout again")
	}
	old := f.GatewayAPIKey
	if old != "" {
		f.GatewayAPIKey = ""
		if err := config.Save(f); err != nil {
			return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot write the config file %s: %s", path, config.Reason(err)).
				WithHint("set SALAD_CLI_CONFIG_DIR to a writable folder, then run saladcloud ai-gateway logout again")
		}
	}
	ovs := stillSupplied(envGatewayKey)
	result := gatewayLogoutResult{Removed: old != "", EnvOverrides: overrideNames(ovs), Action: "logged_out", OK: true}
	if err := output.Emit(result, func(w *output.Writer) {
		if old == "" {
			w.Println("No AI Gateway key was saved; nothing to remove")
			return
		}
		w.Printf("Logged out: removed the saved AI Gateway key (%s)\n", maskAPIKey(old))
	}); err != nil {
		return wrapErr(err)
	}
	warnOverrides(errw(), afterRemove, ovs)
	return nil
}
