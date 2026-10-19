package cli

import (
	"fmt"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

// whoamiTable is whoami's table with the given rows' values.
func whoamiTable(apiKey, org, project, gateway, auth, orgs string) string {
	return fmt.Sprintf(
		"api-key             %s\norganization        %s\nproject             %s\nai-gateway-api-key  "+
			"%s\nauthenticated     "+
			"  %s\norganizations       %s\nconfig-path         <path>\n",
		apiKey,
		org,
		project,
		gateway,
		auth,
		orgs,
	)
}

func TestWhoamiBranches(t *testing.T) {
	t.Parallel()
	k := testAPIKey
	twelve := make(map[string][]string)
	for i := 1; i <= 12; i++ {
		twelve[fmt.Sprintf("org-%02d", i)] = []string{"default"}
	}
	noListing := newFakeAPI()
	noListing.listing = 404
	runBranches(t, []branchCase{
		{
			id: "a variable hides the saved key",
			s: session{
				args: []string{"whoami"}, env: map[string]string{"SALAD_API_KEY": k},
				saved: &config.File{APIKey: keyRotated, Organization: "my-org"},
			},
			stdout: whoamiTable(
				"****...H13m (from SALAD_API_KEY; saved: ****...9QxZ)",
				"my-org (from config)",
				`none set, using "default"`,
				"not set",
				"yes",
				"acme-prod, my-org",
			),
			doc: map[string]any{
				"api_key":                  "****...H13m",
				"api_key_source":           "env",
				"saved_api_key":            "****...9QxZ",
				"organization":             "my-org",
				"saved_organization":       "my-org",
				"organization_found":       true,
				"project":                  "default",
				"project_source":           "default",
				"saved_project":            nil,
				"saved_ai_gateway_api_key": nil,
				"authenticated":            true,
				"organizations":            "[acme-prod my-org]",
				"config_path":              "<path>",
			},
		},
		{
			id: "the same key saved and in the variable",
			s: session{
				args: []string{"whoami"}, env: map[string]string{"SALAD_API_KEY": k},
				saved: &config.File{APIKey: k, Organization: "my-org", Project: "staging"},
			},
			stdout: whoamiTable(
				"****...H13m (from SALAD_API_KEY)",
				"my-org (from config)",
				"staging (from config)",
				"not set",
				"yes",
				"acme-prod, my-org",
			),
			doc: map[string]any{"saved_api_key": "****...H13m", "saved_project": "staging"},
		},
		{
			id: "a flag hides the saved organization",
			s: session{
				args:  []string{"whoami", "--org", "acme-prod"},
				saved: &config.File{APIKey: k, Organization: "my-org"},
			},
			stdout: whoamiTable(
				"****...H13m (from config)",
				"acme-prod (from --org; saved: my-org)",
				`none set, using "default"`,
				"not set",
				"yes",
				"acme-prod, my-org",
			),
			doc: map[string]any{
				"organization_source": "flag",
				"saved_organization":  "my-org",
				"organization_found":  true,
			},
		},
		{
			id: "the key rejected",
			s: session{
				args: []string{"whoami"},
				env:  map[string]string{"SALAD_API_KEY": testOtherAPIKey, "SALAD_ORGANIZATION": "my-org"},
			},
			stdout: whoamiTable(
				"****...ABCD (from SALAD_API_KEY)",
				"my-org (from SALAD_ORGANIZATION)",
				`none set, using "default"`,
				"not set",
				"no (the API rejected the key)",
				"unknown",
			),
			doc: map[string]any{"authenticated": false, "organization_found": nil, "organizations": nil},
		},
		{
			id: "the organization not found",
			s: session{
				args: []string{"whoami"}, saved: &config.File{APIKey: k, Organization: "old-org", Project: "default"},
				env: map[string]string{"SALAD_AI_GATEWAY_API_KEY": testGatewayKey},
			},
			stdout: whoamiTable(
				"****...H13m (from config)",
				"old-org (from config; not found)",
				"default (from config)",
				"****...EFGH (from SALAD_AI_GATEWAY_API_KEY)",
				"yes",
				"acme-prod, my-org",
			),
			doc: map[string]any{
				"organization_found":        false,
				"authenticated":             true,
				"ai_gateway_api_key":        "****...EFGH",
				"ai_gateway_api_key_source": "env",
			},
		},
		{
			id:     "nothing set",
			s:      session{args: []string{"whoami"}},
			stdout: whoamiTable("not set", "not set", `none set, using "default"`, "not set", "unknown", "unknown"),
			doc: map[string]any{
				"api_key":            nil,
				"authenticated":      nil,
				"organization_found": nil,
				"organizations":      nil,
				"saved_api_key":      nil,
			},
		},
		{
			id: "no listing, the organization found",
			s: session{
				args:  []string{"whoami"},
				saved: &config.File{APIKey: k, Organization: "my-org"},
				api:   noListing,
			},
			stdout: whoamiTable(
				"****...H13m (from config)",
				"my-org (from config)",
				`none set, using "default"`,
				"not set",
				"yes",
				"unknown",
			),
			doc: map[string]any{"authenticated": true, "organization_found": true, "organizations": nil},
		},
		{
			id: "no listing, no organization",
			s:  session{args: []string{"whoami"}, saved: &config.File{APIKey: k}, api: noListing},
			stdout: whoamiTable(
				"****...H13m (from config)",
				"not set",
				`none set, using "default"`,
				"not set",
				"unknown",
				"unknown",
			),
			doc: map[string]any{"authenticated": nil, "organization_found": nil},
		},
		{
			id: "twelve organizations",
			s:  session{args: []string{"whoami"}, saved: &config.File{APIKey: k}, api: withOrgs(twelve)},
			stdout: whoamiTable("****...H13m (from config)", "not set", `none set, using "default"`, "not set", "yes",
				"org-01, org-02, org-03, org-04, org-05, org-06, org-07, org-08, org-09, org-10 and 2 more"),
			doc: map[string]any{
				"organizations": "[org-01 org-02 org-03 org-04 org-05 org-06 org-07 org-08 org-09 org-10 org-11 " +
					"org-12]",
			},
		},
	})
}

// TestWhoamiNetworkError: whoami reports a missing or rejected key, but a
// network error is still an error (exit 7).
func TestWhoamiNetworkError(t *testing.T) {
	t.Parallel()
	o := withMode(session{
		args: []string{"whoami"}, saved: &config.File{APIKey: testAPIKey},
		env: map[string]string{"SALAD_CLI_API_BASE_URL": "http://127.0.0.1:1"},
	}, "json").run(t)
	if doc, err := o.errorDocument(); o.code != 7 || err != nil || doc["kind"] != "network" {
		t.Errorf("exit %d, %v %v", o.code, doc, err)
	}
}

func TestLogoutBranches(t *testing.T) {
	t.Parallel()
	k := testAPIKey
	logout := []string{"logout"}
	const removed = "Logged out: removed the saved API key (****...H13m)\n"
	runBranches(t, []branchCase{
		{
			id: "organization and project stay",
			s: session{
				args: logout,
				saved: &config.File{
					APIKey:        k,
					Organization:  "my-org",
					Project:       "default",
					GatewayAPIKey: testGatewayKey,
				},
			},
			stdout: removed + "Organization and project stay saved: my-org/default\n",
			after:  &config.File{Organization: "my-org", Project: "default", GatewayAPIKey: testGatewayKey},
			doc: map[string]any{
				"removed":       true,
				"organization":  "my-org",
				"project":       "default",
				"env_overrides": "[]",
				"action":        "logged_out",
				"ok":            true,
			},
		},
		{
			id:     "the organization stays",
			s:      session{args: logout, saved: &config.File{APIKey: k, Organization: "my-org"}},
			stdout: removed + "Organization stays saved: my-org\n",
			after:  &config.File{Organization: "my-org"},
			doc:    map[string]any{"removed": true, "organization": "my-org", "project": nil},
		},
		{
			id:     "nothing else saved",
			s:      session{args: logout, saved: &config.File{APIKey: k}},
			stdout: removed,
			after:  &config.File{},
			doc:    map[string]any{"removed": true, "organization": nil, "project": nil},
		},
		{
			id:     "no key saved",
			s:      session{args: logout, saved: &config.File{Organization: "my-org", Project: "default"}},
			stdout: "No API key was saved; nothing to remove\n",
			doc:    map[string]any{"removed": false, "organization": "my-org", "project": "default"},
		},
		{
			id: "the variable still set, at a terminal", s: session{
				args: logout, tty: []string{}, env: map[string]string{"SALAD_API_KEY": testOtherAPIKey},
				saved: &config.File{APIKey: k, Organization: "my-org", Project: "default"},
			},
			stdout: removed + "Organization and project stay saved: my-org/default\n",
			stderr: "warning: commands still use SALAD_API_KEY (****...ABCD). To remove the variable:\n" +
				"  PowerShell:  Remove-Item Env:SALAD_API_KEY\n  cmd:         set SALAD_API_KEY=\n" +
				"If it comes back in a new terminal, remove it permanently:\n  " +
				"[Environment]::SetEnvironmentVariable('SALAD_API_KEY', $null, 'User')\n" +
				"Then open a new terminal (restart VS Code if you use its terminal).\n",
			after: &config.File{Organization: "my-org", Project: "default"},
			doc:   map[string]any{"env_overrides": "[SALAD_API_KEY]"},
		},
		{
			id:     "the variable still set, no terminal, macOS",
			s:      session{args: logout, os: "darwin", env: map[string]string{"SALAD_API_KEY": testOtherAPIKey}},
			stdout: "No API key was saved; nothing to remove\n",
			stderr: "warning: commands still use SALAD_API_KEY (****...ABCD)\n",
			doc:    map[string]any{"removed": false, "env_overrides": "[SALAD_API_KEY]"},
		},
	})
}
