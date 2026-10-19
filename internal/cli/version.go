package cli

import (
	"runtime"

	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
	"github.com/saladtechnologies/salad-cloud-cli/internal/version"
)

type versionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Stage     string `json:"stage"`      // "beta" until the CLI leaves beta
	IssuesURL string `json:"issues_url"` // where to report bugs
}

// addVersionCommands adds version.go's commands to the tree.
func (app *App) addVersionCommands(root *cobra.Command) {
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the SaladCloud CLI version",
		Args:  cobra.NoArgs,
		RunE:  app.runVersion,
	})
	// saladcloud --version is the version command, so -o json returns the
	// same document. The root runs only for --version or with no command at
	// all, which prints the help as before.
	root.Flags().Bool("version", false, "print the version (same as saladcloud version)")
	root.RunE = func(cmd *cobra.Command, args []string) error {
		fr := readFlags(cmd.Flags())
		v := fr.Bool("version")
		if cerr := fr.Err(); cerr != nil {
			return cerr
		}
		if v {
			return app.runVersion(cmd, args)
		}
		return cmd.Help()
	}
}

func (app *App) runVersion(_ *cobra.Command, _ []string) error {
	v := versionInfo{
		Version:   version.Version,
		Commit:    version.Commit,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		Stage:     version.Stage,
		IssuesURL: version.IssuesURL,
	}
	return wrapErr(app.printer.Emit(v, func(w *output.Writer) {
		w.Printf("saladcloud %s (%s) %s/%s\n", v.Version, v.Commit, v.OS, v.Arch)
		w.Printf("This is a %s release; report issues at %s\n", v.Stage, v.IssuesURL)
	}))
}
