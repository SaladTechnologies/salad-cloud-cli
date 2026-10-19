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

// addUseCommands adds use.go's commands to the tree.
func (app *App) addUseCommands(root *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "use <org>[/<project>]",
		Short: "Switch the saved organization and project",
		Long: `Switch the organization and project saved in the config file, after checking
them against the API with the API key commands use now. It never prompts.

A saved project belongs to its organization, so the two move together. With
<org> alone, the project is the organization's "default" when it has one;
when it has none, the organization is saved with no project (container groups
and queues need one; inference, transcription and the AI Gateway do not).
Both are the names in the portal URL
(https://portal.salad.com/organizations/<org>/projects/<project>), not the
display names.

SALAD_ORGANIZATION and SALAD_PROJECT, when set, are used instead of what use
saves; use says so.`,
		Example: `  # Switch the organization, and to its "default" project
  saladcloud use my-org

  # Switch organization and project together
  saladcloud use my-org/staging`,
		Args: cobra.ExactArgs(1),
		RunE: app.runUse,
	}
	root.AddCommand(cmd)
}

// useResult is use's JSON contract: the context saved (project null when
// none), the variables that will be used instead, and the action shape every
// state-changing command carries.
type useResult struct {
	Organization string   `json:"organization"`
	Project      *string  `json:"project"`
	EnvOverrides []string `json:"env_overrides"`
	Action       string   `json:"action"`
	OK           bool     `json:"ok"`
}

// useTarget is use's argument, split: the organization, and the project ("" when none was named).
type useTarget struct{ org, project string }

// parseUseTarget splits the positional <org>[/<project>] argument on the
// first "/". An empty organization or project, or a second "/", is a usage
// error.
func parseUseTarget(arg string) (useTarget, *clierr.Error) {
	org, project, slash := strings.Cut(strings.TrimSpace(arg), "/")
	if org == "" || slash && (project == "" || strings.Contains(project, "/")) {
		return useTarget{}, clierr.New(clierr.CodeUsage, clierr.KindUsage, `invalid target "%s"`, arg).
			WithHint("the target is <org> or <org>/<project>, for example saladcloud use acme-prod/staging")
	}
	return useTarget{org: org, project: project}, nil
}

// useRun is one run of use: the key it checks with, the config file before it, and what its errors need to know.
type useRun struct {
	key       string
	keyFrom   string
	saved     config.File
	defaulted bool // no project was named, so use tries "default"
	app       *App // the run
}

func (app *App) runUse(cmd *cobra.Command, args []string) error {
	target, perr := parseUseTarget(args[0])
	if perr != nil {
		return perr
	}
	org, project := target.org, target.project
	if cerr := app.checkUseNames(org, project); cerr != nil {
		return cerr
	}
	u, cerr := app.newUseRun()
	if cerr != nil {
		return cerr
	}
	cerr = u.checkOrganization(cmd.Context(), org)
	if cerr != nil {
		return cerr
	}
	u.defaulted = project == ""
	if u.defaulted {
		project = config.DefaultProject
	}
	project, cerr = u.checkProject(cmd.Context(), org, project)
	if cerr != nil {
		return cerr
	}
	return u.save(org, project)
}

// checkUseNames checks the organization and project use's argument names (the project only when there is one).
func (app *App) checkUseNames(org, project string) *clierr.Error {
	if cerr := app.checkName(nameKindOrganization, org, ""); cerr != nil {
		return cerr
	}
	if project != "" {
		return app.checkName(nameKindProject, project, "")
	}
	return nil
}

// newUseRun starts a run of use: the config file read, and the API key commands use now, which use checks with.
func (app *App) newUseRun() (*useRun, *clierr.Error) {
	if cerr := app.configReady("use"); cerr != nil {
		return nil, cerr
	}
	res, err := config.Resolve(app.Env, "", "")
	if err != nil {
		return nil, clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot read the config file: %v", err)
	}
	saved := app.savedConfig() // configReady has read it
	if res.APIKey == "" {
		return nil, clierr.New(clierr.CodeAuth, clierr.KindAuth, "no API key set").
			WithHint("run saladcloud login in a terminal, or set SALAD_API_KEY")
	}
	keyFrom := originOf(res.APIKeySource, "", envAPIKey)
	key, ok := normalizeKey(res.APIKey)
	if !ok {
		return nil, malformedKey(labelAPIKey, key, keyFrom)
	}
	return &useRun{key: key, keyFrom: keyFrom, saved: saved, defaulted: false, app: app}, nil
}

