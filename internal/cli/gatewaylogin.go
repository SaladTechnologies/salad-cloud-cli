package cli

import (
	"context"
	"net/http"
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

	invalidPastedGatewayKey = "That AI Gateway key is invalid. Check that you copied all of it, " +
		"and that it isn't your SaladCloud API key (that one goes in saladcloud login)."
	quotedPastedGatewayKey = "That AI Gateway key is invalid. Remove the quotes around it and paste it again."
)

// logoutCmdName is the name of logout and of ai-gateway logout.
const logoutCmdName = "logout"

func (app *App) gatewayLoginCmd() *cobra.Command {
	var keyFlag string
	cmd := &cobra.Command{
		Use:   loginCmdName,
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
		RunE: func(cmd *cobra.Command, args []string) error { return app.runGatewayLogin(cmd, args, keyFlag) },
	}
	cmd.Flags().StringVar(
		&keyFlag,
		loginFlagAPIKey,
		"",
		"the AI Gateway key; - reads it from the first line of stdin (a key typed here is exposed on the command line)",
	)
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
	saved    config.File
	terminal bool
	key      string
	keyFrom  string // keyFromFlag, keyFromStdin, envGatewayKey, or "" (pasted)
	models   int
	credits  bool // false after a 402
	app      *App // the run
}

func (app *App) runGatewayLogin(cmd *cobra.Command, _ []string, keyFlag string) error {
	g, cerr := app.newGatewayLogin(cmd, keyFlag)
	if cerr != nil {
		return cerr
	}
	ctx := cmd.Context()
	switch {
	case keyFlag == "-":
		if cerr := g.keyFromStdin(); cerr != nil {
			return cerr
		}
	case keyFlag != "":
		fprintln(app.errw(), "warning: --api-key exposes the key on the command line; pipe it instead")
		g.key, g.keyFrom = keyFlag, keyFromFlag
	case g.terminal:
		if cerr := g.askForKey(ctx); cerr != nil {
			return cerr
		}
		return g.save()
	case app.Env.Getenv(envGatewayKey) != "":
		g.key, g.keyFrom = app.Env.Getenv(envGatewayKey), envGatewayKey
	default:
		return clierr.New(clierr.CodeAuth, clierr.KindAuth, "no AI Gateway key provided").
			WithHint("run saladcloud ai-gateway login in a terminal, or set SALAD_AI_GATEWAY_API_KEY")
	}
	return g.checkNamedKey(ctx)
}

// newGatewayLogin starts a run of ai-gateway login: --api-key given empty is refused, and the config file is read
// as it was before login.
func (app *App) newGatewayLogin(cmd *cobra.Command, keyFlag string) (*gatewayLogin, *clierr.Error) {
	if cmd.Flags().Changed("api-key") && keyFlag == "" {
		return nil, clierr.New(clierr.CodeUsage, clierr.KindUsage, "%s is empty", "--api-key").
			WithHint("check that the variable you passed is set")
	}
	if cerr := app.configReady("ai-gateway login"); cerr != nil {
		return nil, cerr
	}
	g := &gatewayLogin{
		terminal: app.atTerminal(),
		credits:  true,
		saved:    config.File{APIKey: "", Organization: "", Project: "", GatewayAPIKey: ""},
		key:      "",
		keyFrom:  "",
		models:   0,
		app:      app,
	}
	g.saved = app.savedConfig() // configReady has read it
	return g, nil
}

// keyFromStdin reads --api-key -'s key: one line, hidden at a terminal.
func (g *gatewayLogin) keyFromStdin() *clierr.Error {
	line, ok := g.app.readKeyLineFromStdin()
	if !ok {
		return g.canceled()
	}
	if strings.TrimSpace(line) == "" {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "--api-key - got no key on stdin").
			WithHint("pipe the key in, or set SALAD_AI_GATEWAY_API_KEY")
	}
	g.key, g.keyFrom = line, keyFromStdin
	return nil
}

