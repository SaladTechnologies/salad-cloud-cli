package cli

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

// TestCpErrorDocument: cp in json and yaml mode leaves stderr to warnings,
// notes and, when sftp fails, one error document carrying sftp's exit code
// and last line; with or without an instance prefix (without one, a note
// names the instance).
func TestCpErrorDocument(t *testing.T) {
	t.Parallel()
	for _, fake := range []string{"ok", "fail"} {
		for _, src := range []string{"my-app:/etc/hostname", "my-app/8a2f:/etc/hostname"} {
			for _, mode := range []string{"json", "yaml"} {
				o := session{
					args: []string{"cg", "cp", src, filepath.Join(t.TempDir(), "h"), "-o", mode},
					env:  with(keyEnv(), "SALAD_CLI_TEST_FAKE_SFTP", fake),
					deps: func(d *Deps) { d.SFTPProgram = os.Args[0] },
				}.run(t)
				checkMachineStderr(t, o)
				if fake != "ok" {
					checkCpChildExit(t, src+" "+mode, &o)
				} else if o.code != 0 {
					t.Errorf("%s %s: exit %d\n%s", src, mode, o.code, o.stderr)
				}
			}
		}
	}
}

// checkCpChildExit checks the outcome of the cp run named id, whose sftp failed: exit 1 with a child_exit error
// document that ends with sftp's last line.
func checkCpChildExit(t *testing.T, id string, o *outcome) {
	t.Helper()
	doc, err := o.errorDocument()
	if err != nil || o.code != 1 || doc["kind"] != "child_exit" ||
		!strings.HasSuffix(
			assertAs[string](t, doc["message"]),
			`exited with status 1; remote open("/etc/hostname"): Permission denied`,
		) {
		t.Errorf("%s: exit %d, document %v (%v)", id, o.code, doc, err)
	}
}

// TestShadowedFlagSaysWhy: --project on an organization-scoped command and
// --org on a command that reads neither are unknown flags whose hint says
// why.
func TestShadowedFlagSaysWhy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"gpus", "list", "--project", "default"}, "gpus list is organization-scoped and takes no --project; " +
			"run saladcloud gpus list"},
		{[]string{"use", "my-org", "--org", "acme-prod"}, "use takes neither --org nor --project; run saladcloud use " +
			"<org>[/<project>]"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			t.Parallel()
			app := newTestRun(t)
			_, err := app.runRoot(t, tc.args...)
			var cerr *clierr.Error
			if !errors.As(err, &cerr) || cerr.Code != clierr.CodeUsage ||
				cerr.Message != "unknown flag: "+tc.args[len(tc.args)-2] ||
				!strings.HasPrefix(cerr.HintText(), tc.want) ||
				!strings.Contains(cerr.HintText(), "to list its flags") {
				t.Errorf("%v: %v (hint %q)", tc.args, err, cerr.HintText())
			}
		})
	}
}

// TestEveryCommandIsClassified: each top-level command is listed as reading
// both --org and --project, the organization only, or neither, and its
// commands offer exactly the flags they read; a new command must be added
// here, so it cannot offer a flag it ignores by default.
func TestEveryCommandIsClassified(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	classes := map[string]string{
		"availability": "both", "container-groups": "both", "doctor": "both", "login": "both",
		"logs": "both", "queues": "both", "whoami": "both",
		"gpus": "org", "inference": "org", "quotas": "org", "webhook-secret": "org",
		"agent": "neither", "ai-gateway": "neither", "changelog": "neither", "completion": "neither",
		"config": "neither", "logout": "neither", "orgs": "neither", "ssh-key": "neither",
		"use": "neither", "version": "neither",
	}
	for _, top := range app.root.Commands() {
		if top.Hidden || top.Name() == "help" {
			continue
		}
		class, ok := classes[top.Name()]
		if !ok {
			t.Errorf(
				"%s is not classified: add it to this table and, if it reads no organization or project, to "+
					"contextFreeCommands "+
					"or orgScoped",
				top.Name(),
			)
			continue
		}
		walkTree(top, func(c *cobra.Command) {
			org, project := readsOrg(c), readsProject(c)
			if want := class != "neither"; org != want {
				t.Errorf("%s: offers --org %t, want %t", c.CommandPath(), org, want)
			}
			if want := class == "both"; project != want {
				t.Errorf("%s: offers --project %t, want %t", c.CommandPath(), project, want)
			}
		})
	}
}

