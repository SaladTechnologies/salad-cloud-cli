package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

func TestPickModel(t *testing.T) {
	ids := []string{"b", "a"}
	if m, cerr := pickModel("", ids); cerr != nil || m != "a" {
		t.Errorf("default = %q %v, want the first model in sorted order (listing order is unstable)", m, cerr)
	}
	if m, cerr := pickModel("b", ids); cerr != nil || m != "b" {
		t.Errorf("explicit listed model = %q %v", m, cerr)
	}
	if _, cerr := pickModel("zzz", ids); cerr == nil || cerr.Code != clierr.CodeInvalidInput || !strings.Contains(cerr.HintText(), "a, b") {
		t.Errorf("unknown model = %v, want invalid_input listing the available ids", cerr)
	}
	// Listing unavailable: trust the flag, refuse to guess otherwise.
	if m, cerr := pickModel("x", nil); cerr != nil || m != "x" {
		t.Errorf("flag without a listing = %q %v", m, cerr)
	}
	if _, cerr := pickModel("", nil); cerr == nil {
		t.Error("no flag and no listing must be an error, not a guess")
	}
}

func TestGatewayTestResultShape(t *testing.T) {
	b, err := json.Marshal(gatewayTestResult{Model: "m", Prompt: "p", Reply: "r", Usage: map[string]any{"completion_tokens": 3.0}, OK: true})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	json.Unmarshal(b, &got)
	for _, k := range []string{"model", "prompt", "reply", "usage", "ok"} {
		if _, ok := got[k]; !ok {
			t.Errorf("key %q missing from %s", k, b)
		}
	}
}

func TestUsageSummary(t *testing.T) {
	if got := usageSummary(nil); got != "no usage reported" {
		t.Errorf("nil usage = %q", got)
	}
	got := usageSummary(map[string]any{"prompt_tokens": 12.0, "completion_tokens": 3.0, "details": map[string]any{}})
	if got != "completion tokens 3, prompt tokens 12" {
		t.Errorf("usage = %q", got)
	}
}

// TestAIGatewayHelpPointsAtWhoami: there is no gateway status verb; the help
// says where the key in effect is shown.
func TestAIGatewayHelpPointsAtWhoami(t *testing.T) {
	c, _, err := rootCmd.Find([]string{"ai-gateway"})
	if err != nil || !strings.Contains(c.Long, "To see which key is in effect and where it came from, run saladcloud whoami.") {
		t.Errorf("ai-gateway --help lacks the whoami line: %q", c.Long)
	}
}
