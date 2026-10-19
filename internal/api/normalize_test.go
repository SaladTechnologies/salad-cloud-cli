package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"

	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

func TestNormalizeNumericEnums(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		raw     string
		want    string // the document returned, re-encoded with its keys sorted when changed
		changed bool
	}{
		{
			// Mapped values become their names at any depth; string values stay.
			name: "mapped",
			raw: `{"items":[{"name":"legacy","restart_policy":1},{"name":"ok","restart_policy":"never"},` +
				`{"nested":{"restart_policy":3}}]}`,
			want: `{"items":[{"name":"legacy","restart_policy":"always"},{"name":"ok","restart_policy":"never"},` +
				`{"nested":{"restart_policy":"never"}}]}`,
			changed: true,
		},
		{
			// Undefined numeric values (the 0-default leak) are dropped so the
			// document still decodes; the field simply comes back unset.
			name:    "undefined dropped",
			raw:     `{"restart_policy":0,"priority":4}`,
			want:    `{"priority":"batch"}`,
			changed: true,
		},
		{name: "enum-free", raw: `{"a":1}`, want: `{"a":1}`},
		{name: "not JSON", raw: `{`, want: `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixed, changed := normalizeNumericEnums([]byte(tc.raw))
			if string(fixed) != tc.want || changed != tc.changed {
				t.Errorf("normalizeNumericEnums(%s) = %s, %t; want %s, %t", tc.raw, fixed, changed, tc.want, tc.changed)
			}
		})
	}
}

// TestRestJSONNumericEnumRetry: a group list whose
// legacy entry carries restart_policy as an integer must still decode.
func TestRestJSONNumericEnumRetry(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		respond(t, w, `{"items":[{"name":"legacy","restart_policy":2}]}`)
	}))
	t.Cleanup(srv.Close)
	client, cerr := New(&config.Resolved{APIKey: "k", Organization: "o", Project: "p"}, testOptions(baseURLs(srv.URL)))
	if cerr != nil {
		t.Fatalf("New: %v", cerr)
	}
	got, cerr := restJSON[containergroups.ContainerGroupCollection](t.Context(), client, "GET", "/x", "", nil)
	if cerr != nil {
		t.Fatalf("restJSON: %v", cerr)
	}
	if len(got.Items) != 1 || got.Items[0].RestartPolicy == nil || string(*got.Items[0].RestartPolicy) != "on_failure" {
		t.Fatalf("decoded = %+v, want on_failure", got.Items)
	}
}