// TestKeyLikeSavedNames: config set refuses a key-like organization or
// project, and one already saved is hidden by the config commands, logout,
// doctor and the run-time warning; only config get returns it.
func TestKeyLikeSavedNames(t *testing.T) {
	t.Parallel()
	k := testAPIKey
	o := session{args: []string{"config", "set", "organization", k}, saved: savedLogin()}.run(t)
	if o.code != 6 || !strings.Contains(o.stderr, "that looks like an API key, not an organization name") ||
		o.config.Organization == k {
		t.Errorf("config set: exit %d, saved %q\n%s", o.code, o.config.Organization, o.stderr)
	}
	saved := &config.File{APIKey: k, Organization: k, Project: "staging"}
	commands := [][]string{
		{"config", "list"},
		{"config", "list", "-o", "json"},
		{"logout"},
		{"logout", "-o", "json"},
		{"cg", "list", "--org", "my-org"},
		{"doctor", "--org", "my-org"},
	}
	outs := make([]outcome, 0, len(commands))
	for _, args := range commands {
		o := session{args: args, saved: saved}.run(t)
		outs = append(outs, o)
		if strings.Contains(o.all(), k) {
			t.Errorf("%v printed the key:\n%s", args, o.all())
		}
	}
	if !strings.Contains(outs[0].stdout, "hidden, looks like an API key") ||
		!strings.Contains(outs[1].stdout, "****...H13m") {
		t.Errorf("config list:\n%s\n%s", outs[0].stdout, outs[1].stdout)
	}
	if !strings.Contains(outs[4].stderr, `the saved project "staging" is for "hidden, looks like an API key"`) {
		t.Errorf("cg list warning:\n%s", outs[4].stderr)
	}
	g := (session{args: []string{"config", "get", "organization"}, saved: saved}).run(
		t,
	)
	if !strings.Contains(
		g.stdout,
		k,
	) {
		t.Errorf("config get does not return the saved value: %q", g.stdout)
	}
}

// TestHiddenKeyReadWithCI: with CI set, a key piped to --api-key - at a real
// terminal is still read hidden, never echoed.
func TestHiddenKeyReadWithCI(t *testing.T) {
	t.Parallel()
	o := session{
		args: []string{"config", "set", "api-key", "-"},
		tty:  []string{testAPIKey},
		env:  map[string]string{"CI": "true"},
	}.run(
		t,
	)
	if o.code != 0 || o.config.APIKey != testAPIKey || strings.Contains(o.device, testAPIKey) {
		t.Errorf("exit %d, saved %q, terminal %q", o.code, o.config.APIKey, o.device)
	}
}

// TestSSHQuietInMachineMode: in json and yaml mode OpenSSH's informational
// lines (the first-connection host key line) are kept off stderr.
func TestSSHQuietInMachineMode(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	t.Cleanup(func() { app.setOutputMode(t, "table") })
	for _, mode := range []string{"table", "json", "yaml"} {
		app.setOutputMode(t, mode)
		got := slices.Contains(app.hostKeyOptions("8a2f13ba-01c2-4a29-9c4d-52c7e5a44e37"), "LogLevel=ERROR")
		if want := mode != "table"; got != want {
			t.Errorf("%s mode: LogLevel=ERROR %t, want %t", mode, got, want)
		}
	}
}

// TestOrgsListingWithoutItems: orgs list and orgs get report a listing that
// came back without its items as an error, not as no organizations.
func TestOrgsListingWithoutItems(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	app.stubAPI(t, map[string]string{"/v1/organizations": `{}`})
	for _, args := range [][]string{{"orgs", "list"}, {"orgs", "get", "my-org"}} {
		_, err := app.runRoot(t, args...)
		var cerr *clierr.Error
		if !errors.As(err, &cerr) || cerr.Kind != clierr.KindAPIError ||
			!strings.Contains(cerr.Message, "cannot read") {
			t.Errorf("%v: %v, want an api_error", args, err)
		}
	}
}
