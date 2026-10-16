package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// The CLI talks to the API over its own thin HTTP transport and uses the SDK
// only as the codegen'd type layer. The SDK's transport validates responses
// against the schema tags and rejects valid API responses (a non-nil
// util.Nullable[string] carrying a `pattern` tag fails its pattern check —
// e.g. current_state.description on every container group), so we decode
// with plain json.Unmarshal instead. This transport is the deliberately
// disposable translation layer: when the API revision lands, it gets
// replaced without the commands above it having to change.

// restTimeout bounds each API request.
const restTimeout = 60 * time.Second

// restRetries429 is the number of retries after a 429, with restBackoff sleeps.
var restBackoff = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}

var restHTTPClient = &http.Client{Timeout: restTimeout, Transport: debugTransport{http.DefaultTransport}}

// Probe makes one unauthenticated GET and returns its status, through the
// same transport as every other request (so --debug shows it and it carries
// the User-Agent). doctor uses it to tell "unreachable" from "rejected".
func Probe(ctx context.Context, url string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := restHTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// restJSON performs one API request and decodes the JSON response into T.
// contentType is used when body != nil ("" means application/json). 429s are
// retried up to len(restBackoff) times, context-aware. Empty 2xx bodies
// decode to the zero T (mutating verbs return 202 with no body). A 404 under
// an assumed project that does not exist becomes AssumedProjectMissing.
func restJSON[T any](ctx context.Context, c *Client, method, path, contentType string, body any) (*T, *clierr.Error) {
	out, err := doRestJSON[T](ctx, c, method, path, contentType, body)
	if err != nil && err.HTTPStatus != nil && *err.HTTPStatus == http.StatusUnauthorized && c.OnRejected != nil {
		return nil, c.OnRejected()
	}
	if err != nil && err.Code == clierr.CodeNotFound {
		if gerr := c.orgGone(ctx, path); gerr != nil {
			return nil, gerr
		}
		if perr := c.assumedProjectMissing(ctx, method, path); perr != nil {
			return nil, perr
		}
	}
	return out, err
}

// doRestJSON is restJSON without the assumed-project check.
func doRestJSON[T any](ctx context.Context, c *Client, method, path, contentType string, body any) (*T, *clierr.Error) {
	var payload []byte
	if body != nil {
		if raw, ok := body.(json.RawMessage); ok {
			// A raw document goes on the wire byte-for-byte: json.Marshal
			// would compact it and escape HTML characters (< > &), mutating
			// the caller's bytes — a merge patch must be sent as written.
			payload = raw
		} else {
			var err error
			payload, err = json.Marshal(body)
			if err != nil {
				return nil, clierr.New(
					clierr.CodeGeneric,
					clierr.KindAPIError,
					"cannot encode the request body: %v",
					err,
				)
			}
		}
	}
	endpoint := c.baseURL + path

	var lastErr *clierr.Error
	for attempt := 0; ; attempt++ {
		var reader io.Reader
		if payload != nil {
			reader = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
		if err != nil {
			return nil, clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot build the request: %v", err)
		}
		req.Header.Set("Salad-Api-Key", c.apiKey)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", UserAgent)
		if payload != nil {
			if contentType == "" {
				contentType = "application/json"
			}
			req.Header.Set("Content-Type", contentType)
		}

		resp, err := restHTTPClient.Do(req)
		if err != nil {
			return nil, transportErr(ctx, "network error", err)
		}
		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, readErr(ctx, "response", err)
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			out := new(T)
			if len(bytes.TrimSpace(respBody)) == 0 {
				return out, nil
			}
			if err := json.Unmarshal(respBody, out); err != nil {
				// Some responses carry numeric enum values (e.g.
				// restart_policy 1/2/3 instead of always/on_failure/never).
				// Normalize and retry once before giving up.
				if fixed, changed := normalizeNumericEnums(respBody); changed {
					if err2 := json.Unmarshal(fixed, out); err2 == nil {
						return out, nil
					}
				}
				return nil, clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot decode the response: %v", err)
			}
			return out, nil
		}

		lastErr = errorFromBody(resp.StatusCode, respBody)
		if resp.StatusCode != http.StatusTooManyRequests || attempt >= len(restBackoff) {
			return nil, lastErr
		}
		select {
		case <-ctx.Done():
			return nil, clierr.Cancelled() // Ctrl-C while waiting to retry
		case <-time.After(restBackoff[attempt]):
		}
	}
}

// restNoBody is restJSON for verbs whose success response body is ignored.
func restNoBody(ctx context.Context, c *Client, method, path string, body any) *clierr.Error {
	_, err := restJSON[json.RawMessage](ctx, c, method, path, "", body)
	return err
}

// seg path-escapes one URL path segment.
func seg(s string) string { return url.PathEscape(s) }

// subPath is "/" and each of parts path-escaped, joined with "/"; "" when there are none.
func subPath(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	escaped := make([]string, len(parts))
	for i, part := range parts {
		escaped[i] = seg(part)
	}
	return "/" + strings.Join(escaped, "/")
}

// groupPath builds /organizations/{org}/projects/{project}/containers[/...].
func (c *Client) groupPath(parts ...string) string {
	return fmt.Sprintf("/organizations/%s/projects/%s/containers", seg(c.org), seg(c.project)) + subPath(parts)
}

// numericEnumNames maps the enum values some responses carry as raw
// integers to their documented string forms, per field (docs/notes.md).
var numericEnumNames = map[string]map[float64]string{
	"restart_policy": {1: "always", 2: "on_failure", 3: "never"},
	"priority":       {1: "high", 2: "medium", 3: "low", 4: "batch"},
}

// normalizeNumericEnums walks a JSON document and repairs known
// numeric-enum leaks: mapped values are rewritten to their string forms,
// and undefined values (e.g. 0) are dropped so the rest of the document
// still decodes. Returns the re-encoded document and whether anything
// changed.
func normalizeNumericEnums(raw []byte) ([]byte, bool) {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return raw, false
	}
	changed := false
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for field, names := range numericEnumNames {
				if n, ok := t[field].(float64); ok {
					if name, ok := names[n]; ok {
						t[field] = name
					} else {
						delete(t, field)
					}
					changed = true
				}
			}
			for _, child := range t {
				walk(child)
			}
		case []any:
			for _, child := range t {
				walk(child)
			}
		}
	}
	walk(doc)
	if !changed {
		return raw, false
	}
	fixed, err := json.Marshal(doc)
	if err != nil {
		return raw, false
	}
	return fixed, true
}
