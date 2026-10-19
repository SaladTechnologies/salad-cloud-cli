package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
)

// whoamiResult is whoami's JSON contract. Every key is always present;
// null means not set, or not known. Each value's saved_* twin is what the
// config file holds (masked for keys), null when nothing is saved, so a
// variable or flag hiding a different saved value shows as a source other
// than config with a saved_* that differs. authenticated is whether the API
// accepted the key (null: nothing to check); organization_found is the
// organization check (null: not checked); organizations is the key's
// organizations, null when the listing was unavailable.
type whoamiResult struct {
	APIKey              *string  `json:"api_key"`
	APIKeySource        *string  `json:"api_key_source"`
	SavedAPIKey         *string  `json:"saved_api_key"`
	Organization        *string  `json:"organization"`
	OrganizationSource  *string  `json:"organization_source"`
	SavedOrganization   *string  `json:"saved_organization"`
	OrganizationFound   *bool    `json:"organization_found"`
	Project             *string  `json:"project"`
	ProjectSource       *string  `json:"project_source"`
	SavedProject        *string  `json:"saved_project"`
	GatewayAPIKey       *string  `json:"ai_gateway_api_key"`
	GatewayAPIKeySource *string  `json:"ai_gateway_api_key_source"`
	SavedGatewayAPIKey  *string  `json:"saved_ai_gateway_api_key"`
	Authenticated       *bool    `json:"authenticated"`
	Organizations       []string `json:"organizations"`
	ConfigPath          string   `json:"config_path"`
}

// addWhoamiCommands adds whoami.go's commands to the tree.
func (app *App) addWhoamiCommands(root *cobra.Command) {
	cmd := &cobra.Command{
		Use: "whoami",
		// gh's habit is 'gh auth status'; 'status' anywhere suggests this.
		SuggestFor: []string{"status"},
		Short:      "Show the API key, organization and project in use, and where each came from",
		Long: `Show the API key (masked), organization, project and AI Gateway key in use,
where each came from (a flag, a variable or the config file), and the saved
value a variable or flag is hiding.

authenticated says whether the API accepted the key, checked through the list
of your organizations (or the organization, when that list is unavailable).
whoami reports and never fails over a missing or rejected key or an
organization that is not found; saladcloud doctor judges. A network error is
still an error.

In JSON every key is always present: null means not set or not known.
organizations comes from an endpoint that is not part of the documented API;
it is null whenever that list is unavailable.`,
		Args: cobra.NoArgs,
		RunE: app.runWhoami,
	}
	root.AddCommand(cmd)
}

func (app *App) runWhoami(cmd *cobra.Command, _ []string) error {
	path, res, cerr := app.whoamiResolve()
	if cerr != nil {
		return cerr
	}
	saved := app.savedConfig()
	chk, cerr := app.whoamiCheckKey(cmd.Context(), &res)
	if cerr != nil {
		return cerr
	}
	r := whoamiResult{
		APIKey: maskedOrNil(
			res.APIKey,
		),
		APIKeySource:       strOrNil(res.APIKeySource),
		SavedAPIKey:        maskedOrNil(saved.APIKey),
		Organization:       app.nameOrNil(res.Organization),
		OrganizationSource: strOrNil(res.OrganizationSource),
		SavedOrganization:  app.nameOrNil(saved.Organization),
		OrganizationFound:  chk.found,
		Project: app.nameOrNil(
			res.Project,
		),
		ProjectSource:       strOrNil(res.ProjectSource),
		SavedProject:        app.nameOrNil(saved.Project),
		GatewayAPIKey:       maskedOrNil(res.GatewayAPIKey),
		GatewayAPIKeySource: strOrNil(res.GatewayAPIKeySource),
		SavedGatewayAPIKey:  maskedOrNil(saved.GatewayAPIKey),
		Authenticated:       chk.authenticated,
		Organizations:       chk.orgs,
		ConfigPath:          path,
	}
	return wrapErr(app.printer.Emit(r, func(w *output.Writer) { app.whoamiTable(w, path, &res, saved, &chk) }))
}

