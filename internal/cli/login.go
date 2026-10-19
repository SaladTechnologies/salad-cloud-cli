package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

const (
	apiKeyPortalURL   = "https://portal.salad.com/api-key" //nolint:gosec // the portal page that issues keys, not a key
	organizationsURL  = "https://portal.salad.com/organizations"
	envAPIKey         = "SALAD_API_KEY" //nolint:gosec // the variable's name, not a key
	envOrganization   = "SALAD_ORGANIZATION"
	envProject        = "SALAD_PROJECT"
	keyFromStdin      = "stdin"
	keyFromFlag       = "--api-key"
	nothingSavedHint  = "nothing was saved; run saladcloud login again"
	nothingSavedLater = "nothing was saved; run saladcloud login again in a minute"
	invalidPastedKey  = "That API key is invalid. Check that you copied all of it, " +
		"and that it isn't an AI Gateway key (those go in saladcloud ai-gateway login)."
	quotedPastedKey = "That API key is invalid. Remove the quotes around it and paste it again."
)

// loginCmdName is the login command's name, as its hints and the config file checks spell it.
const loginCmdName = "login"

// login's flag names, as cobra spells them.
const (
	loginFlagAPIKey  = "api-key"
	loginFlagOrg     = "org"
	loginFlagProject = "project"
)

// orgMenuDisplayGap is the room between the longest organization name and the display names in the numbered list.
const orgMenuDisplayGap = 3

// addLoginCommands adds login.go's commands to the tree.
func (app *App) addLoginCommands(root *cobra.Command) {
	var keyFlag string
	cmd := &cobra.Command{
		Use:   loginCmdName,
		Short: "Save your API key, organization and project",
		Long: `Save your API key, organization and project to the config file, each checked
against the API first. Nothing is saved until all three are settled.

At a terminal, login asks for the key with the input hidden (Enter at the prompt
opens ` + apiKeyPortalURL + ` in your browser), then the organization and
the project, offering what is saved. Without a terminal it never prompts: the
key comes from --api-key - (the first line of stdin), --api-key, or
SALAD_API_KEY, which is used but not saved, and the organization from --org,
SALAD_ORGANIZATION or the config file.

The project is --project, SALAD_PROJECT, the one saved for that organization,
or "default". Container groups and queues need one; inference, transcription
and the AI Gateway do not, so a missing project never fails login.
Organizations and projects are named as in the portal URL
(https://portal.salad.com/organizations/<org>/projects/<project>), not by the
display names the portal shows.

SALAD_CLI_NO_PROMPT, or CI set to anything but empty, false or 0, makes login
behave as if there is no terminal. Scripts and CI can skip login and set
SALAD_API_KEY, SALAD_ORGANIZATION and SALAD_PROJECT instead.`,
		Example: `  # At a terminal
  saladcloud login

  # Scripted, bash or zsh (only the first line of stdin is read)
  printf '%s' "$KEY" | saladcloud login --api-key - --org my-org

  # Scripted, PowerShell
  $env:KEY | saladcloud login --api-key - --org my-org`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return app.runLogin(cmd, args, keyFlag) },
	}
	cmd.Flags().StringVar(
		&keyFlag,
		loginFlagAPIKey,
		"",
		"the API key; - reads it from the first line of stdin (a key typed here is exposed on the command line)",
	)
	root.AddCommand(cmd)
}

// loginResult is login's JSON contract: what was saved and where commands
// read the key from now, the variables that override it, and the action
// shape every state-changing command carries.
type loginResult struct {
	Organization string  `json:"organization"`
	Project      *string `json:"project"`
	// SavedProjectFound is whether the project saved for this organization
	// was found (false: login fell back); null when none was saved or a
	// flag or variable named the project.
	SavedProjectFound *bool  `json:"saved_project_found"`
	APIKey            string `json:"api_key"`
	APIKeySource      string `json:"api_key_source"`
	APIKeySaved       bool   `json:"api_key_saved"`
	// SavedAPIKey is the saved key, masked, that commands use where
	// SALAD_API_KEY is not set; null when none is saved.
	SavedAPIKey  *string  `json:"saved_api_key"`
	EnvOverrides []string `json:"env_overrides"`
	ConfigPath   string   `json:"config_path"`
	Action       string   `json:"action"`
	OK           bool     `json:"ok"`
}

