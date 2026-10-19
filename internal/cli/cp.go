// cp copies files to/from a running instance by driving the system sftp
// client against the same relay endpoint 'saladcloud container-groups ssh' uses. SFTP is
// the reliable half of the relay: unlike one-shot ssh commands, sftp exit
// codes propagate correctly, so cp's exit code is trustworthy.

package cli

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
)

// remoteSpec is one side of a cp transfer in <group>[/<instance>]:<path> form.
type remoteSpec struct {
	Group          string
	InstancePrefix string
	Path           string
}

const remoteSpecSyntax = "<group>[/<instance-prefix>]:<path>"

// cpResult is cp's output: what was copied, from or to which instance, and
// that it happened.
type cpResult struct {
	Group    string `json:"group"`
	Instance string `json:"instance"`
	Action   string `json:"action"`
	OK       bool   `json:"ok"`
	Src      string `json:"src"`
	Dst      string `json:"dst"`
}

// msysPathList matches what Git Bash (MSYS) makes of an argument such as
// my-app/8a2f:/data before the CLI sees it: a slash before the colon makes
// it look like a POSIX path list, which MSYS converts into a Windows one,
// my-app\8a2f;C:\Program Files\Git\data.
var msysPathList = regexp.MustCompile(`;[A-Za-z]:\\`)

// msysRewritten reports an argument Git Bash rewrote as a Windows path list.
// No real path takes that shape: a Windows file name cannot hold a colon.
func (app *App) msysRewritten(arg string) *clierr.Error {
	if !app.underGitBash() || !msysPathList.MatchString(arg) {
		return nil
	}
	return clierr.New(clierr.CodeUsage, clierr.KindUsage,
		`"%s" was rewritten by Git Bash, which reads <group>/<instance>:<path> as a path list`, arg).
		WithHint("run the command again with MSYS_NO_PATHCONV=1 in front of it, or from PowerShell or cmd")
}

// parseRemoteSpec classifies one cp argument. It returns (nil, nil) for a
// local path. Mirroring scp's heuristics, a Windows drive-letter path
// (C:\x or C:/x) or anything with a backslash before the first colon is
// local; otherwise a colon marks the argument as remote.
func (app *App) parseRemoteSpec(arg string) (*remoteSpec, *clierr.Error) {
	if cerr := app.msysRewritten(arg); cerr != nil {
		return nil, cerr
	}
	head, path, ok := strings.Cut(arg, ":")
	if !ok || cpLocalHead(head) {
		return nil, nil
	}
	if head == "" || path == "" {
		return nil, badRemotePath(arg)
	}
	return remoteSpecOf(arg, head, path)
}

