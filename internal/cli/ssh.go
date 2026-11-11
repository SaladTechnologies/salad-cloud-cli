// SSH access to running instances over the SaladCloud relay. Each
// instance's relay endpoint is exposed by the public API as ssh_ip/ssh_port
// (see 'saladcloud container-groups instances list'); authentication is the account's one
// registered SSH public key (see 'saladcloud ssh-key setup').
//
// Two relay rules shape this file:
//
//   - The relay execs command strings as raw argv without a shell, so a
//     one-shot command is always wrapped as a single "sh -c '...'"
//     argument (buildSSHArgs).
//   - The relay does not propagate exec exit codes — a successful one-shot
//     command will exit 1. Interactive sessions and SFTP behave
//     normally, so 'saladcloud container-groups cp' exit codes are trustworthy but
//     'saladcloud container-groups ssh <group> -- <cmd>' exit codes are not (the CLI prints
//     a stderr note).

package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"
	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// relayExitCodeNote is printed to stderr after a one-shot command whose
// ssh child exited 0 or 1 — the codes an executed command actually
// produces through the relay (1 is the relay's constant answer for any
// executed command).
const relayExitCodeNote = "note: the relay does not propagate command exit codes (a successful command will exit 1)"

// sessionFailedHint is the fix when ssh or sftp exits 255: its own
// transport or authentication failure, so no command ran. The instance had
// an SSH endpoint (the CLI checks before running the child), which leaves
// two causes, told apart by what ssh printed. (It never says "publickey",
// so a check for ssh's own denial is not fooled by it.)
const sessionFailedHint = "if ssh said permission denied, the key must be registered at " + sshKeysPage +
	" on the account that owns the API key in use, and be on this machine " +
	"(WSL and containers have their own ~/.ssh), and a key with a passphrase needs ssh-add first; " +
	"to see the key and where to register it, run saladcloud ssh-key setup; " +
	"if ssh said the connection was closed or refused, the instance is still starting, so try again in a minute"

// sshSessionFailedExit is the exit code of an ssh or sftp whose own transport or authentication failed.
const sshSessionFailedExit = 255

// sshConfigAliasIDLen is how many characters of the instance ID an ssh_config Host alias keeps.
const sshConfigAliasIDLen = 8

// oneShotExitNote selects the stderr note printed after a one-shot
// command, keyed on the ssh child's exit code: 0 and 1 get the relay
// exit-code caveat; every other code gets none (255, a session that failed,
// carries its fix in the error's hint).
func oneShotExitNote(exitCode int) string {
	switch exitCode {
	case 0, 1:
		return relayExitCodeNote
	default:
		return ""
	}
}

// childExitCode recovers the child's exit code from a runChild result:
// 0 on success, the propagated code for a real child exit, and -1 when
// the child never produced one (not found on PATH, spawn failure).
func childExitCode(runErr *clierr.Error) int {
	switch {
	case runErr == nil:
		return 0
	case runErr.Kind == clierr.KindChildExit:
		return runErr.Code
	default:
		return -1
	}
}

// sshTarget is a resolved instance relay endpoint.
type sshTarget struct {
	InstanceID string
	IP         string
	Port       int64
}

// resolveSSHInstance picks the transfer/session target from a group's
// instance listing. With a prefix, it must match exactly one instance ID;
// without one, the first instance in state "running" wins.
func resolveSSHInstance(
	instances []containergroups.ContainerGroupInstance,
	group, prefix string,
) (*containergroups.ContainerGroupInstance, *clierr.Error) {
	if prefix != "" {
		return resolveSSHInstancePrefix(instances, group, prefix)
	}
	for i := range instances {
		if instances[i].State != nil &&
			*instances[i].State == containergroups.THE_CONTAINER_GROUP_INSTANCE_STATE_RUNNING {
			return &instances[i], nil
		}
	}
	return nil, clierr.New(clierr.CodeGeneric, clierr.KindAPIError,
		"no running instances in container group %s", group).
		WithHint("to list its instances, run saladcloud container-groups instances list -c %s", group)
}