// login is one run of the command.
type login struct {
	saved    config.File // the config file before login
	terminal bool

	key     string // the API key being logged in with
	keyFrom string // keyFromFlag, keyFromStdin, envAPIKey, or "" (pasted)

	// orgs is the key's organizations, from the listing; nil when the
	// listing could not list (the organization check decides alone).
	orgs []api.Organization

	orgName, orgFrom         string // --org or SALAD_ORGANIZATION, with its source
	projectName, projectFrom string // --project or SALAD_PROJECT, with its source

	// savedProjectFound is whether step 5 found the project saved for the
	// organization; nil when it did not look for one.
	savedProjectFound *bool
	app               *App // the run
}

// errKeyRejected is a pasted key the organization check rejected after an
// unavailable listing let it through: login asks for the key again.
var errKeyRejected = clierr.New(clierr.CodeAuth, clierr.KindAuth, "the API key was rejected")

func (app *App) runLogin(cmd *cobra.Command, _ []string, keyFlag string) error {
	orgFlag, projectFlag := app.flags.org, app.flags.project
	if cerr := loginCheckFlags(cmd, keyFlag, orgFlag, projectFlag); cerr != nil {
		return cerr
	}
	l, cerr := app.newLogin(orgFlag, projectFlag)
	if cerr != nil {
		return cerr
	}
	ctx := cmd.Context()
	if cerr := l.obtainKey(ctx, keyFlag); cerr != nil {
		return cerr
	}
	return l.pickAndSave(ctx)
}

// loginCheckFlags rejects a login flag given empty, and an --org that names a project too, before anything else
// is read.
func loginCheckFlags(cmd *cobra.Command, keyFlag, orgFlag, projectFlag string) *clierr.Error {
	flags := []struct{ name, value string }{
		{loginFlagAPIKey, keyFlag}, {loginFlagOrg, orgFlag}, {loginFlagProject, projectFlag},
	}
	for _, f := range flags {
		if cmd.Flags().Changed(f.name) && f.value == "" {
			return clierr.New(clierr.CodeUsage, clierr.KindUsage, "%s is empty", "--"+f.name).
				WithHint("check that the variable you passed is set")
		}
	}
	if org, project, ok := strings.Cut(orgFlag, "/"); ok {
		if org == "" {
			org = "my-org"
		}
		if project == "" {
			project = config.DefaultProject
		}
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "--org takes an organization only").
			WithHint("pass the project separately, for example saladcloud login --org %s --project %s", org, project)
	}
	return nil
}

// newLogin starts a run of login: the organization and project named by flag or variable, each checked, and the
// config file as it was before login.
func (app *App) newLogin(orgFlag, projectFlag string) (*login, *clierr.Error) {
	l := &login{
		terminal:          app.atTerminal(),
		saved:             config.File{APIKey: "", Organization: "", Project: "", GatewayAPIKey: ""},
		key:               "",
		keyFrom:           "",
		orgs:              nil,
		orgName:           "",
		orgFrom:           "",
		projectName:       "",
		projectFrom:       "",
		savedProjectFound: nil,
		app:               app,
	}
	org := app.namedValue(orgFlag, "--org", envOrganization)
	project := app.namedValue(projectFlag, projectFlagSpelling, envProject)
	l.orgName, l.orgFrom = org.value, org.from
	l.projectName, l.projectFrom = project.value, project.from
	names := []struct{ kind, name, from string }{
		{nameKindOrganization, l.orgName, l.orgFrom}, {nameKindProject, l.projectName, l.projectFrom},
	}
	for _, n := range names {
		if n.name != "" {
			if cerr := app.checkName(n.kind, n.name, n.from); cerr != nil {
				return nil, cerr
			}
		}
	}
	if cerr := app.configReady(loginCmdName); cerr != nil {
		return nil, cerr
	}
	l.saved = app.savedConfig() // configReady has read it
	return l, nil
}

// obtainKey is step 1: where the key comes from, then step 2 or 3 for it (the prompt, or the check of a named key).
func (l *login) obtainKey(ctx context.Context, keyFlag string) *clierr.Error {
	app := l.app
	switch {
	case keyFlag == "-":
		if cerr := l.keyFromStdin(); cerr != nil {
			return cerr
		}
	case keyFlag != "":
		fprintln(app.errw(), "warning: --api-key exposes the key on the command line; pipe it instead")
		l.key, l.keyFrom = keyFlag, keyFromFlag
	case l.terminal:
		l.openKeyExchange()
		if cerr := l.askForKey(ctx); cerr != nil {
			return cerr
		}
	case app.Env.Getenv(envAPIKey) != "":
		l.key, l.keyFrom = app.Env.Getenv(envAPIKey), envAPIKey
	case l.saved.APIKey != "":
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "no API key provided, and a key is already saved").
			WithHint("to switch organization or project, run saladcloud use <org>/<project>")
	default:
		return clierr.New(clierr.CodeAuth, clierr.KindAuth, "no API key provided").
			WithHint("run saladcloud login in a terminal, or set SALAD_API_KEY")
	}
	if l.keyFrom != "" {
		return l.checkNamedKey(ctx)
	}
	return nil
}

