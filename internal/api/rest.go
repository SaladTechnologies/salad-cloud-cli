package api

import (
	"bytes"
	"cmp"
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

// Probe makes one unauthenticated GET and returns its status, through the
// same transport as every other request (so --debug shows it and it carries
// the User-Agent). doctor uses it to tell "unreachable" from "rejected".
func Probe(ctx context.Context, target string, opts Options) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return 0, fmt.Errorf("building the probe: %w", err)
	}
	req.Header.Set("User-Agent", userAgent())
	dbg := opts.debugger()
	resp, err := newHTTPClient(restTimeout, dbg).Do(req)
	if err != nil {
		return 0, fmt.Errorf("probing the API: %w", err)
	}
	dbg.closeFailed(resp.Body.Close())
	return resp.StatusCode, nil
}

// restJSON performs one API request and decodes the JSON response into T.
// contentType is used when body != nil ("" means application/json). 429s are
// retried up to len(c.backoff) times, context-aware. Empty 2xx bodies
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
	payload, cerr := encodeBody(body)
	if cerr != nil {
		return nil, cerr
	}
	endpoint := c.baseURL + path

	for attempt := 0; ; attempt++ {
		status, respBody, cerr := c.send(ctx, method, endpoint, contentType, payload)
		if cerr != nil {
			return nil, cerr
		}
		if status >= 200 && status < 300 {
			return decodeSuccess[T](respBody)
		}
		lastErr := errorFromBody(status, respBody)
		if status != http.StatusTooManyRequests || attempt >= len(c.backoff) {
			return nil, lastErr
		}
		select {
		case <-ctx.Done():
			return nil, clierr.Canceled() // Ctrl-C while waiting to retry
		case <-time.After(c.backoff[attempt]):
		}
	}
}

// encodeBody is the request body's bytes, nil when there is no body.
func encodeBody(body any) ([]byte, *clierr.Error) {
	if body == nil {
		return nil, nil
	}
	if raw, ok := body.(json.RawMessage); ok {
		// A raw document goes on the wire byte-for-byte: json.Marshal
		// would compact it and escape HTML characters (< > &), mutating
		// the caller's bytes — a merge patch must be sent as written.
		return raw, nil
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, clierr.New(
			clierr.CodeGeneric,
			clierr.KindAPIError,
			"cannot encode the request body: %v",
			err,
		)
	}
	return payload, nil
}

// send makes one attempt at a request, reading payload afresh, and returns the response's status and whole body.
// The Content-Type header goes only with a payload, as contentType or else application/json.
func (c *Client) send(
	ctx context.Context,
	method, endpoint, contentType string,
	payload []byte,
) (int, []byte, *clierr.Error) {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return 0, nil, clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot build the request: %v", err)
	}
	req.Header.Set("Salad-Api-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent())
	if payload != nil {
		req.Header.Set("Content-Type", cmp.Or(contentType, "application/json"))
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, transportErr(ctx, "network error", err, c.now)
	}
	respBody, err := io.ReadAll(resp.Body)
	c.dbg.closeFailed(resp.Body.Close())
	if err != nil {
		return 0, nil, readErr(ctx, "response", err)
	}
	return resp.StatusCode, respBody, nil
}

// decodeSuccess decodes a 2xx response body into a new T; an empty body is the zero T (mutating verbs return 202
// with no body).
func decodeSuccess[T any](respBody []byte) (*T, *clierr.Error) {
	out := new(T)
	if len(bytes.TrimSpace(respBody)) == 0 {
		return out, nil
	}
	err := json.Unmarshal(respBody, out)
	if err == nil {
		return out, nil
	}
	// Some responses carry numeric enum values (e.g. restart_policy 1/2/3
	// instead of always/on_failure/never). Normalize and retry once before
	// giving up.
	if fixed, changed := normalizeNumericEnums(respBody); changed {
		if err2 := json.Unmarshal(fixed, out); err2 == nil {
			return out, nil
		}
	}
	return nil, clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot decode the response: %v", err)
}

// restNoBody is restJSON for verbs whose success response body is ignored.
func restNoBody(ctx context.Context, c *Client, method, path string) *clierr.Error {
	_, err := restJSON[json.RawMessage](ctx, c, method, path, "", nil)
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
func numericEnumNames() map[string]map[float64]string {
	return map[string]map[float64]string{
		"restart_policy": {1: "always", 2: "on_failure", 3: "never"},
		"priority":       {1: "high", 2: "medium", 3: "low", 4: "batch"},
	}
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
	if !repairEnums(doc) {
		return raw, false
	}
	fixed, err := json.Marshal(doc)
	if err != nil {
		return raw, false
	}
	return fixed, true
}

// repairEnums repairs the numeric enum fields of v and of every object under it, and reports whether it changed
// anything.
func repairEnums(v any) bool {
	changed := false
	switch t := v.(type) {
	case map[string]any:
		changed = repairEnumFields(t)
		for _, child := range t {
			if repairEnums(child) {
				changed = true
			}
		}
	case []any:
		for _, child := range t {
			if repairEnums(child) {
				changed = true
			}
		}
	}
	return changed
}

// repairEnumFields rewrites one object's numeric enum fields to their names, drops the ones with no name, and
// reports whether it changed anything.
func repairEnumFields(obj map[string]any) bool {
	changed := false
	for field, names := range numericEnumNames() {
		n, ok := obj[field].(float64)
		if !ok {
			continue
		}
		if name, ok := names[n]; ok {
			obj[field] = name
		} else {
			delete(obj, field)
		}
		changed = true
	}
	return changed
}