// resolveSSHInstancePrefix is resolveSSHInstance with an ID prefix, which must match exactly one instance ID.
func resolveSSHInstancePrefix(
	instances []containergroups.ContainerGroupInstance,
	group, prefix string,
) (*containergroups.ContainerGroupInstance, *clierr.Error) {
	var matches []*containergroups.ContainerGroupInstance
	for i := range instances {
		if strings.HasPrefix(ptr.Deref(instances[i].Id), prefix) {
			matches = append(matches, &instances[i])
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, clierr.New(clierr.CodeNotFound, clierr.KindNotFound,
			`no instance of container group %s matches ID prefix "%s"`, group, prefix).
			WithHint("to list its instances, run saladcloud container-groups instances list -c %s", group)
	default:
		ids := make([]string, len(matches))
		for i, m := range matches {
			ids[i] = ptr.Deref(m.Id)
		}
		return nil, clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
			`instance ID prefix "%s" is ambiguous in container group %s`, prefix, group).
			WithHint("give more of the ID; it matches %s", strings.Join(ids, ", "))
	}
}

// pickInstance is resolveSSHInstance for ssh, sftp, and cp: when no
// instance was named and one is chosen for the caller, it says which on w
// (stderr), so the choice is never silent. A named instance, even by
// prefix, prints nothing.
func pickInstance(
	w io.Writer,
	instances []containergroups.ContainerGroupInstance,
	group, prefix string,
) (*containergroups.ContainerGroupInstance, *clierr.Error) {
	inst, cerr := resolveSSHInstance(instances, group, prefix)
	if cerr == nil && prefix == "" {
		fprintf(w, "note: using instance %s\n", ptr.Deref(inst.Id))
	}
	return inst, cerr
}

// sshTargetOf extracts the relay endpoint, erroring when the instance has
// none yet. The platform assigns ssh_ip/ssh_port some time after the
// instance reads running, so "running" alone does not mean reachable.
func sshTargetOf(inst *containergroups.ContainerGroupInstance, group string) (sshTarget, *clierr.Error) {
	if inst.SshIp == nil || inst.SshPort == nil || *inst.SshIp == "" {
		return sshTarget{}, clierr.New(clierr.CodeGeneric, clierr.KindAPIError,
			"the platform has not assigned an SSH endpoint to instance %s of container group %s yet",
			ptr.Deref(inst.Id), group).
			WithHint(
				"retry in a minute, or to check its SSH column, run saladcloud container-groups instances list -c %s",
				group,
			)
	}
	return sshTarget{InstanceID: ptr.Deref(inst.Id), IP: *inst.SshIp, Port: *inst.SshPort}, nil
}

// hostKeyOptions are the ssh -o options every relay connection carries:
// accept a new host key, record it in the CLI's known_hosts (quoted, since
// ssh reads the value as a list of files and a Windows profile path can
// hold a space), and file it under the instance rather than the relay's
// ip:port, which a later instance can be given.
func (app *App) hostKeyOptions(instanceID string) []string {
	opts := []string{"-o", "StrictHostKeyChecking=accept-new"}
	if app.printer.Machine() {
		// OpenSSH logs "Permanently added ..." at INFO; in json and yaml mode
		// stderr carries only warnings, notes and the error document.
		opts = append(opts, "-o", "LogLevel=ERROR")
	}
	if p := app.KnownHostsPath(); p != "" {
		opts = append(opts, "-o", "UserKnownHostsFile="+sshConfigQuote(p))
	}
	if instanceID != "" {
		opts = append(opts, "-o", "HostKeyAlias="+hostKeyAlias(instanceID))
	}
	return opts
}

// hostKeyAlias is the name an instance's host key is recorded under.
func hostKeyAlias(instanceID string) string { return "saladcloud-" + instanceID }

// shSingleQuote wraps s in single quotes for a POSIX shell, escaping
// embedded single quotes ('it's' -> 'it'\”s').
func shSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// remoteCommand wraps a one-shot command for the relay, which execs raw
// argv without a shell: the whole command becomes one "sh -c '...'" string
// so pipes, &&, and multi-arg commands behave as typed.
func remoteCommand(words []string) string {
	return "sh -c " + shSingleQuote(strings.Join(words, " "))
}

