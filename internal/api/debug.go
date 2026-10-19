package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

// DebugLevel is how much --debug reports on stderr.
type DebugLevel int

// The debug levels, in increasing detail.
const (
	DebugOff      DebugLevel = iota // no --debug: nothing
	DebugRequests                   // --debug: method, URL, status, duration per request
	DebugBodies                     // --debug=body: plus headers and bodies, redacted
)

// Options configures one run's clients: the environment their base URLs come
// from, what --debug reports and where, and the retry waits and clock tests
// shorten or fix.
type Options struct {
	Getenv   func(string) string
	Debug    DebugLevel
	DebugOut io.Writer        // where --debug writes; nil discards
	Backoff  []time.Duration  // the waits between retries after a 429; nil: 2s, 4s, 8s
	Now      func() time.Time // the clock certificate messages read; nil: time.Now
}

// backoff is o's waits between retries after a 429.
func (o Options) backoff() []time.Duration {
	if o.Backoff != nil {
		return o.Backoff
	}
	return []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}
}

// clock is o's clock.
func (o Options) clock() func() time.Time {
	if o.Now != nil {
		return o.Now
	}
	return time.Now
}

// debugger is o's --debug reporting.
func (o Options) debugger() debugger {
	out := o.DebugOut
	if out == nil {
		out = io.Discard
	}
	return debugger{level: o.Debug, out: out}
}

// debugger is how much --debug reports and where.
type debugger struct {
	level DebugLevel
	out   io.Writer
}

// debugf writes a --debug line to stderr. A failed write there has nowhere to
// be reported, so this, the one place --debug writes, drops its error.
func (d debugger) debugf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	_, _ = io.WriteString(d.out, line) //nolint:errcheck // a failed write to stderr has nowhere to be reported
}

// closeBody closes a response body the CLI is done with. Its error says
// nothing about the response, so it is only reported under --debug.
func (d debugger) closeBody(body io.Closer) { d.closeFailed(body.Close()) }

// closeFailed reports, under --debug, a response body that did not close.
func (d debugger) closeFailed(err error) {
	if err != nil && d.level != DebugOff {
		d.debugf("debug: closing a response body: %v\n", err)
	}
}

// newHTTPClient is an HTTP client with timeout whose requests d reports.
func newHTTPClient(timeout time.Duration, d debugger) *http.Client {
	return &http.Client{Timeout: timeout, Transport: debugTransport{base: http.DefaultTransport, debugger: d}}
}

// debugTransport logs each request at the --debug level, then hands it to
// the real transport. Credentials never reach the log: the key headers,
// credential fields, header values and environment variable values in JSON
// bodies, and signed S4 URL query strings are redacted.
type debugTransport struct {
	base http.RoundTripper
	debugger
}

func (d debugTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if d.level == DebugOff {
		resp, err := d.base.RoundTrip(req)
		if err != nil {
			return nil, fmt.Errorf("no response: %w", err)
		}
		return resp, nil
	}
	if d.level >= DebugBodies {
		d.debugf("debug: > %s %s\n", req.Method, redactText(req.URL.String()))
		for _, k := range sortedHeaderKeys(req.Header) {
			d.debugf("debug: > %s: %s\n", k, redactHeader(k, req.Header.Get(k)))
		}
		d.debugf("debug: > %s\n", d.requestBodyForLog(req))
	}
	start := time.Now()
	resp, err := d.base.RoundTrip(req)
	took := time.Since(start).Round(time.Millisecond)
	if err != nil {
		d.debugf("debug: %s %s -> error after %s: %v\n",
			req.Method,
			redactText(req.URL.String()),
			took,
			err,
		)
		return nil, fmt.Errorf("no response: %w", err)
	}
	d.debugf("debug: %s %s -> %d (%s)\n", req.Method, redactText(req.URL.String()), resp.StatusCode, took)
	if d.level >= DebugBodies && resp.Body != nil {
		body, rerr := io.ReadAll(resp.Body)
		d.closeBody(resp.Body)
		resp.Body = io.NopCloser(bytes.NewReader(body))
		if rerr != nil {
			return nil, fmt.Errorf("reading the response for --debug: %w", rerr)
		}
		d.debugf("debug: < %s\n", redactBody(body))
	}
	return resp, nil
}

