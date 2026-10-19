package cli

import (
	"errors"

	"github.com/spf13/cobra"

	saladcloudcli "github.com/saladtechnologies/salad-cloud-cli"
	"github.com/saladtechnologies/salad-cloud-cli/internal/changelog"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
	"github.com/saladtechnologies/salad-cloud-cli/internal/version"
)

// changelogResult is changelog's JSON contract: the list envelope, one item
// per release, with the CLI's version and --since beside it. Items is never
// null: an empty list means nothing is newer than --since.
type changelogResult struct {
	Version string              `json:"version"` // the running CLI's version
	Since   *string             `json:"since"`   // the --since value as given, or null
	Items   []changelog.Release `json:"items"`
}

// addChangelogCommands adds changelog.go's commands to the tree.
func (app *App) addChangelogCommands(root *cobra.Command) {
	var since string
	cmd := &cobra.Command{
		Use:   "changelog",
		Short: "Print the CLI changelog, optionally only what is newer than a date or version",
		Long: `Print CHANGELOG.md as embedded in this binary, so an agent or script can
learn what changed without network access or a checkout.

--since takes a date (YYYY-MM-DD) or a version (X.Y.Z) and prints only the
releases newer than it; the Unreleased section is always included. Agents
that learned this CLI from older material should run
"saladcloud changelog --since <date they were trained on>" before relying
on remembered behavior.

In JSON mode the result is {"version","since","items":[{"version",
"date","body","title"}]}, one item per release; items is an empty list,
never null.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			all := changelog.Parse(saladcloudcli.Changelog)
			releases, err := changelog.Since(all, since)
			if err != nil {
				if errors.Is(err, changelog.ErrBadSince) {
					return clierr.New(clierr.CodeUsage, clierr.KindUsage, `invalid --since "%s": %v`, since, err)
				}
				return wrapErr(err)
			}
			if releases == nil {
				releases = []changelog.Release{} //nolint:revive // the JSON contract renders none as [], not null
			}
			res := changelogResult{Version: version.String(), Items: releases, Since: nil}
			if since != "" {
				res.Since = &since
			}
			return wrapErr(app.printer.Emit(res, func(w *output.Writer) {
				if len(releases) == 0 {
					w.Printf("No releases newer than %s\n", since)
					return
				}
				w.Print(changelog.Render(releases))
			}))
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "only releases newer than this date (YYYY-MM-DD) or version (X.Y.Z)")
	root.AddCommand(cmd)
}
