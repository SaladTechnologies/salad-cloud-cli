package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

func TestMaskAPIKey(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "(not set)"},
		{"shortkey", "****"},
		{"salad_cloud_user_1234567890", "****...7890"},
	}
	for _, tt := range tests {
		if got := maskAPIKey(tt.in); got != tt.want {
			t.Errorf("maskAPIKey(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestStrOrNil(t *testing.T) {
	if strOrNil("") != nil {
		t.Error("strOrNil(\"\") should be nil")
	}
	if got := strOrNil("x"); got == nil || *got != "x" {
		t.Errorf("strOrNil(\"x\") = %v, want \"x\"", got)
	}
	if maskedOrNil("") != nil {
		t.Error("maskedOrNil(\"\") should be nil")
	}
	if got := maskedOrNil("salad_cloud_user_1234567890"); got == nil || *got != "****...7890" {
		t.Errorf("maskedOrNil = %v, want \"****...7890\"", got)
	}
}

// TestConfigListResultStableJSONShape: config list is a list like every
// other, {"items": [...]} with the path beside it; every config key has an
// entry, and an unset value is null, never a missing entry.
func TestConfigListResultStableJSONShape(t *testing.T) {
	app := newTestRun(t)
	data, err := json.Marshal(app.configListOf(config.File{}, "/tmp/config.json"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc struct {
		Items []map[string]json.RawMessage `json:"items"`
		Path  string                       `json:"config_path"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	keys := make([]string, 0, len(doc.Items))
	for _, item := range doc.Items {
		var key string
		noErr(t, json.Unmarshal(item["key"], &key))
		keys = append(keys, key)
		if raw, ok := item["value"]; !ok || string(raw) != "null" {
			t.Errorf("%s: value = %s (present %v), want null when unset", key, raw, ok)
		}
		if len(item) != 2 {
			t.Errorf("%s: %d keys, want key and value", key, len(item))
		}
	}
	if got := strings.Join(keys, ", "); got != configKeys {
		t.Errorf("entries = %s, want %s", got, configKeys)
	}
	if doc.Path != "/tmp/config.json" {
		t.Errorf("path = %q, want /tmp/config.json", doc.Path)
	}
	var top map[string]json.RawMessage
	noErr(t, json.Unmarshal(data, &top))
	if len(top) != 2 {
		t.Errorf("got %d top-level keys, want items and path", len(top))
	}
}

// TestConfigListEmitsItems runs the command: values set in the file appear
// in their entries, the API key masked.
func TestConfigListEmitsItems(t *testing.T) {
	app := newTestRun(t)
	app.setenv("SALAD_CLI_CONFIG_DIR", t.TempDir())
	if err := config.Save(
		app.Env,
		config.File{APIKey: "salad_cloud_user_1234567890", Organization: "my-org"},
	); err != nil {
		t.Fatal(err)
	}
	out, err := app.runRoot(t, "config", "list", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Items []struct {
			Key   string  `json:"key"`
			Value *string `json:"value"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || len(doc.Items) != 4 {
		t.Fatalf("config list -o json = %s (%v), want four entries in items", out, err)
	}
	got := make(map[string]string)
	for _, it := range doc.Items {
		if it.Value != nil {
			got[it.Key] = *it.Value
		}
	}
	if got["api-key"] != "****...7890" || got["organization"] != "my-org" || len(got) != 2 {
		t.Errorf("values = %v, want the masked api-key and my-org only", got)
	}
}

// TestConfigSetKeyFromStdinAtATerminal: config set api-key - reads the key
// hidden at a terminal, as the logins read --api-key -; Ctrl-C cancels.
func TestConfigSetKeyFromStdinAtATerminal(t *testing.T) {
	o := session{args: []string{"config", "set", "api-key", "-"}, tty: []string{testAPIKey}}.run(t)
	if o.code != 0 || o.config.APIKey != testAPIKey || strings.Contains(o.device, testAPIKey) {
		t.Errorf("exit %d, saved %q, terminal %q", o.code, o.config.APIKey, o.device)
	}
	o = session{args: []string{"config", "set", "api-key", "-"}, tty: []string{keyCtrlC}}.run(t)
	if o.code != 1 || !strings.Contains(o.stderr, "config set canceled, nothing changed") || o.config.APIKey != "" {
		t.Errorf("Ctrl-C: exit %d, saved %q\n%s", o.code, o.config.APIKey, o.stderr)
	}
}