// pickAndSave is steps 4 to 6: the organization, the project and the save, asking for the key again whenever the
// organization check rejects a pasted one.
func (l *login) pickAndSave(ctx context.Context) error {
	for {
		org, cerr := l.pickOrganization(ctx)
		if cerr == errKeyRejected {
			l.tellPastedKeyInvalid(l.key)
			if askErr := l.askForKey(ctx); askErr != nil {
				return askErr
			}
			continue
		}
		if cerr != nil {
			return cerr
		}
		project, cerr := l.pickProject(ctx, org)
		if cerr != nil {
			return cerr
		}
		return l.save(org, project)
	}
}

// tellPastedKeyInvalid tells the person at the terminal that the key they pasted is invalid, and why when it can
// tell.
func (l *login) tellPastedKeyInvalid(key string) {
	if quotedKey(key) {
		l.app.tell("%s", quotedPastedKey)
	} else {
		l.app.tell("%s", invalidPastedKey)
	}
}

// loginNamed is a value the user named for login, and where it came from (the flag's spelling or the variable);
// both are "" when none was named.
type loginNamed struct{ value, from string }

// namedValue is a value the user named for login: the flag, else the
// variable, with where it came from.
func (app *App) namedValue(flag, flagName, env string) loginNamed {
	if flag = strings.TrimSpace(flag); flag != "" {
		return loginNamed{value: flag, from: flagName}
	}
	if v := strings.TrimSpace(app.Env.Getenv(env)); v != "" {
		return loginNamed{value: v, from: env}
	}
	return loginNamed{value: "", from: ""}
}

// keyFromStdin reads --api-key -'s key: one line, hidden at a terminal.
func (l *login) keyFromStdin() *clierr.Error {
	line, ok := l.app.readKeyLineFromStdin()
	if !ok {
		return l.canceled()
	}
	if strings.TrimSpace(line) == "" {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "--api-key - got no key on stdin").
			WithHint("pipe the key in, or set SALAD_API_KEY")
	}
	l.key, l.keyFrom = line, keyFromStdin
	return nil
}

// readKeyLineFromStdin reads the one line --api-key - takes, hidden whenever stdin is a terminal (prompts or not);
// ok is false when the hidden read was canceled. A failed read off a terminal is an empty line, which the caller
// reports as no key.
func (app *App) readKeyLineFromStdin() (string, bool) {
	if app.Console.Terminal() {
		answer, err := app.Console.ReadHidden()
		if err != nil {
			return "", false
		}
		return answer, true
	}
	line, err := readLine(bufio.NewReader(app.Stdin))
	if err != nil {
		line = "" // a failed read is no key; the caller's check says so
	}
	return line, true
}

// canceled is the error for Ctrl-C or end of input at a prompt, or Ctrl-C
// during a request: it says what did not change.
func (l *login) canceled() *clierr.Error {
	if l.saved.APIKey != "" {
		return clierr.New(
			clierr.CodeGeneric,
			clierr.KindInterrupted,
			"login canceled, your saved API key (%s) is unchanged",
			maskAPIKey(l.saved.APIKey),
		)
	}
	return clierr.New(clierr.CodeGeneric, clierr.KindInterrupted, "login canceled, nothing changed")
}

// failed is an API failure after the key step: Ctrl-C reads as canceled;
// any other error says nothing was saved, unless it carries its own fix.
func (l *login) failed(cerr *clierr.Error) *clierr.Error {
	switch {
	case cerr.IsCancel():
		return l.canceled()
	case cerr.Hint != nil:
		return cerr
	case cerr.Code == clierr.CodeRateLimited:
		return cerr.WithHint(nothingSavedLater)
	}
	return cerr.WithHint(nothingSavedHint)
}

// rejected reports whether the API rejected the key itself (401).
func rejected(cerr *clierr.Error) bool {
	return cerr != nil && cerr.HTTPStatus != nil && *cerr.HTTPStatus == http.StatusUnauthorized
}

