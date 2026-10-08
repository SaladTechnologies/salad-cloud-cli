package cli

import (
	"fmt"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

// TestRunTimeErrors covers the credential and context errors any command
// can meet after login (a rejected key, no key, organization or project, an
// invalid name), as every command reports them.
func TestRunTimeErrors(t *testing.T) {
	K, O := testAPIKey, testOtherAPIKey
	saved := &config.File{APIKey: K, Organization: "my-org", Project: "default"}
	quotas := []string{"quotas", "get"}
	test := []string{"ai-gateway", "test"}
	noDefault := withOrgs(map[string][]string{"my-org": {"staging"}})
	gwSaved := func(key string) *config.File { return &config.File{GatewayAPIKey: key} }
	runBranches(t, []branchCase{
		{id: "401, the key from config", status: 401, s: session{args: quotas, saved: &config.File{APIKey: O, Organization: "my-org"}}, code: 3, kind: "auth",
			stderr: errLines("API key ****...ABCD (from config) was rejected", "run saladcloud login to save a new key")},
		{id: "401, SALAD_API_KEY, another key saved", status: 401, s: session{args: quotas, saved: saved, env: map[string]string{"SALAD_API_KEY": O}}, code: 3, kind: "auth",
			stderr: errLines("API key ****...ABCD (from SALAD_API_KEY) was rejected", "remove SALAD_API_KEY to use the saved API key (****...H13m), or set it to a working key")},
		{id: "401, SALAD_API_KEY, none saved", status: 401, s: session{args: quotas, env: map[string]string{"SALAD_API_KEY": O, "SALAD_ORGANIZATION": "my-org"}}, code: 3, kind: "auth",
			stderr: errLines("API key ****...ABCD (from SALAD_API_KEY) was rejected", "set SALAD_API_KEY to a working key, or remove it and run saladcloud login")},
		{id: "401, a key in quotes", status: 401, s: session{args: quotas, env: map[string]string{"SALAD_API_KEY": `"` + K + `"`, "SALAD_ORGANIZATION": "my-org"}}, code: 3, kind: "auth",
			stderr: errLines(`API key ****...13m" (from SALAD_API_KEY) was rejected`, "remove the quotes around it")},
		{id: "no organization", s: session{args: quotas, saved: &config.File{APIKey: K}}, code: 2, kind: "usage",
			stderr: errLines("no organization set", "pass --org, set SALAD_ORGANIZATION, or run saladcloud use <org>")},
		{id: "no API key", s: session{args: quotas, env: map[string]string{"SALAD_ORGANIZATION": "my-org"}}, code: 3, kind: "auth",
			stderr: errLines("no API key set", "run saladcloud login in a terminal, or set SALAD_API_KEY")},
		{id: "no AI Gateway key", s: session{args: test}, code: 3, kind: "auth",
			stderr: errLines("no AI Gateway key set", "run saladcloud ai-gateway login in a terminal, or set SALAD_AI_GATEWAY_API_KEY")},
		{id: "gateway 401, from config", status: 401, s: session{args: test, saved: gwSaved(testOtherGatewayKey), api: gatewayFake()}, code: 3, kind: "auth",
			stderr: errLines("AI Gateway key ****...WXYZ (from config) was rejected", "run saladcloud ai-gateway login to save a new key")},
		{id: "gateway 401, the variable, another key saved", status: 401, s: session{args: test, saved: gwSaved(testGatewayKey), api: gatewayFake(),
			env: map[string]string{"SALAD_AI_GATEWAY_API_KEY": testOtherGatewayKey}}, code: 3, kind: "auth",
			stderr: errLines("AI Gateway key ****...WXYZ (from SALAD_AI_GATEWAY_API_KEY) was rejected", "remove SALAD_AI_GATEWAY_API_KEY to use the saved AI Gateway key (****...EFGH), or set it to a working key")},
		{id: "gateway 401, the variable, none saved", status: 401, s: session{args: test, api: gatewayFake(), env: map[string]string{"SALAD_AI_GATEWAY_API_KEY": testOtherGatewayKey}},
			code: 3, kind: "auth",
			stderr: errLines("AI Gateway key ****...WXYZ (from SALAD_AI_GATEWAY_API_KEY) was rejected", "set SALAD_AI_GATEWAY_API_KEY to a working key, or remove it and run saladcloud ai-gateway login")},
		{id: "gateway 402", status: 402, s: session{args: test, saved: gwSaved(keyNoCredits), api: gatewayFake()}, code: 10, kind: "not_available",
			stderr: errLines("the AI Gateway key's organization is out of credits", "add credits from the organization's AI Gateway page: https://portal.salad.com/ai-gateway")},
		{id: "gateway 403", status: 403, s: session{args: test, saved: gwSaved(keyNoAccess), api: gatewayFake()}, code: 3, kind: "auth",
			stderr: errLines("AI Gateway key ****...NOAC (from config) has no AI Gateway access", "check that you are still a member of its organization; if you are, contact support")},
		{id: "no project, no default", s: session{args: []string{"cg", "list"}, saved: &config.File{APIKey: K, Organization: "my-org"}, api: noDefault}, code: 2, kind: "usage",
			stderr: "warning: using project \"default\" because none was set; pass --project or set SALAD_PROJECT to choose one\n" +
				errLines(`no valid project set: my-org has no "default" project`, "create one at https://portal.salad.com/organizations/my-org/projects/new, then pass --project or run saladcloud use my-org/<project>")},
		{id: "an invalid name from --org", s: session{args: []string{"quotas", "get", "--org", "Acme Prod"}, saved: saved}, code: 6, kind: "invalid_input",
			stderr: errLines(`invalid organization name "Acme Prod" (from --org)`, "it looks like a display name; use the name from the portal URL, the part after /organizations/")},
		{id: "an invalid name from SALAD_PROJECT", s: session{args: []string{"cg", "list"}, saved: saved, env: map[string]string{"SALAD_PROJECT": "STAGING"}}, code: 6, kind: "invalid_input",
			stderr: errLines(`invalid project name "STAGING" (from SALAD_PROJECT)`, `did you mean "staging"?`)},
	})
}

// TestRunTimeNameCheckMakesNoRequest: an invalid name from a flag or
// variable fails before any request, and a project only where the command
// uses one.
func TestRunTimeNameCheckMakesNoRequest(t *testing.T) {
	saved := &config.File{APIKey: testAPIKey, Organization: "my-org", Project: "default"}
	o := session{args: []string{"quotas", "get", "--org", "Acme Prod"}, saved: saved}.run(t)
	if o.code != 6 || len(o.api.Requests()) != 0 {
		t.Errorf("exit %d after requests %v", o.code, o.api.Requests())
	}
	// quotas is organization-scoped: an invalid SALAD_PROJECT is not its business.
	o = session{args: []string{"quotas", "get"}, saved: saved, env: map[string]string{"SALAD_PROJECT": "STAGING"}}.run(t)
	if o.code != 0 {
		t.Errorf("an organization-scoped command failed over the project: exit %d\n%s", o.code, o.stderr)
	}
}

// TestVanishedOrganization: a 404 under an organization the key can no
// longer see makes one extra request, the organization check, and is
// reported as the organization, with the fix for where it came from; names
// are listed only at a terminal (which costs the listing).
func TestVanishedOrganization(t *testing.T) {
	K := testAPIKey
	gone := &config.File{APIKey: K, Organization: "old-org", Project: "default"}
	list := []string{"cg", "list"}
	runBranches(t, []branchCase{
		{id: "from config", status: 404, s: session{args: list, saved: gone}, code: 4, kind: "not_found",
			stderr: errLines(`organization "old-org" (from config) not found`, "to list yours, run saladcloud orgs list; to switch, run saladcloud use <org>")},
		{id: "from config, at a terminal", status: 404, s: session{args: list, saved: gone, tty: []string{}}, code: 4, kind: "not_found",
			stderr: errLines(`organization "old-org" (from config) not found`, "your organizations: acme-prod, my-org; to switch, run saladcloud use <org>")},
		{id: "from SALAD_ORGANIZATION", status: 404, s: session{args: list, saved: &config.File{APIKey: K}, env: map[string]string{"SALAD_ORGANIZATION": "old-org"}}, code: 4, kind: "not_found",
			stderr: "warning: using project \"default\" because none was set; pass --project or set SALAD_PROJECT to choose one\n" +
				errLines(`organization "old-org" (from SALAD_ORGANIZATION) not found`, "to list yours, run saladcloud orgs list; fix or remove SALAD_ORGANIZATION")},
		{id: "from --org, at a terminal", status: 404, s: session{args: []string{"cg", "get", "my-app", "--org", "old-org", "--project", "default"}, saved: &config.File{APIKey: K}, tty: []string{}},
			code: 4, kind: "not_found",
			stderr: errLines(`organization "old-org" (from --org) not found`, "your organizations: acme-prod, my-org")},
		{id: "the organization check itself", status: 404, s: session{args: []string{"quotas", "get"}, saved: gone}, code: 4, kind: "not_found",
			stderr: errLines(`organization "old-org" (from config) not found`, "to list yours, run saladcloud orgs list; to switch, run saladcloud use <org>")},
	})
	o := session{args: list, saved: gone}.run(t)
	if got := fmt.Sprint(o.api.Requests()); got != "[GET /organizations/old-org/projects/default/containers GET /organizations/old-org/quotas]" {
		t.Errorf("requests %s, want the listing and one organization check", got)
	}
	o = session{args: []string{"quotas", "get"}, saved: gone}.run(t)
	if got := fmt.Sprint(o.api.Requests()); got != "[GET /organizations/old-org/quotas]" {
		t.Errorf("requests %s, want only the organization check", got)
	}
	// An organization that exists keeps the command's own not-found error.
	o = session{args: []string{"cg", "get", "missing"}, saved: &config.File{APIKey: K, Organization: "my-org", Project: "default"}}.run(t)
	if e, _ := o.errorDocumentOrTable(); e != `container group "missing" not found in my-org/default` {
		t.Errorf("a group that does not exist: %q", e)
	}
}

// errorDocumentOrTable is the error message from a table-mode run.
func (o outcome) errorDocumentOrTable() (string, string) { return tableError(o.stderr) }
