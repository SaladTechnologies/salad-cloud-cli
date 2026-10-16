// gateway: the narrow AI Gateway commands: models, and a key check.
//
// No chat client (a settled decision in AGENTS.md). The gateway is
// OpenAI-compatible precisely so nothing needs wrapping, and an agent
// already has a better client than a CLI for it. What belongs here is the
// setup surface: the separate key, model discovery, and one completion to
// prove the key is live — with a prompt of the user's choosing, because a
// key check that says something back is also the demo.

package cli

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
)

// gatewayTestDefaultPrompt is what `ai-gateway test` sends when no --prompt is
// given: short, and it asks for a short answer.
const gatewayTestDefaultPrompt = "Reply with one short sentence confirming you received this."

// Token budgets for `ai-gateway test`. The gateway's models are reasoning
// models; with thinking disabled (the default here) a one-line answer costs a
// handful of tokens, with thinking on it takes hundreds of tokens of
// reasoning before the first word.
const (
	gatewayTestMaxTokens         = 64
	gatewayTestMaxTokensThinking = 1024
)

// defaultMaxTokens is ai-gateway test's budget when --max-tokens is not set.
func defaultMaxTokens(thinking bool) int {
	if thinking {
		return gatewayTestMaxTokensThinking
	}
	return gatewayTestMaxTokens
}

// gatewayTestResult is `ai-gateway test`'s JSON contract.
type gatewayTestResult struct {
	Model  string         `json:"model"`
	Prompt string         `json:"prompt"`
	Reply  string         `json:"reply"`
	Usage  map[string]any `json:"usage"`
	OK     bool           `json:"ok"`
}

// newGateway resolves configuration and builds the gateway client. Its
// errors from the gateway go through gatewayFailed.
func newGateway() (*api.Gateway, *gatewayKey, *clierr.Error) {
	res, err := config.Resolve("", "")
	if err != nil {
		path, _ := config.Path()
		return nil, nil, clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot read the config file %s: %s", path, readReason(err)).
			WithHint("fix or delete it")
	}
	from := originOf(res.GatewayAPIKeySource, "", envGatewayKey)
	if res.GatewayAPIKey != "" {
		key, ok := normalizeKey(res.GatewayAPIKey)
		if !ok {
			return nil, nil, malformedKey(labelGatewayKey, key, from)
		}
		res.GatewayAPIKey = key
	}
	saved, _ := config.Load()
	g, cerr := api.NewGateway(res)
	return g, &gatewayKey{res.GatewayAPIKey, from, saved.GatewayAPIKey}, cerr
}

// gatewayKey is the AI Gateway key a command uses, where it came from, and
// the saved one.
type gatewayKey struct{ key, from, saved string }

// failed turns the gateway's 401, 402 and 403 into errors that name the key
// and the fix for where it came from.
func (k *gatewayKey) failed(cerr *clierr.Error) *clierr.Error {
	if cerr == nil || cerr.HTTPStatus == nil || k == nil {
		return cerr
	}
	switch *cerr.HTTPStatus {
	case http.StatusUnauthorized:
		e := clierr.New(clierr.CodeAuth, clierr.KindAuth, "AI Gateway key %s%s was rejected", maskAPIKey(k.key), fromTag(k.from)).
			WithStatus(http.StatusUnauthorized)
		switch {
		case quotedKey(k.key):
			return e.WithHint("remove the quotes around it")
		case k.from != envGatewayKey:
			return e.WithHint("run saladcloud ai-gateway login to save a new key")
		case k.saved != "" && k.saved != k.key:
			return e.WithHint(
				"remove SALAD_AI_GATEWAY_API_KEY to use the saved AI Gateway key (%s), or set it to a working key",
				maskAPIKey(k.saved),
			)
		}
		return e.WithHint(
			"set SALAD_AI_GATEWAY_API_KEY to a working key, or remove it and run saladcloud ai-gateway login",
		)
	case http.StatusForbidden:
		return clierr.New(clierr.CodeAuth, clierr.KindAuth, "AI Gateway key %s%s has no AI Gateway access", maskAPIKey(k.key), fromTag(k.from)).
			WithStatus(http.StatusForbidden).
			WithHint("check that you are still a member of its organization; if you are, contact support")
	case http.StatusPaymentRequired:
		return clierr.New(clierr.CodeNotAvailable, clierr.KindNotAvailable, "the AI Gateway key's organization is out of credits").
			WithStatus(http.StatusPaymentRequired).
			WithHint("add credits from the organization's AI Gateway page: %s", gatewayKeyPage)
	}
	return cerr
}

