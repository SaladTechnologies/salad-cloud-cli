// sftp opens an INTERACTIVE sftp session on a running instance — the
// exploratory counterpart to 'saladcloud container-groups cp' (which drives sftp with a
// one-command batch file). Same relay endpoint, same instance resolution,
// same flags as ssh; exit codes propagate correctly over SFTP.

package cli

import (
	"github.com/spf13/cobra"
)

// addSFTPCommands adds sftp.go's commands to the tree.
func (app *App) addSFTPCommands(cg *cobra.Command) {
	var (
		user     string
		identity string
		instance string
	)

	sftpCmd := &cobra.Command{
		Use:   "sftp <group> [--instance <id>]",
		Short: "Open an interactive SFTP session on a running instance",
		Long: `Open an interactive SFTP session on a running instance of a container
group, over the same relay endpoint as saladcloud container-groups ssh.
--instance picks the instance and may be an ID prefix; without it the CLI
picks the group's first running instance and says which on stderr.

For one-shot scripted transfers, use saladcloud container-groups cp instead.
The system sftp client must be on PATH, and authentication uses the one SSH
public key registered on your account; to set it up, run saladcloud
ssh-key setup.`,
		Example: `  # Browse the first running instance of my-app
  saladcloud container-groups sftp my-app

  # A specific instance (ID prefix is enough)
  saladcloud container-groups sftp my-app --instance 8a2f13ba`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			group := args[0]
			client, _, cerr := app.NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			collection, cerr := client.ListInstances(cmd.Context(), group)
			if cerr != nil {
				return groupNotFound(cerr, group, client.Org(), client.Project())
			}
			inst, cerr := pickInstance(app.errw(), collection.Instances, group, instance)
			if cerr != nil {
				return cerr
			}
			target, cerr := sshTargetOf(inst, group)
			if cerr != nil {
				return cerr
			}

			//nolint:revive // if-return: returned directly, a nil *clierr.Error would be a non-nil error
			if runErr := app.runChild(
				"sftp",
				app.buildSFTPArgs(target, user, app.resolveIdentity(identity), ""),
			); runErr != nil {
				return runErr
			}
			return nil
		},
	}
	sftpCmd.Flags().StringVar(&instance, ancestorFlagInstance, "",
		"the instance to open: its ID or a unique prefix (default: the first running instance)")
	sftpCmd.Flags().StringVar(&user, "user", "root",
		"SSH username; selects the container user, which the image must define (root always exists)")
	sftpCmd.Flags().StringVarP(&identity, "identity", "i", "", "identity (private key) file, passed through to sftp -i")
	app.addHiddenGroupFlag(sftpCmd)
	annotate(sftpCmd, annotationFlagForArg, ancestorFlagInstance)
	cg.AddCommand(sftpCmd)
}
