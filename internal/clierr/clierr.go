// Package clierr defines the CLI's stable exit codes and structured error type.
package clierr

import "fmt"

// Exit codes. These are part of the CLI's public contract — scripts and
// agents depend on them. Do not renumber.
const (
	CodeOK           = 0
	CodeGeneric      = 1
	CodeUsage        = 2
	CodeAuth         = 3
	CodeNotFound     = 4
	CodeRateLimited  = 5
	CodeInvalidInput = 6
	CodeNetwork      = 7
	CodeNotAvailable = 10
)

// Kind is the error document's "kind": a closed set, public like the exit
// codes. Every kind is one of the constants below, and a new failure mode
// maps onto an existing kind. Adding a kind is a contract change: here, in
// the table in docs/guide.md, and in the CHANGELOG (a test checks the first
// two, and that no call site spells a kind as a literal).
type Kind string

// The kinds, each with the exit code it goes with.
const (
	KindUsage               Kind = "usage"                // 2: the command line is wrong
	KindInvalidInput        Kind = "invalid_input"        // 6: a value breaks a platform rule
	KindUnknownField        Kind = "unknown_field"        // 6: a definition file has a field the API does not take
	KindAuth                Kind = "auth"                 // 3: no credential, or the API rejected it
	KindNotFound            Kind = "not_found"            // 4
	KindRateLimited         Kind = "rate_limited"         // 5
	KindNetwork             Kind = "network"              // 7
	KindNotAvailable        Kind = "not_available"        // 10: the platform cannot serve it right now
	KindAPIError            Kind = "api_error"            // 1: any other API failure, and local I/O
	KindInternal            Kind = "internal"             // 1: a CLI bug
	KindInterrupted         Kind = "interrupted"          // 1: Ctrl-C during a wait, a prompt or a request, or end of input at a prompt
	KindWaitTimeout         Kind = "wait_timeout"         // 1: a wait ran out of time
	KindGroupStopped        Kind = "group_stopped"        // 1: a waited-for group stopped instead
	KindGroupFailed         Kind = "group_failed"         // 1: a waited-for group failed
	KindGroupSucceeded      Kind = "group_succeeded"      // 1: a waited-for group exited instead of running
	KindJobFailed           Kind = "job_failed"           // 1: a waited-for job failed
	KindJobCancelled        Kind = "job_cancelled"        // 1: a waited-for job was cancelled
	KindTranscriptionFailed Kind = "transcription_failed" // 1: a transcription job failed or reported an error
	KindChildExit           Kind = "child_exit"           // the child's code: ssh, cp or sftp exited nonzero
)

// Kinds lists every kind, in the guide table's order.
var Kinds = []Kind{
	KindUsage, KindInvalidInput, KindUnknownField, KindAuth, KindNotFound, KindRateLimited, KindNetwork,
	KindNotAvailable, KindAPIError, KindInternal, KindInterrupted, KindWaitTimeout, KindGroupStopped,
	KindGroupFailed, KindGroupSucceeded, KindJobFailed, KindJobCancelled, KindTranscriptionFailed, KindChildExit,
}

// Error is the CLI's structured error. It marshals directly into the
// machine-parseable stderr error document in JSON output mode, which is
// written outside output.Emit, so every key is present here by tag: hint is
// null when there is no fix to suggest, http_status when no HTTP response
// caused the error.
type Error struct {
	Code       int     `json:"exit_code"`
	Kind       Kind    `json:"kind"`
	Message    string  `json:"message"`
	Hint       *string `json:"hint"`
	HTTPStatus *int    `json:"http_status"`

	// cancel marks the error a request returns when Ctrl-C stopped it; the
	// CLI names the command in its message (Cancelled).
	cancel bool
	// final marks an error that already says what was not found (Final).
	final bool
}

// Final marks e as already naming what went wrong, so a command that would
// add its own context to an error of this kind (a 404 rewritten to name the
// group) leaves it as it is; it returns e.
func (e *Error) Final() *Error {
	e.final = true
	return e
}

// IsFinal reports whether e was marked Final.
func (e *Error) IsFinal() bool { return e != nil && e.final }

// Error implements the error interface.
func (e *Error) Error() string { return e.Message }

// WithHint sets the error's hint, the fix shown under the message (in table
// mode a hint: line, in the error document the hint key), and returns e.
func (e *Error) WithHint(format string, args ...any) *Error {
	h := fmt.Sprintf(format, args...)
	e.Hint = &h
	return e
}

// WithStatus sets the status of the HTTP response that caused the error,
// for an error the CLI builds in place of the API's own, and returns e.
func (e *Error) WithStatus(status int) *Error {
	e.HTTPStatus = &status
	return e
}

// HintText returns the hint, or "" when there is none.
func (e *Error) HintText() string {
	if e.Hint == nil {
		return ""
	}
	return *e.Hint
}

// Cancelled is the error for a request that Ctrl-C stopped. Its message is
// a placeholder: the CLI reports it as "<command> cancelled".
func Cancelled() *Error {
	return &Error{Code: CodeGeneric, Kind: KindInterrupted, Message: "cancelled", cancel: true}
}

// IsCancel reports whether e is a request that Ctrl-C stopped (Cancelled).
func (e *Error) IsCancel() bool { return e != nil && e.cancel }

// New builds an *Error with a formatted message.
func New(code int, kind Kind, format string, args ...any) *Error {
	return &Error{Code: code, Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// HTTP builds the *Error for an HTTP response with the given status.
func HTTP(code int, kind Kind, status int, message string) *Error {
	return &Error{Code: code, Kind: kind, Message: message, HTTPStatus: &status}
}
