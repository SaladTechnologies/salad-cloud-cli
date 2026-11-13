package api

import (
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

// TestCheckBaseURL: a base-URL variable is accepted when it is https, or http on this machine, and refused as a
// usage error otherwise, since the key would cross the network unencrypted.
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
		{"http://stand-in.localhost", true},
		{"http://[::1]:8080", true},
		{"http://api.example.com", false},
		{"http://192.168.1.20:8080", false},
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

// TestClientsRefuseAnUnencryptedBaseURL: neither client is built for a server that would get its key over http.
func TestClientsRefuseAnUnencryptedBaseURL(t *testing.T) {
	t.Parallel()
	res := &config.Resolved{APIKey: "test-key", GatewayAPIKey: "gateway-key"}
	for _, name := range baseURLVars() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			opts := testOptions(map[string]string{name: "http://api.example.com"})
			var cerr *clierr.Error
			if name == envGatewayBaseURL {
				_, cerr = NewGateway(res, opts)
			} else {
				_, cerr = New(res, opts)
			}
			if cerr == nil || cerr.Code != clierr.CodeUsage || !strings.Contains(cerr.Message, name) {
				t.Errorf("error = %v, want a usage error naming %s", cerr, name)
			}
		})
	}
}

// TestRemoteBaseURLs: only a variable that names a server on another machine is reported, by its host.
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
		{Env: envAPIBaseURL, Host: "api.example.com:8443"},
		{Env: envGatewayBaseURL, Host: "gateway.example.com"},
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

// TestRedirectToAnotherHostCarriesNoKey: a redirect within the server keeps the key, and one to another server
// arrives without it.
func TestRedirectToAnotherHostCarriesNoKey(t *testing.T) {
	t.Parallel()
	seen := &keyLog{mu: sync.Mutex{}, keys: make(map[string]string)}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.note(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`)) //nolint:errcheck // a test server's reply
	}))
	t.Cleanup(other.Close)
	const quotas, moved, away = "/organizations/my-org/quotas", "/moved", "/away"
	var first *httptest.Server
	first = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.note(r)
		switch r.URL.Path {
		case quotas:
			http.Redirect(w, r, first.URL+moved, http.StatusTemporaryRedirect)
		case moved:
			http.Redirect(w, r, other.URL+away, http.StatusTemporaryRedirect)
		}
	}))
	t.Cleanup(first.Close)

	if _, cerr := newRestTestClient(t, first.URL).GetQuotas(t.Context()); cerr != nil {
		t.Fatalf("GetQuotas: %v", cerr)
	}
	for path, want := range map[string]string{quotas: "test-key", moved: "test-key", away: ""} {
		if got, ok := seen.at(path); !ok || got != want {
			t.Errorf("request to %s carried key %q (made: %v), want %q", path, got, ok, want)
		}
	}
}