// buildSSHArgs builds the argv (after the "ssh" program name) for an
// interactive session (command empty) or a one-shot command.
func (app *App) buildSSHArgs(t sshTarget, user, identity string, command []string) []string {
	args := append([]string{"-p", strconv.FormatInt(t.Port, 10)}, app.hostKeyOptions(t.InstanceID)...)
	if identity != "" {
		args = append(args, "-i", identity)
	}
	if len(command) > 0 {
		args = append(args, "-o", "BatchMode=yes")
	}
	args = append(args, user+"@"+t.IP)
	if len(command) > 0 {
		args = append(args, remoteCommand(command))
	}
	return args
}

// sshConfigAlias is the Host alias for one instance: the group name plus
// the first 8 characters of the instance ID.
func sshConfigAlias(group, instanceID string) string {
	short := instanceID
	if len(short) > sshConfigAliasIDLen {
		short = short[:sshConfigAliasIDLen]
	}
	return fmt.Sprintf("saladcloud-%s-%s", group, short)
}

// renderSSHConfig renders ssh_config Host blocks for every running,
// SSH-ready instance of the group, plus a usage header. It returns the
// text and the number of Host blocks rendered.
func (app *App) renderSSHConfig(
	group string,
	instances []containergroups.ContainerGroupInstance,
	user string,
) (string, int) {
	return app.renderSSHConfigIdentity(group, instances, user, "")
}

// renderSSHConfigIdentity is renderSSHConfig with an IdentityFile line in
// every block when identity is non-empty, so a config written for the
// CLI's dedicated key works from plain ssh and VS Code too.
func (app *App) renderSSHConfigIdentity(
	group string,
	instances []containergroups.ContainerGroupInstance,
	user, identity string,
) (string, int) {
	var b strings.Builder
	b.WriteString("# add to ~/.ssh/config; VS Code Remote-SSH can then attach\n")
	n := 0
	for i := range instances {
		inst := &instances[i]
		if inst.State == nil || *inst.State != containergroups.THE_CONTAINER_GROUP_INSTANCE_STATE_RUNNING {
			continue
		}
		if inst.SshIp == nil || inst.SshPort == nil {
			continue
		}
		fprintf(&b, "\nHost %s\n    HostName %s\n    Port %d\n    User %s\n    StrictHostKeyChecking accept-new\n",
			sshConfigAlias(group, ptr.Deref(inst.Id)), *inst.SshIp, *inst.SshPort, user)
		if kh := app.KnownHostsPath(); kh != "" {
			fprintf(&b, "    UserKnownHostsFile %s\n", sshConfigQuote(kh))
		}
		if id := ptr.Deref(inst.Id); id != "" {
			fprintf(&b, "    HostKeyAlias %s\n", hostKeyAlias(id))
		}
		if identity != "" {
			fprintf(&b, "    IdentityFile %s\n", identity)
		}
		n++
	}
	return b.String(), n
}

// sshConfigQuote quotes an ssh_config value that contains spaces.
func sshConfigQuote(v string) string {
	if strings.ContainsAny(v, " \t") {
		return `"` + v + `"`
	}
	return v
}

// runChild runs a program with the CLI's stdio attached (interactive TTY
// passthrough works because ssh talks to the inherited terminal directly)
// and maps the outcome onto the CLI's exit-code contract: the child's own
// exit code is propagated.
func (app *App) runChild(name string, args []string) *clierr.Error {
	return app.runChildTo(app.Stdout, name, args)
}

// runChildTo is runChild with the child's stdout sent to stdout: cp sends
// sftp's batch echo to stderr, so its own stdout carries one result.
func (app *App) runChildTo(stdout io.Writer, name string, args []string) *clierr.Error {
	return app.runChildWith(stdout, app.errw(), name, args)
}

// runChildQuiet runs a child with its stdout and stderr held back, for cp
// in json and yaml mode, where stderr carries only warnings, notes and the
// error document. When the child fails, its last line of output ends the
// error's message, after "; ".
func (app *App) runChildQuiet(name string, args []string) *clierr.Error {
	var out bytes.Buffer
	cerr := app.runChildWith(&out, &out, name, args)
	if cerr != nil && cerr.Kind == clierr.KindChildExit {
		if last := lastLine(out.String()); last != "" {
			cerr.Message += "; " + last
		}
	}
	return cerr
}

