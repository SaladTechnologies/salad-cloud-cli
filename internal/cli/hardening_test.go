package cli

import (
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// TestCheckSSHUser: --user is a user name, never something ssh would read as an option or ssh_config as a second
// directive.
func TestCheckSSHUser(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		user string
		ok   bool
	}{
		{"root", true},
		{"www-data", true},
		{"_apt", true},
		{"user.name", true},
		{"1000", true},
		{"", false},
		{"-oProxyCommand=calc", false},
		{".hidden", false},
		{"root\n    ProxyCommand calc", false},
		{"two words", false},
		{"me@example.com", false},
	} {
		cerr := checkSSHUser(tt.user)
		if (cerr == nil) != tt.ok {
			t.Errorf("checkSSHUser(%q) = %v, want ok %v", tt.user, cerr, tt.ok)
		}
		if cerr != nil && (cerr.Code != clierr.CodeUsage || strings.ContainsAny(cerr.Message, "\n\r")) {
			t.Errorf("checkSSHUser(%q) = exit %d %q, want a one-line usage error", tt.user, cerr.Code, cerr.Message)
		}
	}
}

// TestSSHUserCheckedBeforeAnyRequest: ssh, sftp and cp refuse a --user that is an ssh option without a key set, so
// before they build a client.
func TestSSHUserCheckedBeforeAnyRequest(t *testing.T) {
	t.Parallel()
	const option = "--user=-oProxyCommand=calc"
	for _, args := range [][]string{
		{"container-groups", "ssh", "my-app", option},
		{"container-groups", "sftp", "my-app", option},
		{"container-groups", "cp", "my-app:/data/out.json", "out.json", option},
	} {
		t.Run(args[1], func(t *testing.T) {
			t.Parallel()
			got := newTestRun(t).runExecute(t, args...)
			if got.code != clierr.CodeUsage || !strings.Contains(got.stderr, "invalid --user") {
				t.Errorf("exit %d, stderr %q; want exit 2 and invalid --user", got.code, got.stderr)
			}
		})
	}
}

// TestCpRefusesControlCharacters: a path with a line break never reaches the sftp batch file, where a line can run a
// local command.
func TestCpRefusesControlCharacters(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	for _, args := range [][]string{
		{"model.bin\n!calc", "my-app:/models/model.bin"},
		{"my-app:/outputs/a\rb", "./out"},
		{"./in", "my-app:/data/\x1b[2J"},
	} {
		_, cerr := app.cpPlanOf(args)
		if cerr == nil || cerr.Code != clierr.CodeUsage ||
			!strings.Contains(cerr.Message, "holds a control character") {
			t.Errorf("cpPlanOf(%q) = %v, want the control-character usage error", args, cerr)
		}
		if cerr != nil && strings.ContainsAny(cerr.Message, "\n\r\x1b") {
			t.Errorf("cpPlanOf(%q): message %q shows the character itself", args, cerr.Message)
		}
	}
	if _, cerr := app.cpPlanOf([]string{"./in dir/model v2.bin", "my-app:/models/model v2.bin"}); cerr != nil {
		t.Errorf("a path with spaces was refused: %v", cerr)
	}
}

// TestSSHEndpointFromAPIIsChecked: an SSH endpoint that is not an address and a port is refused by ssh, sftp and cp
// and left out of --config, so nothing the API sends becomes an ssh option or an ssh_config directive.
func TestSSHEndpointFromAPIIsChecked(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	const goodID = "8a2f13ba-01c2-4a29-9c4d-52c7e5a44e37"
	bad := []containergroups.ContainerGroupInstance{
		testInstance(goodID, "running", "203.0.113.7\n    ProxyCommand calc", 30123),
		testInstance(goodID, "running", "-oProxyCommand=calc", 30123),
		testInstance(goodID, "running", "203.0.113.7", 0),
		testInstance(goodID, "running", "203.0.113.7", 70000),
		testInstance("8a2f\n    ProxyCommand calc", "running", "203.0.113.7", 30123),
	}
	for i := range bad {
		if _, cerr := sshTargetOf(&bad[i], "my-app"); cerr == nil || cerr.Code != clierr.CodeGeneric ||
			strings.ContainsAny(cerr.Message, "\n\r") {
			t.Errorf("sshTargetOf(%q, %d) = %v, want a one-line error", *bad[i].SshIp, *bad[i].SshPort, cerr)
		}
	}
	good := []containergroups.ContainerGroupInstance{
		testInstance(goodID, "running", "203.0.113.7", 30123),
		testInstance("dddd4444", "running", "2001:db8::7", 30124),
		testInstance("eeee5555", "running", "relay.example.com", 22),
	}
	text, n := app.renderSSHConfigIdentity("my-app", append(bad, good...), "root", "")
	if n != len(good) || strings.Contains(text, "ProxyCommand") {
		t.Errorf("--config rendered %d blocks, want %d and no ProxyCommand:\n%s", n, len(good), text)
	}
}

// TestRemoteBaseURLWarning: a base-URL variable naming another machine draws one warning per run, in json mode
// too, and a stand-in on this machine draws none.
func TestRemoteBaseURLWarning(t *testing.T) {
	t.Parallel()
	t.Run("another machine", func(t *testing.T) {
		t.Parallel()
		app := newTestRun(t)
		app.setenv("SALAD_CLI_API_BASE_URL", "https://api.example.com/api/public")
		app.setOutputMode(t, "json")
		got := app.captureStderr(t, func() {
			app.apiOptions()
			app.apiOptions()
		})
		want := `warning: SALAD_CLI_API_BASE_URL is set, so requests and the key they carry go to ` +
			`"api.example.com", not to SaladCloud` + "\n"
		if got != want {
			t.Errorf("stderr = %q, want %q", got, want)
		}
	})
	t.Run("this machine", func(t *testing.T) {
		t.Parallel()
		app := newTestRun(t)
		app.setenv("SALAD_CLI_API_BASE_URL", "http://127.0.0.1:8080")
		if got := app.captureStderr(t, func() { app.apiOptions() }); got != "" {
			t.Errorf("stderr = %q, want nothing", got)
		}
	})
	t.Run("plain http to another machine", func(t *testing.T) {
		t.Parallel()
		app := newTestRun(t)
		app.setenv("SALAD_API_KEY", "test-key")
		app.setenv("SALAD_CLI_API_BASE_URL", "http://api.example.com")
		got := app.runExecute(t, "quotas", "get", "--org", "my-org")
		if got.code != clierr.CodeUsage || !strings.Contains(got.stderr, "must be an https URL") {
			t.Errorf("exit %d, stderr %q; want exit 2 and must be an https URL", got.code, got.stderr)
		}
	})
}
