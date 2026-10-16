package cli

import (
	"fmt"
	"io"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/webhooksecretkey"
	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// webhook-secret is a singleton (one per organization): get plus the
// rotate action, never list. Both print secret_key whole: returning it is
// the command's purpose, like 'gh auth token'; masking applies to stored
// credentials, not to this.
func init() {
	wsCmd := &cobra.Command{
		Use:   "webhook-secret",
		Short: "The key that signs the organization's webhook deliveries (get, rotate)",
		Long: `The organization's webhook secret key: the platform signs every webhook it
delivers (inference and queue job webhooks) with it, so a receiver can
verify a delivery came from SaladCloud. There is one per organization.

Both commands print the key itself; keep their output out of logs.`,
	}

	emit := func(k *webhooksecretkey.WebhookSecretKey) error {
		return wrapErr(output.Emit(k, func(w io.Writer) {
			fmt.Fprintln(w, ptr.Deref(k.SecretKey))
		}))
	}
	run := func(call func(c *api.Client, cmd *cobra.Command) (*webhooksecretkey.WebhookSecretKey, *clierr.Error)) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			client, _, cerr := NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			k, cerr := call(client, cmd)
			if cerr != nil {
				return cerr
			}
			return emit(k)
		}
	}

	wsCmd.AddCommand(&cobra.Command{
		Use:   "get",
		Short: "Print the organization's webhook secret key",
		Args:  cobra.NoArgs,
		RunE: run(func(c *api.Client, cmd *cobra.Command) (*webhooksecretkey.WebhookSecretKey, *clierr.Error) {
			return c.GetWebhookSecret(cmd.Context())
		}),
	})
	wsCmd.AddCommand(&cobra.Command{
		Use:   "rotate",
		Short: "Replace the webhook secret key and print the new one",
		Long: `Replace the organization's webhook secret key and print the new one.
Deliveries are signed with the new key from now on, so receivers that
verify with the old one start rejecting them until they are updated.`,
		Args: cobra.NoArgs,
		RunE: run(func(c *api.Client, cmd *cobra.Command) (*webhooksecretkey.WebhookSecretKey, *clierr.Error) {
			return c.RotateWebhookSecret(cmd.Context())
		}),
	})
	orgScoped(wsCmd)
	Register(wsCmd)
}