// askForKey is step 2: the key prompt at a terminal, until a key the API
// accepts, Ctrl-C or end of input. A pasted key is read back before it is
// checked. The first time, openKeyExchange prints the lines ahead of it.
func (l *login) askForKey(ctx context.Context) *clierr.Error {
	app := l.app
	for {
		answer, err := app.askHidden(app.keyPrompt())
		if err != nil {
			return l.canceled()
		}
		if answer == "" {
			if app.browserVisible() {
				app.openOffered(apiKeyPortalURL)
			}
			continue
		}
		key, ok := normalizeKey(answer)
		if !ok {
			app.tell("%s", malformedKeyRetry(labelAPIKey))
			continue
		}
		app.say("API key: %s", maskAPIKey(key))
		orgs, cerr := app.listOrganizations(ctx, key)
		if rejected(cerr) {
			l.tellPastedKeyInvalid(key)
			continue
		}
		if cerr != nil {
			return l.failed(cerr)
		}
		l.key, l.keyFrom, l.orgs = key, "", orgs
		return nil
	}
}

// openKeyExchange prints the lines ahead of the first key prompt: the variables that will still override what login
// saves, the saved key, and where to get one.
func (l *login) openKeyExchange() {
	app := l.app
	ovs := app.stillSupplied(envAPIKey)
	if v := app.Env.Getenv(envOrganization); v != "" && l.orgFrom == "--org" && l.orgName != v {
		ovs = append(ovs, app.stillSupplied(envOrganization)...)
	}
	if v := app.Env.Getenv(envProject); v != "" && l.projectFrom == projectFlagSpelling && l.projectName != v {
		ovs = append(ovs, app.stillSupplied(envProject)...)
	}
	if line := app.beforePrompt(loginCmdName, ovs); line != "" {
		app.tell("%s", line)
	}
	if l.saved.APIKey != "" {
		app.tell("Saved API key: %s (Ctrl-C to keep it)", maskAPIKey(l.saved.APIKey))
		app.tell("hint: to switch organization or project instead, run saladcloud use <org>/<project>")
	}
	app.tell("Get your API key at %s", apiKeyPortalURL)
}

// checkNamedKey is step 3 for a key from a flag, stdin or SALAD_API_KEY:
// checked first, then read back with its source, or the error instead.
func (l *login) checkNamedKey(ctx context.Context) *clierr.Error {
	app := l.app
	key, ok := normalizeKey(l.key)
	if !ok {
		return malformedKey(labelAPIKey, key, l.keyFrom)
	}
	l.key = key
	orgs, cerr := app.listOrganizations(ctx, key)
	if rejected(cerr) {
		return l.keyInvalid()
	}
	if cerr != nil {
		return l.failed(cerr)
	}
	l.orgs = orgs
	switch l.keyFrom {
	case envAPIKey:
		app.say("No API key provided, using SALAD_API_KEY (%s).", maskAPIKey(key))
	case keyFromStdin:
		app.say("API key: %s", maskAPIKey(key))
	default:
		app.say("API key: %s%s", maskAPIKey(key), fromTag(l.keyFrom))
	}
	return nil
}

// keyInvalid is the error for a named key the API rejected, with the fix
// for where it came from.
func (l *login) keyInvalid() *clierr.Error {
	e := clierr.New(clierr.CodeAuth, clierr.KindAuth, "API key %s%s is invalid", maskAPIKey(l.key), fromTag(l.keyFrom)).
		WithStatus(http.StatusUnauthorized)
	switch {
	case quotedKey(l.key):
		return e.WithHint("remove the quotes around it")
	case l.keyFrom != envAPIKey:
		return e.WithHint("check that you copied all of it, and that it isn't an AI Gateway key")
	case l.saved.APIKey != "" && l.saved.APIKey != l.key:
		return e.WithHint(
			"remove SALAD_API_KEY to use the saved API key (%s), or set it to a working key",
			maskAPIKey(l.saved.APIKey),
		)
	}
	return e.WithHint("set SALAD_API_KEY to a working key, or remove it and run saladcloud login")
}

// listOrganizations reads the key's organizations: nil when the listing
// cannot list; a 401 means the key is invalid.
func (app *App) listOrganizations(ctx context.Context, key string) ([]api.Organization, *clierr.Error) {
	c, cerr := api.New(
		config.Resolved{
			APIKey:              key,
			APIKeySource:        "",
			Organization:        "",
			OrganizationSource:  "",
			Project:             "",
			ProjectSource:       "",
			GatewayAPIKey:       "",
			GatewayAPIKeySource: "",
			SkippedProject:      "",
			SkippedProjectOrg:   "",
		},
		app.apiOptions(),
	)
	if cerr != nil {
		return nil, cerr
	}
	list, cerr := c.OrganizationListing(ctx)
	if cerr != nil || list == nil {
		return nil, cerr
	}
	return list.Items, nil
}

// orgNames is the organizations' names, nil when the listing was
// unavailable.
func (l *login) orgNames() []string {
	if l.orgs == nil {
		return nil
	}
	names := make([]string, 0, len(l.orgs))
	for _, o := range l.orgs {
		names = append(names, ptr.Deref(o.OrganizationName))
	}
	return names
}

