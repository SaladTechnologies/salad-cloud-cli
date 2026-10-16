package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

func TestParseRemoteSpec(t *testing.T) {
	cases := []struct {
		in   string
		want *remoteSpec // nil = local
	}{
		{"model.safetensors", nil},
		{"./result.json", nil},
		{"/abs/path/file", nil},
		{`C:\Users\me\file.bin`, nil},  // Windows drive letter
		{"C:/Users/me/file.bin", nil},  // Windows drive letter, forward slashes
		{`.\dir\file:with-colon`, nil}, // backslash before colon: local
		{`C:\Users\Jane Doe\out.json`, nil},
		{`D:relative\out.json`, nil}, // drive-relative
		{`\\server\share\out.json`, nil},
		{`.\out.json`, nil},
		{"my-app:/outputs/result.json", &remoteSpec{Group: "my-app", Path: "/outputs/result.json"}},
		{"my-app/8a2f13ba:/data", &remoteSpec{Group: "my-app", InstancePrefix: "8a2f13ba", Path: "/data"}},
		{"my-app/8a2f13ba:C:/data", &remoteSpec{Group: "my-app", InstancePrefix: "8a2f13ba", Path: "C:/data"}},
		{"my-app:relative/path", &remoteSpec{Group: "my-app", Path: "relative/path"}},
		{"my-app:/path:with:colons", &remoteSpec{Group: "my-app", Path: "/path:with:colons"}},
	}
	for _, c := range cases {
		got, err := parseRemoteSpec(c.in)
		if err != nil {
			t.Errorf("parseRemoteSpec(%q) unexpected error: %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseRemoteSpec(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestParseRemoteSpecErrors(t *testing.T) {
	for _, in := range []string{
		"my-app:",            // empty remote path
		":/path",             // empty group
		"my-app//inst:/path", // empty segment
		"/my-app/inst:/path", // leading slash: empty first segment
		"a/b/c:/path",        // too many segments
	} {
		_, err := parseRemoteSpec(in)
		if err == nil {
			t.Errorf("parseRemoteSpec(%q): want usage error, got nil", in)
			continue
		}
		if err.Code != clierr.CodeUsage {
			t.Errorf("parseRemoteSpec(%q) code = %d, want %d (usage)", in, err.Code, clierr.CodeUsage)
		}
	}
}

// TestParseRemoteSpecGitBashRewrite: Git Bash turns my-app/8a2f13ba:/etc/x
// into a Windows path list before the CLI runs; the CLI says so and how to
// stop it, instead of reading both sides as local.
func TestParseRemoteSpecGitBashRewrite(t *testing.T) {
	t.Setenv("MSYSTEM", "MINGW64") // the check runs only under Git Bash
	for _, in := range []string{
		`sr-ex2\0521ee78;C:\Program Files\Git\etc\hostname`,
		`my-app\8a2f13ba;D:\msys64\data;D:\msys64\more`,
	} {
		_, err := parseRemoteSpec(in)
		if err == nil {
			t.Errorf("parseRemoteSpec(%q): want usage error, got nil", in)
			continue
		}
		if err.Code != clierr.CodeUsage || !strings.Contains(err.Message, "rewritten by Git Bash") ||
			!strings.Contains(err.HintText(), "MSYS_NO_PATHCONV=1") {
			t.Errorf(
				"parseRemoteSpec(%q) = (%d, %q), want a usage error naming MSYS_NO_PATHCONV=1",
				in,
				err.Code,
				err.Message,
			)
		}
	}
}

// The test binary stands in for sftp when SALAD_CLI_TEST_FAKE_SFTP is set: it
// prints sftp's batch echo on stdout and exits, before the test flags parse.
func init() {
	switch os.Getenv("SALAD_CLI_TEST_FAKE_SFTP") {
	case "":
	case "fail":
		fmt.Println(`sftp> get "/etc/hostname" "./h"`)
		fmt.Fprintln(os.Stderr, `remote open("/etc/hostname"): Permission denied`)
		os.Exit(1)
	default:
		fmt.Println(`sftp> get "/etc/hostname" "./h"`)
		os.Exit(0)
	}
}

// runCp runs cp against a stub API with the test binary as sftp (fake
// says how it behaves) and returns the CLI's stdout, the process's stderr
// (sftp's and the CLI's), and the error.
func runCp(t *testing.T, fake string, mode ...string) (out, stderr string, err error) {
	t.Helper()
	stubAPI(t, map[string]string{
		"/containers/my-app/instances": `{"instances":[{"id":"8a2f13ba-01c2-4a29-9c4d-52c7e5a44e37","state":"running","ssh_ip":"203.0.113.7","ssh_port":30123}]}`,
	})
	old := sftpProgram
	sftpProgram = os.Args[0]
	t.Cleanup(func() { sftpProgram = old })
	t.Setenv("SALAD_CLI_TEST_FAKE_SFTP", fake)

	realOut, realErr := os.Stdout, os.Stderr
	outFile, _ := os.CreateTemp(t.TempDir(), "stdout")
	errFile, _ := os.CreateTemp(t.TempDir(), "stderr")
	os.Stdout, os.Stderr = outFile, errFile
	out, err = runRoot(t, append([]string{"cg", "cp", "my-app/8a2f:/etc/hostname", "./h"}, mode...)...)
	os.Stdout, os.Stderr = realOut, realErr
	// Closed before TempDir's cleanup: Windows cannot remove an open file.
	outFile.Close()
	errFile.Close()
	childOut, _ := os.ReadFile(outFile.Name())
	if len(childOut) != 0 {
		t.Errorf("sftp wrote %q to stdout; stdout carries only the result", childOut)
	}
	childErr, _ := os.ReadFile(errFile.Name())
	return out, string(childErr), err
}

// TestCpStdoutCarriesOnlyTheResult: stdout holds the one result document;
// in table mode sftp's batch echo reaches stderr, and in json and yaml mode
// stderr stays empty, as it does for every command that succeeds there.
func TestCpStdoutCarriesOnlyTheResult(t *testing.T) {
	out, stderr, err := runCp(t, "ok", "-o", "json")
	if err != nil {
		t.Fatalf("cp: %v", err)
	}
	if stderr != "" {
		t.Errorf("json mode: stderr = %q, want nothing", stderr)
	}
	want := `{"group":"my-app","instance":"8a2f13ba-01c2-4a29-9c4d-52c7e5a44e37","action":"copied","ok":true,"src":"my-app/8a2f:/etc/hostname","dst":"./h"}`
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(out)); err != nil || compact.String() != want {
		t.Errorf("stdout = %s, want %s", out, want)
	}
	if _, stderr, err := runCp(t, "ok", "-o", "yaml"); err != nil || stderr != "" {
		t.Errorf("yaml mode: err %v, stderr = %q, want nothing", err, stderr)
	}
	if _, stderr, err := runCp(t, "ok", "-o", "table"); err != nil || !strings.Contains(stderr, "sftp> get") {
		t.Errorf("table mode: err %v, stderr = %q, want sftp's batch echo", err, stderr)
	}
}

// TestCpFailureInJSON: a failing sftp's output stays off stderr in json
// mode, and its last line ends the error's message.
func TestCpFailureInJSON(t *testing.T) {
	_, stderr, err := runCp(t, "fail", "-o", "json")
	var cerr *clierr.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("cp: %v, want a child_exit error", err)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing before the error document", stderr)
	}
	if !strings.HasSuffix(cerr.Message, `exited with status 1; remote open("/etc/hostname"): Permission denied`) ||
		cerr.Kind != clierr.KindChildExit ||
		cerr.Code != 1 {
		t.Errorf("error = %+v, want child_exit 1 ending with sftp's last line", cerr)
	}
}

// TestCpResultShape: cp -o json emits its own result, never sftp's echo.
func TestCpResultShape(t *testing.T) {
	v := cpResult{Group: "g", Instance: "8a2f13ba-0000", Action: "copied", OK: true, Src: "g:/etc/x", Dst: "./x"}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"group":"g","instance":"8a2f13ba-0000","action":"copied","ok":true,"src":"g:/etc/x","dst":"./x"}`
	if string(b) != want {
		t.Errorf("cp result = %s, want %s", b, want)
	}
}

func TestBuildSFTPBatchPut(t *testing.T) {
	got := buildSFTPBatch(true, false, `C:\Users\me\model.bin`, "/models/model.bin")
	want := "put \"C:/Users/me/model.bin\" \"/models/model.bin\"\n"
	if got != want {
		t.Errorf("batch = %q, want %q", got, want)
	}
}

func TestBuildSFTPBatchGetRecursive(t *testing.T) {
	got := buildSFTPBatch(false, true, "./outputs", "/outputs")
	want := "get -r \"/outputs\" \"./outputs\"\n"
	if got != want {
		t.Errorf("batch = %q, want %q", got, want)
	}
}

func TestBuildSFTPBatchQuoting(t *testing.T) {
	got := buildSFTPBatch(true, false, `my "quoted" file`, `/data/back\slash`)
	want := "put \"my \\\"quoted\\\" file\" \"/data/back\\\\slash\"\n"
	if got != want {
		t.Errorf("batch = %q, want %q", got, want)
	}
}

func TestBuildSFTPArgs(t *testing.T) {
	target := sshTarget{InstanceID: "aaaa1111", IP: "203.0.113.7", Port: 30123}
	got := buildSFTPArgs(target, "root", "", "/tmp/batch")
	want := []string{
		"-P",
		"30123",
		"-o",
		"StrictHostKeyChecking=accept-new",
		"-o",
		"UserKnownHostsFile=/cfg/known_hosts",
		"-o",
		"HostKeyAlias=saladcloud-aaaa1111",
		"-b",
		"/tmp/batch",
		"root@203.0.113.7",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}

func TestBuildSFTPArgsInteractive(t *testing.T) {
	// Empty batchPath = interactive 'saladcloud container-groups sftp' session: no -b flag.
	target := sshTarget{InstanceID: "aaaa1111", IP: "203.0.113.7", Port: 30123}
	got := buildSFTPArgs(target, "root", "", "")
	want := []string{
		"-P",
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

func TestBuildSFTPArgsInteractiveIdentity(t *testing.T) {
	target := sshTarget{IP: "203.0.113.7", Port: 22}
	got := buildSFTPArgs(target, "worker", "/home/me/.ssh/key", "")
	want := []string{
		"-P", "22",
		"-o", "StrictHostKeyChecking=accept-new", "-o", "UserKnownHostsFile=/cfg/known_hosts",
		"-i", "/home/me/.ssh/key",
		"worker@203.0.113.7",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}

func TestBuildSFTPArgsIdentity(t *testing.T) {
	target := sshTarget{IP: "203.0.113.7", Port: 22}
	got := buildSFTPArgs(target, "worker", "/home/me/.ssh/key", "/tmp/batch")
	want := []string{
		"-P", "22",
		"-o", "StrictHostKeyChecking=accept-new", "-o", "UserKnownHostsFile=/cfg/known_hosts",
		"-i", "/home/me/.ssh/key",
		"-b", "/tmp/batch",
		"worker@203.0.113.7",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}