// checkNamedKey checks a key from a flag, stdin or SALAD_AI_GATEWAY_API_KEY, then reports one from the variable
// unsaved and saves any other, read back with its source.
func (g *gatewayLogin) checkNamedKey(ctx context.Context) error {
	app := g.app
	key, ok := normalizeKey(g.key)
	if !ok {
		return malformedKey(labelGatewayKey, key, g.keyFrom)
	}
	g.key = key
	if cerr := g.check(ctx); cerr != nil {
		if rejected(cerr) {
			return g.keyInvalid()
		}
		return g.failed(cerr)
	}
	if g.keyFrom == envGatewayKey {
		return g.reportUnsaved()
	}
	if g.keyFrom == keyFromStdin {
		app.say("AI Gateway key: %s", maskAPIKey(g.key)) // a piped key's source shows only in its error
	} else {
		app.say("AI Gateway key: %s%s", maskAPIKey(g.key), fromTag(g.keyFrom))
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
		return clierr.New(
			clierr.CodeAuth,
			clierr.KindAuth,
			"AI Gateway key %s%s has no AI Gateway access",
			maskAPIKey(g.key),
			fromTag(g.keyFrom),
		).WithStatus(http.StatusForbidden).
			WithHint("check that you are still a member of its organization; if you are, contact support")
	case cerr.Code == clierr.CodeRateLimited:
		return cerr.WithHint("nothing was saved; run saladcloud ai-gateway login again in a minute")
	}
	return cerr.WithHint("nothing was saved; run saladcloud ai-gateway login again")
}