// checkOrganization is the organization check, which decides found or not
// found: (true, nil) found, (false, nil) not found.
func (l *login) checkOrganization(ctx context.Context, org string) (bool, *clierr.Error) {
	app := l.app
	c, cerr := api.New(
		config.Resolved{
			APIKey:              l.key,
			Organization:        org,
			APIKeySource:        "",
			OrganizationSource:  "",
			Project:             "",
			ProjectSource:       "",
			GatewayAPIKey:       "",
			GatewayAPIKeySource: "",
			SkippedProject:      "",
			SkippedProjectOrg:   "",
		},
		app.apiOptions(),
	)
	if cerr != nil {
		return false, cerr
	}
	cerr = c.CheckOrg(ctx)
	switch {
	case cerr == nil:
		return true, nil
	case cerr.Code == clierr.CodeNotFound:
		return false, nil
	case rejected(cerr) && l.keyFrom == "":
		return false, errKeyRejected
	case rejected(cerr):
		return false, l.keyInvalid()
	}
	return false, l.failed(cerr)
}

// pickOrganization is step 4: --org, SALAD_ORGANIZATION, the saved one,
// then the key's organizations.
func (l *login) pickOrganization(ctx context.Context) (string, *clierr.Error) {
	if l.orgs != nil && len(l.orgs) == 0 {
		return "", clierr.New(clierr.CodeNotFound, clierr.KindNotFound, "you have no organizations").
			WithHint("create one, or accept an invitation, at %s", organizationsURL)
	}
	if l.orgName != "" {
		return l.namedOrganization(ctx)
	}
	def := ""
	if saved := l.saved.Organization; saved != "" {
		org, offer, cerr := l.savedOrganization(ctx, saved)
		switch {
		case cerr != nil:
			return "", cerr
		case offer:
			def = org
		case org != "":
			return org, nil
		}
	}
	return l.chooseOrganization(ctx, def)
}

// namedOrganization is step 4 for the organization from --org or SALAD_ORGANIZATION: used when the organization
// check finds it, an error when it does not.
func (l *login) namedOrganization(ctx context.Context) (string, *clierr.Error) {
	app := l.app
	found, cerr := l.checkOrganization(ctx, l.orgName)
	if cerr != nil {
		return "", cerr
	}
	if !found {
		e := clierr.New(
			clierr.CodeNotFound,
			clierr.KindNotFound,
			`organization "%s"%s not found`,
			l.orgName,
			fromTag(l.orgFrom),
		).WithStatus(http.StatusNotFound)
		if h := app.orgsClause(l.orgNames()); h != "" {
			e = e.WithHint("%s", h)
		}
		return "", e
	}
	app.say("Organization: %s%s", l.orgName, fromTag(l.orgFrom))
	return l.orgName, nil
}

// checkableSavedName reports whether a saved organization or project name is worth an API check: one that breaks
// the name pattern or looks like an API key is never sent.
func (l *login) checkableSavedName(name string) bool {
	return validName(name) && !l.app.looksLikeKey(name)
}

// savedOrganization is step 4 for the saved organization: it returns the saved one when it was found ("" when not),
// and whether it is only to be offered as the pick's default rather than used as it is.
func (l *login) savedOrganization(ctx context.Context, saved string) (string, bool, *clierr.Error) {
	found := false
	if l.checkableSavedName(saved) {
		var cerr *clierr.Error
		if found, cerr = l.checkOrganization(ctx, saved); cerr != nil {
			return "", false, cerr
		}
	}
	switch {
	case found && (!l.terminal || len(l.orgs) == 1):
		l.app.say("Organization: %s", saved)
		return saved, false, nil
	case found:
		return saved, true, nil
	}
	return "", false, l.savedOrganizationMissing(saved)
}

// savedOrganizationMissing reports a saved organization that was not found or looks like an API key: the error
// without a terminal, a line before the pick at one (nil then).
func (l *login) savedOrganizationMissing(saved string) *clierr.Error {
	app := l.app
	switch {
	case !l.terminal && app.looksLikeKey(saved):
		return keyLikeName(
			nameKindOrganization,
			settingFromConfig,
		).WithHint("pass --org or set SALAD_ORGANIZATION; to list yours, run saladcloud orgs list")
	case !l.terminal:
		e := clierr.New(clierr.CodeNotFound, clierr.KindNotFound, `saved organization "%s" not found`, saved).
			WithHint("%s", hintClauses("pass --org or set SALAD_ORGANIZATION", app.orgsClause(nil)))
		if validName(saved) { // the organization check answered 404
			e = e.WithStatus(http.StatusNotFound)
		}
		return e
	case app.looksLikeKey(saved):
		app.tell("The saved organization looks like an API key, not an organization name.")
	default:
		app.tell(`Saved organization "%s" not found.`, saved)
	}
	return nil
}

