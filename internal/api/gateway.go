// The AI Gateway client: the smallest slice of an OpenAI-compatible endpoint
// the CLI has any business wrapping.
//
// The gateway (https://ai.salad.cloud/v1) speaks the OpenAI chat API so that
// every existing client can use it unchanged; a CLI chat verb would be the
// one wrapper that knows nothing its caller does not, so there is none.
// What the CLI does add is the setup surface
// where the gotchas live: a separate, organization-scoped key with its own
// env var name; model IDs that otherwise live only in docs; and "is my key
// live and billed?", which nothing answers short of a completion. Hence
// exactly two calls here — list models, and one minimal completion — and a
// hand-rolled client, since the pinned SDK has no gateway surface.

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

// DefaultGatewayURL is the production AI Gateway base URL.
const DefaultGatewayURL = "https://ai.salad.cloud/v1"

// GatewayPricingURL documents per-token rates and the subscription tier.
const GatewayPricingURL = "https://docs.salad.com/ai-gateway/reference/pricing"

// Gateway is the AI Gateway client.
type Gateway struct {
	key     string
	baseURL string
}

// NewGateway builds a Gateway from resolved configuration. It needs the
// gateway key (not the SaladCloud API key). SALAD_CLI_AI_GATEWAY_BASE_URL overrides
// the base URL (for tests).
func NewGateway(res config.Resolved) (*Gateway, *clierr.Error) {
	if res.GatewayAPIKey == "" {
		return nil, clierr.New(clierr.CodeAuth, clierr.KindAuth, "no AI Gateway key set").
			WithHint("run saladcloud ai-gateway login in a terminal, or set SALAD_AI_GATEWAY_API_KEY")
	}
	return &Gateway{key: res.GatewayAPIKey, baseURL: gatewayBaseURL()}, nil
}

// NewPublicGateway builds a Gateway with no key, for the one public route:
// the model list answers anyone the same.
func NewPublicGateway() *Gateway { return &Gateway{baseURL: gatewayBaseURL()} }

// gatewayBaseURL is the gateway base URL in effect: the default, or
// SALAD_CLI_AI_GATEWAY_BASE_URL (for tests).
func gatewayBaseURL() string {
	if override := os.Getenv("SALAD_CLI_AI_GATEWAY_BASE_URL"); override != "" {
		return strings.TrimRight(override, "/")
	}
	return DefaultGatewayURL
}

// BaseURL returns the gateway base URL in effect.
func (g *Gateway) BaseURL() string { return g.baseURL }

// GatewayModels is the /v1/models response. Each model is kept as its raw
// object so json output shows whatever the gateway returns (the beta adds
// fields), while IDs are extracted for the table and for picking a default.
type GatewayModels struct {
	Object string           `json:"object"`
	Data   []map[string]any `json:"data"`
}

// IDs returns the model ids in response order.
func (m *GatewayModels) IDs() []string {
	out := make([]string, 0, len(m.Data))
	for _, d := range m.Data {
		if id, ok := d["id"].(string); ok {
			out = append(out, id)
		}
	}
	return out
}

// ListModels fetches the model listing. The gateway serves /v1/models
// without authentication — a missing or bogus key gets the same 200 — so
// this proves nothing about the key. Use CheckKey for that.
func (g *Gateway) ListModels(ctx context.Context) (*GatewayModels, *clierr.Error) {
	return gatewayJSON[GatewayModels](ctx, g, http.MethodGet, "/models", nil)
}

// CheckKey verifies the key the only way the gateway allows: with a
// completion, which is the one route that enforces authentication (401 for
// a bad key). It lists models first (public) to learn a
// model id, then sends a one-token request to it. The cost is a token or
// two; the alternative — trusting /v1/models — accepted "not-a-real-key".
// The model list comes back with the completion's error too, once listed.
func (g *Gateway) CheckKey(ctx context.Context) (*GatewayModels, *clierr.Error) {
	models, cerr := g.ListModels(ctx)
	if cerr != nil {
		return nil, cerr
	}
	model, ok := DefaultModel(models.IDs())
	if !ok {
		return nil, clierr.New(
			clierr.CodeGeneric,
			clierr.KindAPIError,
			"the AI Gateway listed no models; cannot check the key",
		)
	}
	if _, cerr := g.Complete(ctx, model, "ping", 1, false); cerr != nil {
		return models, cerr
	}
	return models, nil
}

// DefaultModel is the model the CLI uses when none is named: the first id in
// sorted order. The gateway's own listing order changes between calls, and
// a default that changes is not a default. ok is false for an empty listing.
func DefaultModel(ids []string) (model string, ok bool) {
	if len(ids) == 0 {
		return "", false
	}
	sorted := append([]string(nil), ids...)
	slices.Sort(sorted)
	return sorted[0], true
}

