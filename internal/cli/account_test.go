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
func (app *testRun) stubAPI(t *testing.T, routes map[string]string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for suffix, body := range routes {
			if strings.HasSuffix(r.URL.Path, suffix) {
				respond(t, w, body)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	app.setenv("SALAD_CLI_API_BASE_URL", srv.URL)
	app.setenv("SALAD_CLI_CONFIG_DIR", t.TempDir())
	app.setenv("SALAD_API_KEY", "salad_cloud_user_test0000")
	app.setenv("SALAD_ORGANIZATION", "my-org")
	app.setenv("SALAD_PROJECT", "default")
}

const orgsBody = `{"items":[{"organization_name":"my-org","display_name":"My ` +
	`Org","organization_id":"o-1","create_time":"2026-01-01T00:00:00Z"}]}`

func TestOrgsListAndGet(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	app.stubAPI(t, map[string]string{"/v1/organizations": orgsBody})

	out, err := app.runRoot(t, "orgs", "list", "-o", "json")
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
}

// TestOrgsTableAndNotFound: the tables show the listing, and an organization it does not list is not found.
func TestOrgsTableAndNotFound(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    []string
		want    []string // in the output, or in the error when one is wanted
		wantErr bool
	}{
		{
			name:    "list table",
			args:    []string{"orgs", "list", "-o", "table"},
			want:    []string{"NAME", "My Org"},
			wantErr: false,
		},
		{
			name:    "get table",
			args:    []string{"orgs", "get", "my-org", "-o", "table"},
			want:    []string{"o-1"},
			wantErr: false,
		},
		{
			name:    "get an unlisted org",
			args:    []string{"orgs", "get", "other-org"},
			want:    []string{"not found"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app := newTestRun(t)
			app.stubAPI(t, map[string]string{"/v1/organizations": orgsBody})
			got, err := app.runRoot(t, tc.args...)
			if err != nil {
				got = err.Error()
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("%v = %q, %v; want an error: %t", tc.args, got, err, tc.wantErr)
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("%v = %q, want it to contain %q", tc.args, got, w)
				}
			}
		})
	}
}

func TestQuotasGet(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	app.stubAPI(
		t,
		map[string]string{
			"/quotas": `{"container_groups_quotas":{"container_replicas_quota":10,"container_replicas_used":3}}`,
		},
	)

	out, err := app.runRoot(t, "quotas", "get", "-o", "json")
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
	out, err = app.runRoot(
		t,
		"quotas",
		"get",
		"-o",
		"table",
	)
	if err != nil || !strings.Contains(out, "replicas") ||
		!strings.Contains(out, "3 / 10") {
		t.Errorf("quotas get table = %q (%v)", out, err)
	}
}

// A 200 whose rows have no organization_name means the endpoint changed
// shape: whoami reports the listing as unavailable, not as zero orgs.
func TestWhoamiNamelessOrgRowsAreUnknown(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	app.stubAPI(t, map[string]string{"/v1/organizations": `{"items":[{"id":"x"}]}`, "/quotas": `{}`})
	out, err := app.runRoot(t, "whoami", "-o", "json", "--org", "", "--project", "")
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
	t.Parallel()
	app := newTestRun(t)
	app.setenv("SALAD_CLI_CONFIG_DIR", t.TempDir())
	app.setenv("SALAD_API_KEY", "")
	noErr(t, config.Save(app.Env, config.File{APIKey: "k", Organization: "my-org", Project: "p", GatewayAPIKey: "g"}))
	out, err := app.runRoot(t, "logout", "-o", "json")
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	for _, want := range []string{`"removed": true,`, `"organization": "my-org",`, `"action": "logged_out",`} {
		if !strings.Contains(out, want) {
			t.Errorf("logout -o json lacks %s:\n%s", want, out)
		}
	}
	want := config.File{APIKey: "", Organization: "my-org", Project: "p", GatewayAPIKey: "g"}
	if f := must(config.Load(app.Env))(t); f != want {
		t.Errorf("after logout the config is %+v, want %+v; only the API key goes", f, want)
	}
	if _, err := app.runRoot(t, "ai-gateway", "logout"); err != nil {
		t.Fatalf("ai-gateway logout: %v", err)
	}
	want.GatewayAPIKey = ""
	if f := must(config.Load(app.Env))(t); f != want {
		t.Errorf("after ai-gateway logout the config is %+v, want %+v", f, want)
	}
}
