package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

// stubAPI serves canned responses by path suffix and points the CLI at it.
func stubAPI(t *testing.T, routes map[string]string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for suffix, body := range routes {
			if strings.HasSuffix(r.URL.Path, suffix) {
				_, _ = w.Write([]byte(body))
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("SALAD_CLI_API_BASE_URL", srv.URL)
	t.Setenv("SALAD_CLI_CONFIG_DIR", t.TempDir())
	t.Setenv("SALAD_API_KEY", "salad_cloud_user_test0000")
	t.Setenv("SALAD_ORGANIZATION", "my-org")
	t.Setenv("SALAD_PROJECT", "default")
}

const orgsBody = `{"items":[{"organization_name":"my-org","display_name":"My ` +
	`Org","organization_id":"o-1","create_time":"2026-01-01T00:00:00Z"}]}`

func TestOrgsListAndGet(t *testing.T) {
	stubAPI(t, map[string]string{"/v1/organizations": orgsBody})

	out, err := runRoot(t, "orgs", "list", "-o", "json")
	if err != nil {
		t.Fatalf("orgs list: %v", err)
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err = json.Unmarshal([]byte(out), &list); err != nil || len(list.Items) != 1 {
		t.Fatalf("orgs list -o json = %s (%v)", out, err)
	}
	if _, present := list.Items[0]["update_time"]; !present {
		t.Errorf("update_time missing; every key is present: %v", list.Items[0])
	}
	if out, err = runRoot(
		t,
		"orgs",
		"list",
		"-o",
		"table",
	); err != nil || !strings.Contains(out, "NAME") ||
		!strings.Contains(out, "My Org") {
		t.Errorf("orgs list table = %q (%v)", out, err)
	}
	if out, err = runRoot(t, "orgs", "get", "my-org", "-o", "table"); err != nil || !strings.Contains(out, "o-1") {
		t.Errorf("orgs get table = %q (%v)", out, err)
	}
	if _, err = runRoot(t, "orgs", "get", "other-org"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("orgs get of an unlisted org = %v, want not found", err)
	}
}

func TestQuotasGet(t *testing.T) {
	stubAPI(
		t,
		map[string]string{
			"/quotas": `{"container_groups_quotas":{"container_replicas_quota":10,"container_replicas_used":3}}`,
		},
	)

	out, err := runRoot(t, "quotas", "get", "-o", "json")
	if err != nil {
		t.Fatalf("quotas get: %v", err)
	}
	var doc map[string]map[string]any
	if err = json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("quotas get -o json = %s", out)
	}
	q := doc["container_groups_quotas"]
	if q["container_replicas_quota"] != float64(10) {
		t.Errorf("quota = %v", q)
	}
	if _, present := q["max_container_group_restarts_per_minute"]; !present {
		t.Errorf("an absent quota renders null, not missing: %v", q)
	}
	if out, err = runRoot(
		t,
		"quotas",
		"get",
		"-o",
		"table",
	); err != nil || !strings.Contains(out, "replicas") ||
		!strings.Contains(out, "3 / 10") {
		t.Errorf("quotas get table = %q (%v)", out, err)
	}
}

// A 200 whose rows have no organization_name means the endpoint changed
// shape: whoami reports the listing as unavailable, not as zero orgs.
func TestWhoamiNamelessOrgRowsAreUnknown(t *testing.T) {
	stubAPI(t, map[string]string{"/v1/organizations": `{"items":[{"id":"x"}]}`, "/quotas": `{}`})
	out, err := runRoot(t, "whoami", "-o", "json", "--org", "", "--project", "")
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatal(err)
	}
	if string(m["organizations"]) != "null" {
		t.Errorf("organizations = %s, want null", m["organizations"])
	}
}

// logout removes the key and leaves the organization and project.
func TestLogoutKeepsContext(t *testing.T) {
	t.Setenv("SALAD_CLI_CONFIG_DIR", t.TempDir())
	t.Setenv("SALAD_API_KEY", "")
	if err := config.Save(
		config.File{APIKey: "k", Organization: "my-org", Project: "p", GatewayAPIKey: "g"},
	); err != nil {
		t.Fatal(err)
	}
	out, err := runRoot(t, "logout", "-o", "json")
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	if !strings.Contains(out, `"removed": true,`) || !strings.Contains(out, `"organization": "my-org",`) ||
		!strings.Contains(out, `"action": "logged_out",`) {
		t.Errorf("logout -o json = %s", out)
	}
	f, _ := config.Load()
	if f.APIKey != "" || f.Organization != "my-org" || f.Project != "p" || f.GatewayAPIKey != "g" {
		t.Errorf("after logout the config is %+v; only the API key goes", f)
	}
	if _, err := runRoot(t, "ai-gateway", "logout"); err != nil {
		t.Fatalf("ai-gateway logout: %v", err)
	}
	if f, _ = config.Load(); f.GatewayAPIKey != "" || f.Organization != "my-org" {
		t.Errorf("after ai-gateway logout the config is %+v", f)
	}
}
