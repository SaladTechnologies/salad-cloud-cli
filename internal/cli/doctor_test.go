package cli

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

// homeSSH is a file in the run's ~/.ssh as the CLI prints it on this OS.
func homeSSH(name string) string { return filepath.Join("<home>", ".ssh", name) }

// dRow is one doctor row as the table shows it.
type dRow struct{ name, status, detail string }

// doctorTable renders doctor's table: the header, then the rows.
func doctorTable(rows ...dRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-28s%-8s%s\n", "CHECK", "STATUS", "DETAIL")
	for _, r := range rows {
		fmt.Fprintf(&b, "%-28s%-8s%s\n", r.name, r.status, r.detail)
	}
	return b.String()
}

// The rows most cases share; every run here goes to a stand-in API through
// SALAD_CLI_API_BASE_URL, which api-reachable reports.
var (
	rConfig    = dRow{"config", "PASS", "<path>"}
	rReachable = dRow{"api-reachable", "WARN", "<api> (from SALAD_CLI_API_BASE_URL)"}
	rKey       = dRow{"api-key", "PASS", "****...H13m (from config)"}
	rOrg       = dRow{"organization", "PASS", "my-org (from config)"}
	rProject   = dRow{"project", "PASS", "default (from config)"}
	rGateway   = dRow{"ai-gateway-api-key-present", "SKIP", "none (optional)"}
	rSSH       = dRow{"local-ssh-key-present", "SKIP", "none (optional)"}
)

const (
	hReachable = "hint: api-reachable: every command uses this server; remove SALAD_CLI_API_BASE_URL to use " +
		"https://api.salad.com\n"
	hGateway = "hint: ai-gateway-api-key-present: to use the AI Gateway, run saladcloud ai-gateway login\n"
	hSSH     = "hint: local-ssh-key-present: to use container-groups ssh, cp or sftp, run saladcloud ssh-key setup\n"
)