// check runs the key check (the model list, then one billed completion): a
// 402 is a working key whose organization is out of credits, which is
// saved.
func (g *gatewayLogin) check(ctx context.Context) *clierr.Error {
	app := g.app
	gw, cerr := api.NewGateway(
		config.Resolved{
			GatewayAPIKey:       g.key,
			APIKey:              "",
			APIKeySource:        "",
			Organization:        "",
			OrganizationSource:  "",
			Project:             "",
			ProjectSource:       "",
			GatewayAPIKeySource: "",
			SkippedProject:      "",
			SkippedProjectOrg:   "",
		},
		app.apiOptions(),
	)
	if cerr != nil {
		return cerr
	}
	models, cerr := gw.CheckKey(ctx)
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
	e := clierr.New(
		clierr.CodeAuth,
		clierr.KindAuth,
		"AI Gateway key %s%s is invalid",
		maskAPIKey(g.key),
		fromTag(g.keyFrom),
	).WithStatus(http.StatusUnauthorized)
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
func (g *gatewayLogin) askForKey(ctx context.Context) *clierr.Error {
	app := g.app
	g.openKeyExchange()
	for {
		answer, err := app.askHidden(app.keyPrompt())
		if err != nil {
			return g.canceled()
		}
		if answer == "" {
			if app.browserVisible() {
				app.openOffered(gatewayKeyPage)
			}
			continue
		}
		key, ok := normalizeKey(answer)
		if !ok {
			app.tell("%s", malformedKeyRetry(labelGatewayKey))
			continue
		}
		app.say("AI Gateway key: %s", maskAPIKey(key))
		g.key, g.keyFrom = key, ""
		cerr := g.check(ctx)
		if rejected(cerr) {
			g.tellPastedKeyInvalid(key)
			continue
		}
		if cerr != nil {
			return g.failed(cerr)
		}
		return nil
	}
}

// openKeyExchange prints the lines ahead of the key prompt: the variable that will still override what is saved,
// the saved key, and where to get one.
func (g *gatewayLogin) openKeyExchange() {
	app := g.app
	if line := app.beforePrompt("ai-gateway login", app.stillSupplied(envGatewayKey)); line != "" {
		app.tell("%s", line)
	}
	if g.saved.GatewayAPIKey != "" {
		app.tell("Saved AI Gateway key: %s (Ctrl-C to keep it)", maskAPIKey(g.saved.GatewayAPIKey))
	}
	app.tell("Get your AI Gateway key at %s", gatewayKeyPage)
}

// tellPastedKeyInvalid tells the person at the terminal that the key they pasted is invalid, and why when it can
// tell.
func (g *gatewayLogin) tellPastedKeyInvalid(key string) {
	if quotedKey(key) {
		g.app.tell("%s", quotedPastedGatewayKey)
	} else {
		g.app.tell("%s", invalidPastedGatewayKey)
	}
}

// result is the JSON document for this run.
func (g *gatewayLogin) result(saved bool, path string, ovs []override) gatewayLoginResult {
	app := g.app
	source := settingFromConfig
	if app.Env.Getenv(envGatewayKey) != "" {
		source = settingFromEnv
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
func (g *gatewayLogin) warnNoCredits() {
	app := g.app
	fprintln(
		app.errw(),
		"warning: the key's organization is out of credits, so AI Gateway requests are rejected until you add some",
	)
	app.hintLine("add credits from the organization's AI Gateway page: %s", gatewayKeyPage)
}

// save writes the key and reports it.
func (g *gatewayLogin) save() error {
	app := g.app
	f := g.saved
	f.GatewayAPIKey = g.key
	path, err := config.Path(app.Env)
	if err == nil {
		err = config.Save(app.Env, f)
	}
	if err != nil {
		return clierr.New(
			clierr.CodeGeneric,
			clierr.KindAPIError,
			"cannot write the config file %s: %s",
			path,
			config.Reason(err),
		).WithHint("%s", unwritableHint("ai-gateway login"))
	}
	ovs := app.overridesOf(f, envGatewayKey)
	err = app.printer.Emit(g.result(true, path, ovs), func(w *output.Writer) {
		w.Println("AI Gateway key saved")
		w.Printf("Config at: %s\n", path)
	})
	if err != nil {
		return wrapErr(err)
	}
	if g.credits {
		app.hintLine("to try it, run saladcloud ai-gateway test (sends one billed request)")
	}
	app.warnOverrides(app.errw(), afterSave, ovs)
	if !g.credits {
		g.warnNoCredits()
	}
	return nil
}

// reportUnsaved is the result for a key from SALAD_AI_GATEWAY_API_KEY with
// no terminal: checked, not saved.
func (g *gatewayLogin) reportUnsaved() error {
	app := g.app
	path := app.configPath()
	app.say("No AI Gateway key provided, using SALAD_AI_GATEWAY_API_KEY (%s).", maskAPIKey(g.key))
	err := app.printer.Emit(g.result(false, path, nil), func(w *output.Writer) {
		fprintln(
			w,
			"The AI Gateway key works. It is not saved, so it works only where SALAD_AI_GATEWAY_API_KEY is set.",
		)
	})
	if err != nil {
		return wrapErr(err)
	}
	app.hintLine("to save it, pipe it to saladcloud ai-gateway login --api-key -")
	if !g.credits {
		g.warnNoCredits()
	}
	return nil
}

func (app *App) gatewayLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   logoutCmdName,
		Short: "Remove the saved AI Gateway key",
		Long: `Remove the AI Gateway key from the config file. When SALAD_AI_GATEWAY_API_KEY
is set, commands still use it, and logout says so.`,
		Args: cobra.NoArgs,
		RunE: app.runGatewayLogout,
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

func (app *App) runGatewayLogout(_ *cobra.Command, _ []string) error {
	path, err := config.Path(app.Env)
	if err != nil {
		return clierr.New(
			clierr.CodeGeneric,
			clierr.KindAPIError,
			"cannot find a folder for the config file: %s",
			config.Reason(err),
		)
	}
	f, err := config.Load(app.Env)
	if err != nil {
		return clierr.New(
			clierr.CodeGeneric,
			clierr.KindAPIError,
			"cannot read the config file %s: %s",
			path,
			readReason(err),
		).WithHint("fix or delete it, then run saladcloud ai-gateway logout again")
	}
	old := f.GatewayAPIKey
	if old != "" {
		f.GatewayAPIKey = ""
		if saveErr := config.Save(app.Env, f); saveErr != nil {
			return clierr.New(
				clierr.CodeGeneric,
				clierr.KindAPIError,
				"cannot write the config file %s: %s",
				path,
				config.Reason(saveErr),
			).WithHint("set SALAD_CLI_CONFIG_DIR to a writable folder, then run saladcloud ai-gateway logout again")
		}
	}
	ovs := app.stillSupplied(envGatewayKey)
	result := gatewayLogoutResult{Removed: old != "", EnvOverrides: overrideNames(ovs), Action: "logged_out", OK: true}
	err = app.printer.Emit(result, func(w *output.Writer) {
		if old == "" {
			w.Println("No AI Gateway key was saved; nothing to remove")
			return
		}
		w.Printf("Logged out: removed the saved AI Gateway key (%s)\n", maskAPIKey(old))
	})
	if err != nil {
		return wrapErr(err)
	}
	app.warnOverrides(app.errw(), afterRemove, ovs)
	return nil
}