// GatewayCompletion is the part of a chat completion the CLI reports.
type GatewayCompletion struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
			// Reasoning models return their thinking here and may leave
			// Content empty when max_tokens runs out first.
			Reasoning        string `json:"reasoning"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage map[string]any `json:"usage"`
}

// FinishReason returns the first choice's finish reason ("" when absent).
func (c *GatewayCompletion) FinishReason() string {
	if len(c.Choices) == 0 {
		return ""
	}
	return c.Choices[0].FinishReason
}

// ReasoningLen returns the length of the first choice's reasoning text.
func (c *GatewayCompletion) ReasoningLen() int {
	if len(c.Choices) == 0 {
		return 0
	}
	return len(c.Choices[0].Message.Reasoning) + len(c.Choices[0].Message.ReasoningContent)
}

// Reply returns the first choice's text, trimmed.
func (c *GatewayCompletion) Reply() string {
	if len(c.Choices) == 0 {
		return ""
	}
	return strings.TrimSpace(c.Choices[0].Message.Content)
}

// Complete sends one user message and returns the completion. No history,
// no streaming, no system prompt: this exists to prove a key works against
// a model, not to chat.
//
// thinking=false disables the models' reasoning phase via
// chat_template_kwargs.enable_thinking, the one switch the gateway honors
// (reasoning_effort, a top-level enable_thinking, a reasoning object and a
// /no_think suffix are ignored). With thinking on, the hosted models spend
// hundreds of tokens before the first word of the answer.
func (g *Gateway) Complete(
	ctx context.Context,
	model, prompt string,
	maxTokens int,
	thinking bool,
) (*GatewayCompletion, *clierr.Error) {
	body := map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
		"max_tokens": maxTokens,
	}
	if !thinking {
		body["chat_template_kwargs"] = map[string]any{"enable_thinking": false}
	}
	return gatewayJSON[GatewayCompletion](ctx, g, http.MethodPost, "/chat/completions", body)
}

// gatewayJSON performs one gateway request with bearer auth and decodes the
// JSON response. Errors map onto the CLI's codes: 401/403 → auth (the
// gateway key, not the API key, was rejected), 404 → not found (an unknown
// model), 429 → rate limited, 402 → not available (billing), else generic;
// the gateway's own error message is kept when it has one.
func gatewayJSON[T any](ctx context.Context, g *Gateway, method, path string, body any) (*T, *clierr.Error) {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot encode the request: %v", err)
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.baseURL+path, reader)
	if err != nil {
		return nil, clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot build the request: %v", err)
	}
	if g.key != "" {
		req.Header.Set("Authorization", "Bearer "+g.key)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := restHTTPClient.Do(req)
	if err != nil {
		return nil, transportErr(ctx, "network error", err)
	}
	defer closeBody(resp.Body)
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, readErr(ctx, "response", err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		out := new(T)
		if err := json.Unmarshal(respBody, out); err != nil {
			return nil, clierr.New(
				clierr.CodeGeneric,
				clierr.KindAPIError,
				"cannot decode the AI Gateway's response: %v",
				err,
			)
		}
		return out, nil
	}
	msg := gatewayErrorMessage(respBody)
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		if msg == "" {
			msg = "the AI Gateway rejected this key"
		}
		return nil, clierr.HTTP(clierr.CodeAuth, clierr.KindAuth, resp.StatusCode, msg).
			WithHint("use the AI Gateway key (SALAD_AI_GATEWAY_API_KEY or the saved one), not the SaladCloud API key")
	case http.StatusNotFound:
		if msg == "" {
			msg = "not found"
		}
		return nil, clierr.HTTP(clierr.CodeNotFound, clierr.KindNotFound, resp.StatusCode, msg).
			WithHint("to list the model ids, run saladcloud ai-gateway models list")
	case http.StatusTooManyRequests:
		if msg == "" {
			msg = "rate limited by the AI Gateway"
		}
		return nil, clierr.HTTP(clierr.CodeRateLimited, clierr.KindRateLimited, resp.StatusCode, msg)
	case http.StatusPaymentRequired:
		if msg == "" {
			msg = "the AI Gateway declined the request for billing reasons"
		}
		return nil, clierr.HTTP(clierr.CodeNotAvailable, clierr.KindNotAvailable, resp.StatusCode, msg).
			WithHint("check the pricing and the subscription tier at %s", GatewayPricingURL)
	}
	if msg == "" {
		msg = fmt.Sprintf("AI Gateway returned HTTP %d", resp.StatusCode)
	}
	code, kind := codeForStatus(resp.StatusCode)
	return nil, clierr.HTTP(code, kind, resp.StatusCode, msg)
}

// gatewayErrorMessage extracts the OpenAI-style error message
// ({"error":{"message":…}}) or a bare {"message":…}, else "".
func gatewayErrorMessage(body []byte) string {
	var doc struct {
		Error   any    `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return ""
	}
	switch e := doc.Error.(type) {
	case map[string]any:
		if m, ok := e["message"].(string); ok {
			return strings.TrimSpace(m)
		}
	case string:
		return strings.TrimSpace(e)
	}
	return strings.TrimSpace(doc.Message)
}