// chooseOrganization is step 4 when nothing named or saved settled it: the only organization, else the pick at a
// terminal, with def offered; an error without one.
func (l *login) chooseOrganization(ctx context.Context, def string) (string, *clierr.Error) {
	app := l.app
	if !l.terminal {
		return "", clierr.New(clierr.CodeUsage, clierr.KindUsage, "no organization set").
			WithHint("%s", hintClauses("pass --org or set SALAD_ORGANIZATION", app.orgsClause(nil)))
	}
	if l.orgs == nil {
		return l.typeOrganization(ctx, def)
	}
	if len(l.orgs) == 1 {
		org := ptr.Deref(l.orgs[0].OrganizationName)
		app.say("Organization: %s", org)
		return org, nil
	}
	return l.selectOrganization(ctx, def)
}

// selectOrganization shows the numbered list and asks for a number or a
// name (4d, 4h, 4i).
func (l *login) selectOrganization(ctx context.Context, def string) (string, *clierr.Error) {
	app := l.app
	for _, line := range orgMenu(l.orgs) {
		app.tell("%s", line)
	}
	choices := fmt.Sprintf("pick 1-%d or type a name", len(l.orgs))
	var chosen string
	_, err := app.askUntil(
		"Select an organization by number or name"+bracketed(def)+":",
		func(answer string) (bool, string, *clierr.Error) {
			switch {
			case answer == "" && def != "":
				chosen = def
				return true, "", nil
			case answer == "":
				return false, "", nil
			}
			name, ok := l.orgMenuAnswer(answer, choices)
			if !ok {
				return false, name, nil // the line that asks again
			}
			found, cerr := l.checkOrganization(ctx, name)
			if cerr != nil {
				return false, "", cerr
			}
			if !found {
				//nolint:gocritic // shows the name as typed; %q would escape it
				return false, fmt.Sprintf(`"%s" not found; %s.`, name, choices), nil
			}
			chosen = name
			return true, "", nil
		},
	)
	if err != nil {
		return "", l.endedAt(err)
	}
	app.say("Organization: %s", chosen)
	return chosen, nil
}

// orgMenuAnswer resolves an answer to the numbered list, a number or a name, to the organization it names and true;
// when it names none, it returns the line that asks again and false.
func (l *login) orgMenuAnswer(answer, choices string) (string, bool) {
	if n, err := strconv.Atoi(answer); err == nil {
		if n < 1 || n > len(l.orgs) {
			//nolint:gocritic // shows the answer as typed; %q would escape it
			return fmt.Sprintf(`"%s" isn't on the list; %s.`, answer, choices), false
		}
		return ptr.Deref(l.orgs[n-1].OrganizationName), true
	}
	if !validName(answer) {
		return nameRetry(nameKindOrganization, answer, l.key), false
	}
	return answer, true
}

// typeOrganization asks for a name when there is no list to pick from (4j).
func (l *login) typeOrganization(ctx context.Context, def string) (string, *clierr.Error) {
	app := l.app
	var chosen string
	_, err := app.askUntil(
		"Type an organization name"+bracketed(def)+":",
		func(answer string) (bool, string, *clierr.Error) {
			switch {
			case answer == "" && def != "":
				chosen = def
				return true, "", nil
			case answer == "":
				return false, "", nil
			case !validName(answer):
				return false, nameRetry(nameKindOrganization, answer, l.key), nil
			}
			found, cerr := l.checkOrganization(ctx, answer)
			if cerr != nil {
				return false, "", cerr
			}
			if !found {
				return false, fmt.Sprintf(
					`Organization "%s" not found. Visit %s to see your organizations, `+
						`create one or accept an invitation.`,
					answer,
					organizationsURL,
				), nil
			}
			chosen = answer
			return true, "", nil
		},
	)
	if err != nil {
		return "", l.endedAt(err)
	}
	app.say("Organization: %s", chosen)
	return chosen, nil
}

// endedAt turns what ended a prompt into login's error: an error from a
// check is that error; Ctrl-C or end of input is the cancel.
func (l *login) endedAt(err error) *clierr.Error {
	var cerr *clierr.Error
	if errors.As(err, &cerr) {
		return cerr
	}
	return l.canceled()
}

