package cli

import (
	"encoding/json"
	"testing"
)

// TestWhoamiResultStableJSONShape is the JSON contract test: with nothing
// set, every key is still present and unset values are null. Agents parse
// this shape — keys must never appear or disappear based on state.
func TestWhoamiResultStableJSONShape(t *testing.T) {
	data, err := json.Marshal(whoamiResult{ConfigPath: "/tmp/config.toml"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	nullKeys := []string{
		"api_key", "api_key_source",
		"organization", "organization_source",
		"project", "project_source",
		"ai_gateway_api_key", "ai_gateway_api_key_source",
		"authenticated", "organizations",
		"saved_api_key", "saved_organization", "saved_project", "saved_ai_gateway_api_key", "organization_found",
	}
	for _, k := range nullKeys {
		raw, ok := m[k]
		if !ok {
			t.Errorf("key %q missing — every key must always be present", k)
			continue
		}
		if string(raw) != "null" {
			t.Errorf("key %q = %s, want null when unset", k, raw)
		}
	}
	if got, ok := m["config_path"]; !ok || string(got) != `"/tmp/config.toml"` {
		t.Errorf("config_path = %s, want %q", got, "/tmp/config.toml")
	}
	if len(m) != len(nullKeys)+1 {
		t.Errorf("got %d keys, want %d", len(m), len(nullKeys)+1)
	}
}

// TestWhoamiResultSetValues checks the set side of the contract: values
// render as their JSON types, and an empty org listing renders as [].
func TestWhoamiResultSetValues(t *testing.T) {
	authed := true
	data, err := json.Marshal(whoamiResult{
		APIKey:             maskedOrNil("salad_cloud_user_1234567890"),
		APIKeySource:       strOrNil("env"),
		Organization:       strOrNil("my-org"),
		OrganizationSource: strOrNil("config"),
		Authenticated:      &authed,
		Organizations:      []string{},
		ConfigPath:         "/tmp/config.toml",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := map[string]string{
		"api_key":                   `"****...7890"`,
		"api_key_source":            `"env"`,
		"organization":              `"my-org"`,
		"organization_source":       `"config"`,
		"project":                   "null",
		"project_source":            "null",
		"ai_gateway_api_key":        "null",
		"ai_gateway_api_key_source": "null",
		"authenticated":             "true",
		"organizations":             "[]",
	}
	for k, w := range want {
		if got := string(m[k]); got != w {
			t.Errorf("key %q = %s, want %s", k, got, w)
		}
	}
}