// stubSSH makes ssh present or not, and keys passphrase-protected or not.
func stubSSH(d *Deps, sshInstalled, passphrase bool) {
	d.LookPath = func(name string) (string, error) {
		if sshInstalled {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
	d.HasPassphrase = func(string) (bool, bool) { return passphrase, true }
}

func failed(n int, names string) string {
	checks := "checks"
	if n == 1 {
		checks = "check"
	}
	return fmt.Sprintf(
		"saladcloud: error: %d %s failed: %s\nhint: each failing row's hint above says how to fix it\n",
		n,
		checks,
		names,
	)
}

func TestDoctorRows(t *testing.T) {
	k, o := testAPIKey, testOtherAPIKey
	good := &config.File{APIKey: k, Organization: "my-org", Project: "default"}
	doctor := []string{"doctor"}
	noListing := newFakeAPI()
	noListing.listing = 404
	orgDown := newFakeAPI()
	orgDown.status["/quotas"] = http.StatusServiceUnavailable
	projectDown := newFakeAPI()
	projectDown.status["/containers"] = http.StatusServiceUnavailable
	noDefault := withOrgs(map[string][]string{"my-org": {"staging"}})
	skipOrg := dRow{"organization", "SKIP", "my-org (from config); checking it needs an API key"}
	skipNone := dRow{"organization", "SKIP", "none set"}
	skipProject := dRow{"project", "SKIP", "none set"}
	skipProjectSet := dRow{"project", "SKIP", "default (from config); checking it needs a working organization"}
	runBranches(t, withDeps(func(d *Deps) { stubSSH(d, true, false) }, failingHint([]branchCase{
		{
			id: "all good",
			s:  session{args: doctor, saved: good},
			stdout: doctorTable(
				rConfig,
				rReachable,
				rKey,
				rOrg,
				rProject,
				rGateway,
				rSSH,
			),
			stderr: hReachable + hGateway + hSSH,
		},
		{
			id: "no config file yet", s: session{args: doctor, env: keyEnv},
			stdout: doctorTable(
				dRow{"config", "PASS", "none yet at <path>; using flags and environment"},
				rReachable,
				dRow{
					"api-key",
					"PASS",
					"****...H13m (from SALAD_API_KEY)",
				},
				dRow{"organization", "PASS", "my-org (from SALAD_ORGANIZATION)"},
				dRow{"project", "PASS", "default (from SALAD_PROJECT)"},
				rGateway,
				rSSH,
			),
			stderr: hReachable + hGateway + hSSH,
		},
		{
			id:   "a corrupt config file",
			s:    session{args: doctor, rawConfig: "{", env: keyEnv},
			code: 1,
			kind: "api_error",
			stdout: doctorTable(
				dRow{"config", "FAIL", "<path> cannot be read: unexpected end of JSON input"},
				rReachable,
				dRow{
					"api-key",
					"PASS",
					"****...H13m (from SALAD_API_KEY)",
				},
				dRow{"organization", "PASS", "my-org (from SALAD_ORGANIZATION)"},
				dRow{"project", "PASS", "default (from SALAD_PROJECT)"},
				rGateway,
				rSSH,
			),
			stderr: "hint: config: fix or delete it, then run saladcloud login " +
				"again\n" + hReachable + hGateway + hSSH + failed(
				1,
				"config",
			),
		},
		{
			id:   "no API key",
			s:    session{args: doctor},
			code: 1,
			kind: "api_error",
			stdout: doctorTable(dRow{"config", "PASS", "none yet at <path>; using flags and environment"}, rReachable,
				dRow{"api-key", "FAIL", "not set"}, skipNone, skipProject, rGateway, rSSH),
			stderr: hReachable + "hint: api-key: run saladcloud login, or set SALAD_API_KEY; for the AI Gateway " +
				"only, run saladcloud ai-gateway login\n" +
				hGateway + hSSH + failed(
				1,
				"api-key",
			),
		},
		{
			id: "the AI Gateway only",
			s:  session{args: doctor, env: map[string]string{"SALAD_AI_GATEWAY_API_KEY": testGatewayKey}},
			stdout: doctorTable(
				dRow{"config", "PASS", "none yet at <path>; using flags and environment"},
				rReachable,
				dRow{"api-key", "WARN", "not set; only the AI Gateway is set up"},
				skipNone,
				skipProject,
				dRow{
					"ai-gateway-api-key-present",
					"PASS",
					"****...EFGH (from SALAD_AI_GATEWAY_API_KEY; not tested)",
				},
				rSSH,
			),
			stderr: hReachable + "hint: api-key: to use SaladCloud beyond the AI Gateway, run saladcloud " +
				"login\n" + hSSH,
		},
		{
			id:   "the key rejected, from config",
			s:    session{args: doctor, saved: &config.File{APIKey: o, Organization: "my-org"}},
			code: 1,
			kind: "api_error",
			stdout: doctorTable(
				rConfig,
				rReachable,
				dRow{"api-key", "FAIL", "****...ABCD (from config) rejected"},
				skipOrg,
				skipProject,
				rGateway,
				rSSH,
			),
			stderr: hReachable + "hint: api-key: run saladcloud login to save a new key\n" + hGateway + hSSH + failed(
				1,
				"api-key",
			),
		},
		{
			id:   "the key rejected, SALAD_API_KEY, another saved",
			s:    session{args: doctor, saved: good, env: map[string]string{"SALAD_API_KEY": o}},
			code: 1,
			kind: "api_error",
			stdout: doctorTable(
				rConfig,
				rReachable,
				dRow{"api-key", "FAIL", "****...ABCD (from SALAD_API_KEY) rejected"},
				skipOrg,
				skipProjectSet,
				rGateway,
				rSSH,
			),
			stderr: hReachable + "hint: api-key: remove SALAD_API_KEY to use the saved API key (****...H13m), or set " +
				"it to a working key\n" + hGateway + hSSH +
				"warning: commands use SALAD_API_KEY (****...ABCD) instead of the saved API key " +
				"(****...H13m)\n" + failed(
				1,
				"api-key",
			),
		},
		{
			id:   "the key rejected, SALAD_API_KEY, none saved",
			s:    session{args: doctor, env: map[string]string{"SALAD_API_KEY": o}},
			code: 1,
			kind: "api_error",
			stdout: doctorTable(
				dRow{"config", "PASS", "none yet at <path>; using flags and environment"},
				rReachable,
				dRow{
					"api-key",
					"FAIL",
					"****...ABCD (from SALAD_API_KEY) rejected",
				},
				skipNone,
				skipProject,
				rGateway,
				rSSH,
			),
			stderr: hReachable + "hint: api-key: set SALAD_API_KEY to a working key, or remove it and run saladcloud " +
				"login\n" + hGateway + hSSH + failed(
				1,
				"api-key",
			),
		},
		{
			id:   "a key no key can be",
			s:    session{args: doctor, env: map[string]string{"SALAD_API_KEY": "salad_cloud_user_café000DTb9"}},
			code: 1,
			kind: "api_error",
			stdout: doctorTable(
				dRow{"config", "PASS", "none yet at <path>; using flags and environment"},
				rReachable,
				dRow{
					"api-key",
					"FAIL",
					"****...DTb9 (from SALAD_API_KEY) contains characters an API key cannot have",
				},
				skipNone,
				skipProject,
				rGateway,
				rSSH,
			),
			stderr: hReachable + "hint: api-key: check for quotes or stray characters in " +
				"it\n" + hGateway + hSSH + failed(
				1,
				"api-key",
			),
		},
		{
			id: "no listing, no organization",
			s:  session{args: doctor, saved: &config.File{APIKey: k}, api: noListing},
			stdout: doctorTable(
				rConfig,
				rReachable,
				dRow{"api-key", "WARN", "****...H13m (from config); cannot be checked without an organization"},
				skipNone,
				skipProject,
				rGateway,
				rSSH,
			),
			stderr: hReachable + "hint: api-key: to set one, run saladcloud use <org>\n" + hGateway + hSSH,
		},
		{
			id: "no listing, through the organization",
			s:  session{args: doctor, saved: good, api: noListing},
			stdout: doctorTable(
				rConfig,
				rReachable,
				rKey,
				rOrg,
				rProject,
				rGateway,
				rSSH,
			),
			stderr: hReachable + hGateway + hSSH,
		},
		{
			id:   "no organization",
			s:    session{args: doctor, saved: &config.File{APIKey: k}},
			code: 1,
			kind: "api_error",
			stdout: doctorTable(
				rConfig,
				rReachable,
				rKey,
				dRow{"organization", "FAIL", "none set"},
				skipProject,
				rGateway,
				rSSH,
			),
			stderr: hReachable + "hint: organization: to list yours, run saladcloud orgs list; to set one, run " +
				"saladcloud use <org>\n" + hGateway + hSSH + failed(
				1,
				"organization",
			),
		},
		{
			id:   "no organization, at a terminal",
			s:    session{args: doctor, saved: &config.File{APIKey: k}, tty: []string{}},
			code: 1,
			kind: "api_error",
			stdout: doctorTable(
				rConfig,
				rReachable,
				rKey,
				dRow{"organization", "FAIL", "none set"},
				skipProject,
				rGateway,
				rSSH,
			),
			stderr: hReachable + "hint: organization: your organizations: acme-prod, my-org; to set one, run " +
				"saladcloud use <org>\n" + hGateway + hSSH + failed(
				1,
				"organization",
			),
		},
		{
			id: "the organization from SALAD_ORGANIZATION not found",
			s: session{
				args:  doctor,
				saved: &config.File{APIKey: k},
				env:   map[string]string{"SALAD_ORGANIZATION": "acme-stagng"},
			},
			code: 1,
			kind: "api_error",
			stdout: doctorTable(
				rConfig,
				rReachable,
				rKey,
				dRow{"organization", "FAIL", `"acme-stagng" (from SALAD_ORGANIZATION) not found`},
				dRow{"project", "SKIP", "none set"},
				rGateway,
				rSSH,
			),
			stderr: hReachable + "hint: organization: to list yours, run saladcloud orgs list; fix or remove " +
				"SALAD_ORGANIZATION\n" + hGateway + hSSH + failed(
				1,
				"organization",
			),
		},
		{
			id:   "the saved organization not found",
			s:    session{args: doctor, saved: &config.File{APIKey: k, Organization: "old-org"}, tty: []string{}},
			code: 1,
			kind: "api_error",
			stdout: doctorTable(
				rConfig,
				rReachable,
				rKey,
				dRow{"organization", "FAIL", `"old-org" (from config) not found`},
				skipProject,
				rGateway,
				rSSH,
			),
			stderr: hReachable + "hint: organization: your organizations: acme-prod, my-org; to switch, run " +
				"saladcloud use <org>\n" + hGateway + hSSH + failed(
				1,
				"organization",
			),
		},
		{
			id: "an organization name that cannot be",
			s: session{
				args:  doctor,
				saved: &config.File{APIKey: k},
				env:   map[string]string{"SALAD_ORGANIZATION": "Acme Prod"},
			},
			code: 1,
			kind: "api_error",
			stdout: doctorTable(
				rConfig,
				rReachable,
				rKey,
				dRow{"organization", "FAIL", `"Acme Prod" (from SALAD_ORGANIZATION) is not a valid organization name`},
				skipProject,
				rGateway,
				rSSH,
			),
			stderr: hReachable + "hint: organization: it looks like a display name; use the name from the portal " +
				"URL, the part after /organizations/\n" + hGateway + hSSH + failed(
				1,
				"organization",
			),
		},
		{
			id: "the organization could not be checked",
			s:  session{args: doctor, saved: good, api: orgDown},
			stdout: doctorTable(
				rConfig,
				rReachable,
				rKey,
				dRow{"organization", "WARN", "my-org (from config); could not be checked: the API answered 503"},
				skipProjectSet,
				rGateway,
				rSSH,
			),
			stderr: hReachable + "hint: organization: try again in a minute\n" + hGateway + hSSH,
		},
		{
			id: "no project, default exists",
			s:  session{args: doctor, saved: &config.File{APIKey: k, Organization: "my-org"}},
			stdout: doctorTable(
				rConfig,
				rReachable,
				rKey,
				rOrg,
				dRow{"project", "WARN", `none set, using "default"`},
				rGateway,
				rSSH,
			),
			stderr: hReachable + "hint: project: container group and queue commands warn each time; to choose one, " +
				"set SALAD_PROJECT or run saladcloud use my-org/<project>\n" + hGateway + hSSH,
		},
		{
			id: "no project, no default",
			s:  session{args: doctor, saved: &config.File{APIKey: k, Organization: "my-org"}, api: noDefault},
			stdout: doctorTable(
				rConfig,
				rReachable,
				rKey,
				rOrg,
				dRow{"project", "WARN", `none set; my-org has no "default" project`},
				rGateway,
				rSSH,
			),
			stderr: hReachable + "hint: project: container groups and queues need one; create one at " +
				"https://portal.salad.com/organizations/my-org/projects/new, then run saladcloud use " +
				"my-org/<project>\n" + hGateway + hSSH,
		},
		{
			id: "the saved project is another organization's",
			s: session{
				args:  []string{"doctor", "--org", "acme-prod"},
				saved: &config.File{APIKey: k, Organization: "my-org", Project: "staging"},
			},
			stdout: doctorTable(
				rConfig,
				rReachable,
				rKey,
				dRow{"organization", "PASS", "acme-prod (from --org)"},
				dRow{
					"project",
					"WARN",
					`none set for acme-prod, using "default"; the saved project "staging" is for my-org`,
				},
				rGateway,
				rSSH,
			),
			stderr: hReachable + "hint: project: to choose one for acme-prod, run saladcloud use " +
				"acme-prod/<project>\n" + hGateway + hSSH,
		},
		{
			id:   "the saved project not found",
			s:    session{args: doctor, saved: &config.File{APIKey: k, Organization: "my-org", Project: "stagng"}},
			code: 1,
			kind: "api_error",
			stdout: doctorTable(
				rConfig,
				rReachable,
				rKey,
				rOrg,
				dRow{"project", "FAIL", `"stagng" (from config) not found in my-org`},
				rGateway,
				rSSH,
			),
			stderr: hReachable + "hint: project: to switch, run saladcloud use " +
				"my-org/<project>\n" + hGateway + hSSH + failed(
				1,
				"project",
			),
		},
		{
			id:   "SALAD_PROJECT not found",
			s:    session{args: doctor, saved: good, env: map[string]string{"SALAD_PROJECT": "stagng"}},
			code: 1,
			kind: "api_error",
			stdout: doctorTable(
				rConfig,
				rReachable,
				rKey,
				rOrg,
				dRow{"project", "FAIL", `"stagng" (from SALAD_PROJECT) not found in my-org`},
				rGateway,
				rSSH,
			),
			stderr: hReachable + "hint: project: fix or remove SALAD_PROJECT, or create it at " +
				"https://portal.salad.com/organizations/my-org/projects/new\n" + hGateway + hSSH +
				"warning: commands use SALAD_PROJECT (stagng) instead of the saved project (default)\n" + failed(
				1,
				"project",
			),
		},
		{
			id: "a project name that cannot be",
			s: session{
				args:  doctor,
				saved: &config.File{APIKey: k, Organization: "my-org"},
				env:   map[string]string{"SALAD_PROJECT": "Staging Env"},
			},
			code: 1,
			kind: "api_error",
			stdout: doctorTable(
				rConfig,
				rReachable,
				rKey,
				rOrg,
				dRow{"project", "FAIL", `"Staging Env" (from SALAD_PROJECT) is not a valid project name`},
				rGateway,
				rSSH,
			),
			stderr: hReachable + "hint: project: it looks like a display name; use the name from the portal URL, the " +
				"part after /projects/\n" + hGateway + hSSH + failed(
				1,
				"project",
			),
		},
		{
			id: "the project could not be checked",
			s:  session{args: doctor, saved: good, api: projectDown},
			stdout: doctorTable(
				rConfig,
				rReachable,
				rKey,
				rOrg,
				dRow{"project", "WARN", "default (from config); could not be checked: the API answered 503"},
				rGateway,
				rSSH,
			),
			stderr: hReachable + "hint: project: try again in a minute\n" + hGateway + hSSH,
		},
		{
			id: "an AI Gateway key and an SSH key",
			s: session{
				args:   doctor,
				sshKey: true,
				saved: &config.File{
					APIKey:        k,
					Organization:  "my-org",
					Project:       "default",
					GatewayAPIKey: testGatewayKey,
				},
			},
			stdout: doctorTable(
				rConfig,
				rReachable,
				rKey,
				rOrg,
				rProject,
				dRow{"ai-gateway-api-key-present", "PASS", "****...EFGH (from config; not tested)"},
				dRow{"local-ssh-key-present", "PASS", homeSSH("id_ed25519.pub") + " (registration not checked)"},
			),
			stderr: hReachable,
		},
		{
			id: "a variable overrides, at a terminal",
			s: session{
				args:  doctor,
				saved: good,
				tty:   []string{},
				env:   map[string]string{"SALAD_ORGANIZATION": "acme-prod"},
			},
			stdout: doctorTable(
				rConfig,
				rReachable,
				rKey,
				dRow{"organization", "PASS", "acme-prod (from SALAD_ORGANIZATION)"},
				dRow{
					"project",
					"WARN",
					`none set for acme-prod, using "default"; the saved project "default" is for my-org`,
				},
				rGateway,
				rSSH,
			),
			stderr: hReachable + "hint: project: to choose one for acme-prod, run saladcloud use " +
				"acme-prod/<project>\n" + hGateway + hSSH +
				"warning: commands use SALAD_ORGANIZATION (acme-prod) instead of the saved organization (my-org). To " +
				"remove the variable:\n" + removeOrgSteps,
		},
		{
			id:   "two checks fail",
			s:    session{args: doctor, rawConfig: "{"},
			code: 1,
			kind: "api_error",
			stdout: doctorTable(
				dRow{"config", "FAIL", "<path> cannot be read: unexpected end of JSON input"},
				rReachable,
				dRow{"api-key", "FAIL", "not set"},
				skipNone,
				skipProject,
				rGateway,
				rSSH,
			),
			stderr: "hint: config: fix or delete it, then run saladcloud login again\n" + hReachable +
				"hint: api-key: run saladcloud login, or set SALAD_API_KEY; for the AI Gateway only, run saladcloud " +
				"ai-gateway login\n" + hGateway + hSSH + failed(2, "config, api-key"),
		},
	})))
}

// failingHint sets the json-mode hint of a failing doctor run: the error
// document has no table above it to point at.
func failingHint(cases []branchCase) []branchCase {
	for i := range cases {
		if cases[i].code != 0 {
			cases[i].jsonHint = "each failing row's hint says how to fix it"
		}
	}
	return cases
}

// TestDoctorSSHRows: the local SSH key row's warnings.
func TestDoctorSSHRows(t *testing.T) {
	good := &config.File{APIKey: testAPIKey, Organization: "my-org", Project: "default", GatewayAPIKey: testGatewayKey}
	rG := dRow{"ai-gateway-api-key-present", "PASS", "****...EFGH (from config; not tested)"}
	t.Run("a passphrase", func(t *testing.T) {
		runBranches(t, withDeps(func(d *Deps) { stubSSH(d, true, true) }, []branchCase{
			{
				id: "a key of your own",
				s:  session{args: []string{"doctor"}, saved: good, sshKey: true},
				stdout: doctorTable(
					rConfig,
					rReachable,
					rKey,
					rOrg,
					rProject,
					rG,
					dRow{
						"local-ssh-key-present",
						"WARN",
						homeSSH(
							"id_ed25519.pub",
						) + " has a passphrase, which cp and one-shot ssh commands cannot ask for",
					},
				),
				stderr: hReachable + "hint: local-ssh-key-present: run ssh-add " + homeSSH(
					"id_ed25519",
				) + " first; to generate a key without one, run saladcloud ssh-key setup --generate\n",
			},
			{
				id: "the CLI's own key",
				s:  session{args: []string{"doctor"}, saved: good, sshKey: true, sshKeyName: "saladcloud_ed25519.pub"},
				stdout: doctorTable(
					rConfig,
					rReachable,
					rKey,
					rOrg,
					rProject,
					rG,
					dRow{
						"local-ssh-key-present",
						"WARN",
						homeSSH(
							"saladcloud_ed25519.pub",
						) + " has a passphrase, which cp and one-shot ssh commands cannot ask for",
					},
				),
				stderr: hReachable + "hint: local-ssh-key-present: run ssh-add " + homeSSH(
					"saladcloud_ed25519",
				) + " first\n",
			},
		}))
	})
	t.Run("ssh not installed", func(t *testing.T) {
		for _, tc := range []struct{ os, hint string }{
			{"windows", "add the OpenSSH Client optional feature in Windows Settings, then open a new terminal"},
			{"darwin", "macOS includes it at /usr/bin/ssh; check that /usr/bin is on your PATH"},
			{"linux", "install your distribution's OpenSSH client (openssh-client on Debian and Ubuntu, " +
				"openssh-clients on Fedora)"},
		} {
			runBranches(t, withDeps(func(d *Deps) { stubSSH(d, false, false) }, []branchCase{
				{
					id: tc.os,
					s:  session{args: []string{"doctor"}, saved: good, sshKey: true, os: tc.os},
					stdout: doctorTable(
						rConfig,
						rReachable,
						rKey,
						rOrg,
						rProject,
						rG,
						dRow{"local-ssh-key-present", "WARN", "key found, but ssh is not installed"},
					),
					stderr: hReachable + "hint: local-ssh-key-present: " + tc.hint + "\n",
				},
			}))
		}
	})
}

// TestDoctorJSON: rows carry their hint (null under a PASS row), and a
// failing run's error says the rows' hints say how to fix it.
func TestDoctorJSON(t *testing.T) {
	o := withMode(session{
		args: []string{"doctor"}, saved: &config.File{APIKey: testOtherAPIKey},
		deps: func(d *Deps) { stubSSH(d, true, false) },
	}, "json").run(t)
	doc := o.document(t)
	items := assertAs[[]any](t, doc["items"])
	first, key := assertAs[map[string]any](t, items[0]), assertAs[map[string]any](t, items[2])
	if first["name"] != "config" || first["hint"] != nil || key["name"] != "api-key" ||
		key["hint"] != "run saladcloud login to save a new key" {
		t.Errorf("rows %v / %v", first, key)
	}
	e, err := o.errorDocument()
	if err != nil || e["message"] != "1 check failed: api-key" ||
		e["hint"] != "each failing row's hint says how to fix it" {
		t.Errorf("error %v %v", e, err)
	}
}

// TestDoctorReachable: the API row names https://api.salad.com when no
// stand-in is set, and a failure says to check the network.
func TestDoctorReachable(t *testing.T) {
	app := newTestRun(t)
	old := app.ProductionAPI
	t.Cleanup(func() { app.ProductionAPI = old })
	app.setenv("SALAD_CLI_API_BASE_URL", "")
	srv := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }),
	)
	defer srv.Close()
	app.ProductionAPI = srv.URL
	d := &doctorRun{ctx: t.Context(), app: app.App}
	if r := d.reachableRow(); r.Status != checkPass || r.Detail != "https://api.salad.com" || r.Hint != nil ||
		!d.reachable {
		t.Errorf("reachable: %+v", r)
	}
	app.ProductionAPI = "http://127.0.0.1:1"
	d = &doctorRun{ctx: t.Context(), app: app.App}
	if r := d.reachableRow(); r.Status != checkFail || !strings.HasPrefix(r.Detail, "https://api.salad.com: ") ||
		r.Hint == nil ||
		*r.Hint != "check your network or proxy settings" ||
		d.reachable {
		t.Errorf("unreachable: %+v", r)
	}
}
