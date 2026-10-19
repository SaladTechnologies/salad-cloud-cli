package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/version"
)

// orgsLong is the shared note on the organizations endpoint.
const orgsLong = `This uses an organizations endpoint that is not part of the documented API,
so it may change without notice; if it does, this command may fail until the
CLI catches up. The documented commands do not depend on it.`

// addOrgsCommands adds orgs.go's commands to the tree.
func (app *App) addOrgsCommands(root *cobra.Command) {
	orgsCmd := &cobra.Command{
		Use:     "orgs",
		Aliases: []string{"org", "organizations"},
		Short:   "Your organizations (list, get)",
		Long:    "The organizations the API key's account is a member of.\n\n" + orgsLong,
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List your organizations",
		Long:  "List the organizations the API key's account is a member of.\n\n" + orgsLong,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, _, cerr := app.NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			return app.runMaybeWatch(cmd, func(ctx context.Context) *clierr.Error {
				orgs, cerr := client.ListOrganizations(ctx)
				if cerr == nil {
					cerr = unreadableListing(orgs)
				}
				if cerr != nil {
					return cerr
				}
				err := app.printer.Emit(orgs, func(w *output.Writer) {
					w.Println("NAME\tDISPLAY NAME\tCREATED")
					for _, o := range orgs.Items {
						fprintf(
							w,
							"%s\t%s\t%s\n",
							ptr.Deref(o.OrganizationName),
							ptr.Deref(o.DisplayName),
							timeCell(o.CreateTime),
						)
					}
				})
				if err != nil {
					return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "%v", err)
				}
				return nil
			})
		},
	}
	app.addWatchFlags(listCmd)
	orgsCmd.AddCommand(listCmd)

	orgsCmd.AddCommand(&cobra.Command{
		Use:   "get <name>",
		Short: "Show one of your organizations",
		Long: `Show one organization the API key's account is a member of, by its URL
name. It is looked up in the same listing orgs list prints.

` + orgsLong,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, cerr := app.NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			orgs, cerr := client.ListOrganizations(cmd.Context())
			if cerr == nil {
				cerr = unreadableListing(orgs)
			}
			if cerr != nil {
				return cerr
			}
			org := findOrganization(orgs, args[0])
			if org == nil {
				return clierr.New(clierr.CodeNotFound, clierr.KindNotFound,
					`organization "%s" not found`, args[0]).WithHint("to list yours, run saladcloud orgs list")
			}
			return wrapErr(app.printer.Emit(org, func(w *output.Writer) {
				row := func(k, v string) { w.Printf("%s\t%s\n", k, v) }
				row("Name", ptr.Deref(org.OrganizationName))
				row("Display name", ptr.Deref(org.DisplayName))
				row("ID", ptr.Deref(org.OrganizationID))
				row("Created", timeCell(org.CreateTime))
				row("Updated", timeCell(org.UpdateTime))
			}))
		},
	})
	root.AddCommand(orgsCmd)
}

// findOrganization returns the listed organization with the given URL
// name, or nil.
func findOrganization(list *api.OrganizationList, name string) *api.Organization {
	for i := range list.Items {
		if ptr.Deref(list.Items[i].OrganizationName) == name {
			return &list.Items[i]
		}
	}
	return nil
}

// unreadableListing is the error for an organizations listing that came back
// without its items or with nameless rows: the endpoint is outside the
// documented API and has changed shape, which is unknown, never "you have
// no organizations".
func unreadableListing(l *api.OrganizationList) *clierr.Error {
	if l == nil || l.Usable() {
		return nil
	}
	return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "the list of your organizations came back in a form this CLI cannot read").
		WithHint("update the CLI, or report it at %s", version.IssuesURL)
}
