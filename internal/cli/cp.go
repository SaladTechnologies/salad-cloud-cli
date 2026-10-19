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

// sftpProgram is the sftp client cp runs. Tests replace it.
var sftpProgram = "sftp"

// msysPathList matches what Git Bash (MSYS) makes of an argument such as
// my-app/8a2f:/data before the CLI sees it: a slash before the colon makes
// it look like a POSIX path list, which MSYS converts into a Windows one,
// my-app\8a2f;C:\Program Files\Git\data.
var msysPathList = regexp.MustCompile(`;[A-Za-z]:\\`)

// msysRewritten reports an argument Git Bash rewrote as a Windows path list.
// No real path takes that shape: a Windows file name cannot hold a colon.
func msysRewritten(arg string) *clierr.Error {
	if !underGitBash() || !msysPathList.MatchString(arg) {
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
func parseRemoteSpec(arg string) (*remoteSpec, *clierr.Error) {
	if cerr := msysRewritten(arg); cerr != nil {
		return nil, cerr
	}
	head, path, ok := strings.Cut(arg, ":")
	if !ok {
		return nil, nil
	}
	if len(head) == 1 && isASCIILetter(head[0]) {
		return nil, nil // Windows drive letter
	}
	if strings.Contains(head, `\`) {
		return nil, nil
	}
	if head == "" || path == "" {
		return nil, badRemotePath(arg)
	}
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
func buildSFTPArgs(t sshTarget, user, identity, batchPath string) []string {
	args := append([]string{"-P", strconv.FormatInt(t.Port, 10)}, hostKeyOptions(t.InstanceID)...)
	if identity != "" {
		args = append(args, "-i", identity)
	}
	if batchPath != "" {
		args = append(args, "-b", batchPath)
	}
	return append(args, user+"@"+t.IP)
}

func init() {
	var (
		user      string
		identity  string
		recursive bool
	)

	cpCmd := &cobra.Command{
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
		RunE: func(cmd *cobra.Command, args []string) error {
			srcSpec, cerr := parseRemoteSpec(args[0])
			if cerr != nil {
				return cerr
			}
			dstSpec, cerr := parseRemoteSpec(args[1])
			if cerr != nil {
				return cerr
			}
			if srcSpec != nil && dstSpec != nil {
				return clierr.New(clierr.CodeUsage, clierr.KindUsage, "both sides are remote").
					WithHint("give exactly one remote side, written as %s", remoteSpecSyntax)
			}
			if srcSpec == nil && dstSpec == nil {
				return clierr.New(clierr.CodeUsage, clierr.KindUsage, "no remote side").
					WithHint("give exactly one remote side, written as %s", remoteSpecSyntax)
			}
			upload := dstSpec != nil
			spec, local := dstSpec, args[0]
			if !upload {
				spec, local = srcSpec, args[1]
			}

			client, _, cerr := NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			collection, cerr := client.ListInstances(cmd.Context(), spec.Group)
			if cerr != nil {
				return groupNotFound(cerr, spec.Group, client.Org(), client.Project())
			}
			inst, cerr := pickInstance(os.Stderr, collection.Instances, spec.Group, spec.InstancePrefix)
			if cerr != nil {
				return cerr
			}
			target, cerr := sshTargetOf(inst, spec.Group)
			if cerr != nil {
				return cerr
			}

			batch, err := os.CreateTemp("", "saladcloud-sftp-*.batch")
			if err != nil {
				return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot create the sftp batch file: %v", err)
			}
			defer func() {
				if err := os.Remove(batch.Name()); err != nil {
					fprintf(errw(), "warning: could not remove the sftp batch file %s: %v\n", batch.Name(), err)
				}
			}()
			if _, err := batch.WriteString(buildSFTPBatch(upload, recursive, local, spec.Path)); err != nil {
				err = errors.Join(err, batch.Close())
				return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot write the sftp batch file: %v", err)
			}
			if err := batch.Close(); err != nil {
				return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot write the sftp batch file: %v", err)
			}

			// sftp echoes its batch ("sftp> get ...") on stdout; in table
			// mode that goes to stderr with its progress, so stdout carries
			// the one result. In json and yaml mode stderr carries only
			// warnings, notes and the error document, so sftp's output is
			// held back, and its last line ends the error when it fails.
			sftpArgs := buildSFTPArgs(target, user, resolveIdentity(identity), batch.Name())
			var runErr *clierr.Error
			if output.Machine() {
				runErr = runChildQuiet(sftpProgram, sftpArgs)
			} else {
				runErr = runChildTo(os.Stderr, sftpProgram, sftpArgs)
			}
			if runErr != nil {
				return runErr
			}
			v := cpResult{
				Group:    spec.Group,
				Instance: target.InstanceID,
				Action:   "copied",
				OK:       true,
				Src:      args[0],
				Dst:      args[1],
			}
			return wrapErr(output.Emit(v, func(w *output.Writer) {
				w.Printf("Copied %s to %s (instance %s)\n", v.Src, v.Dst, v.Instance)
			}))
		},
	}
	cpCmd.Flags().
		StringVar(&user, "user", "root", "SSH username; selects the container user, which the image must define (root always exists)")
	cpCmd.Flags().StringVarP(&identity, "identity", "i", "", "identity (private key) file, passed through to sftp -i")
	cpCmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "copy directories recursively")
	addHiddenGroupFlag(cpCmd)
	RegisterContainerGroups(cpCmd)
}