// lastLine is the last non-blank line of s, trimmed.
func lastLine(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for _, line := range slices.Backward(lines) {
		if l := strings.TrimSpace(line); l != "" {
			return l
		}
	}
	return ""
}

func (app *App) runChildWith(stdout, stderr io.Writer, name string, args []string) *clierr.Error {
	child := exec.Command(name, args...) //nolint:gosec,noctx // ssh/sftp by design; a ctx would kill it on Ctrl-C
	child.Env = app.Environ()
	child.Stdin = app.Stdin
	child.Stdout = stdout
	child.Stderr = stderr
	err := child.Run()
	if err == nil {
		return nil
	}
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		code := ee.ExitCode()
		if code <= 0 { // -1: killed by a signal, no real exit code
			code = clierr.CodeGeneric
		}
		e := clierr.New(code, clierr.KindChildExit, "%s exited with status %d", name, ee.ExitCode())
		if ee.ExitCode() == sshSessionFailedExit {
			e = e.WithHint("%s", sessionFailedHint)
		}
		return e
	}
	if errors.Is(err, exec.ErrNotFound) {
		return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "%s not found", filepath.Base(name)).
			WithHint("%s", app.sshMissingHint(filepath.Base(name)))
	}
	return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot run %s: %v", name, err)
}

// sshOptions are the flags of 'container-groups ssh'.
type sshOptions struct {
	user, identity, instance string
	useConfig                bool
}

// addSSHCommands adds ssh.go's commands to the tree.
func (app *App) addSSHCommands(cg *cobra.Command) {
	var o sshOptions
	sshCmd := app.newSSHCmd(&o)
	sshCmd.Flags().StringVar(&o.instance, ancestorFlagInstance, "",
		"the instance to connect to: its ID or a unique prefix (default: the first running instance)")
	sshCmd.Flags().StringVar(&o.user, "user", "root",
		"SSH username; selects the container user, which the image must define (root always exists)")
	sshCmd.Flags().StringVarP(&o.identity, "identity", "i", "",
		"identity (private key) file for ssh -i (default: ~/.ssh/saladcloud_ed25519 when present)")
	sshCmd.Flags().BoolVar(&o.useConfig, "config", false,
		"print ssh_config Host blocks for the group's running instances (or only --instance) instead of connecting")
	app.addHiddenGroupFlag(sshCmd)
	annotate(sshCmd, annotationFlagForArg, ancestorFlagInstance)
	cg.AddCommand(sshCmd)
}

// newSSHCmd is 'saladcloud container-groups ssh <group>', its flags bound to o by addSSHCommands.
func (app *App) newSSHCmd(o *sshOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "ssh <group> [--instance <id>] [-- <command>...]",
		Short: "Open an SSH session or run a one-shot command on a running instance",
		Long: `Open an interactive SSH session on a running instance of a container
group, over the relay endpoint the API exposes as ssh_ip/ssh_port.

--instance picks the instance and may be an ID prefix: without it the CLI
picks the group's first running instance and says which on stderr.
Everything after -- runs on the instance as a one-shot command instead of
starting a shell; the CLI wraps it in a single sh -c '...' because the
relay execs raw argv without a shell. The relay does not propagate command
exit codes, so a successful one-shot command exits 1 (a stderr note
reminds you).

The SSH username selects the container user: the default "root" works in
any image, while any other --user must actually exist in the image.

Authentication uses the one SSH public key registered on your account; to
set it up, run saladcloud ssh-key setup. The system ssh client must be on
PATH.

With --config, no session is opened; instead ssh_config Host blocks for
the group's running instances (or only --instance) are printed to stdout,
ready to append to ~/.ssh/config (see the examples, for bash or zsh and for
PowerShell), after which plain ssh and VS Code Remote-SSH can connect by
Host alias.`,
		Example: `  # Shell on the first running instance of my-app
  saladcloud container-groups ssh my-app

  # Shell on a specific instance (ID prefix is enough)
  saladcloud container-groups ssh my-app --instance 8a2f13ba

  # One-shot command instead of a shell (its exit code is not trustworthy; see above)
  saladcloud container-groups ssh my-app -- nvidia-smi

  # Append ssh_config Host entries for VS Code Remote-SSH (bash, zsh)
  saladcloud container-groups ssh my-app --config >> ~/.ssh/config

  # The same in PowerShell
  saladcloud container-groups ssh my-app --config | Add-Content -Encoding ascii ~/.ssh/config`,
		Args: sshOneArgBeforeDash,
		RunE: func(cmd *cobra.Command, args []string) error { return app.runSSH(cmd, args, o) },
	}
}