// requestBodyForLog renders a request body for --debug=body without
// consuming it. A multipart upload (a file to S4) is not shown.
func (d debugTransport) requestBodyForLog(req *http.Request) string {
	if req.Body == nil || req.Body == http.NoBody {
		return "(no body)"
	}
	if strings.HasPrefix(req.Header.Get("Content-Type"), "multipart/") {
		return "(file upload, not shown)"
	}
	if req.GetBody == nil {
		return "(body not shown)"
	}
	rc, err := req.GetBody()
	if err != nil {
		return "(body not shown)"
	}
	defer d.closeBody(rc)
	b, err := io.ReadAll(rc)
	if err != nil {
		return "(body not shown)"
	}
	return redactBody(b)
}

// redactedHeaders carry credentials.
func redactedHeaders() map[string]bool {
	return map[string]bool{"Salad-Api-Key": true, "Authorization": true}
}

func redactHeader(name, value string) string {
	if redactedHeaders()[http.CanonicalHeaderKey(name)] {
		return "REDACTED"
	}
	return value
}

func sortedHeaderKeys(h http.Header) []string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// credentialField matches a JSON string field that holds a credential: the
// webhook secret, API keys, and the registry and log-sink credentials a
// container group definition can carry.
var credentialField = regexp.MustCompile(
	`"(secret_key|api_key|password|personal_access_token|secret_access_key|access_key_id|service_key|api_token|ingestion_key|token)"\s*:\s*"(?:[^"\\]|\\.)*"`,
)

// signedURL matches an S4 URL's query string, which holds its signing token.
var signedURL = regexp.MustCompile(`(https?://storage-api\.salad\.com/[^"\s?]*)\?[^"\s]*`)

// redactText removes credentials from a URL or a body that is not JSON.
func redactText(s string) string {
	s = credentialField.ReplaceAllString(s, `"$1":"REDACTED"`)
	return signedURL.ReplaceAllString(s, `$1?REDACTED`)
}

// credentialKeys are the JSON fields whose string value is a credential.
func credentialKeys() map[string]bool {
	return map[string]bool{
		"secret_key": true, "api_key": true, "password": true, "personal_access_token": true,
		"secret_access_key": true, "access_key_id": true, "service_key": true, "api_token": true,
		"ingestion_key": true, "token": true,
	}
}

// redactBody removes credentials from a request or response body. A JSON
// body is walked, so nothing depends on how a value is spelled: credential
// fields, the value of every header in a headers list (log-sink and probe
// headers carry bearer tokens), and every environment variable's value are
// replaced. Anything else falls back to redactText.
func redactBody(body []byte) string {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return redactText(string(body))
	}
	var buf bytes.Buffer
	writeRedacted(&buf, v, "", "")
	return signedURL.ReplaceAllString(buf.String(), `$1?REDACTED`)
}

// writeRedacted writes v as compact JSON with object keys sorted. key is the
// field v sits under and parent the field its enclosing value sits under,
// which together say whether a string is a secret.
func writeRedacted(buf *bytes.Buffer, v any, key, parent string) {
	switch t := v.(type) {
	case map[string]any:
		writeRedactedObject(buf, t, key)
	case []any:
		writeRedactedArray(buf, t, key)
	case string:
		if secretString(key, parent) {
			buf.WriteString(`"REDACTED"`)
			return
		}
		writeJSONString(buf, t)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			b = []byte(`null`) // not reached: a decoded JSON value always encodes
		}
		buf.Write(b)
	}
}

// writeRedactedObject is writeRedacted for an object under key: its keys sorted, each value redacted by its own key
// and this one.
func writeRedactedObject(buf *bytes.Buffer, obj map[string]any, key string) {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		writeJSONString(buf, k)
		buf.WriteByte(':')
		writeRedacted(buf, obj[k], k, key)
	}
	buf.WriteByte('}')
}

// writeRedactedArray is writeRedacted for an array under key.
func writeRedactedArray(buf *bytes.Buffer, arr []any, key string) {
	buf.WriteByte('[')
	for i, e := range arr {
		if i > 0 {
			buf.WriteByte(',')
		}
		// An element sits under the list's own field.
		writeRedacted(buf, e, key, key)
	}
	buf.WriteByte(']')
}

// SecretField reports whether a string value under key, inside a value
// under parent, is a credential: the rule --debug=body redacts by, shared
// with every other place the CLI shows a group's values, so no warning
// prints a secret.
func SecretField(key, parent string) bool { return secretString(key, parent) }

// secretString reports whether a string under key, inside a value under
// parent, is a credential.
func secretString(key, parent string) bool {
	return credentialKeys()[key] ||
		parent == "environment_variables" ||
		parent == "headers" && key != "name"
}

func writeJSONString(buf *bytes.Buffer, s string) {
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		buf.WriteString(`""`) // not reached: a Go string always encodes
		return
	}
	buf.Truncate(buf.Len() - 1) // Encode's newline
}
