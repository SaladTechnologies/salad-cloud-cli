package api

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/logs"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

// TestWriteFailureMarking pins which failures mark a write as one the API may have applied: a server error, a
// timeout status, a dropped or cut-off connection and an undecodable 2xx may have reached the API and say nothing
// about whether it acted; a refusal does not, and a read never does.
func TestWriteFailureMarking(t *testing.T) {
	t.Parallel()
	patch := func(c *Client, ctx context.Context) *clierr.Error {
		_, cerr := c.UpdateGroupRaw(ctx, "my-app", []byte(`{"replicas":2}`))
		return cerr
	}
	get := func(c *Client, ctx context.Context) *clierr.Error {
		_, cerr := c.GetGroup(ctx, "my-app")
		return cerr
	}
	query := func(c *Client, ctx context.Context) *clierr.Error {
		_, cerr := c.QueryLogs(ctx, logs.LogEntryQuery{})
		return cerr
	}
	gateway504 := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
		respond(t, w, "<html><body>504 Gateway Time-out</body></html>")
	}
	hangUp := func(w http.ResponseWriter, _ *http.Request) { hangUpOn(t, w) }
	cases := []struct {
		name    string
		handler http.HandlerFunc
		call    func(*Client, context.Context) *clierr.Error
		marked  bool
		kind    clierr.Kind
		status  int // 0: no http_status
	}{
		{"patch 504", gateway504, patch, true, clierr.KindAPIError, http.StatusGatewayTimeout},
		{"patch 500", statusHandler(http.StatusInternalServerError), patch, true, clierr.KindAPIError, 500},
		{"patch 408", statusHandler(http.StatusRequestTimeout), patch, true, clierr.KindAPIError, 408},
		{"patch dropped connection", hangUp, patch, true, clierr.KindNetwork, 0},
		{
			"patch 200 undecodable",
			func(w http.ResponseWriter, _ *http.Request) { respond(t, w, "{not json") },
			patch,
			true,
			clierr.KindAPIError,
			0,
		},
		{"patch 400", statusHandler(http.StatusBadRequest), patch, false, clierr.KindInvalidInput, 400},
		{"patch 409", statusHandler(http.StatusConflict), patch, false, clierr.KindAPIError, 409},
		{"get 504", gateway504, get, false, clierr.KindAPIError, http.StatusGatewayTimeout},
		{"get dropped connection", hangUp, get, false, clierr.KindNetwork, 0},
		{"log query 504", gateway504, query, false, clierr.KindAPIError, http.StatusGatewayTimeout},
		{"patch 200 cut off", cutOff(t, http.StatusOK), patch, true, clierr.KindNetwork, 0},
		{"get 200 cut off", cutOff(t, http.StatusOK), get, false, clierr.KindNetwork, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(tc.handler)
			t.Cleanup(srv.Close)
			cerr := tc.call(newRestTestClient(t, srv.URL), t.Context())
			if cerr == nil {
				t.Fatal("got no error")
			}
			if cerr.MayHaveApplied() != tc.marked {
				t.Errorf("MayHaveApplied = %v, want %v (error %q)", cerr.MayHaveApplied(), tc.marked, cerr.Message)
			}
			checkMarkedError(t, cerr, markWant{marked: tc.marked, kind: tc.kind, status: tc.status})
		})
	}
}

// checkMarkedError checks that cerr says it may have been applied exactly when marked, and its kind and status
// (0: no http_status).
func checkMarkedError(t *testing.T, cerr *clierr.Error, want markWant) {
	t.Helper()
	says := strings.Contains(cerr.Message, "may have been applied")
	if says != want.marked {
		t.Errorf("message %q: says it may have applied = %v, want %v", cerr.Message, says, want.marked)
	}
	if cerr.Kind != want.kind {
		t.Errorf("kind = %s, want %s", cerr.Kind, want.kind)
	}
	got := 0
	if cerr.HTTPStatus != nil {
		got = *cerr.HTTPStatus
	}
	if got != want.status {
		t.Errorf("http_status = %d, want %d", got, want.status)
	}
}

// markWant is what checkMarkedError expects: whether the message says the change may have been applied, the kind,
// and the status (0: no http_status).
type markWant struct {
	marked bool
	kind   clierr.Kind
	status int
}

// hangUpOn closes the connection under w without an answer.
func hangUpOn(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	hj, ok := w.(http.Hijacker)
	if !ok {
		t.Error("the response writer cannot hijack its connection")
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		t.Errorf("hijack: %v", err)
		return
	}
	reportErr(t, conn.Close())
}

// cutOff answers with status and the start of a body, then hangs up before the rest arrives.
func cutOff(t *testing.T, status int) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(status)
		respond(t, w, `{"title":`)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		hangUpOn(t, w)
	}
}

