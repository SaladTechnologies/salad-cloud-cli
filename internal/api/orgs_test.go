package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

func orgsClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("SALAD_CLI_API_BASE_URL", srv.URL)
	c, err := New(config.Resolved{APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestOrganizationsListingPages: the listing asks for 100 a page and
// follows next_page_token until it is empty.
func TestOrganizationsListingPages(t *testing.T) {
	var asked []string
	c := orgsClient(t, func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.RawQuery)
		switch r.URL.Query().Get("page_token") {
		case "":
			fmt.Fprint(
				w,
				`{"items":[{"organization_name":"a"},{"organization_name":"b"}],"total_count":3,"next_page_token":"t2"}`,
			)
		case "t2":
			fmt.Fprint(w, `{"items":[{"organization_name":"c"}],"next_page_token":""}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	list, err := c.OrganizationListing(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(list.OrgNames()); got != "[a b c]" || list.TotalCount == nil || *list.TotalCount != 3 {
		t.Errorf("names %s, total %v", got, list.TotalCount)
	}
	if fmt.Sprint(asked) != "[page_size=100 page_size=100&page_token=t2]" {
		t.Errorf("queries %v", asked)
	}
}

// TestOrganizationsListingStopsOnARepeatedToken: a token seen before ends
// the listing rather than looping.
func TestOrganizationsListingStopsOnARepeatedToken(t *testing.T) {
	calls := 0
	c := orgsClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"items":[{"organization_name":"a"}],"next_page_token":"same"}`)
	})
	if _, err := c.ListOrganizations(t.Context()); err != nil || calls != 2 {
		t.Errorf("err %v after %d calls, want 2", err, calls)
	}
}

// TestOrganizationListingByStatus: only a 401 means the key is invalid; a
// 403, a 404 or rows without names mean no list; rate limiting, a server
// error or the network is that error.
func TestOrganizationListingByStatus(t *testing.T) {
	old := restBackoff
	restBackoff = []time.Duration{time.Millisecond}
	t.Cleanup(func() { restBackoff = old })
	for _, tc := range []struct {
		name   string
		status int
		body   string
		list   bool // a list comes back
		code   int  // the error's exit code; 0: none
	}{
		{"200 with names", 200, `{"items":[{"organization_name":"a"}]}`, true, 0},
		{"200, none", 200, `{"items":[]}`, true, 0},
		{"200 without names", 200, `{"items":[{"display_name":"A"}]}`, false, 0},
		{"200 without items", 200, `{}`, false, 0},
		{"200 with items null", 200, `{"items":null}`, false, 0},
		{"401", 401, ``, false, clierr.CodeAuth},
		{"403", 403, ``, false, 0},
		{"404", 404, ``, false, 0},
		{"429", 429, ``, false, clierr.CodeRateLimited},
		{"500", 500, ``, false, clierr.CodeGeneric},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := orgsClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			list, err := c.OrganizationListing(t.Context())
			if (list != nil) != tc.list {
				t.Errorf("list = %v, want a list: %t", list, tc.list)
			}
			switch {
			case tc.code == 0 && err != nil:
				t.Errorf("err = %v, want none", err)
			case tc.code != 0 && (err == nil || err.Code != tc.code):
				t.Errorf("err = %v, want exit %d", err, tc.code)
			}
			if tc.status == 401 && (err == nil || err.HTTPStatus == nil || *err.HTTPStatus != 401) {
				t.Errorf("a 401 must keep its status: %v", err)
			}
		})
	}
	t.Run("network", func(t *testing.T) {
		t.Setenv("SALAD_CLI_API_BASE_URL", "http://127.0.0.1:1")
		c, _ := New(config.Resolved{APIKey: "k"})
		if list, err := c.OrganizationListing(
			t.Context(),
		); list != nil || err == nil ||
			err.Code != clierr.CodeNetwork {
			t.Errorf("list %v, err %v; want a network error", list, err)
		}
	})
}
