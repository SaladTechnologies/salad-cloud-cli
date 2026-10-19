package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

// TestKeyLikeNamesAreNeverShown: a key in an organization or project slot
// (equal to a key in use or saved, or longer than any name) fails before any
// request, by where it came from, without the value.
func TestKeyLikeNamesAreNeverShown(t *testing.T) {
	k := testAPIKey
	saved := &config.File{APIKey: k, Organization: "my-org", Project: "default"}
	long := strings.Repeat("a", 64)
	runBranches(t, []branchCase{
		{
			id: "the API key in SALAD_ORGANIZATION",
			s: session{
				args:  []string{"quotas", "get"},
				saved: saved,
				env:   map[string]string{"SALAD_ORGANIZATION": k},
			},
			code: 6,
			kind: "invalid_input",
			stderr: errLines(
				"SALAD_ORGANIZATION looks like an API key, not an organization name",
				"set it to the organization's name from the portal URL, the part after /organizations/; the key goes "+
					"in "+
					"SALAD_API_KEY",
			),
		},
		{
			id:   "a value longer than a name in --org",
			s:    session{args: []string{"quotas", "get", "--org", long}, saved: saved},
			code: 6,
			kind: "invalid_input",
			stderr: errLines(
				"--org looks like an API key, not an organization name",
				"pass the organization's name from the portal URL, the part after /organizations/",
			),
		},
		{
			id:   "the API key in SALAD_PROJECT",
			s:    session{args: []string{"cg", "list"}, saved: saved, env: map[string]string{"SALAD_PROJECT": k}},
			code: 6,
			kind: "invalid_input",
			stderr: errLines(
				"SALAD_PROJECT looks like an API key, not a project name",
				"set it to the project's name from the portal URL, the part after /projects/; the key goes in "+
					"SALAD_API_KEY",
			),
		},
		{
			id:   "the API key saved as the organization",
			s:    session{args: []string{"cg", "list"}, saved: &config.File{APIKey: k, Organization: k}},
			code: 6,
			kind: "invalid_input",
			stderr: errLines(
				"the saved organization looks like an API key, not an organization name",
				"to switch, run saladcloud use <org>",
			),
		},
		{
			id:   "the API key as use's argument",
			s:    session{args: []string{"use", k}, saved: saved},
			code: 6,
			kind: "invalid_input",
			stderr: errLines(
				"that looks like an API key, not an organization name",
				"the target is <org> or <org>/<project>, for example saladcloud use acme-prod/staging",
			),
		},
	})

	// whoami and doctor report it without the value or a request for it.
	env := map[string]string{"SALAD_ORGANIZATION": k}
	o := session{args: []string{"whoami"}, saved: saved, env: env}.run(t)
	if o.code != 0 ||
		!strings.Contains(o.stdout, "hidden, looks like an API key (from SALAD_ORGANIZATION; saved: my-org)") {
		t.Errorf("whoami: exit %d\n%s", o.code, o.stdout)
	}
	j := session{args: []string{"whoami", "-o", "json"}, saved: saved, env: env}.run(t)
	if doc := j.document(t); doc["organization"] != "****...H13m" || doc["organization_found"] != false {
		t.Errorf("whoami json: organization %v, organization_found %v", doc["organization"], doc["organization_found"])
	}
	d := session{args: []string{"doctor", "-o", "json"}, saved: saved, env: env}.run(t)
	items := asType[[]any](d.document(t)["items"])
	shown := false
	for _, it := range items {
		if row := asType[map[string]any](it); row["name"] == "organization" {
			shown = row["status"] == "fail" &&
				row["detail"] == "(from SALAD_ORGANIZATION) looks like an API key, not an organization name"
		}
	}
	if !shown {
		t.Errorf("doctor: %s", d.stdout)
	}
	// A variable warning after a config write hides it too.
	u := session{args: []string{"use", "my-org"}, saved: saved, env: env}.run(t)
	if !strings.Contains(u.stderr, "commands will use SALAD_ORGANIZATION (hidden, looks like an API key)") {
		t.Errorf("use: stderr\n%s", u.stderr)
	}
	for _, r := range []outcome{o, j, d, u} {
		if strings.Contains(r.all(), k) {
			t.Errorf("the key was printed:\n%s", r.all())
		}
		for _, req := range r.api.Requests() {
			if strings.Contains(req, k) {
				t.Errorf("the key was sent in a request path: %s", req)
			}
		}
	}
}