// statusHandler answers every request with status and an empty body.
func statusHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }
}

// TestWriteNeverSentIsNotMarked: a write that never got a connection never reached the API.
func TestWriteNeverSentIsNotMarked(t *testing.T) {
	t.Parallel()
	// Port 1 is outside the ephemeral range, so no parallel test's server can be listening on it.
	c := newRestTestClient(t, "http://127.0.0.1:1")
	_, cerr := c.UpdateGroupRaw(t.Context(), "my-app", []byte(`{"replicas":2}`))
	if cerr == nil || cerr.Kind != clierr.KindNetwork {
		t.Fatalf("error = %v, want a network error", cerr)
	}
	if cerr.MayHaveApplied() {
		t.Errorf("a refused connection is marked as may have applied: %q", cerr.Message)
	}
}

// TestWriteCanceledAfterSendingIsMarked: Ctrl-C while the API holds a write is a cancel that may have applied.
func TestWriteCanceledAfterSendingIsMarked(t *testing.T) {
	t.Parallel()
	got, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		should(io.Copy(io.Discard, r.Body))(t) // the server notices the client going away only once the body is read
		close(got)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) }) // runs first, so Close does not wait on the handler
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-got
		cancel()
	}()
	_, cerr := newRestTestClient(t, srv.URL).UpdateGroupRaw(ctx, "my-app", []byte(`{"replicas":2}`))
	if !cerr.IsCancel() || !cerr.MayHaveApplied() {
		t.Fatalf("error = %+v, want a cancel marked as may have applied", cerr)
	}
}

// TestDeadlineDuringBackoffIsNotACancel: a caller's deadline that runs out while waiting to retry a 429 returns the
// 429, not the Ctrl-C error, so a wait can tell its own deadline from Ctrl-C.
func TestDeadlineDuringBackoffIsNotACancel(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(statusHandler(http.StatusTooManyRequests))
	t.Cleanup(srv.Close)
	c := newRestTestClient(t, srv.URL)
	c.backoff = []time.Duration{time.Hour}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	t.Cleanup(cancel)
	_, cerr := c.GetGroup(ctx, "my-app")
	if cerr == nil || cerr.IsCancel() || cerr.Kind != clierr.KindRateLimited {
		t.Fatalf("error = %+v, want the 429 (rate_limited), not a cancel", cerr)
	}
}

// TestDebugBodyCutOffWriteIsMarked: --debug=body reads the body itself; a write whose body is cut off there is a
// network error that may have applied, as it is without --debug.
func TestDebugBodyCutOffWriteIsMarked(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(cutOff(t, http.StatusOK))
	t.Cleanup(srv.Close)
	opts := testOptions(baseURLs(srv.URL))
	opts.Debug, opts.DebugOut = DebugBodies, io.Discard
	c, cerr := New(testResolved(), opts)
	if cerr != nil {
		t.Fatalf("New: %v", cerr)
	}
	_, cerr = c.UpdateGroupRaw(t.Context(), "my-app", []byte(`{"replicas":2}`))
	if cerr == nil || cerr.Kind != clierr.KindNetwork || !cerr.MayHaveApplied() {
		t.Fatalf("error = %+v, want a network error marked as may have applied", cerr)
	}
}

// TestWriteNeverBuiltIsNotMarked: a request that could not be built never left this machine.
func TestWriteNeverBuiltIsNotMarked(t *testing.T) {
	t.Parallel()
	c := newRestTestClient(t, "http://127.0.0.1:1")
	_, cerr := restJSON[json.RawMessage](t.Context(), c, "NOT A METHOD", "/x", "", nil)
	if cerr == nil || cerr.MayHaveApplied() {
		t.Fatalf("error = %+v, want an unmarked build error", cerr)
	}
}

// TestNeverSent pins the transport errors that say a request never left this machine.
func TestNeverSent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"dns", &net.DNSError{Err: "no such host", Name: "api.invalid"}, true},
		{"refused", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, true},
		{"unknown authority", x509.UnknownAuthorityError{}, true},
		{"hostname", x509.HostnameError{}, true},
		{"expired", x509.CertificateInvalidError{}, true},
		{"read reset", &net.OpError{Op: "read", Err: errors.New("connection reset by peer")}, false},
		{"eof", io.ErrUnexpectedEOF, false},
		{"timeout", context.DeadlineExceeded, false}, // may have come after the request was sent: marked, to be safe
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wrapped := fmt.Errorf("no response: %w", &url.Error{Op: "Patch", URL: "https://x", Err: tc.err})
			if got := neverSent(wrapped); got != tc.want {
				t.Errorf("neverSent(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// testResolved is the context newRestTestClient's client has.
func testResolved() *config.Resolved {
	return &config.Resolved{APIKey: "test-key", Organization: "my-org", Project: "default"}
}