// orgMenu is the numbered list of organizations, with the display name
// where it differs from the URL name.
func orgMenu(orgs []api.Organization) []string {
	width := 0
	for _, o := range orgs {
		width = max(width, len(ptr.Deref(o.OrganizationName)))
	}
	digits := len(strconv.Itoa(len(orgs)))
	lines := make([]string, len(orgs))
	for i, o := range orgs {
		name, display := ptr.Deref(o.OrganizationName), ptr.Deref(o.DisplayName)
		if display == "" || display == name {
			lines[i] = fmt.Sprintf("  %*d  %s", digits, i+1, name)
			continue
		}
		lines[i] = fmt.Sprintf("  %*d  %-*s(%s)", digits, i+1, width+orgMenuDisplayGap, name, display)
	}
	return lines
}

// bracketed is a prompt's default, " [x]", or "" when there is none.
func bracketed(def string) string {
	if def == "" {
		return ""
	}
	return " [" + def + "]"
}

// projectCreateURL is the portal page that creates a project in org.
func projectCreateURL(org string) string {
	return "https://portal.salad.com/organizations/" + org + "/projects/new"
}

// checkProject is the project check: (true, nil) found, (false, nil) not
// found.
func (l *login) checkProject(ctx context.Context, org, project string) (bool, *clierr.Error) {
	app := l.app
	c, cerr := api.New(
		config.Resolved{
			APIKey:              l.key,
			Organization:        org,
			Project:             project,
			APIKeySource:        "",
			OrganizationSource:  "",
			ProjectSource:       "",
			GatewayAPIKey:       "",
			GatewayAPIKeySource: "",
			SkippedProject:      "",
			SkippedProjectOrg:   "",
		},
		app.apiOptions(),
	)
	if cerr != nil {
		return false, cerr
	}
	cerr = c.CheckProject(ctx)
	switch {
	case cerr == nil:
		return true, nil
	case cerr.Code == clierr.CodeNotFound:
		return false, nil
	}
	return false, l.failed(cerr)
}

// pickProject is step 5: --project, SALAD_PROJECT, the one saved for the
// same organization, then "default". "" is no project, which never fails
// login.
func (l *login) pickProject(ctx context.Context, org string) (string, *clierr.Error) {
	app := l.app
	if l.projectName != "" {
		return l.namedProject(ctx, org)
	}
	if saved := l.saved.Project; saved != "" && l.saved.Organization == org {
		found, cerr := l.lookUpSavedProject(ctx, org, saved)
		if cerr != nil {
			return "", cerr
		}
		if found {
			return l.offerProject(ctx, org, saved)
		}
	}
	found, cerr := l.checkProject(ctx, org, config.DefaultProject)
	if cerr != nil {
		return "", cerr
	}
	if found {
		return l.offerProject(ctx, org, config.DefaultProject)
	}
	if !l.terminal {
		return "", nil
	}
	app.tell(`Project "default" not found in %s. Create one later if needed.`, org)
	return l.typeProject(ctx, org, "Type a project name, or press Enter to skip:", "")
}

// namedProject is step 5 for the project from --project or SALAD_PROJECT: used when the project check finds it in
// org, an error when it does not.
func (l *login) namedProject(ctx context.Context, org string) (string, *clierr.Error) {
	found, cerr := l.checkProject(ctx, org, l.projectName)
	if cerr != nil {
		return "", cerr
	}
	if !found {
		return "", clierr.New(
			clierr.CodeNotFound,
			clierr.KindNotFound,
			`project "%s"%s not found in %s`,
			l.projectName,
			fromTag(l.projectFrom),
			org,
		).WithStatus(http.StatusNotFound).
			WithHint("create one at %s", projectCreateURL(org))
	}
	l.app.say("Project: %s%s", l.projectName, fromTag(l.projectFrom))
	return l.projectName, nil
}

// lookUpSavedProject checks the project saved for org and records the answer for the result; one that was not
// found, or looks like an API key, gets a line saying so before login falls back.
func (l *login) lookUpSavedProject(ctx context.Context, org, saved string) (bool, *clierr.Error) {
	app := l.app
	found := false
	if l.checkableSavedName(saved) {
		var cerr *clierr.Error
		if found, cerr = l.checkProject(ctx, org, saved); cerr != nil {
			return false, cerr
		}
	}
	l.savedProjectFound = &found
	if found {
		return true, nil
	}
	//nolint:gocritic // shows the saved name as written; %q would escape it
	line := fmt.Sprintf(`Saved project "%s" not found in %s. Create one at %s.`, saved, org, projectCreateURL(org))
	if app.looksLikeKey(saved) {
		line = "The saved project looks like an API key, not a project name."
	}
	if l.terminal {
		app.tell("%s", line)
	} else {
		app.say("%s", line)
	}
	return false, nil
}

