package cli

import (
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

// parseUseTarget splits the positional <org>[/<project>] argument on the
// first "/". An empty organization or project, or a second "/", is a usage
// error.
func parseUseTarget(arg string) (org, project string, err *clierr.Error) {
	org, project, slash := strings.Cut(strings.TrimSpace(arg), "/")
	if org == "" || slash && (project == "" || strings.Contains(project, "/")) {
		return "", "", clierr.New(clierr.CodeUsage, clierr.KindUsage, `invalid target "%s"`, arg).
			WithHint("the target is <org> or <org>/<project>, for example saladcloud use acme-prod/staging")
	}
	return org, project, nil
}

func (app *App) runUse(cmd *cobra.Command, args []string) error {
	org, project, perr := parseUseTarget(args[0])
	if perr != nil {
		return perr
	}
	if cerr := app.checkName("organization", org, ""); cerr != nil {
		return cerr
	}
	if project != "" {
		if cerr := app.checkName("project", project, ""); cerr != nil {
			return cerr
		}
	}
	if cerr := app.configReady("use"); cerr != nil {
		return cerr
	}
	res, err := config.Resolve(app.Env, "", "")
	if err != nil {
		return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot read the config file: %v", err)
	}
	saved := app.savedConfig() // configReady has read it
	if res.APIKey == "" {
		return clierr.New(clierr.CodeAuth, clierr.KindAuth, "no API key set").
			WithHint("run saladcloud login in a terminal, or set SALAD_API_KEY")
	}
	keyFrom := originOf(res.APIKeySource, "", envAPIKey)
	key, ok := normalizeKey(res.APIKey)
	if !ok {
		return malformedKey(labelAPIKey, key, keyFrom)
	}
	u := &useRun{key: key, keyFrom: keyFrom, saved: saved}

	client, cerr := api.New(
		config.Resolved{
			APIKey:              key,
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
		return cerr
	}
	if cerr = client.CheckOrg(cmd.Context()); cerr != nil {
		if cerr.Code != clierr.CodeNotFound {
			return u.failed(cerr)
		}
		e := clierr.New(clierr.CodeNotFound, clierr.KindNotFound, `organization "%s" not found`, org).WithStatus(404)
		names := []string(nil)
		if app.atTerminal() {
			if list, lerr := client.OrganizationListing(cmd.Context()); lerr == nil && list != nil {
				names = list.OrgNames()
			} else if lerr != nil && lerr.IsCancel() {
				return u.canceled()
			}
		}
		if h := app.orgsClause(names); h != "" {
			e = e.WithHint("%s", h)
		}
		return e
	}

	defaulted := project == ""
	if defaulted {
		project = config.DefaultProject
	}
	pc, cerr := api.New(
		config.Resolved{
			APIKey:              key,
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
		return cerr
	}
	if cerr := pc.CheckProject(cmd.Context()); cerr != nil {
		switch {
		case cerr.Code != clierr.CodeNotFound:
			return u.failed(cerr)
		case !defaulted:
			return clierr.New(clierr.CodeNotFound, clierr.KindNotFound, `project "%s" not found in %s`, project, org).
				WithStatus(404).WithHint("create one at %s", projectCreateURL(org))
		}
		app.say(`Project "default" not found in %s. Create one later if needed.`, org)
		project = ""
	}

	f := saved
	f.Organization, f.Project = org, project
	path, err := config.Path(app.Env)
	if err == nil {
		err = config.Save(app.Env, f)
	}
	if err != nil {
		return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot write the config file %s: %s", path, config.Reason(err)).
			WithHint("%s", unwritableHint("use"))
	}
	ovs := app.overridesOf(f, envOrganization, envProject)
	result := useResult{
		Organization: org,
		Project:      strOrNil(project),
		EnvOverrides: overrideNames(ovs),
		Action:       "switched",
		OK:           true,
	}
	if err := app.printer.Emit(result, func(w *output.Writer) {
		if project == "" {
			w.Printf("Switched to %s, no project set\n", org)
			return
		}
		w.Printf("Switched to %s/%s\n", org, project)
	}); err != nil {
		return wrapErr(err)
	}
	if defaulted && project != "" {
		app.hintLine(`no project named, so "default" is used; for another, run saladcloud use %s/<project>`, org)
	}
	app.warnOverrides(app.errw(), afterSave, ovs)
	return nil
}

// useRun is what use's errors need to know.
type useRun struct {
	key     string
	keyFrom string
	saved   config.File
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
		e := clierr.New(clierr.CodeAuth, clierr.KindAuth, "API key %s%s is invalid", maskAPIKey(u.key), fromTag(u.keyFrom)).
			WithStatus(401)
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