// checkOrganization is the organization check: nil when org is found, else the error, which lists the key's
// organizations at a terminal.
func (u *useRun) checkOrganization(ctx context.Context, org string) *clierr.Error {
	client, cerr := u.client(org, "")
	if cerr != nil {
		return cerr
	}
	cerr = client.CheckOrg(ctx)
	switch {
	case cerr == nil:
		return nil
	case cerr.Code != clierr.CodeNotFound:
		return u.failed(cerr)
	}
	e := clierr.New(clierr.CodeNotFound, clierr.KindNotFound, `organization "%s" not found`, org).
		WithStatus(http.StatusNotFound)
	names := []string(nil)
	if u.app.atTerminal() {
		if list, lerr := client.OrganizationListing(ctx); lerr == nil && list != nil {
			names = list.OrgNames()
		} else if lerr != nil && lerr.IsCancel() {
			return u.canceled()
		}
	}
	if h := u.app.orgsClause(names); h != "" {
		e = e.WithHint("%s", h)
	}
	return e
}

// checkProject is the project check in org: the project when it is found; "" when the defaulted "default" is not,
// which use says and saves the organization alone.
func (u *useRun) checkProject(ctx context.Context, org, project string) (string, *clierr.Error) {
	pc, cerr := u.client(org, project)
	if cerr != nil {
		return "", cerr
	}
	cerr = pc.CheckProject(ctx)
	switch {
	case cerr == nil:
		return project, nil
	case cerr.Code != clierr.CodeNotFound:
		return "", u.failed(cerr)
	case !u.defaulted:
		return "", clierr.New(clierr.CodeNotFound, clierr.KindNotFound, `project "%s" not found in %s`, project, org).
			WithStatus(http.StatusNotFound).WithHint("create one at %s", projectCreateURL(org))
	}
	u.app.say(`Project "default" not found in %s. Create one later if needed.`, org)
	return "", nil
}

// client is an API client for use's checks: the key in use, with org and project.
func (u *useRun) client(org, project string) (*api.Client, *clierr.Error) {
	return api.New(
		config.Resolved{
			APIKey:              u.key,
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
		u.app.apiOptions(),
	)
}

// save writes the organization and project, then prints the result, the hint for a defaulted project, and the
// variables that will be used instead.
func (u *useRun) save(org, project string) error {
	app := u.app
	f := u.saved
	f.Organization, f.Project = org, project
	if _, cerr := app.saveContextConfig(f, "use"); cerr != nil {
		return cerr
	}
	ovs := app.overridesOf(f, envOrganization, envProject)
	result := useResult{
		Organization: org,
		Project:      strOrNil(project),
		EnvOverrides: overrideNames(ovs),
		Action:       "switched",
		OK:           true,
	}
	err := app.printer.Emit(result, func(w *output.Writer) {
		if project == "" {
			w.Printf("Switched to %s, no project set\n", org)
			return
		}
		w.Printf("Switched to %s/%s\n", org, project)
	})
	if err != nil {
		return wrapErr(err)
	}
	if u.defaulted && project != "" {
		app.hintLine(`no project named, so "default" is used; for another, run saladcloud use %s/<project>`, org)
	}
	app.warnOverrides(app.errw(), afterSave, ovs)
	return nil
}

func (*useRun) canceled() *clierr.Error {
	return clierr.New(clierr.CodeGeneric, clierr.KindInterrupted, "use canceled, nothing changed")
}

// failed is a check that failed for another reason than not found: Ctrl-C
// reads as canceled, a rejected key names its source and the fix there,
// anything else says nothing was changed.
func (u *useRun) failed(cerr *clierr.Error) *clierr.Error {
	switch {
	case cerr.IsCancel():
		return u.canceled()
	case rejected(cerr):
		e := clierr.New(
			clierr.CodeAuth,
			clierr.KindAuth,
			"API key %s%s is invalid",
			maskAPIKey(u.key),
			fromTag(u.keyFrom),
		).WithStatus(http.StatusUnauthorized)
		switch {
		case quotedKey(u.key):
			return e.WithHint("remove the quotes around it")
		case u.keyFrom != envAPIKey:
			return e.WithHint("run saladcloud login to save a new key")
		case u.saved.APIKey != "" && u.saved.APIKey != u.key:
			return e.WithHint(
				"remove SALAD_API_KEY to use the saved API key (%s), or set it to a working key",
				maskAPIKey(u.saved.APIKey),
			)
		}
		return e.WithHint("set SALAD_API_KEY to a working key, or remove it and run saladcloud login")
	case cerr.Code == clierr.CodeRateLimited:
		return cerr.WithHint("nothing was changed; run saladcloud use again in a minute")
	}
	return cerr.WithHint("nothing was changed; run saladcloud use again")
}
