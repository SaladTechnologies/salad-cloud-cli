package api

import (
	"context"
	"encoding/json"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

func newTestGateway(t *testing.T, url string) *Gateway {
	t.Helper()
	t.Setenv("SALAD_CLI_AI_GATEWAY_BASE_URL", url)
	g, cerr := NewGateway(config.Resolved{GatewayAPIKey: "gw-key"})
	if cerr != nil {
		t.Fatalf("NewGateway: %v", cerr)
	}
	return g
}

func TestNewGatewayNeedsItsOwnKey(t *testing.T) {
	_, cerr := NewGateway(config.Resolved{APIKey: "salad-api-key"})
	if cerr == nil || cerr.Code != clierr.CodeAuth {
		t.Fatalf("NewGateway without a gateway key = %v, want an auth error", cerr)
	}
	if cerr.Message != "no AI Gateway key set" || cerr.HintText() != "run saladcloud ai-gateway login in a terminal, or set SALAD_AI_GATEWAY_API_KEY" {
		t.Errorf("message %q, hint %q", cerr.Message, cerr.HintText())
	}
}

func TestGatewayListModelsAndComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer gw-key" {
			t.Errorf("Authorization = %q, want bearer gateway key", got)
		}
		switch r.URL.Path {
		case "/models":
			w.Write([]byte(`{"object":"list","data":[{"id":"qwen3.6-35b-a3b","object":"model","owned_by":"salad"},{"id":"gemma-4-26b","object":"model"}]}`))
		case "/chat/completions":
			var req map[string]any
			json.NewDecoder(r.Body).Decode(&req)
			if req["model"] != "qwen3.6-35b-a3b" {
				t.Errorf("model = %v", req["model"])
			}
			msgs := req["messages"].([]any)
			if len(msgs) != 1 || msgs[0].(map[string]any)["content"] != "say hi" {
				t.Errorf("messages = %v, want one user message", msgs)
			}
			// thinking=false must send the one switch the gateway honors.
			kw, _ := req["chat_template_kwargs"].(map[string]any)
			if kw == nil || kw["enable_thinking"] != false {
				t.Errorf("chat_template_kwargs = %v, want enable_thinking false", req["chat_template_kwargs"])
			}
			w.Write([]byte(`{"model":"qwen3.6-35b-a3b","choices":[{"message":{"role":"assistant","content":"  Hi there.\n"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":3}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	g := newTestGateway(t, srv.URL)

	models, cerr := g.ListModels(context.Background())
	if cerr != nil {
		t.Fatalf("ListModels: %v", cerr)
	}
	if ids := models.IDs(); len(ids) != 2 || ids[0] != "qwen3.6-35b-a3b" {
		t.Errorf("IDs = %v", ids)
	}
	c, cerr := g.Complete(context.Background(), "qwen3.6-35b-a3b", "say hi", 16, false)
	if cerr != nil {
		t.Fatalf("Complete: %v", cerr)
	}
	if c.Reply() != "Hi there." || c.Usage["completion_tokens"] != float64(3) {
		t.Errorf("reply %q usage %v", c.Reply(), c.Usage)
	}
}

// TestGatewayCheckKeyUsesACompletion: /v1/models is public, so a key check
// that stops there accepts any string. CheckKey must go on to a completion
// and surface its 401.
func TestGatewayCheckKeyUsesACompletion(t *testing.T) {
	var sawCompletion bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Write([]byte(`{"object":"list","data":[{"id":"small"},{"id":"big"}]}`))
		case "/chat/completions":
			sawCompletion = true
			var req map[string]any
			json.NewDecoder(r.Body).Decode(&req)
			if req["model"] != "big" || req["max_tokens"] != float64(1) {
				t.Errorf("probe = model %v max_tokens %v, want the first model in sorted order (DefaultModel) and 1 token", req["model"], req["max_tokens"])
			}
			w.WriteHeader(401)
			w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
		}
	}))
	defer srv.Close()
	g := newTestGateway(t, srv.URL)
	_, cerr := g.CheckKey(context.Background())
	if !sawCompletion {
		t.Fatal("CheckKey never sent a completion; /v1/models alone does not authenticate")
	}
	if cerr == nil || cerr.Code != clierr.CodeAuth {
		t.Errorf("CheckKey = %v, want the completion's auth error", cerr)
	}
}

func TestGatewayCompletionReasoningFields(t *testing.T) {
	var c GatewayCompletion
	if err := json.Unmarshal([]byte(`{"model":"m","choices":[{"message":{"content":"","reasoning":"thinking hard"},"finish_reason":"length"}],"usage":{"completion_tokens":64}}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.Reply() != "" || c.FinishReason() != "length" || c.ReasoningLen() != len("thinking hard") {
		t.Errorf("reply %q finish %q reasoning %d", c.Reply(), c.FinishReason(), c.ReasoningLen())
	}
}

func TestGatewayErrorMapping(t *testing.T) {
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
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tt.status)
			w.Write([]byte(tt.body))
		}))
		g := newTestGateway(t, srv.URL)
		_, cerr := g.ListModels(context.Background())
		srv.Close()
		if cerr == nil {
			t.Errorf("status %d: no error", tt.status)
			continue
		}
		if cerr.Code != tt.wantCode || ptr.Deref(cerr.HTTPStatus) != tt.status {
			t.Errorf("status %d: code %d http %d, want code %d", tt.status, cerr.Code, ptr.Deref(cerr.HTTPStatus), tt.wantCode)
		}
		if text := cerr.Message + " " + cerr.HintText(); !strings.Contains(text, tt.wantText) {
			t.Errorf("status %d: %q lacks %q", tt.status, text, tt.wantText)
		}
	}
}