// offerProject uses a project that exists: offered in brackets at a
// terminal, used as it is without one.
func (l *login) offerProject(ctx context.Context, org, project string) (string, *clierr.Error) {
	app := l.app
	if !l.terminal {
		app.say("Project: %s", project)
		return project, nil
	}
	return l.typeProject(ctx, org, "Type a project name"+bracketed(project)+":", project)
}

// typeProject asks for a project until one that exists, Enter (the default,
// or no project when there is none), Ctrl-C or end of input.
func (l *login) typeProject(ctx context.Context, org, prompt, def string) (string, *clierr.Error) {
	app := l.app
	chosen := ""
	_, err := app.askUntil(prompt, func(answer string) (bool, string, *clierr.Error) {
		if answer == "" {
			chosen = def
			return true, "", nil
		}
		if !validName(answer) {
			return false, nameRetry(nameKindProject, answer, l.key), nil
		}
		found, cerr := l.checkProject(ctx, org, answer)
		if cerr != nil {
			return false, "", cerr
		}
		if !found {
			return false, fmt.Sprintf( //nolint:gocritic // shows the answer as typed; %q would escape it
				`Project "%s" not found in %s. Create one at %s.`,
				answer,
				org,
				projectCreateURL(org),
			), nil
		}
		chosen = answer
		return true, "", nil
	})
	if err != nil {
		return "", l.endedAt(err)
	}
	if chosen != "" {
		app.say("Project: %s", chosen)
	}
	return chosen, nil
}

// save is step 6: the key (unless it came from SALAD_API_KEY),
// organization and project together, a project saved for another
// organization cleared; then the summary, and what overrides it.
func (l *login) save(org, project string) error {
	app := l.app
	f := l.saved
	keySaved := l.keyFrom != envAPIKey
	if keySaved {
		f.APIKey = l.key
	}
	f.Organization, f.Project = org, project
	path, cerr := app.saveContextConfig(f, loginCmdName)
	if cerr != nil {
		return cerr
	}
	vars := []string{envOrganization, envProject}
	if keySaved {
		vars = append([]string{envAPIKey}, vars...)
	}
	ovs := app.overridesOf(f, vars...)
	source := settingFromConfig
	if app.Env.Getenv(envAPIKey) != "" {
		source = settingFromEnv
	}
	savedKey := f.APIKey // what commands use where SALAD_API_KEY is not set
	result := loginResult{
		Organization:      org,
		Project:           strOrNil(project),
		SavedProjectFound: l.savedProjectFound,
		APIKey:            maskAPIKey(l.key),
		APIKeySource:      source,
		APIKeySaved:       keySaved,
		SavedAPIKey:       maskedOrNil(savedKey),
		EnvOverrides:      overrideNames(ovs),
		ConfigPath:        path,
		Action:            "logged_in",
		OK:                true,
	}
	err := app.printer.Emit(result, func(w *output.Writer) {
		if project == "" {
			w.Printf("Logged in: %s, no project set\n", org)
		} else {
			w.Printf("Logged in: %s/%s\n", org, project)
		}
		w.Printf("Config at: %s\n", path)
	})
	if err != nil {
		return wrapErr(err)
	}
	if !keySaved {
		l.sayKeyNotSaved()
	}
	app.warnOverrides(app.errw(), afterSave, ovs)
	if l.terminal && l.saved.APIKey == "" {
		app.say("Next, check that everything works:")
		app.say("  saladcloud doctor")
	}
	return nil
}

// saveContextConfig writes the config file for login or use (command) and returns its path, or the error that says
// it could not.
func (app *App) saveContextConfig(f config.File, command string) (string, *clierr.Error) {
	path, err := config.Path(app.Env)
	if err == nil {
		err = config.Save(app.Env, f)
	}
	if err != nil {
		return path, clierr.New(
			clierr.CodeGeneric,
			clierr.KindAPIError,
			"cannot write the config file %s: %s",
			path,
			config.Reason(err),
		).WithHint("%s", unwritableHint(command))
	}
	return path, nil
}

// sayKeyNotSaved explains, after a login with SALAD_API_KEY, that the key works only where the variable is set, and
// which key commands use elsewhere.
func (l *login) sayKeyNotSaved() {
	app := l.app
	app.say("The API key is not saved, so it works only where SALAD_API_KEY is set.")
	if l.saved.APIKey != "" && l.saved.APIKey != l.key {
		app.say(
			"Where SALAD_API_KEY is not set, commands use the saved API key (%s).",
			maskAPIKey(l.saved.APIKey),
		)
	}
	app.hintLine("to save it, pipe it to saladcloud login --api-key -")
}