// sshOneArgBeforeDash is the positional check of ssh: the one positional is <group>; everything after -- is the
// remote command and is not counted.
func sshOneArgBeforeDash(cmd *cobra.Command, args []string) error {
	n := cmd.ArgsLenAtDash()
	if n < 0 {
		n = len(args)
	}
	if n != 1 {
		return fmt.Errorf("%w 1 arg(s) before --, received %d", errAccepts, n)
	}
	return nil
}

// runSSH opens a session (or runs the -- command) on an instance of the group, or with --config prints Host blocks.
func (app *App) runSSH(cmd *cobra.Command, args []string, o *sshOptions) error {
	pos, command := args, []string(nil)
	if n := cmd.ArgsLenAtDash(); n >= 0 {
		pos, command = args[:n], args[n:]
	}
	group := pos[0]
	if o.useConfig && len(command) > 0 {
		return clierr.New(
			clierr.CodeUsage,
			clierr.KindUsage,
			"--config prints Host blocks and cannot be combined with a -- command",
		)
	}
	// Git Bash turns a remote path such as /opt into C:/Program Files/Git/opt
	// before the CLI sees it; the remote command would run on the wrong path.
	if cerr := app.checkCommandRewrites(command); cerr != nil {
		return cerr
	}

	client, _, cerr := app.NewClient(cmd)
	if cerr != nil {
		return cerr
	}
	collection, cerr := client.ListInstances(cmd.Context(), group)
	if cerr != nil {
		return groupNotFound(cerr, group, client.Org(), client.Project())
	}
	if o.useConfig {
		return app.sshPrintConfig(collection.Instances, group, o)
	}
	return app.sshConnect(collection.Instances, group, o, command)
}

// sshPrintConfig writes ssh_config Host blocks for the group's running instances, or only --instance, to stdout.
func (app *App) sshPrintConfig(
	instances []containergroups.ContainerGroupInstance,
	group string,
	o *sshOptions,
) error {
	if o.instance != "" {
		inst, err := resolveSSHInstance(instances, group, o.instance)
		if err != nil {
			return err
		}
		instances = []containergroups.ContainerGroupInstance{*inst}
	}
	text, n := app.renderSSHConfigIdentity(group, instances, o.user, app.resolveIdentity(o.identity))
	if n == 0 {
		return clierr.New(clierr.CodeGeneric, clierr.KindAPIError,
			"no running SSH-ready instances in container group %s", group).
			WithHint("to list its instances, run saladcloud container-groups instances list -c %s", group)
	}
	_, err := io.WriteString(app.Stdout, text)
	return wrapErr(err)
}

// sshConnect runs ssh to the picked instance of the group: a shell, or the one-shot command when one is given.
func (app *App) sshConnect(
	instances []containergroups.ContainerGroupInstance,
	group string,
	o *sshOptions,
	command []string,
) error {
	inst, cerr := pickInstance(app.errw(), instances, group, o.instance)
	if cerr != nil {
		return cerr
	}
	target, cerr := sshTargetOf(inst, group)
	if cerr != nil {
		return cerr
	}

	sshArgs := app.buildSSHArgs(target, o.user, app.resolveIdentity(o.identity), command)
	runErr := app.runChild("ssh", sshArgs)
	if len(command) > 0 {
		if note := oneShotExitNote(childExitCode(runErr)); note != "" {
			fprintln(app.errw(), note)
		}
	}
	if runErr != nil {
		return runErr
	}
	return nil
}
