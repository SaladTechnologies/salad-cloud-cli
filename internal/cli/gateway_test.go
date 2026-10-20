package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

func TestPickModel(t *testing.T) {
	t.Parallel()
	ids := []string{"b", "a"}
	tests := []struct {
		name string
		flag string
		ids  []string
		want string // the model; "" when an error is wanted
		code int    // the exit code wanted with the error
		hint string // a fragment of the error's hint
	}{
		// The first model in sorted order: the gateway's listing order is unstable.
		{name: "default", flag: "", ids: ids, want: "a", code: 0, hint: ""},
		{name: "explicit listed model", flag: "b", ids: ids, want: "b", code: 0, hint: ""},
		{name: "unknown model", flag: "zzz", ids: ids, want: "", code: clierr.CodeInvalidInput, hint: "a, b"},
		// Listing unavailable: trust the flag, refuse to guess otherwise.
		{name: "flag without a listing", flag: "x", ids: nil, want: "x", code: 0, hint: ""},
		{name: "no flag and no listing", flag: "", ids: nil, want: "", code: clierr.CodeGeneric, hint: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, cerr := pickModel(tc.flag, tc.ids)
			if tc.want != "" {
				if cerr != nil || m != tc.want {
					t.Errorf("pickModel(%q) = %q %v, want %q", tc.flag, m, cerr, tc.want)
				}
				return
			}
			if cerr == nil || cerr.Code != tc.code || !strings.Contains(cerr.HintText(), tc.hint) {
				t.Errorf(
					"pickModel(%q) = %q %v, want exit %d with a hint containing %q",
					tc.flag,
					m,
					cerr,
					tc.code,
					tc.hint,
				)
			}
		})
	}
}

func TestGatewayTestResultShape(t *testing.T) {
	t.Parallel()
	b, err := json.Marshal(
		gatewayTestResult{
			Model:  "m",
			Prompt: "p",
			Reply:  "r",
			Usage:  map[string]any{"completion_tokens": 3.0},
			OK:     true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	noErr(t, json.Unmarshal(b, &got))
	for _, k := range []string{"model", "prompt", "reply", "usage", "ok"} {
		if _, ok := got[k]; !ok {
			t.Errorf("key %q missing from %s", k, b)
		}
	}
}

func TestUsageSummary(t *testing.T) {
	t.Parallel()
	if got := usageSummary(nil); got != "no usage reported" {
		t.Errorf("nil usage = %q", got)
	}
	got := usageSummary(
		map[string]any{"prompt_tokens": 12.0, "completion_tokens": 3.0, "details": make(map[string]any)},
	)
	if got != "completion tokens 3, prompt tokens 12" {
		t.Errorf("usage = %q", got)
	}
}

// TestAIGatewayHelpPointsAtWhoami: there is no gateway status verb; the help
// says where the key in effect is shown.
func TestAIGatewayHelpPointsAtWhoami(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	c, _, err := app.root.Find([]string{"ai-gateway"})
	if err != nil ||
		!strings.Contains(c.Long, "To see which key is in effect and where it came from, run saladcloud whoami.") {
		t.Errorf("ai-gateway --help lacks the whoami line: %q", c.Long)
	}
}