// TestInvalidOrganizationName: whoami reports a name that cannot exist
// without sending it, and orgs, which never uses the organization, lists
// regardless.
func TestInvalidOrganizationName(t *testing.T) {
	saved := &config.File{APIKey: testAPIKey}
	env := map[string]string{"SALAD_ORGANIZATION": "Acme Prod"}
	o := session{args: []string{"whoami"}, saved: saved, env: env}.run(t)
	if o.code != 0 ||
		!strings.Contains(o.stdout, `"Acme Prod" (from SALAD_ORGANIZATION), not a valid organization name`) {
		t.Errorf("whoami: exit %d\n%s%s", o.code, o.stdout, o.stderr)
	}
	for _, req := range o.api.Requests() {
		if strings.Contains(req, "Acme") {
			t.Errorf("whoami sent the name: %s", req)
		}
	}
	doc := (session{args: []string{"whoami", "-o", "json"}, saved: saved, env: env}).run(t).
		document(t)
	if doc["organization_found"] != false {
		t.Errorf("organization_found = %v, want false", doc["organization_found"])
	}
	if l := (session{args: []string{"orgs", "list"}, saved: saved, env: env}).run(t); l.code != 0 {
		t.Errorf("orgs list: exit %d\n%s", l.code, l.stderr)
	}
}

// TestTrimmedValues: a trailing carriage return (a Windows-saved file
// sourced in WSL or Git Bash) neither breaks a name nor reads as another key.
func TestTrimmedValues(t *testing.T) {
	saved := &config.File{APIKey: testAPIKey, Organization: "my-org", Project: "default"}
	o := session{
		args:  []string{"quotas", "get"},
		saved: saved,
		env:   map[string]string{"SALAD_ORGANIZATION": "my-org\r"},
	}.run(
		t,
	)
	if o.code != 0 || len(o.api.Requests()) == 0 || o.api.Requests()[0] != "GET /organizations/my-org/quotas" {
		t.Errorf("exit %d, requests %v\n%s", o.code, o.api.Requests(), o.stderr)
	}
	d := session{
		args:  []string{"doctor"},
		saved: saved,
		env:   map[string]string{"SALAD_API_KEY": testAPIKey + "\r"},
	}.run(
		t,
	)
	if strings.Contains(d.stderr, "warning: commands use SALAD_API_KEY") {
		t.Errorf("the same key with a carriage return read as another:\n%s", d.stderr)
	}
}

// TestFlagsACommandDoesNotRead: a command that never reads the
// organization or the project neither offers nor accepts --org or
// --project.
func TestFlagsACommandDoesNotRead(t *testing.T) {
	saved := &config.File{APIKey: testAPIKey, Organization: "my-org", Project: "default"}
	for _, args := range [][]string{
		{"use", "my-org", "--project", "staging"},
		{"ai-gateway", "models", "list", "--org", "my-org"},
		{"orgs", "list", "--org", "my-org"},
		{"version", "--org", "my-org"},
		{"gpus", "list", "--project", "default"},
	} {
		o := session{args: args, saved: saved}.run(t)
		flag := args[len(args)-2]
		if o.code != 2 || !strings.Contains(o.stderr, "unknown flag: "+flag) {
			t.Errorf("%v: exit %d, stderr %q; want 2 and unknown flag", args, o.code, o.stderr)
		}
	}
	if o := (session{args: []string{"quotas", "get", "--org", "my-org"}, saved: saved}).run(t); o.code != 0 {
		t.Errorf("quotas get --org: exit %d\n%s", o.code, o.stderr)
	}
	walkTree(rootCmd, func(c *cobra.Command) {
		top := c
		for top.Parent() != nil && top.Parent() != rootCmd {
			top = top.Parent()
		}
		if top == rootCmd || !contextFreeCommands()[top.Name()] {
			return
		}
		if readsOrg(c) || readsProject(c) {
			t.Errorf("%s offers --org or --project, which it never reads", c.CommandPath())
		}
	})
}