// cpLocalHead reports whether head, the part of a cp argument before its first colon, makes the argument a local
// path: a Windows drive letter, or anything with a backslash.
func cpLocalHead(head string) bool {
	if len(head) == 1 && isASCIILetter(head[0]) {
		return true // Windows drive letter
	}
	return strings.Contains(head, `\`)
}

// remoteSpecOf splits the remote cp argument arg, head:path with neither part empty, into its group, instance prefix
// and path.
func remoteSpecOf(arg, head, path string) (*remoteSpec, *clierr.Error) {
	parts := strings.Split(head, "/")
	switch len(parts) {
	case 1:
		return &remoteSpec{Group: parts[0], Path: path, InstancePrefix: ""}, nil
	case 2:
		if parts[0] == "" || parts[1] == "" {
			return nil, badRemotePath(arg)
		}
		return &remoteSpec{Group: parts[0], InstancePrefix: parts[1], Path: path}, nil
	default:
		return nil, badRemotePath(arg)
	}
}

// badRemotePath is the error for a remote argument of the wrong shape.
func badRemotePath(arg string) *clierr.Error {
	return clierr.New(clierr.CodeUsage, clierr.KindUsage, `invalid remote path "%s"`, arg).
		WithHint("write it as %s", remoteSpecSyntax)
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// sftpQuoteLocal renders a local path as one sftp batch token. Windows
// backslashes become forward slashes (which Windows OpenSSH accepts for
// local paths) so they are not eaten as batch-file escapes.
func sftpQuoteLocal(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	return `"` + strings.ReplaceAll(p, `"`, `\"`) + `"`
}

// sftpQuoteRemote renders a remote path as one sftp batch token,
// backslash-escaping the batch tokenizer's special characters.
func sftpQuoteRemote(p string) string {
	p = strings.ReplaceAll(p, `\`, `\\`)
	return `"` + strings.ReplaceAll(p, `"`, `\"`) + `"`
}

// buildSFTPBatch renders the one-command sftp batch file: put (upload)
// when the destination is remote, get (download) when the source is.
func buildSFTPBatch(upload, recursive bool, localPath, remotePath string) string {
	flag := ""
	if recursive {
		flag = " -r"
	}
	if upload {
		return fmt.Sprintf("put%s %s %s\n", flag, sftpQuoteLocal(localPath), sftpQuoteRemote(remotePath))
	}
	return fmt.Sprintf("get%s %s %s\n", flag, sftpQuoteRemote(remotePath), sftpQuoteLocal(localPath))
}

// buildSFTPArgs builds the argv (after the "sftp" program name). An empty
// batchPath means an interactive session (no -b flag) — 'saladcloud container-groups sftp';
// cp always passes its one-command batch file.
func (app *App) buildSFTPArgs(t sshTarget, user, identity, batchPath string) []string {
	args := append([]string{"-P", strconv.FormatInt(t.Port, 10)}, app.hostKeyOptions(t.InstanceID)...)
	if identity != "" {
		args = append(args, "-i", identity)
	}
	if batchPath != "" {
		args = append(args, "-b", batchPath)
	}
	return append(args, user+"@"+t.IP)
}

// cpOptions are the flags of 'container-groups cp'.
type cpOptions struct {
	user, identity string
	recursive      bool
}

// addCpCommands adds cp.go's commands to the tree.
func (app *App) addCpCommands(cg *cobra.Command) {
	var o cpOptions
	cpCmd := app.newCpCmd(&o)
	cpCmd.Flags().
		StringVar(&o.user, "user", "root", "SSH username; selects the container user, which the image must define (root always exists)")
	cpCmd.Flags().StringVarP(&o.identity, "identity", "i", "", "identity (private key) file, passed through to sftp -i")
	cpCmd.Flags().BoolVarP(&o.recursive, "recursive", "r", false, "copy directories recursively")
	app.addHiddenGroupFlag(cpCmd)
	cg.AddCommand(cpCmd)
}

// newCpCmd is 'saladcloud container-groups cp <src> <dst>', its flags bound to o by addCpCommands.
func (app *App) newCpCmd(o *cpOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "cp <src> <dst>",
		Short: "Copy files to or from an instance over SFTP",
		Long: `Copy files between the local machine and a running instance over SFTP,
through the same relay endpoint as saladcloud container-groups ssh. Exactly one side must
be remote, written as ` + remoteSpecSyntax + `; the instance
prefix is optional (the group's first running instance is used without
it). The system sftp client must be on PATH. stdout carries one result
(with -o json: {"group","instance","action":"copied","ok","src","dst"}).
In table mode everything sftp prints goes to stderr; with -o json or
-o yaml it is held back, and when sftp fails its last line ends the
error's message. When sftp fails, its exit code (which the relay
propagates correctly for SFTP) becomes the CLI's exit code, with kind
child_exit.

With -r, directories follow sftp's rule: when the destination directory
already exists, the source is copied inside it (cp -r ./input my-app:/data
makes /data/input); when it does not, it is created as the copy. To
download only a directory's contents, end the remote path with /.

In Git Bash, an argument with a slash before the colon
(my-app/8a2f13ba:/data) is rewritten as a Windows path list before the
CLI sees it; put MSYS_NO_PATHCONV=1 in front of the command there.

Authentication uses the one SSH public key registered on your account; to
set it up, run saladcloud ssh-key setup.`,
		Example: `  # Upload a local file into an instance of my-app
  saladcloud container-groups cp model.safetensors my-app:/models/model.safetensors

  # Download results from a specific instance (ID prefix)
  saladcloud container-groups cp my-app/8a2f13ba:/outputs/result.json ./result.json

  # Recursive directory copy (lands in /data/dataset when /data exists)
  saladcloud container-groups cp -r ./dataset my-app:/data

  # Download a directory's contents into ./results
  saladcloud container-groups cp -r my-app:/outputs/. ./results`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error { return app.runCp(cmd, args, o) },
	}
}

// runCp copies between the local path and the remote side of args, one sftp batch command.
func (app *App) runCp(cmd *cobra.Command, args []string, o *cpOptions) error {
	plan, cerr := app.cpPlanOf(args)
	if cerr != nil {
		return cerr
	}
	target, cerr := app.cpTarget(cmd, plan.spec)
	if cerr != nil {
		return cerr
	}
	batch, cerr := app.cpWriteBatch(buildSFTPBatch(plan.upload, o.recursive, plan.local, plan.spec.Path))
	if cerr != nil {
		return cerr
	}
	defer app.cpRemoveBatch(batch)

	// sftp echoes its batch ("sftp> get ...") on stdout; in table
	// mode that goes to stderr with its progress, so stdout carries
	// the one result. In json and yaml mode stderr carries only
	// warnings, notes and the error document, so sftp's output is
	// held back, and its last line ends the error when it fails.
	sftpArgs := app.buildSFTPArgs(target, o.user, app.resolveIdentity(o.identity), batch)
	var runErr *clierr.Error
	if app.printer.Machine() {
		runErr = app.runChildQuiet(app.SFTPProgram, sftpArgs)
	} else {
		runErr = app.runChildTo(app.errw(), app.SFTPProgram, sftpArgs)
	}
	if runErr != nil {
		return runErr
	}
	v := cpResult{
		Group:    plan.spec.Group,
		Instance: target.InstanceID,
		Action:   "copied",
		OK:       true,
		Src:      args[0],
		Dst:      args[1],
	}
	return wrapErr(app.printer.Emit(v, func(w *output.Writer) {
		w.Printf("Copied %s to %s (instance %s)\n", v.Src, v.Dst, v.Instance)
	}))
}

// cpPlan is what a cp command line asks for: the remote side, the local path, and the direction.
type cpPlan struct {
	spec   *remoteSpec
	local  string
	upload bool
}

// cpPlanOf parses cp's two arguments, of which exactly one must be remote.
func (app *App) cpPlanOf(args []string) (cpPlan, *clierr.Error) {
	srcSpec, cerr := app.parseRemoteSpec(args[0])
	if cerr != nil {
		return cpPlan{}, cerr
	}
	dstSpec, cerr := app.parseRemoteSpec(args[1])
	if cerr != nil {
		return cpPlan{}, cerr
	}
	if srcSpec != nil && dstSpec != nil {
		return cpPlan{}, clierr.New(clierr.CodeUsage, clierr.KindUsage, "both sides are remote").
			WithHint("give exactly one remote side, written as %s", remoteSpecSyntax)
	}
	if srcSpec == nil && dstSpec == nil {
		return cpPlan{}, clierr.New(clierr.CodeUsage, clierr.KindUsage, "no remote side").
			WithHint("give exactly one remote side, written as %s", remoteSpecSyntax)
	}
	if dstSpec != nil {
		return cpPlan{spec: dstSpec, local: args[0], upload: true}, nil
	}
	return cpPlan{spec: srcSpec, local: args[1], upload: false}, nil
}

// cpTarget finds the SSH endpoint of the instance spec names (the group's first running one without a prefix).
func (app *App) cpTarget(cmd *cobra.Command, spec *remoteSpec) (sshTarget, *clierr.Error) {
	client, _, cerr := app.NewClient(cmd)
	if cerr != nil {
		return sshTarget{}, cerr
	}
	collection, cerr := client.ListInstances(cmd.Context(), spec.Group)
	if cerr != nil {
		return sshTarget{}, groupNotFound(cerr, spec.Group, client.Org(), client.Project())
	}
	inst, cerr := pickInstance(app.errw(), collection.Instances, spec.Group, spec.InstancePrefix)
	if cerr != nil {
		return sshTarget{}, cerr
	}
	return sshTargetOf(inst, spec.Group)
}

// cpWriteBatch writes the sftp batch file and returns its name; the caller removes it with cpRemoveBatch. A batch
// file that cannot be written is removed here.
func (app *App) cpWriteBatch(content string) (string, *clierr.Error) {
	batch, err := os.CreateTemp("", "saladcloud-sftp-*.batch")
	if err != nil {
		return "", clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot create the sftp batch file: %v", err)
	}
	if _, err := batch.WriteString(content); err != nil {
		err = errors.Join(err, batch.Close())
		app.cpRemoveBatch(batch.Name())
		return "", clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot write the sftp batch file: %v", err)
	}
	if err := batch.Close(); err != nil {
		app.cpRemoveBatch(batch.Name())
		return "", clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot write the sftp batch file: %v", err)
	}
	return batch.Name(), nil
}

// cpRemoveBatch removes the sftp batch file name, warning on stderr when it cannot.
func (app *App) cpRemoveBatch(name string) {
	if err := os.Remove(name); err != nil {
		fprintf(app.errw(), "warning: could not remove the sftp batch file %s: %v\n", name, err)
	}
}