// pickModel chooses the model for `ai-gateway test`: the flag when given
// (validated against the listing when one is available), else the first
// listed model in sorted order — the gateway's own listing order changes
// between calls, and a default that changes is not a default.
func pickModel(flag string, ids []string) (string, *clierr.Error) {
	ids = append([]string(nil), ids...)
	sort.Strings(ids)
	if flag != "" {
		if len(ids) == 0 {
			return flag, nil
		}
		if slices.Contains(ids, flag) {
			return flag, nil
		}
		return "", clierr.New(
			clierr.CodeInvalidInput,
			clierr.KindInvalidInput,
			`model "%s" is not available to this key`,
			flag,
		).WithHint("pass one of these to --model; %s", strings.Join(ids, ", "))
	}
	model, ok := api.DefaultModel(ids)
	if !ok {
		return "", clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "the AI Gateway listed no models for this key").
			WithHint("pass --model")
	}
	return model, nil
}

func init() {
	gatewayCmd := &cobra.Command{
		Use:   "ai-gateway",
		Short: "AI Gateway: list models and verify the gateway key",
		Long: `The SaladCloud AI Gateway is an OpenAI-compatible endpoint
(` + api.DefaultGatewayURL + `) with its own, organization-scoped key, not the
SaladCloud API key. To save it, run saladcloud ai-gateway login, or set
SALAD_AI_GATEWAY_API_KEY (get one at ` + gatewayKeyPage + `).

This is deliberately a small surface: discover models and check the key.
To actually use the gateway, point any OpenAI client (or curl) at the
endpoint with the key as a bearer token; there is nothing a CLI wrapper
could add to that call.

To see which key is in effect and where it came from, run saladcloud whoami.`,
	}

	modelsCmd := &cobra.Command{
		Use:     "models",
		Aliases: []string{"model"},
		Short:   "The models the AI Gateway serves",
	}
	modelsListCmd := &cobra.Command{
		Use:   "list",
		Short: "List the models the AI Gateway serves (no key needed)",
		Long: `List the models the AI Gateway serves. The list is public and the same for
every key, so no AI Gateway key is needed; with one set, it is sent.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			g, _, cerr := newGateway()
			if cerr != nil && cerr.Code == clierr.CodeAuth {
				g, cerr = api.NewPublicGateway(), nil
			}
			if cerr != nil {
				return cerr
			}
			models, cerr := g.ListModels(cmd.Context())
			if cerr != nil {
				return cerr
			}
			if output.Mode() == "table" {
				fmt.Fprintf(os.Stderr, "pricing and the subscription tier: %s\n", api.GatewayPricingURL)
			}
			// The list envelope every list has; each model stays the
			// gateway's own object.
			result := gatewayModelsResult{Items: models.Data}
			if result.Items == nil {
				result.Items = []map[string]any{}
			}
			return wrapErr(output.Emit(result, func(w io.Writer) {
				fmt.Fprintln(w, "MODEL\tOWNED BY")
				for _, m := range result.Items {
					id, _ := m["id"].(string)
					owner, _ := m["owned_by"].(string)
					if owner == "" {
						owner = "-"
					}
					fmt.Fprintf(w, "%s\t%s\n", id, owner)
				}
			}))
		},
	}
	modelsCmd.AddCommand(modelsListCmd)

	var (
		model     string
		prompt    string
		maxTokens int
		thinking  bool
	)
	testCmd := &cobra.Command{
		Use:   "test",
		Short: "Send one short completion to verify the gateway key works (exit 0 on a reply)",
		Long: `Send one user message to a model and print the reply. This proves the
gateway key is accepted and billed for a model, the one thing no other
command can tell you. It is not a chat: no history, no streaming, no
system prompt. Use any OpenAI client for that.

--model defaults to the first model in sorted order; --prompt defaults to a
request for a one-sentence acknowledgement. The models reason before they
answer, which costs hundreds of tokens and can eat the whole budget, so
thinking is turned off for this check; pass --thinking to see it (the
budget then defaults to 1024).`,
		Example: `  saladcloud ai-gateway test
  saladcloud ai-gateway test --prompt "say hello in one sentence"
  saladcloud ai-gateway test --model <model-id> -o json   # ids: saladcloud ai-gateway models list`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			g, key, cerr := newGateway()
			if cerr != nil {
				return cerr
			}
			var ids []string
			if models, merr := g.ListModels(cmd.Context()); merr == nil {
				ids = models.IDs()
			} else if model == "" {
				return key.failed(merr) // no model to fall back to; surface the real error
			}
			chosen, cerr := pickModel(model, ids)
			if cerr != nil {
				return cerr
			}
			if prompt == "" {
				prompt = gatewayTestDefaultPrompt
			}
			if !cmd.Flags().Changed("max-tokens") {
				maxTokens = defaultMaxTokens(thinking)
			}
			c, cerr := g.Complete(cmd.Context(), chosen, prompt, maxTokens, thinking)
			if cerr != nil {
				return key.failed(cerr)
			}
			reply := c.Reply()
			if reply == "" {
				if c.FinishReason() == "length" || c.ReasoningLen() > 0 {
					return clierr.New(clierr.CodeGeneric, clierr.KindAPIError,
						"the key works, but %s spent the whole %d-token budget reasoning (%s)",
						chosen, maxTokens, usageSummary(c.Usage)).WithHint("raise --max-tokens or drop --thinking")
				}
				return clierr.New(
					clierr.CodeGeneric,
					clierr.KindAPIError,
					"the gateway answered but the completion was empty (model %s)",
					chosen,
				)
			}
			v := gatewayTestResult{Model: chosen, Prompt: prompt, Reply: reply, Usage: c.Usage, OK: true}
			return wrapErr(output.Emit(v, func(w io.Writer) {
				fmt.Fprintf(w, "%s\n", reply)
				fmt.Fprintf(os.Stderr, "AI Gateway key works: model %s answered (%s)\n", chosen, usageSummary(c.Usage))
			}))
		},
	}
	testCmd.Flags().StringVar(&model, "model", "", "model id (default: the first model id in sorted order)")
	testCmd.Flags().StringVar(&prompt, "prompt", "", "the one user message to send")
	// The default depends on --thinking, so the flag's own default is 0 and
	// the help states both: cobra would otherwise print a second default.
	testCmd.Flags().
		IntVar(&maxTokens, "max-tokens", 0, fmt.Sprintf("completion token budget (default %d, or %d with --thinking)", gatewayTestMaxTokens, gatewayTestMaxTokensThinking))
	testCmd.Flags().
		BoolVar(&thinking, "thinking", false, "let the model reason before answering (hundreds of tokens; off by default)")

	gatewayCmd.AddCommand(gatewayLoginCmd(), gatewayLogoutCmd(), modelsCmd, testCmd)
	Register(gatewayCmd)
}

// usageSummary renders token usage compactly, in a stable key order.
func usageSummary(u map[string]any) string {
	if len(u) == 0 {
		return "no usage reported"
	}
	keys := make([]string, 0, len(u))
	for k := range u {
		if _, ok := u[k].(float64); ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %v", strings.ReplaceAll(k, "_", " "), u[k]))
	}
	return strings.Join(parts, ", ")
}

// gatewayModelsResult is ai-gateway models list's JSON: {"items": [...]}, each
// item the gateway's model object as it sent it.
type gatewayModelsResult struct {
	Items []map[string]any `json:"items"`
}
