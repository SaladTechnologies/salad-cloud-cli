package cli

import (
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// testInstance builds a ContainerGroupInstance for resolution tests.
// ip/port zero-values mean "no SSH endpoint".
func testInstance(id, state, ip string, port int64) containergroups.ContainerGroupInstance {
	inst := containergroups.ContainerGroupInstance{Id: ptr.To(id)}
	if state != "" {
		s := containergroups.TheContainerGroupInstanceState(state)
		inst.State = &s
	}
	if ip != "" {
		inst.SshIp = ptr.To(ip)
		inst.SshPort = ptr.To(port)
	}
	return inst
}

func TestResolveSSHInstancePrefixMatch(t *testing.T) {
	instances := []containergroups.ContainerGroupInstance{
		testInstance("8a2f13ba-01c2-4a29-9c4d-52c7e5a44e37", "running", "1.2.3.4", 30001),
		testInstance("9b3e24cb-1111-2222-3333-444455556666", "running", "1.2.3.5", 30002),
	}
	inst, err := resolveSSHInstance(instances, "my-app", "8a2f")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := ptr.Deref(inst.Id); got != "8a2f13ba-01c2-4a29-9c4d-52c7e5a44e37" {
		t.Errorf("resolved id = %q, want the 8a2f... instance", got)
	}
}

func TestResolveSSHInstancePrefixMatchesNonRunning(t *testing.T) {
	// An explicit prefix may select a non-running instance; the SSH-ready
	// check happens later in sshTargetOf.
	instances := []containergroups.ContainerGroupInstance{
		testInstance("aaaa1111", "downloading", "", 0),
	}
	inst, err := resolveSSHInstance(instances, "my-app", "aaaa")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := ptr.Deref(inst.Id); got != "aaaa1111" {
		t.Errorf("resolved id = %q, want aaaa1111", got)
	}
}

func TestResolveSSHInstanceAmbiguousPrefix(t *testing.T) {
	instances := []containergroups.ContainerGroupInstance{
		testInstance("8a2f13ba-x", "running", "1.2.3.4", 30001),
		testInstance("8a2f24cb-y", "running", "1.2.3.5", 30002),
	}
	_, err := resolveSSHInstance(instances, "my-app", "8a2f")
	if err == nil {
		t.Fatal("want ambiguity error, got nil")
	}
	if err.Code != clierr.CodeInvalidInput {
		t.Errorf("code = %d, want %d (invalid input)", err.Code, clierr.CodeInvalidInput)
	}
	if !strings.Contains(err.HintText(), "8a2f13ba-x") || !strings.Contains(err.HintText(), "8a2f24cb-y") {
		t.Errorf("hint should list both matches, got %q", err.HintText())
	}
}

func TestResolveSSHInstanceNoPrefixMatch(t *testing.T) {
	instances := []containergroups.ContainerGroupInstance{
		testInstance("aaaa1111", "running", "1.2.3.4", 30001),
	}
	_, err := resolveSSHInstance(instances, "my-app", "ffff")
	if err == nil || err.Code != clierr.CodeNotFound {
		t.Fatalf("err = %v, want not-found error (code %d)", err, clierr.CodeNotFound)
	}
}

func TestResolveSSHInstanceFirstRunning(t *testing.T) {
	instances := []containergroups.ContainerGroupInstance{
		testInstance("aaaa1111", "downloading", "", 0),
		testInstance("bbbb2222", "running", "1.2.3.4", 30001),
		testInstance("cccc3333", "running", "1.2.3.5", 30002),
	}
	inst, err := resolveSSHInstance(instances, "my-app", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := ptr.Deref(inst.Id); got != "bbbb2222" {
		t.Errorf("resolved id = %q, want the first running instance bbbb2222", got)
	}
}

func TestResolveSSHInstanceNoneRunning(t *testing.T) {
	instances := []containergroups.ContainerGroupInstance{
		testInstance("aaaa1111", "downloading", "", 0),
		testInstance("bbbb2222", "allocating", "", 0),
	}
	_, err := resolveSSHInstance(instances, "my-app", "")
	if err == nil {
		t.Fatal("want no-running-instances error, got nil")
	}
	if err.Code != clierr.CodeGeneric {
		t.Errorf("code = %d, want %d (generic)", err.Code, clierr.CodeGeneric)
	}
	if !strings.Contains(err.HintText(), "run saladcloud container-groups instances list -c my-app") {
		t.Errorf("hint should point at saladcloud container-groups instances list -c my-app, got %q", err.HintText())
	}
}

func TestSSHTargetOfNotReady(t *testing.T) {
	inst := testInstance("aaaa1111", "running", "", 0)
	_, err := sshTargetOf(&inst, "my-app")
	if err == nil {
		t.Fatal("want not-SSH-ready error, got nil")
	}
	want := `the platform has not assigned an SSH endpoint to instance aaaa1111 of container group my-app yet; ` +
		`retry in a minute, or to check its SSH column, run saladcloud container-groups instances list -c my-app`
	if errText(err) != want {
		t.Errorf("message = %q, want %q", errText(err), want)
	}
}

func TestSSHTargetOfReady(t *testing.T) {
	inst := testInstance("aaaa1111", "running", "203.0.113.7", 30123)
	target, err := sshTargetOf(&inst, "my-app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := sshTarget{InstanceID: "aaaa1111", IP: "203.0.113.7", Port: 30123}
	if target != want {
		t.Errorf("target = %+v, want %+v", target, want)
	}
}

func TestShSingleQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "'plain'"},
		{"two words", "'two words'"},
		{"it's", `'it'\''s'`},
		{"'leading", `''\''leading'`},
		{"trailing'", `'trailing'\'''`},
		{"a''b", `'a'\'''\''b'`},
		{"", "''"},
		{`double " quote`, `'double " quote'`},
		{"$HOME `cmd` \\", "'$HOME `cmd` \\'"},
	}
	for _, c := range cases {
		if got := shSingleQuote(c.in); got != c.want {
			t.Errorf("shSingleQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRemoteCommand(t *testing.T) {
	got := remoteCommand([]string{"uname", "-a", "&&", "echo", "it's done"})
	want := `sh -c 'uname -a && echo it'\''s done'`
	if got != want {
		t.Errorf("remoteCommand = %q, want %q", got, want)
	}
}

func TestBuildSSHArgsInteractive(t *testing.T) {
	target := sshTarget{InstanceID: "aaaa1111", IP: "203.0.113.7", Port: 30123}
	got := app.buildSSHArgs(target, "root", "", nil)
	want := []string{
		"-p",
		"30123",
		"-o",
		"StrictHostKeyChecking=accept-new",
		"-o",
		"UserKnownHostsFile=/cfg/known_hosts",
		"-o",
		"HostKeyAlias=saladcloud-aaaa1111",
		"root@203.0.113.7",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}

func TestBuildSSHArgsCommandWithIdentityAndUser(t *testing.T) {
	target := sshTarget{InstanceID: "aaaa1111", IP: "203.0.113.7", Port: 30123}
	got := app.buildSSHArgs(target, "worker", "/home/me/.ssh/key", []string{"nvidia-smi", "-L"})
	want := []string{
		"-p", "30123",
		"-o", "StrictHostKeyChecking=accept-new", "-o", "UserKnownHostsFile=/cfg/known_hosts",
		"-o", "HostKeyAlias=saladcloud-aaaa1111",
		"-i", "/home/me/.ssh/key",
		"-o", "BatchMode=yes",
		"worker@203.0.113.7",
		"sh -c 'nvidia-smi -L'",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}

func TestBuildSSHArgsCommandIsSingleArg(t *testing.T) {
	// The whole remote command must be ONE argv element (the relay execs
	// raw argv without a shell — fact verified live).
	target := sshTarget{IP: "203.0.113.7", Port: 22}
	args := app.buildSSHArgs(target, "root", "", []string{"uname", "-a", ";", "whoami"})
	last := args[len(args)-1]
	if last != `sh -c 'uname -a ; whoami'` {
		t.Errorf("final arg = %q, want the whole command wrapped in one sh -c string", last)
	}
}

func TestRenderSSHConfig(t *testing.T) {
	instances := []containergroups.ContainerGroupInstance{
		testInstance("8a2f13ba-01c2-4a29-9c4d-52c7e5a44e37", "running", "203.0.113.7", 30123),
		testInstance("bbbb2222", "downloading", "", 0), // not running: excluded
		testInstance("cccc3333", "running", "", 0),     // running but not SSH-ready: excluded
		testInstance("dddd4444-5555", "running", "198.51.100.9", 30124),
	}
	text, n := app.renderSSHConfig("my-app", instances, "root")
	if n != 2 {
		t.Fatalf("blocks = %d, want 2:\n%s", n, text)
	}
	if !strings.HasPrefix(text, "# add to ~/.ssh/config") {
		t.Errorf("missing comment header:\n%s", text)
	}
	wantBlock := "Host saladcloud-my-app-8a2f13ba\n" +
		"    HostName 203.0.113.7\n" +
		"    Port 30123\n" +
		"    User root\n" +
		"    StrictHostKeyChecking accept-new\n" +
		"    UserKnownHostsFile /cfg/known_hosts\n" +
		"    HostKeyAlias saladcloud-8a2f13ba-01c2-4a29-9c4d-52c7e5a44e37\n"
	if !strings.Contains(text, wantBlock) {
		t.Errorf("missing expected Host block:\n%s\nwant:\n%s", text, wantBlock)
	}
	if !strings.Contains(text, "Host saladcloud-my-app-dddd4444\n") {
		t.Errorf("missing second Host block (8-char alias):\n%s", text)
	}
	if strings.Contains(text, "bbbb2222") || strings.Contains(text, "cccc3333") {
		t.Errorf("excluded instances leaked into config:\n%s", text)
	}
}

func TestOneShotExitNote(t *testing.T) {
	cases := []struct {
		exitCode int
		want     string
	}{
		{0, relayExitCodeNote}, // command ran, relay reported success
		{1, relayExitCodeNote}, // the relay's constant answer for any executed command
		{255, ""},              // ssh transport/auth failure: the error's hint says what to do
		{2, ""},                // any other code: no note
		{127, ""},
		{130, ""},
		{-1, ""}, // child never produced an exit code (spawn failure)
	}
	for _, c := range cases {
		if got := oneShotExitNote(c.exitCode); got != c.want {
			t.Errorf("oneShotExitNote(%d) = %q, want %q", c.exitCode, got, c.want)
		}
	}
}

func TestChildExitCode(t *testing.T) {
	cases := []struct {
		name   string
		runErr *clierr.Error
		want   int
	}{
		{"success", nil, 0},
		{"child exited 1", clierr.New(1, clierr.KindChildExit, "ssh exited with status 1"), 1},
		{"child exited 255", clierr.New(255, clierr.KindChildExit, "ssh exited with status 255"), 255},
		{"not found on PATH", clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "\"ssh\" not found on PATH"), -1},
	}
	for _, c := range cases {
		if got := childExitCode(c.runErr); got != c.want {
			t.Errorf("%s: childExitCode = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestRenderSSHConfigEmpty(t *testing.T) {
	_, n := app.renderSSHConfig("my-app", nil, "root")
	if n != 0 {
		t.Errorf("blocks = %d, want 0", n)
	}
}

// TestSessionFailedHintNamesTheKeyLocation pins the substance of the
// exit-255 hint. The registered key has to live on the machine running the
// CLI, which is easy to get wrong: a WSL shell has its own ~/.ssh and offers
// a different key than the Windows host, producing a publickey denial that
// looks exactly like an unregistered key. That cost real debugging time:
// the live suite ran the linux binary under WSL and was denied every time
// while the same command from Windows worked.
func TestSessionFailedHintNamesTheKeyLocation(t *testing.T) {
	hint := sessionFailedHint
	for _, want := range []string{
		"permission denied", "must be registered", "https://portal.salad.com/ssh-keys", "owns the API key",
		"ssh-key setup", "this machine", "passphrase", "ssh-add", "closed or refused", "still starting", "try again in a " +
			"minute",
	} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint missing %q; got: %s", want, hint)
		}
	}
	// The live suite tells ssh's own denial apart by "publickey"; the hint
	// must not say it.
	if strings.Contains(hint, "publickey") {
		t.Errorf("hint says publickey, which reads as ssh's own denial: %s", hint)
	}
}

// TestRenderSSHConfigIdentity: with an identity, every Host block carries
// IdentityFile so plain ssh and VS Code use the CLI's dedicated key too.
func TestRenderSSHConfigIdentity(t *testing.T) {
	instances := []containergroups.ContainerGroupInstance{
		testInstance("8a2f13ba-01c2-4a29-9c4d-52c7e5a44e37", "running", "203.0.113.7", 30123),
	}
	text, n := app.renderSSHConfigIdentity("my-app", instances, "root", "/home/me/.ssh/saladcloud_ed25519")
	if n != 1 || !strings.Contains(text, "    IdentityFile /home/me/.ssh/saladcloud_ed25519\n") {
		t.Errorf("IdentityFile missing:\n%s", text)
	}
	plain, _ := app.renderSSHConfig("my-app", instances, "root")
	if strings.Contains(plain, "IdentityFile") {
		t.Errorf("no identity given but IdentityFile rendered:\n%s", plain)
	}
}

// ssh reads UserKnownHostsFile as a whitespace-separated list of files, so
// a path with a space (a Windows profile such as "Jane Doe") is quoted.
func TestHostKeyOptionsQuoteASpacedPath(t *testing.T) {
	old := app.KnownHostsPath
	app.KnownHostsPath = func() string { return `C:\Users\Jane Doe\AppData\Roaming\saladcloud\known_hosts` }
	t.Cleanup(func() { app.KnownHostsPath = old })
	got := app.hostKeyOptions("aaaa1111")
	want := []string{
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", `UserKnownHostsFile="C:\Users\Jane Doe\AppData\Roaming\saladcloud\known_hosts"`,
		"-o", "HostKeyAlias=saladcloud-aaaa1111",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("hostKeyOptions = %q, want %q", got, want)
	}
}

// TestHelperExit255 is not a test: runChild runs the test binary with it to
// get a child that exits 255.
func TestHelperExit255(t *testing.T) {
	if app.Env.Getenv("SALAD_CLI_TEST_HELPER_EXIT") == "" {
		t.Skip("a helper for TestRunChildHints")
	}
	os.Exit(255) //nolint:revive // deep-exit: the test binary run as a child that exits 255
}

// TestRunChildHints: a child that exits 255 (ssh or sftp could not open the
// session) carries the SSH Keys hint; a program that is not installed gets
// the per-OS install hint.
func TestRunChildHints(t *testing.T) {
	t.Setenv("SALAD_CLI_TEST_HELPER_EXIT", "1")
	cerr := app.runChildTo(io.Discard, os.Args[0], []string{"-test.run=^TestHelperExit255$"})
	if cerr == nil || cerr.Code != 255 || cerr.Kind != clierr.KindChildExit || cerr.HintText() != sessionFailedHint {
		t.Errorf("exit 255: %+v hint %q", cerr, cerr.HintText())
	}
	old := app.Env.GOOS
	t.Cleanup(func() { app.Env.GOOS = old })
	app.Env.GOOS = "darwin"
	cerr = app.runChild("saladcloud-test-no-such-ssh", nil)
	if cerr == nil || cerr.Message != "saladcloud-test-no-such-ssh not found" ||
		cerr.HintText() != "macOS includes it at /usr/bin/saladcloud-test-no-such-ssh; check that /usr/bin is on "+
			"your PATH" {
		t.Errorf("missing: %+v hint %q", cerr, cerr.HintText())
	}
}
