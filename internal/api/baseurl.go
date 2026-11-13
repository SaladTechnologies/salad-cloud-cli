package api

import (
	"net/netip"
	"net/url"
	"strings"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// The base-URL variables are test knobs: each points the CLI at a stand-in server, which then receives a key. A
// client is not built for one that is not https (checkBaseURL), and the CLI warns on every run about one on
// another machine (RemoteBaseURLs). A stand-in on this machine, which is what the tests use, is accepted over http
// and draws no warning.

// The variables that override a base URL.
const (
	envAPIBaseURL     = "SALAD_CLI_API_BASE_URL"
	envStorageBaseURL = "SALAD_CLI_STORAGE_BASE_URL"
	envGatewayBaseURL = "SALAD_CLI_AI_GATEWAY_BASE_URL"
)

// baseURLVar is a base-URL variable and what the CLI sends to the server it names.
type baseURLVar struct{ env, carries string }

// baseURLVars are the base-URL variables, in the order a warning names them.
func baseURLVars() []baseURLVar {
	return []baseURLVar{
		{envAPIBaseURL, "requests and the API key"},
		{envStorageBaseURL, "file uploads and the API key"},
		{envGatewayBaseURL, "AI Gateway requests and the AI Gateway key"},
	}
}

// BaseURLOverride is a base-URL variable set to a server on another machine.
type BaseURLOverride struct {
	Env     string // the variable
	Host    string // the host it names, with its port when it gives one
	Carries string // what the CLI sends there, as a warning words it
}

// RemoteBaseURLs lists the base-URL variables that are set to an https server on another machine.
func RemoteBaseURLs(getenv func(string) string) []BaseURLOverride {
	var out []BaseURLOverride
	for _, v := range baseURLVars() {
		value := getenv(v.env)
		if value == "" {
			continue
		}
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "https" || u.Host == "" || loopbackHost(u.Hostname()) {
			continue // this machine, or a value no client is built for (checkBaseURL)
		}
		out = append(out, BaseURLOverride{Env: v.env, Host: u.Host, Carries: v.carries})
	}
	return out
}

// CheckAPIBaseURLs is the check [New] makes of the API's and S4's base-URL variables, for a command that asks for
// a key before it builds a client.
func CheckAPIBaseURLs(getenv func(string) string) *clierr.Error {
	for _, name := range []string{envAPIBaseURL, envStorageBaseURL} {
		if err := checkBaseURL(name, getenv(name)); err != nil {
			return err
		}
	}
	return nil
}

// CheckGatewayBaseURL is the check [NewGateway] makes of the AI Gateway's base-URL variable.
func CheckGatewayBaseURL(getenv func(string) string) *clierr.Error {
	return checkBaseURL(envGatewayBaseURL, getenv(envGatewayBaseURL))
}

// checkBaseURL accepts the value of the base-URL variable name when it is unset, an https URL, or an http URL on
// this machine.
func checkBaseURL(name, value string) *clierr.Error {
	if value == "" {
		return nil
	}
	u, err := url.Parse(value)
	if err == nil && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http" && loopbackHost(u.Hostname())) {
		return nil
	}
	return clierr.New(clierr.CodeUsage, clierr.KindUsage, "%s must be an https URL", name).
		WithHint("remove %s to use SaladCloud; http is accepted only for localhost", name)
}

// loopbackHost reports whether host, a URL's host without its port, is this machine: localhost or a loopback
// address. A name under localhost is not counted: not every resolver keeps those on this machine.
func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}
