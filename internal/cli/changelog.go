package cli

import (
	"errors"
	"fmt"
	"io"

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

func init() {
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
		RunE: func(cmd *cobra.Command, _ []string) error {
			since, _ := cmd.Flags().GetString("since")
			all := changelog.Parse(saladcloudcli.Changelog)
			releases, err := changelog.Since(all, since)
			if err != nil {
				if errors.Is(err, changelog.ErrBadSince) {
					return clierr.New(clierr.CodeUsage, clierr.KindUsage, `invalid --since "%s": %v`, since, err)
				}
				return wrapErr(err)
			}
			if releases == nil {
				releases = []changelog.Release{}
			}
			res := changelogResult{Version: version.Version, Items: releases}
			if since != "" {
				res.Since = &since
			}
			return wrapErr(output.Emit(res, func(w io.Writer) {
				if len(releases) == 0 {
					fmt.Fprintf(w, "No releases newer than %s\n", since)
					return
				}
				fmt.Fprint(w, changelog.Render(releases))
			}))
		},
	}
	cmd.Flags().String("since", "", "only releases newer than this date (YYYY-MM-DD) or version (X.Y.Z)")
	Register(cmd)
}