// whoamiResolve is the config file's path and what commands resolve from the flags, the variables and the file.
func (app *App) whoamiResolve() (string, config.Resolved, *clierr.Error) {
	orgFlag, projectFlag := app.flags.org, app.flags.project
	path, err := config.Path(app.Env)
	if err != nil {
		return "", config.Resolved{}, clierr.New(
			clierr.CodeGeneric,
			clierr.KindAPIError,
			"cannot find a folder for the config file: %s",
			config.Reason(err),
		)
	}
	res, err := config.Resolve(app.Env, orgFlag, projectFlag)
	if err != nil {
		e := clierr.New(
			clierr.CodeGeneric,
			clierr.KindAPIError,
			"cannot read the config file %s: %s",
			path,
			readReason(err),
		)
		return "", config.Resolved{}, e.WithHint("fix or delete it")
	}
	return path, res, nil
}

// whoamiCheck is what whoami learned about the key and the organization in use.
type whoamiCheck struct {
	authenticated *bool    // whether the API accepted the key; nil when there was nothing to check
	found         *bool    // the organization check; nil when not checked
	orgs          []string // the key's organizations; nil when the listing was unavailable
	malformed     bool     // the key has characters no key has, so it was never sent
	orgChecks     bool     // the organization can be a URL name, so it is sent
}

