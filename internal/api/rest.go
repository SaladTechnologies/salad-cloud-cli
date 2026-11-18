package api

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"
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

// restRequest is one API request: its method, path, Content-Type ("" means application/json), body, and whether it
// is a write, whose failure after it reached the API is marked MarkMayHaveApplied.
type restRequest struct {
	method, path, contentType string
	body                      any
	write                     bool
}

// restJSON performs one API request and decodes the JSON response into T.
// contentType is used when body != nil ("" means application/json). 429s are
// retried up to len(c.backoff) times, context-aware. Empty 2xx bodies
// decode to the zero T (mutating verbs return 202 with no body). A 404 under
// an assumed project that does not exist becomes AssumedProjectMissing.
//
// Every method but GET is taken to be a write: a write that fails after the request reached the API is marked
// MarkMayHaveApplied. A POST that only reads (a query) goes through restQuery instead.
func restJSON[T any](ctx context.Context, c *Client, method, path, contentType string, body any) (*T, *clierr.Error) {
	req := restRequest{
		method:      method,
		path:        path,
		contentType: contentType,
		body:        body,
		write:       method != http.MethodGet,
	}
	return rest[T](ctx, c, req)
}

// restQuery is restJSON for a POST that only reads (log and availability queries), so its failures are never marked
// as a write that may have applied.
func restQuery[T any](ctx context.Context, c *Client, path string, body any) (*T, *clierr.Error) {
	return rest[T](ctx, c, restRequest{method: http.MethodPost, path: path, contentType: "", body: body, write: false})
}

// rest is restJSON for req.
func rest[T any](ctx context.Context, c *Client, req restRequest) (*T, *clierr.Error) {
	out, err := doRestJSON[T](ctx, c, req)
	if err != nil && err.HTTPStatus != nil && *err.HTTPStatus == http.StatusUnauthorized && c.OnRejected != nil {
		return nil, c.OnRejected()
	}
	if err != nil && err.Code == clierr.CodeNotFound {
		if gerr := c.orgGone(ctx, req.path); gerr != nil {
			return nil, gerr
		}
		if perr := c.assumedProjectMissing(ctx, req.method, req.path); perr != nil {
			return nil, perr
		}
	}
	return out, err
}

// doRestJSON is rest without the assumed-project check.
func doRestJSON[T any](ctx context.Context, c *Client, req restRequest) (*T, *clierr.Error) {
	payload, cerr := encodeBody(req.body)
	if cerr != nil {
		return nil, cerr
	}
	endpoint := c.baseURL + req.path

	for attempt := 0; ; attempt++ {
		rep, cerr := c.send(ctx, req.method, endpoint, req.contentType, payload)
		if cerr != nil {
			return nil, req.failed(cerr, rep)
		}
		if rep.status >= 200 && rep.status < 300 {
			return decodeReply[T](req, rep)
		}
		lastErr := req.refused(rep)
		if rep.status != http.StatusTooManyRequests || attempt >= len(c.backoff) {
			return nil, lastErr
		}
		if werr := waitToRetry(ctx, c.backoff[attempt], lastErr); werr != nil {
			return nil, werr
		}
	}
}

// failed is req's error for an attempt that got no usable response (rep is what it got): a write that had reached
// the API is marked as one that may have been applied.
func (req restRequest) failed(cerr *clierr.Error, rep reply) *clierr.Error {
	if req.write && rep.sent {
		return mayHaveApplied(cerr)
	}
	return cerr
}

// refused is req's error for a reply that is not a 2xx: the API's own, marked as one that may have been applied for a
// write that got a status that does not say it was refused (unsureStatus).
func (req restRequest) refused(rep reply) *clierr.Error {
	e := errorFromBody(rep.status, rep.body)
	if req.write && unsureStatus(rep.status) {
		return mayHaveApplied(e)
	}
	return e
}

// decodeReply decodes a 2xx reply to req. A write's reply that cannot be decoded was still accepted, so its error
// says so and is marked as one that may have been applied.
func decodeReply[T any](req restRequest, rep reply) (*T, *clierr.Error) {
	out, derr := decodeSuccess[T](rep.body)
	if derr != nil && req.write {
		return nil, clierr.New(clierr.CodeGeneric, clierr.KindAPIError,
			"the API accepted the request (HTTP %d), but its response could not be read: %s", rep.status,
			derr.Message).WithStatus(rep.status).MarkMayHaveApplied()
	}
	return out, derr
}

// waitToRetry waits d before a 429 is retried, and returns nil to retry: Ctrl-C returns the canceled-request error
// (a 429 applied nothing), and the caller's deadline returns lastErr, the 429, so a wait can tell the two apart.
func waitToRetry(ctx context.Context, d time.Duration, lastErr *clierr.Error) *clierr.Error {
	select {
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.Canceled) {
			return clierr.Canceled()
		}
		return lastErr
	case <-time.After(d):
		return nil
	}
}

// unsureStatus reports whether a write that got status may still have been applied: a server error or a timeout
// says nothing about whether the API acted on it before it failed, or will act on it later. Any other 4xx was
// refused.
func unsureStatus(status int) bool {
	return status >= http.StatusInternalServerError || status == http.StatusRequestTimeout
}

// mayHaveApplied marks e as a write that reached the API and may have been applied, and says so in its message.
// A canceled request keeps its placeholder message: Run names the command and adds the same words. A hint the
// API's error carried is dropped: checking before sending it again comes first, and the command (or Run) says how.
func mayHaveApplied(e *clierr.Error) *clierr.Error {
	if !e.IsCancel() {
		e.Message += "; " + clierr.MayHaveAppliedText
	}
	e.Hint = nil
	return e.MarkMayHaveApplied()
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

// reply is what one attempt at a request got: the response's status and whole body, and whether the request got a
// connection to the API, so it may have reached it (sent). A request that failed before that (a DNS, connection or
// TLS failure) never left this machine.
type reply struct {
	status int
	body   []byte
	sent   bool
}

// send makes one attempt at a request, reading payload afresh. On an error, the reply still says whether the
// request was sent. The Content-Type header goes only with a payload, as contentType or else application/json.
func (c *Client) send(
	ctx context.Context,
	method, endpoint, contentType string,
	payload []byte,
) (reply, *clierr.Error) {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	var connected atomic.Bool
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) { connected.Store(true) },
	})
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return reply{status: 0, body: nil, sent: false},
			clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot build the request: %v", err)
	}
	req.Header.Set("Salad-Api-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent())
	if payload != nil {
		req.Header.Set("Content-Type", cmp.Or(contentType, "application/json"))
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return reply{status: 0, body: nil, sent: connected.Load()}, transportErr(ctx, "network error", err, c.now)
	}
	respBody, err := io.ReadAll(resp.Body)
	c.dbg.closeFailed(resp.Body.Close())
	if err != nil {
		return reply{status: resp.StatusCode, body: nil, sent: true}, responseReadErr(ctx, resp.StatusCode, err)
	}
	return reply{status: resp.StatusCode, body: respBody, sent: true}, nil
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
