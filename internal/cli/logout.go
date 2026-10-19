package cli

import (
	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
)

// logoutResult is logout's JSON contract: whether a key was removed, what
// stays saved (null when nothing), the variable that still supplies a key,
// and the action shape, without a name because nothing named is acted on.
type logoutResult struct {
	Removed      bool     `json:"removed"`
	Organization *string  `json:"organization"`
	Project      *string  `json:"project"`
	EnvOverrides []string `json:"env_overrides"`
	Action       string   `json:"action"`
	OK           bool     `json:"ok"`
}

func init() {
	Register(&cobra.Command{
		Use:   "logout",
		Short: "Remove the saved API key (the organization and project stay)",
		Long: `Remove the SaladCloud API key from the config file. The saved organization and
project stay, so a later saladcloud login only needs the key again. When
SALAD_API_KEY is set, commands still use it, and logout says so.`,
		Args: cobra.NoArgs,
		RunE: runLogout,
	})
}

func runLogout(_ *cobra.Command, _ []string) error {
	path, err := config.Path(app.Env)
	if err != nil {
		return clierr.New(
			clierr.CodeGeneric,
			clierr.KindAPIError,
			"cannot find a folder for the config file: %s",
			config.Reason(err),
		)
	}
	f, err := config.Load(app.Env)
	if err != nil {
		return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot read the config file %s: %s", path, readReason(err)).
			WithHint("fix or delete it, then run saladcloud logout again")
	}
	old := f.APIKey
	// Decided before the key is removed: a name equal to it is a key.
	orgJSON, projectJSON := nameOrNil(f.Organization), nameOrNil(f.Project)
	orgShown, projectShown := shownName(f.Organization), shownName(f.Project)
	if old != "" {
		f.APIKey = ""
		if err := config.Save(app.Env, f); err != nil {
			return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot write the config file %s: %s", path, config.Reason(err)).
				WithHint("set SALAD_CLI_CONFIG_DIR to a writable folder, then run saladcloud logout again")
		}
	}
	ovs := stillSupplied(envAPIKey)
	result := logoutResult{
		Removed: old != "", Organization: orgJSON, Project: projectJSON,
		EnvOverrides: overrideNames(ovs), Action: "logged_out", OK: true,
	}
	if err := app.printer.Emit(result, func(w *output.Writer) {
		if old == "" {
			w.Println("No API key was saved; nothing to remove")
			return
		}
		w.Printf("Logged out: removed the saved API key (%s)\n", maskAPIKey(old))
		switch {
		case f.Organization != "" && f.Project != "":
			w.Printf("Organization and project stay saved: %s/%s\n", orgShown, projectShown)
		case f.Organization != "":
			w.Printf("Organization stays saved: %s\n", orgShown)
		}
	}); err != nil {
		return wrapErr(err)
	}
	warnOverrides(errw(), afterRemove, ovs)
	return nil
}
