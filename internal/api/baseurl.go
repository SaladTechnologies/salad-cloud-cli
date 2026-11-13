package api

import (
	"net/netip"
	"net/url"
	"strings"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// The base-URL variables are test knobs, but whatever server one names is sent the key with every request. So a
// client refuses one that would send the key unencrypted (checkBaseURL), and the CLI warns on every run about one
// that names another machine (RemoteBaseURLs). A stand-in on this machine, which is what the tests use, is neither
// refused nor warned about.

// The variables that override a base URL.
const (
	envAPIBaseURL     = "SALAD_CLI_API_BASE_URL"
	envStorageBaseURL = "SALAD_CLI_STORAGE_BASE_URL"
	envGatewayBaseURL = "SALAD_CLI_AI_GATEWAY_BASE_URL"
)

// baseURLVars are the base-URL variables, in the order a warning names them.
func baseURLVars() []string { return []string{envAPIBaseURL, envStorageBaseURL, envGatewayBaseURL} }

// BaseURLOverride is a base-URL variable that sends requests to another machine.
type BaseURLOverride struct {
	Env  string // the variable
	Host string // the host it names, with its port when it gives one
}

// RemoteBaseURLs lists the base-URL variables that are set to an https server on another machine, which is sent the
// key with every request.
func RemoteBaseURLs(getenv func(string) string) []BaseURLOverride {
	var out []BaseURLOverride
	for _, name := range baseURLVars() {
		value := getenv(name)
		if value == "" {
			continue
		}
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "https" || u.Host == "" || loopbackHost(u.Hostname()) {
			continue // this machine, or a value a client refuses (checkBaseURL), so nothing is sent to it
		}
		out = append(out, BaseURLOverride{Env: name, Host: u.Host})
	}
	return out
}

// checkBaseURL refuses the value of the base-URL variable name unless it is an https URL, or an http one on this
// machine: over http anyone on the network path reads the key. An unset variable is fine.
func checkBaseURL(name, value string) *clierr.Error {
	if value == "" {
		return nil
	}
	u, err := url.Parse(value)
	if err == nil && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http" && loopbackHost(u.Hostname())) {
		return nil
	}
	return clierr.New(clierr.CodeUsage, clierr.KindUsage,
		"%s must be an https URL: the key is sent to it, and over http anyone on the network can read it", name).
		WithHint("remove %s to use SaladCloud; http is accepted only for localhost", name)
}

// loopbackHost reports whether host, a URL's host without its port, is this machine: localhost, a name under it, or
// a loopback address.
func loopbackHost(host string) bool {
	host = strings.ToLower(host)
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}
