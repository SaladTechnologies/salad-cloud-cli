package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

func newTestGateway(t *testing.T, url string, opts ...Options) *Gateway {
	t.Helper()
	o := testOptions(nil)
	if len(opts) > 0 {
		o = opts[0]
	}
	o.Getenv = getenvOf(map[string]string{"SALAD_CLI_AI_GATEWAY_BASE_URL": url})
	g, cerr := NewGateway(config.Resolved{GatewayAPIKey: "gw-key"}, o)
	if cerr != nil {
		t.Fatalf("NewGateway: %v", cerr)
	}
	return g
}

func TestNewGatewayNeedsItsOwnKey(t *testing.T) {
	t.Parallel()
	_, cerr := NewGateway(config.Resolved{APIKey: "salad-api-key"}, testOptions(nil)) //nolint:gosec // a test value
	if cerr == nil || cerr.Code != clierr.CodeAuth {
		t.Fatalf("NewGateway without a gateway key = %v, want an auth error", cerr)
	}
	if cerr.Message != "no AI Gateway key set" ||
		cerr.HintText() != "run saladcloud ai-gateway login in a terminal, or set SALAD_AI_GATEWAY_API_KEY" {
		t.Errorf("message %q, hint %q", cerr.Message, cerr.HintText())
	}
}

func TestGatewayListModelsAndComplete(t *testing.T) {
	t.Parallel()
	g := newTestGateway(t, modelsAndCompletionServer(t).URL)

	models, cerr := g.ListModels(t.Context())
	if cerr != nil {
		t.Fatalf("ListModels: %v", cerr)
	}
	if ids := models.IDs(); len(ids) != 2 || ids[0] != "qwen3.6-35b-a3b" {
		t.Errorf("IDs = %v", ids)
	}
	c, cerr := g.Complete(t.Context(), "qwen3.6-35b-a3b", "say hi", 16, false)
	if cerr != nil {
		t.Fatalf("Complete: %v", cerr)
	}
	if c.Reply() != "Hi there." || c.Usage["completion_tokens"] != float64(3) {
		t.Errorf("reply %q usage %v", c.Reply(), c.Usage)
	}
}

// modelsAndCompletionServer is TestGatewayListModelsAndComplete's gateway: two models, and a reply to the one
// completion it expects. Every request must carry the gateway key.
func modelsAndCompletionServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer gw-key" {
			t.Errorf("Authorization = %q, want bearer gateway key", got)
		}
		switch r.URL.Path {
		case "/models":
			respond(
				t,
				w,
				`{"object":"list","data":[{"id":"qwen3.6-35b-a3b","object":"model","owned_by":"salad"},`+
					`{"id":"gemma-4-26b","object":"model"}]}`,
			)
		case "/chat/completions":
			checkCompletionRequest(t, r)
			respond(t, w,
				`{"model":"qwen3.6-35b-a3b","choices":[{"message":{"role":"assistant","content":"  Hi `+
					`there.\n"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":3}}`,
			)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// checkCompletionRequest checks the body of TestGatewayListModelsAndComplete's completion: the model, one user
// message, and thinking switched off.
func checkCompletionRequest(t *testing.T, r *http.Request) {
	t.Helper()
	var req map[string]any
	reportErr(t, json.NewDecoder(r.Body).Decode(&req))
	if req["model"] != "qwen3.6-35b-a3b" {
		t.Errorf("model = %v", req["model"])
	}
	msgs := assertAsOrError[[]any](t, req["messages"])
	if len(msgs) != 1 || assertAsOrError[map[string]any](t, msgs[0])["content"] != "say hi" {
		t.Errorf("messages = %v, want one user message", msgs)
	}
	// thinking=false must send the one switch the gateway honors.
	kw := valueAs[map[string]any](req["chat_template_kwargs"])
	if on, isBool := kw["enable_thinking"].(bool); !isBool || on {
		t.Errorf("chat_template_kwargs = %v, want enable_thinking false", req["chat_template_kwargs"])
	}
}

// TestGatewayCheckKeyUsesACompletion: /v1/models is public, so a key check
// that stops there accepts any string. CheckKey must go on to a completion
// and surface its 401.
func TestGatewayCheckKeyUsesACompletion(t *testing.T) {
	t.Parallel()
	var sawCompletion bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			respond(t, w, `{"object":"list","data":[{"id":"small"},{"id":"big"}]}`)
		case "/chat/completions":
			sawCompletion = true
			var req map[string]any
			reportErr(t, json.NewDecoder(r.Body).Decode(&req))
			if req["model"] != "big" || req["max_tokens"] != float64(1) {
				t.Errorf(
					"probe = model %v max_tokens %v, want the first model in sorted order (DefaultModel) and 1 token",
					req["model"],
					req["max_tokens"],
				)
			}
			w.WriteHeader(http.StatusUnauthorized)
			respond(t, w, `{"error":{"message":"invalid api key"}}`)
		}
	}))
	t.Cleanup(srv.Close)
	g := newTestGateway(t, srv.URL)
	_, cerr := g.CheckKey(t.Context())
	if !sawCompletion {
		t.Fatal("CheckKey never sent a completion; /v1/models alone does not authenticate")
	}
	if cerr == nil || cerr.Code != clierr.CodeAuth {
		t.Errorf("CheckKey = %v, want the completion's auth error", cerr)
	}
}

func TestGatewayCompletionReasoningFields(t *testing.T) {
	t.Parallel()
	var c GatewayCompletion
	err := json.Unmarshal(
		[]byte(
			`{"model":"m","choices":[{"message":{"content":"","reasoning":"thinking `+
				`hard"},"finish_reason":"length"}],"usage":{"completion_tokens":64}}`,
		),
		&c,
	)
	if err != nil {
		t.Fatal(err)
	}
	if c.Reply() != "" || c.FinishReason() != "length" || c.ReasoningLen() != len("thinking hard") {
		t.Errorf("reply %q finish %q reasoning %d", c.Reply(), c.FinishReason(), c.ReasoningLen())
	}
}

func TestGatewayErrorMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status   int
		body     string
		wantCode int
		wantText string
	}{
		{401, `{"error":{"message":"invalid api key"}}`, clierr.CodeAuth, "AI Gateway key"},
		{403, ``, clierr.CodeAuth, "rejected this key"},
		{404, `{"error":{"message":"model nope not found"}}`, clierr.CodeNotFound, "gateway models"},
		{429, `{"message":"slow down"}`, clierr.CodeRateLimited, "slow down"},
		{402, ``, clierr.CodeNotAvailable, "billing"},
		{500, `{"error":"boom"}`, clierr.CodeGeneric, "boom"},
	}
	for _, tt := range tests {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tt.status)
			respond(t, w, tt.body)
		}))
		g := newTestGateway(t, srv.URL)
		_, cerr := g.ListModels(t.Context())
		srv.Close()
		if cerr == nil {
			t.Errorf("status %d: no error", tt.status)
			continue
		}
		if cerr.Code != tt.wantCode || ptr.Deref(cerr.HTTPStatus) != tt.status {
			t.Errorf(
				"status %d: code %d http %d, want code %d",
				tt.status,
				cerr.Code,
				ptr.Deref(cerr.HTTPStatus),
				tt.wantCode,
			)
		}
		if text := cerr.Message + " " + cerr.HintText(); !strings.Contains(text, tt.wantText) {
			t.Errorf("status %d: %q lacks %q", tt.status, text, tt.wantText)
		}
	}
}
