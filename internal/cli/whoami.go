package cli

import (
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
	orgFlag, projectFlag := app.flags.org, app.flags.project
	path, err := config.Path(app.Env)
	if err != nil {
		return clierr.New(
			clierr.CodeGeneric,
			clierr.KindAPIError,
			"cannot find a folder for the config file: %s",
			config.Reason(err),
		)
	}
	res, err := config.Resolve(app.Env, orgFlag, projectFlag)
	if err != nil {
		return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot read the config file %s: %s", path, readReason(err)).
			WithHint("fix or delete it")
	}
	saved := app.savedConfig()

	var authenticated, found *bool
	var orgs []string
	malformed := false
	// An organization that cannot be a URL name is reported, not sent.
	orgChecks := res.Organization != "" && validName(res.Organization) && !app.looksLikeKey(res.Organization)
	if res.Organization != "" && !orgChecks {
		no := false
		found = &no
	}
	if res.APIKey != "" {
		yes, no := true, false
		key, ok := normalizeKey(res.APIKey)
		if !ok {
			authenticated, malformed = &no, true // no key has these characters; the API would reject it
		} else {
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
				return cerr
			}
			list, cerr := client.OrganizationListing(cmd.Context())
			switch {
			case rejected(cerr):
				authenticated = &no
			case cerr != nil:
				return cerr
			case list != nil:
				authenticated, orgs = &yes, list.OrgNames()
			}
			if orgChecks && !rejected(cerr) {
				switch cerr := client.CheckOrg(cmd.Context()); {
				case cerr == nil:
					authenticated, found = &yes, &yes
				case cerr.Code == clierr.CodeNotFound:
					authenticated, found = &yes, &no
				case rejected(cerr):
					authenticated = &no
				default:
					return cerr
				}
			}
		}
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
		OrganizationFound:  found,
		Project: app.nameOrNil(
			res.Project,
		),
		ProjectSource:       strOrNil(res.ProjectSource),
		SavedProject:        app.nameOrNil(saved.Project),
		GatewayAPIKey:       maskedOrNil(res.GatewayAPIKey),
		GatewayAPIKeySource: strOrNil(res.GatewayAPIKeySource),
		SavedGatewayAPIKey:  maskedOrNil(saved.GatewayAPIKey),
		Authenticated:       authenticated,
		Organizations:       orgs,
		ConfigPath:          path,
	}
	return wrapErr(app.printer.Emit(r, func(w *output.Writer) {
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
		var organization string
		if res.Organization != "" && !app.looksLikeKey(res.Organization) && !validName(res.Organization) {
			organization = fmt.Sprintf(
				`"%s"%s, not a valid organization name`,
				res.Organization,
				fromTag(originOf(res.OrganizationSource, "--org", envOrganization)),
			)
		} else {
			notFound := ""
			if orgChecks && found != nil && !*found {
				notFound = "not found"
			}
			organization = whoamiRow(
				app.shownName(res.Organization),
				res.OrganizationSource,
				"--org",
				envOrganization,
				app.shownName(saved.Organization),
				saved.Organization != res.Organization,
				notFound,
			)
		}
		w.Printf("organization\t%s\n", organization)
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
		auth := "unknown"
		switch {
		case malformed:
			auth = "no (the API key contains characters an API key cannot have)"
		case authenticated != nil && *authenticated:
			auth = "yes"
		case authenticated != nil:
			auth = "no (the API rejected the key)"
		}
		w.Printf("authenticated\t%s\n", auth)
		w.Printf("organizations\t%s\n", orgListCell(orgs))
		w.Printf("config-path\t%s\n", path)
	}))
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
