package api

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

// TestCheckBaseURL: a base-URL variable is accepted when it is https, or http on this machine, and is a usage
// error otherwise.
func TestCheckBaseURL(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		value string
		ok    bool
	}{
		{"", true},
		{"https://api.example.com/api/public", true},
		{"http://127.0.0.1:8080", true},
		{"http://localhost:8080/v1", true},
		{"http://LOCALHOST:8080", true},
		{"http://[::1]:8080", true},
		{"http://api.example.com", false},
		{"http://192.168.1.20:8080", false},
		{"http://stand-in.localhost", false},
		{"http://localhost.example.com", false},
		{"api.example.com", false},
		{"ftp://api.example.com", false},
	} {
		t.Run(tt.value, func(t *testing.T) {
			t.Parallel()
			cerr := checkBaseURL(envAPIBaseURL, tt.value)
			if (cerr == nil) != tt.ok {
				t.Fatalf("checkBaseURL(%q) = %v, want ok %v", tt.value, cerr, tt.ok)
			}
			if cerr != nil && (cerr.Code != clierr.CodeUsage || !strings.Contains(cerr.Message, envAPIBaseURL)) {
				t.Errorf("error = exit %d %q, want a usage error naming the variable", cerr.Code, cerr.Message)
			}
		})
	}
}

// TestClientsCheckTheirBaseURLs: each client checks the variables it reads, and the error's hint names the
// variable to remove.
func TestClientsCheckTheirBaseURLs(t *testing.T) {
	t.Parallel()
	res := &config.Resolved{APIKey: "test-key", GatewayAPIKey: "gateway-key"}
	for _, v := range baseURLVars() {
		t.Run(v.env, func(t *testing.T) {
			t.Parallel()
			opts := testOptions(map[string]string{v.env: "http://api.example.com"})
			var cerr *clierr.Error
			if v.env == envGatewayBaseURL {
				_, cerr = NewGateway(res, opts)
			} else {
				_, cerr = New(res, opts)
			}
			if cerr == nil || cerr.Code != clierr.CodeUsage || !strings.Contains(cerr.HintText(), "remove "+v.env) {
				t.Errorf("error = %v, want a usage error whose hint names %s", cerr, v.env)
			}
		})
	}
}

// TestRemoteBaseURLs: only a variable that names an https server on another machine is reported, by its host and
// with what goes there.
func TestRemoteBaseURLs(t *testing.T) {
	t.Parallel()
	// A URL can carry a user and password, which the host reported must leave out.
	withUser := url.URL{
		Scheme: "https",
		User:   url.UserPassword("user", "made-up"),
		Host:   "api.example.com:8443",
		Path:   "/api/public",
	}
	got := RemoteBaseURLs(getenvOf(map[string]string{
		envAPIBaseURL:     withUser.String(),
		envStorageBaseURL: "http://127.0.0.1:9000",
		envGatewayBaseURL: "https://gateway.example.com/v1",
	}))
	want := []BaseURLOverride{
		{Env: envAPIBaseURL, Host: "api.example.com:8443", Carries: "requests and the API key"},
		{
			Env:     envGatewayBaseURL,
			Host:    "gateway.example.com",
			Carries: "AI Gateway requests and the AI Gateway key",
		},
	}
	if !slices.Equal(got, want) {
		t.Errorf("RemoteBaseURLs = %+v, want %+v", got, want)
	}
	if none := RemoteBaseURLs(getenvOf(nil)); len(none) != 0 {
		t.Errorf("RemoteBaseURLs with nothing set = %+v, want none", none)
	}
}

// keyLog records the Salad-Api-Key header each request to a test server carried, by path.
type keyLog struct {
	mu   sync.Mutex
	keys map[string]string
}

func (l *keyLog) note(r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.keys[r.URL.Path] = r.Header.Get("Salad-Api-Key")
}

func (l *keyLog) at(path string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	key, ok := l.keys[path]
	return key, ok
}

// TestRedirectToAnotherHostCarriesNoKey: a redirect within the server keeps the key; one to another server arrives
// without it, and so does a redirect from there back to the first.
func TestRedirectToAnotherHostCarriesNoKey(t *testing.T) {
	t.Parallel()
	const quotas, moved, away, back = "/organizations/my-org/quotas", "/moved", "/away", "/back"
	seen := &keyLog{mu: sync.Mutex{}, keys: make(map[string]string)}
	var first, other *httptest.Server
	first = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.note(r)
		switch r.URL.Path {
		case quotas:
			http.Redirect(w, r, first.URL+moved, http.StatusTemporaryRedirect)
		case moved:
			http.Redirect(w, r, other.URL+away, http.StatusTemporaryRedirect)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`)) //nolint:errcheck // a test server's reply
		}
	}))
	t.Cleanup(first.Close)
	other = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.note(r)
		http.Redirect(w, r, first.URL+back, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(other.Close)

	if _, cerr := newRestTestClient(t, first.URL).GetQuotas(t.Context()); cerr != nil {
		t.Fatalf("GetQuotas: %v", cerr)
	}
	for path, want := range map[string]string{quotas: "test-key", moved: "test-key", away: "", back: ""} {
		if got, ok := seen.at(path); !ok || got != want {
			t.Errorf("request to %s carried key %q (made: %v), want %q", path, got, ok, want)
		}
	}
}

// TestRedirectToAnotherHostCarriesNoBody: a request with a body stops at a redirect to another server, which gets no
// request at all; the same redirect within the server is followed with the body.
func TestRedirectToAnotherHostCarriesNoBody(t *testing.T) {
	t.Parallel()
	const groups = "/organizations/my-org/projects/default/containers"
	var mu sync.Mutex
	bodies := make(map[string]string) // "other" or a path on the first server -> the body it received
	note := func(where string, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading the body sent to %s: %v", where, err)
		}
		mu.Lock()
		defer mu.Unlock()
		bodies[where] = string(body)
	}
	other := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { note("other", r) }))
	t.Cleanup(other.Close)
	var first *httptest.Server
	first = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		note(r.URL.Path, r)
		switch r.URL.Path {
		case groups:
			http.Redirect(w, r, first.URL+"/moved", http.StatusTemporaryRedirect)
		case "/moved":
			http.Redirect(w, r, other.URL+"/away", http.StatusPermanentRedirect)
		}
	}))
	t.Cleanup(first.Close)

	const sent = `{"name":"my-app"}`
	_, cerr := newRestTestClient(t, first.URL).CreateGroupRaw(t.Context(), json.RawMessage(sent))
	if cerr == nil || !strings.Contains(cerr.Message, errBodyRedirected.Error()) {
		t.Errorf("error = %v, want %q", cerr, errBodyRedirected)
	}
	mu.Lock()
	got := maps.Clone(bodies)
	mu.Unlock()
	if _, reached := got["other"]; reached {
		t.Errorf("the other server received a request, with body %q", got["other"])
	}
	if got[groups] != sent || got["/moved"] != sent {
		t.Errorf("bodies received by the first server = %v, want %q at both paths", got, sent)
	}
}

// TestRedirectsStopAtTheLimit: a server that redirects forever is given up on after maxRedirects.
func TestRedirectsStopAtTheLimit(t *testing.T) {
	t.Parallel()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/again", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)
	_, cerr := newRestTestClient(t, srv.URL).GetQuotas(t.Context())
	if cerr == nil || !strings.Contains(cerr.Message, errTooManyRedirects.Error()) {
		t.Errorf("error = %v, want %q", cerr, errTooManyRedirects)
	}
}
