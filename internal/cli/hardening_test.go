package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

// TestCheckSSHUser: --user is a user name, one word of letters, digits, '.', '_' and '-'.
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
		{"-root", false},
		{".hidden", false},
		{"root\nmore", false},
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

// TestSSHUserCheckedBeforeAnyRequest: ssh, sftp and cp check --user with no key set, so before they build a
// client.
func TestSSHUserCheckedBeforeAnyRequest(t *testing.T) {
	t.Parallel()
	const user = "--user=-root"
	for _, args := range [][]string{
		{"container-groups", "ssh", "my-app", user},
		{"container-groups", "sftp", "my-app", user},
		{"container-groups", "cp", "my-app:/data/out.json", "out.json", user},
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

// TestCpRefusesControlCharacters: a path with a control character is a usage error, shown on one line; a path with
// spaces is fine.
func TestCpRefusesControlCharacters(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	for _, args := range [][]string{
		{"model.bin\nsecond line", "my-app:/models/model.bin"},
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

// wellFormedID is an instance ID with an ID's shape.
const wellFormedID = "8a2f13ba-01c2-4a29-9c4d-52c7e5a44e37"

// malformedEndpoints are running instances whose SSH endpoint, or ID, is not an address, a port and an ID.
func malformedEndpoints() []containergroups.ContainerGroupInstance {
	return []containergroups.ContainerGroupInstance{
		testInstance(wellFormedID, "running", "203.0.113.7\nmore", 30123),
		testInstance(wellFormedID, "running", "-host", 30123),
		testInstance(wellFormedID, "running", "203.0.113.7", 0),
		testInstance(wellFormedID, "running", "203.0.113.7", 70000),
		testInstance("8a2f\nmore", "running", "203.0.113.7", 30123),
	}
}

// TestSSHEndpointFromAPIIsChecked: an SSH endpoint that is not an address and a port is an error for ssh, sftp and
// cp, on one line.
func TestSSHEndpointFromAPIIsChecked(t *testing.T) {
	t.Parallel()
	bad := malformedEndpoints()
	for i := range bad {
		_, cerr := sshTargetOf(&bad[i], "my-app")
		if cerr == nil || cerr.Code != clierr.CodeGeneric || strings.ContainsAny(cerr.Message, "\n\r") {
			t.Errorf("sshTargetOf(%q, %d) = %v, want a one-line error", *bad[i].SshIp, *bad[i].SshPort, cerr)
		}
	}
}

// TestSSHConfigLeavesOutMalformedEndpoints: --config leaves an instance with a malformed endpoint out with a
// warning, and says why when it is the one --instance names.
func TestSSHConfigLeavesOutMalformedEndpoints(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	bad := malformedEndpoints()
	good := []containergroups.ContainerGroupInstance{
		testInstance(wellFormedID, "running", "203.0.113.7", 30123),
		testInstance("dddd4444", "running", "2001:db8::7", 30124),
		testInstance("eeee5555", "running", "relay.example.com", 22),
	}
	var text string
	var n int
	stderr := app.captureStderr(t, func() {
		text, n = app.renderSSHConfigIdentity("my-app", append(bad, good...), "root", "")
	})
	if n != len(good) || strings.Contains(text, "more") {
		t.Errorf("--config rendered %d blocks, want %d and none of the malformed values:\n%s", n, len(good), text)
	}
	lines, warnings := strings.Count(stderr, "\n"), strings.Count(stderr, "warning: left out")
	if lines != len(bad) || warnings != len(bad) {
		t.Errorf("stderr = %q, want %d one-line warnings", stderr, len(bad))
	}

	only := &sshOptions{user: "root", identity: "", instance: "8a2f", useConfig: true}
	err := app.sshPrintConfig(bad[:1], "my-app", only)
	if err == nil || !strings.Contains(err.Error(), "is not an address and a port") {
		t.Errorf("--config --instance on a malformed endpoint = %v, want the endpoint error", err)
	}
}

// TestSFTPTakesIPv6InBrackets: sftp is given an IPv6 address in brackets and ssh is given it bare; an IPv4 address
// is the same for both.
func TestSFTPTakesIPv6InBrackets(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	for _, tt := range []struct{ ip, ssh, sftp string }{
		{"2001:db8::7", "root@2001:db8::7", "root@[2001:db8::7]"},
		{"203.0.113.7", "root@203.0.113.7", "root@203.0.113.7"},
	} {
		target := sshTarget{InstanceID: "dddd4444", IP: tt.ip, Port: 30124}
		sshArgs := app.buildSSHArgs(target, "root", "", nil)
		if got := sshArgs[len(sshArgs)-1]; got != tt.ssh {
			t.Errorf("ssh destination for %s = %q, want %q", tt.ip, got, tt.ssh)
		}
		sftpArgs := app.buildSFTPArgs(target, "root", "", "")
		if got := sftpArgs[len(sftpArgs)-1]; got != tt.sftp {
			t.Errorf("sftp destination for %s = %q, want %q", tt.ip, got, tt.sftp)
		}
	}
}

// TestInstanceIDShownOnOneLine: an instance ID that does not have an ID's shape is quoted in the note that names the
// instance picked and in the hint that lists the matches of a prefix, so each stays one line.
func TestInstanceIDShownOnOneLine(t *testing.T) {
	t.Parallel()
	instances := []containergroups.ContainerGroupInstance{
		testInstance("8a2f\nwarning: one", "running", "203.0.113.7", 30123),
		testInstance("8a2f\nwarning: two", "running", "203.0.113.8", 30123),
	}
	var note bytes.Buffer
	if _, cerr := pickInstance(&note, instances, "my-app", ""); cerr != nil {
		t.Fatalf("pickInstance: %v", cerr)
	}
	if got := note.String(); strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, "\n") {
		t.Errorf("note = %q, want one line", got)
	}
	_, cerr := pickInstance(&note, instances, "my-app", "8a2f")
	if cerr == nil || cerr.Code != clierr.CodeInvalidInput || strings.Contains(errText(cerr), "\n") {
		t.Errorf("ambiguous prefix = %v, want a one-line invalid-input error", cerr)
	}
	if got := shownInstanceID("8a2f13ba-01c2"); got != "8a2f13ba-01c2" {
		t.Errorf("shownInstanceID of a well-formed ID = %q, want it as it is", got)
	}
}

// TestConfigIdentity: --config quotes an identity file path that holds a space, and refuses one ssh_config cannot
// carry.
func TestConfigIdentity(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	const spaced = `C:\Users\First Last\.ssh\saladcloud_ed25519`
	for _, tt := range []struct {
		path string
		ok   bool
	}{
		{"", true},
		{"/home/me/.ssh/saladcloud_ed25519", true},
		{spaced, true},
		{"key\nmore", false},
		{`ke"y`, false},
	} {
		cerr := checkConfigIdentity(tt.path)
		if (cerr == nil) != tt.ok {
			t.Errorf("checkConfigIdentity(%q) = %v, want ok %v", tt.path, cerr, tt.ok)
		}
		if cerr != nil && (cerr.Code != clierr.CodeUsage || strings.Contains(cerr.Message, "\n")) {
			t.Errorf(
				"checkConfigIdentity(%q) = exit %d %q, want a one-line usage error",
				tt.path,
				cerr.Code,
				cerr.Message,
			)
		}
	}
	instances := []containergroups.ContainerGroupInstance{testInstance("dddd4444", "running", "203.0.113.7", 30123)}
	text, _ := app.renderSSHConfigIdentity("my-app", instances, "root", spaced)
	if want := "    IdentityFile \"" + spaced + "\"\n"; !strings.Contains(text, want) {
		t.Errorf("--config output lacks %q:\n%s", want, text)
	}
	// With no key set, so before ssh builds a client.
	got := app.runExecute(t, "container-groups", "ssh", "my-app", "--config", "-i", `ke"y`)
	if got.code != clierr.CodeUsage || !strings.Contains(got.stderr, "cannot be written to ssh_config") {
		t.Errorf("exit %d, stderr %q; want exit 2 and the identity error", got.code, got.stderr)
	}
}

// TestRemoteBaseURLWarning: a base-URL variable naming another machine draws one warning per run, in json mode
// too, worded by what goes there; a stand-in on this machine draws none; one that is not https is a usage error.
func TestRemoteBaseURLWarning(t *testing.T) {
	t.Parallel()
	t.Run("another machine", func(t *testing.T) {
		t.Parallel()
		app := newTestRun(t)
		app.setenv("SALAD_CLI_API_BASE_URL", "https://api.example.com/api/public")
		app.setenv("SALAD_CLI_AI_GATEWAY_BASE_URL", "https://gateway.example.com/v1")
		app.setOutputMode(t, "json")
		got := app.captureStderr(t, func() {
			app.apiOptions()
			app.apiOptions()
		})
		want := `warning: SALAD_CLI_API_BASE_URL is set, so requests and the API key go to "api.example.com", ` +
			"not to SaladCloud\n" +
			`warning: SALAD_CLI_AI_GATEWAY_BASE_URL is set, so AI Gateway requests and the AI Gateway key go to ` +
			`"gateway.example.com", not to SaladCloud` + "\n"
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
	t.Run("not https", func(t *testing.T) {
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

// TestLoginsCheckBaseURLBeforeAsking: at a terminal, login and ai-gateway login report a base-URL variable that is
// not https before they ask for a key, with the hint that names the variable.
func TestLoginsCheckBaseURLBeforeAsking(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		env  string
		args []string
	}{
		{"SALAD_CLI_API_BASE_URL", []string{"login"}},
		{"SALAD_CLI_STORAGE_BASE_URL", []string{"login"}},
		{"SALAD_CLI_AI_GATEWAY_BASE_URL", []string{"ai-gateway", "login"}},
	} {
		t.Run(tt.env, func(t *testing.T) {
			t.Parallel()
			app := newTestRun(t)
			con := &fakeConsole{terminal: true, answers: nil, device: bytes.Buffer{}, transcript: nil}
			app.Console = con
			app.setenv(tt.env, "http://api.example.com")
			got := app.runExecute(t, tt.args...)
			if got.code != clierr.CodeUsage || !strings.Contains(got.stderr, "remove "+tt.env) {
				t.Errorf("exit %d, stderr %q; want exit 2 and a hint to remove %s", got.code, got.stderr, tt.env)
			}
			if asked := con.device.String(); asked != "" {
				t.Errorf("the terminal was written to before the check: %q", asked)
			}
		})
	}
}

// TestGatewayLoginKeepsAnErrorsOwnHint: a usage error that carries its own fix keeps it; any other failed key
// check, hinted or not, gets the nothing-was-saved hint.
func TestGatewayLoginKeepsAnErrorsOwnHint(t *testing.T) {
	t.Parallel()
	g := &gatewayLogin{
		saved:    config.File{APIKey: "", Organization: "", Project: "", GatewayAPIKey: ""},
		terminal: false,
		key:      "gateway-key",
		keyFrom:  keyFromFlag,
		models:   0,
		credits:  true,
		app:      newTestRun(t).App,
	}
	own := g.failed(
		clierr.New(clierr.CodeUsage, clierr.KindUsage, "%s", "a setting is wrong").WithHint("fix the setting"),
	)
	if own.HintText() != "fix the setting" {
		t.Errorf("hint = %q, want the usage error's own", own.HintText())
	}
	for _, cerr := range []*clierr.Error{
		clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "it failed"),
		clierr.New(clierr.CodeNotFound, clierr.KindNotFound, "no such model").WithHint("list the models"),
	} {
		if got := g.failed(cerr).HintText(); !strings.Contains(got, "nothing was saved") {
			t.Errorf("hint for %q = %q, want the nothing-was-saved hint", cerr.Message, got)
		}
	}
}

// TestDoctorReportsBaseURLOnReachableRow: doctor reports a base-URL variable that is not https on the api-reachable
// row, with its hint, and skips the key check that needs the API.
func TestDoctorReportsBaseURLOnReachableRow(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	app.setenv("SALAD_API_KEY", "test-key")
	app.setenv("SALAD_ORGANIZATION", "my-org")
	app.setenv("SALAD_CLI_API_BASE_URL", "http://api.example.com")
	out := app.runExecute(t, "doctor", "-o", "json")
	var doc doctorResult
	if err := json.Unmarshal([]byte(out.stdout), &doc); err != nil {
		t.Fatalf("doctor -o json: %v\n%s", err, out.stdout)
	}
	rows := make(map[string]checkResult, len(doc.Items))
	for _, item := range doc.Items {
		rows[item.Name] = item
	}
	reach := rows["api-reachable"]
	if reach.Status != checkFail || !strings.Contains(reach.Detail, "must be an https URL") || reach.Hint == nil ||
		!strings.Contains(*reach.Hint, "remove SALAD_CLI_API_BASE_URL") {
		t.Errorf("api-reachable = %+v, want a failure with the base-URL message and hint", reach)
	}
	if key := rows["api-key"]; key.Status != checkSkip {
		t.Errorf("api-key = %+v, want it skipped", key)
	}
}