// whoamiCheckKey asks the API about the key in use and its organization. An organization that cannot be a URL name
// is reported, not sent; a key no key looks like is never sent either.
func (app *App) whoamiCheckKey(ctx context.Context, res *config.Resolved) (whoamiCheck, *clierr.Error) {
	chk := whoamiCheck{
		authenticated: nil,
		found:         nil,
		orgs:          nil,
		malformed:     false,
		orgChecks:     res.Organization != "" && validName(res.Organization) && !app.looksLikeKey(res.Organization),
	}
	if res.Organization != "" && !chk.orgChecks {
		no := false
		chk.found = &no
	}
	if res.APIKey == "" {
		return chk, nil
	}
	key, ok := normalizeKey(res.APIKey)
	if !ok {
		no := false
		chk.authenticated, chk.malformed = &no, true // no key has these characters; the API would reject it
		return chk, nil
	}
	client, cerr := api.New(
		config.Resolved{
			APIKey:              key,
			Organization:        res.Organization,
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
		return chk, cerr
	}
	cerr = chk.authenticate(ctx, client)
	return chk, cerr
}

// authenticate checks the key through the organization listing, then the organization through the organization
// check unless the key was rejected. A rejected key or a missing organization is an answer; any other error is
// returned.
func (chk *whoamiCheck) authenticate(ctx context.Context, client *api.Client) *clierr.Error {
	yes, no := true, false
	list, cerr := client.OrganizationListing(ctx)
	switch {
	case rejected(cerr):
		chk.authenticated = &no
	case cerr != nil:
		return cerr
	case list != nil:
		chk.authenticated, chk.orgs = &yes, list.OrgNames()
	}
	if !chk.orgChecks || rejected(cerr) {
		return nil
	}
	switch cerr := client.CheckOrg(ctx); {
	case cerr == nil:
		chk.authenticated, chk.found = &yes, &yes
	case cerr.Code == clierr.CodeNotFound:
		chk.authenticated, chk.found = &yes, &no
	case rejected(cerr):
		chk.authenticated = &no
	default:
		return cerr
	}
	return nil
}

// whoamiTable is whoami's table: one row per value, with where it came from and what the API said.
func (app *App) whoamiTable(w *output.Writer, path string, res *config.Resolved, saved config.File, chk *whoamiCheck) {
	fprintf(
		w,
		"api-key\t%s\n",
		whoamiRow(
			maskAPIKey(res.APIKey),
			res.APIKeySource,
			"",
			envAPIKey,
			maskAPIKey(saved.APIKey),
			saved.APIKey != "" && !sameKey(saved.APIKey, res.APIKey),
			"",
		),
	)
	w.Printf("organization\t%s\n", app.whoamiOrganizationCell(res, saved, chk))
	project := whoamiRow(
		app.shownName(res.Project),
		res.ProjectSource,
		"--project",
		envProject,
		app.shownName(saved.Project),
		saved.Project != res.Project,
		"",
	)
	if res.ProjectSource == config.SourceDefault {
		project = `none set, using "default"`
	}
	w.Printf("project\t%s\n", project)
	fprintf(
		w,
		"ai-gateway-api-key\t%s\n",
		whoamiRow(
			maskAPIKey(res.GatewayAPIKey),
			res.GatewayAPIKeySource,
			"",
			envGatewayKey,
			maskAPIKey(
				saved.GatewayAPIKey,
			),
			saved.GatewayAPIKey != "" && !sameKey(saved.GatewayAPIKey, res.GatewayAPIKey),
			"",
		),
	)
	w.Printf("authenticated\t%s\n", chk.authenticatedCell())
	w.Printf("organizations\t%s\n", orgListCell(chk.orgs))
	w.Printf("config-path\t%s\n", path)
}

// whoamiOrganizationCell is the organization row: a name that cannot be one is said so, and an organization the
// check did not find is marked.
func (app *App) whoamiOrganizationCell(res *config.Resolved, saved config.File, chk *whoamiCheck) string {
	if res.Organization != "" && !app.looksLikeKey(res.Organization) && !validName(res.Organization) {
		return fmt.Sprintf(
			`"%s"%s, not a valid organization name`,
			res.Organization,
			fromTag(originOf(res.OrganizationSource, "--org", envOrganization)),
		)
	}
	notFound := ""
	if chk.orgChecks && chk.found != nil && !*chk.found {
		notFound = "not found"
	}
	return whoamiRow(
		app.shownName(res.Organization),
		res.OrganizationSource,
		"--org",
		envOrganization,
		app.shownName(saved.Organization),
		saved.Organization != res.Organization,
		notFound,
	)
}

// authenticatedCell is the authenticated row: yes, no and why, or unknown when there was nothing to check.
func (chk *whoamiCheck) authenticatedCell() string {
	switch {
	case chk.malformed:
		return "no (the API key contains characters an API key cannot have)"
	case chk.authenticated != nil && *chk.authenticated:
		return "yes"
	case chk.authenticated != nil:
		return "no (the API rejected the key)"
	}
	return "unknown"
}

// whoamiRow renders one whoami value: "not set", or the value with where it
// came from, the saved value a variable or flag is hiding, and a note.
func whoamiRow(value, source, flag, env, saved string, differs bool, note string) string {
	if source == "" {
		return "not set"
	}
	var parts []string
	if source == "config" {
		parts = append(parts, "from config")
	} else {
		parts = append(parts, "from "+originOf(source, flag, env))
		if differs && saved != "" && saved != "(not set)" {
			parts = append(parts, "saved: "+saved)
		}
	}
	if note != "" {
		parts = append(parts, note)
	}
	return value + " (" + strings.Join(parts, "; ") + ")"
}

// orgListCell is the organizations row: the first 10, then "and N more";
// "unknown" when the listing was unavailable, "none" when there are none.
func orgListCell(names []string) string {
	switch {
	case names == nil:
		return "unknown"
	case len(names) == 0:
		return "none"
	case len(names) <= orgListShown:
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:orgListShown], ", "), len(names)-orgListShown)
}

// nameOrNil is a name for whoami's JSON: null when unset, masked when it
// looks like a key.
func (app *App) nameOrNil(v string) *string {
	if app.looksLikeKey(v) {
		return maskedOrNil(v)
	}
	return strOrNil(v)
}

// sameKey reports whether two keys are the same once normalized as
// commands use them.
func sameKey(a, b string) bool {
	ka, _ := normalizeKey(a)
	kb, _ := normalizeKey(b)
	return ka == kb
}
