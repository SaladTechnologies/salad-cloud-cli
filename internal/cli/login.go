package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
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
	apiKeyPortalURL   = "https://portal.salad.com/api-key"
	organizationsURL  = "https://portal.salad.com/organizations"
	envAPIKey         = "SALAD_API_KEY"
	envOrganization   = "SALAD_ORGANIZATION"
	envProject        = "SALAD_PROJECT"
	keyFromStdin      = "stdin"
	keyFromFlag       = "--api-key"
	nothingSavedHint  = "nothing was saved; run saladcloud login again"
	nothingSavedLater = "nothing was saved; run saladcloud login again in a minute"
	invalidPastedKey  = "That API key is invalid. Check that you copied all of it, and that it isn't an AI Gateway key (those go in saladcloud ai-gateway login)."
	quotedPastedKey   = "That API key is invalid. Remove the quotes around it and paste it again."
)

// addLoginCommands adds login.go's commands to the tree.
func (app *App) addLoginCommands(root *cobra.Command) {
	var keyFlag string
	cmd := &cobra.Command{
		Use:   "login",
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
	cmd.Flags().
		StringVar(&keyFlag, "api-key", "", "the API key; - reads it from the first line of stdin (a key typed here is exposed on the command line)")
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
	ctx      context.Context
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
	for _, f := range []struct{ name, value string }{{"api-key", keyFlag}, {"org", orgFlag}, {"project", projectFlag}} {
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

	l := &login{
		ctx:               cmd.Context(),
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
	l.orgName, l.orgFrom = app.namedValue(orgFlag, "--org", envOrganization)
	l.projectName, l.projectFrom = app.namedValue(projectFlag, "--project", envProject)
	for _, n := range []struct{ kind, name, from string }{{"organization", l.orgName, l.orgFrom}, {"project", l.projectName, l.projectFrom}} {
		if n.name != "" {
			if cerr := app.checkName(n.kind, n.name, n.from); cerr != nil {
				return cerr
			}
		}
	}
	if cerr := app.configReady("login"); cerr != nil {
		return cerr
	}
	l.saved = app.savedConfig() // configReady has read it

	// Step 1: where the key comes from.
	switch {
	case keyFlag == "-":
		if cerr := l.keyFromStdin(); cerr != nil {
			return cerr
		}
	case keyFlag != "":
		fprintln(app.errw(), "warning: --api-key exposes the key on the command line; pipe it instead")
		l.key, l.keyFrom = keyFlag, keyFromFlag
	case l.terminal:
		if cerr := l.askForKey(true); cerr != nil {
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
		if cerr := l.checkNamedKey(); cerr != nil {
			return cerr
		}
	}

	for {
		org, cerr := l.pickOrganization()
		if cerr == errKeyRejected {
			if quotedKey(l.key) {
				app.tell("%s", quotedPastedKey)
			} else {
				app.tell("%s", invalidPastedKey)
			}
			if cerr = l.askForKey(false); cerr != nil {
				return cerr
			}
			continue
		}
		if cerr != nil {
			return cerr
		}
		project, cerr := l.pickProject(org)
		if cerr != nil {
			return cerr
		}
		return l.save(org, project)
	}
}

// namedValue is a value the user named for login: the flag, else the
// variable, with where it came from.
func (app *App) namedValue(flag, flagName, env string) (value, from string) {
	if flag = strings.TrimSpace(flag); flag != "" {
		return flag, flagName
	}
	if v := strings.TrimSpace(app.Env.Getenv(env)); v != "" {
		return v, env
	}
	return "", ""
}

// keyFromStdin reads --api-key -'s key: one line, hidden at a terminal.
func (l *login) keyFromStdin() *clierr.Error {
	app := l.app
	var line string
	if app.Console.Terminal() { // hidden whenever stdin is a terminal, prompts or not
		answer, err := app.Console.ReadHidden()
		if err != nil {
			return l.canceled()
		}
		line = answer
	} else {
		var err error
		if line, err = readLine(bufio.NewReader(app.Stdin)); err != nil {
			line = "" // a failed read is no key; the check below says so
		}
	}
	if strings.TrimSpace(line) == "" {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "--api-key - got no key on stdin").
			WithHint("pipe the key in, or set SALAD_API_KEY")
	}
	l.key, l.keyFrom = line, keyFromStdin
	return nil
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
	return cerr != nil && cerr.HTTPStatus != nil && *cerr.HTTPStatus == 401
}

// askForKey is step 2: the key prompt at a terminal, until a key the API
// accepts, Ctrl-C or end of input. A pasted key is read back before it is
// checked. first prints the lines that open the exchange.
func (l *login) askForKey(first bool) *clierr.Error {
	app := l.app
	if first {
		ovs := app.stillSupplied(envAPIKey)
		if v := app.Env.Getenv(envOrganization); v != "" && l.orgFrom == "--org" && l.orgName != v {
			ovs = append(ovs, app.stillSupplied(envOrganization)...)
		}
		if v := app.Env.Getenv(envProject); v != "" && l.projectFrom == "--project" && l.projectName != v {
			ovs = append(ovs, app.stillSupplied(envProject)...)
		}
		if line := app.beforePrompt("login", ovs); line != "" {
			app.tell("%s", line)
		}
		if l.saved.APIKey != "" {
			app.tell("Saved API key: %s (Ctrl-C to keep it)", maskAPIKey(l.saved.APIKey))
			app.tell("hint: to switch organization or project instead, run saladcloud use <org>/<project>")
		}
		app.tell("Get your API key at %s", apiKeyPortalURL)
	}
	for {
		answer, err := app.ask(app.keyPrompt(), true)
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
		orgs, cerr := app.listOrganizations(l.ctx, key)
		if rejected(cerr) {
			if quotedKey(key) {
				app.tell("%s", quotedPastedKey)
			} else {
				app.tell("%s", invalidPastedKey)
			}
			continue
		}
		if cerr != nil {
			return l.failed(cerr)
		}
		l.key, l.keyFrom, l.orgs = key, "", orgs
		return nil
	}
}

// checkNamedKey is step 3 for a key from a flag, stdin or SALAD_API_KEY:
// checked first, then read back with its source, or the error instead.
func (l *login) checkNamedKey() *clierr.Error {
	app := l.app
	key, ok := normalizeKey(l.key)
	if !ok {
		return malformedKey(labelAPIKey, key, l.keyFrom)
	}
	l.key = key
	orgs, cerr := app.listOrganizations(l.ctx, key)
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
		WithStatus(401)
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
func (l *login) checkOrganization(org string) (bool, *clierr.Error) {
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
	cerr = c.CheckOrg(l.ctx)
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
func (l *login) pickOrganization() (string, *clierr.Error) {
	app := l.app
	if l.orgs != nil && len(l.orgs) == 0 {
		return "", clierr.New(clierr.CodeNotFound, clierr.KindNotFound, "you have no organizations").
			WithHint("create one, or accept an invitation, at %s", organizationsURL)
	}
	if l.orgName != "" {
		found, cerr := l.checkOrganization(l.orgName)
		if cerr != nil {
			return "", cerr
		}
		if !found {
			e := clierr.New(clierr.CodeNotFound, clierr.KindNotFound, `organization "%s"%s not found`, l.orgName, fromTag(l.orgFrom)).
				WithStatus(404)
			if h := app.orgsClause(l.orgNames()); h != "" {
				e = e.WithHint("%s", h)
			}
			return "", e
		}
		app.say("Organization: %s%s", l.orgName, fromTag(l.orgFrom))
		return l.orgName, nil
	}

	def := ""
	if saved := l.saved.Organization; saved != "" {
		found := false
		if validName(saved) && !app.looksLikeKey(saved) {
			var cerr *clierr.Error
			if found, cerr = l.checkOrganization(saved); cerr != nil {
				return "", cerr
			}
		}
		switch {
		case found && (!l.terminal || len(l.orgs) == 1):
			app.say("Organization: %s", saved)
			return saved, nil
		case found:
			def = saved
		case !l.terminal && app.looksLikeKey(saved):
			return "", keyLikeName(
				"organization",
				"config",
			).WithHint("pass --org or set SALAD_ORGANIZATION; to list yours, run saladcloud orgs list")
		case !l.terminal:
			e := clierr.New(clierr.CodeNotFound, clierr.KindNotFound, `saved organization "%s" not found`, saved).
				WithHint("%s", hintClauses("pass --org or set SALAD_ORGANIZATION", app.orgsClause(nil)))
			if validName(saved) { // the organization check answered 404
				e = e.WithStatus(404)
			}
			return "", e
		default:
			if app.looksLikeKey(saved) {
				app.tell("The saved organization looks like an API key, not an organization name.")
			} else {
				app.tell(`Saved organization "%s" not found.`, saved)
			}
		}
	}
	if !l.terminal {
		return "", clierr.New(clierr.CodeUsage, clierr.KindUsage, "no organization set").
			WithHint("%s", hintClauses("pass --org or set SALAD_ORGANIZATION", app.orgsClause(nil)))
	}
	if l.orgs == nil {
		return l.typeOrganization(def)
	}
	if len(l.orgs) == 1 {
		org := ptr.Deref(l.orgs[0].OrganizationName)
		app.say("Organization: %s", org)
		return org, nil
	}
	return l.selectOrganization(def)
}

// selectOrganization shows the numbered list and asks for a number or a
// name (4d, 4h, 4i).
func (l *login) selectOrganization(def string) (string, *clierr.Error) {
	app := l.app
	for _, line := range orgMenu(l.orgs) {
		app.tell("%s", line)
	}
	choices := fmt.Sprintf("pick 1-%d or type a name", len(l.orgs))
	var chosen string
	_, err := app.askUntil(
		"Select an organization by number or name"+bracketed(def)+":",
		false,
		func(answer string) (bool, string, *clierr.Error) {
			switch {
			case answer == "" && def != "":
				chosen = def
				return true, "", nil
			case answer == "":
				return false, "", nil
			}
			name := answer
			if n, err := strconv.Atoi(answer); err == nil {
				if n < 1 || n > len(l.orgs) {
					return false, fmt.Sprintf(`"%s" isn't on the list; %s.`, answer, choices), nil
				}
				name = ptr.Deref(l.orgs[n-1].OrganizationName)
			} else if !validName(answer) {
				return false, nameRetry("organization", answer, l.key), nil
			}
			found, cerr := l.checkOrganization(name)
			if cerr != nil {
				return false, "", cerr
			}
			if !found {
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

// typeOrganization asks for a name when there is no list to pick from (4j).
func (l *login) typeOrganization(def string) (string, *clierr.Error) {
	app := l.app
	var chosen string
	_, err := app.askUntil(
		"Type an organization name"+bracketed(def)+":",
		false,
		func(answer string) (bool, string, *clierr.Error) {
			switch {
			case answer == "" && def != "":
				chosen = def
				return true, "", nil
			case answer == "":
				return false, "", nil
			case !validName(answer):
				return false, nameRetry("organization", answer, l.key), nil
			}
			found, cerr := l.checkOrganization(answer)
			if cerr != nil {
				return false, "", cerr
			}
			if !found {
				return false, fmt.Sprintf(
					`Organization "%s" not found. Visit %s to see your organizations, create one or accept an invitation.`,
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
		lines[i] = fmt.Sprintf("  %*d  %-*s(%s)", digits, i+1, width+3, name, display)
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
func (l *login) checkProject(org, project string) (bool, *clierr.Error) {
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
	cerr = c.CheckProject(l.ctx)
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
func (l *login) pickProject(org string) (string, *clierr.Error) {
	app := l.app
	if l.projectName != "" {
		found, cerr := l.checkProject(org, l.projectName)
		if cerr != nil {
			return "", cerr
		}
		if !found {
			return "", clierr.New(clierr.CodeNotFound, clierr.KindNotFound, `project "%s"%s not found in %s`, l.projectName, fromTag(l.projectFrom), org).
				WithStatus(404).
				WithHint("create one at %s", projectCreateURL(org))
		}
		app.say("Project: %s%s", l.projectName, fromTag(l.projectFrom))
		return l.projectName, nil
	}
	if saved := l.saved.Project; saved != "" && l.saved.Organization == org {
		found := false
		if validName(saved) && !app.looksLikeKey(saved) {
			var cerr *clierr.Error
			if found, cerr = l.checkProject(org, saved); cerr != nil {
				return "", cerr
			}
		}
		l.savedProjectFound = &found
		if found {
			return l.offerProject(org, saved)
		}
		line := fmt.Sprintf(`Saved project "%s" not found in %s. Create one at %s.`, saved, org, projectCreateURL(org))
		if app.looksLikeKey(saved) {
			line = "The saved project looks like an API key, not a project name."
		}
		if l.terminal {
			app.tell("%s", line)
		} else {
			app.say("%s", line)
		}
	}
	found, cerr := l.checkProject(org, config.DefaultProject)
	if cerr != nil {
		return "", cerr
	}
	if found {
		return l.offerProject(org, config.DefaultProject)
	}
	if !l.terminal {
		return "", nil
	}
	app.tell(`Project "default" not found in %s. Create one later if needed.`, org)
	return l.typeProject(org, "Type a project name, or press Enter to skip:", "")
}

// offerProject uses a project that exists: offered in brackets at a
// terminal, used as it is without one.
func (l *login) offerProject(org, project string) (string, *clierr.Error) {
	app := l.app
	if !l.terminal {
		app.say("Project: %s", project)
		return project, nil
	}
	return l.typeProject(org, "Type a project name"+bracketed(project)+":", project)
}

// typeProject asks for a project until one that exists, Enter (the default,
// or no project when there is none), Ctrl-C or end of input.
func (l *login) typeProject(org, prompt, def string) (string, *clierr.Error) {
	app := l.app
	chosen := ""
	_, err := app.askUntil(prompt, false, func(answer string) (bool, string, *clierr.Error) {
		if answer == "" {
			chosen = def
			return true, "", nil
		}
		if !validName(answer) {
			return false, nameRetry("project", answer, l.key), nil
		}
		found, cerr := l.checkProject(org, answer)
		if cerr != nil {
			return false, "", cerr
		}
		if !found {
			return false, fmt.Sprintf(
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
	path, err := config.Path(app.Env)
	if err == nil {
		err = config.Save(app.Env, f)
	}
	if err != nil {
		return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot write the config file %s: %s", path, config.Reason(err)).
			WithHint("%s", unwritableHint("login"))
	}
	vars := []string{envOrganization, envProject}
	if keySaved {
		vars = append([]string{envAPIKey}, vars...)
	}
	ovs := app.overridesOf(f, vars...)
	source := "config"
	if app.Env.Getenv(envAPIKey) != "" {
		source = "env"
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
	if err := app.printer.Emit(result, func(w *output.Writer) {
		if project == "" {
			w.Printf("Logged in: %s, no project set\n", org)
		} else {
			w.Printf("Logged in: %s/%s\n", org, project)
		}
		w.Printf("Config at: %s\n", path)
	}); err != nil {
		return wrapErr(err)
	}
	if !keySaved {
		app.say("The API key is not saved, so it works only where SALAD_API_KEY is set.")
		if l.saved.APIKey != "" && l.saved.APIKey != l.key {
			app.say("Where SALAD_API_KEY is not set, commands use the saved API key (%s).", maskAPIKey(l.saved.APIKey))
		}
		app.hintLine("to save it, pipe it to saladcloud login --api-key -")
	}
	app.warnOverrides(app.errw(), afterSave, ovs)
	if l.terminal && l.saved.APIKey == "" {
		app.say("Next, check that everything works:")
		app.say("  saladcloud doctor")
	}
	return nil
}
