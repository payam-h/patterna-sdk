package patterna

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Codes the SDK sets itself. API errors carry the API's own code (pii_rejected, mode_conflict...).
const (
	CodeTimeout          = "timeout"
	CodeNetwork          = "network_error"
	CodeAborted          = "aborted"
	CodeCircuitOpen      = "circuit_open"
	CodeConsentRequired  = "consent_required"
	CodeConsentViolation = "consent_violation"
	CodeInvalidArgument  = "invalid_argument"
	CodeNotConfigured    = "not_configured"
	CodeBadResponse      = "bad_response"
	CodeQueueFull        = "queue_full"
	CodeClosed           = "closed"
)

// API codes with a typed sentinel. Whether RecommendWithFallback falls back on them:
//
//	no_candidates    422  fallback: nothing eligible to rank (empty catalog, everything excluded)
//	surface_off      409  fallback: the surface's kill switch is on; show your own list
//	unknown_surface  404  fallback, but it is a configuration bug: alert on Recommendation.Cause
//	unknown_request  404  Abandon or ReportInteractions named a request the engine never logged
//	unknown_item     404  a catalog call named an item that does not exist
//	pii_rejected     422  returned, never a fallback: the request carried personal data
//	mode_conflict    422  returned, never a fallback: the consent mode contradicts the stored one
//	mode_required    422  SetUserProfile without Mode for a user Patterna has no row for: a profile
//	                      never creates consent; set the mode first (SetUserMode) or in the profile
//	not_shown        409  Abandon named a slate answered shown=false: hide the block, never abandon
const (
	CodeNoCandidates   = "no_candidates"
	CodeSurfaceOff     = "surface_off"
	CodeUnknownSurface = "unknown_surface"
	CodeUnknownRequest = "unknown_request"
	CodeUnknownItem    = "unknown_item"
	CodePIIRejected    = "pii_rejected"
	CodeModeConflict   = "mode_conflict"
	CodeModeRequired   = "mode_required"
	CodeNotShown       = "not_shown"
)

// Error is every error the client returns.
type Error struct {
	Code      string         // stable machine code
	Op        string         // SDK operation, e.g. "recommend"
	Status    int            // HTTP status when the API answered, else 0
	RequestID string         // the API's request id when known
	Message   string         // human text; never contains a user id
	Details   map[string]any // the API's details object, if any
	Err       error          // underlying cause

	notSent bool // the request never left the process (open breaker)
}

// Sentinels for errors.Is. They match any *Error with the same Code.
var (
	ErrTimeout          = &Error{Code: CodeTimeout}
	ErrNetwork          = &Error{Code: CodeNetwork}
	ErrAborted          = &Error{Code: CodeAborted}
	ErrCircuitOpen      = &Error{Code: CodeCircuitOpen}
	ErrConsentRequired  = &Error{Code: CodeConsentRequired}
	ErrConsentViolation = &Error{Code: CodeConsentViolation}
	ErrInvalidArgument  = &Error{Code: CodeInvalidArgument}
	ErrNotConfigured    = &Error{Code: CodeNotConfigured}
	ErrBadResponse      = &Error{Code: CodeBadResponse}
	ErrQueueFull        = &Error{Code: CodeQueueFull}
	ErrClosed           = &Error{Code: CodeClosed}

	ErrNoCandidates   = &Error{Code: CodeNoCandidates}
	ErrSurfaceOff     = &Error{Code: CodeSurfaceOff}
	ErrUnknownSurface = &Error{Code: CodeUnknownSurface}
	ErrUnknownRequest = &Error{Code: CodeUnknownRequest}
	ErrUnknownItem    = &Error{Code: CodeUnknownItem}
	ErrPIIRejected    = &Error{Code: CodePIIRejected}
	ErrModeConflict   = &Error{Code: CodeModeConflict}
	ErrModeRequired   = &Error{Code: CodeModeRequired}
	ErrNotShown       = &Error{Code: CodeNotShown}
)

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("patterna: ")
	if e.Op != "" {
		b.WriteString(e.Op)
		b.WriteString(": ")
	}
	b.WriteString(e.Code)
	if e.Status != 0 || e.RequestID != "" {
		b.WriteString(" (")
		if e.Status != 0 {
			b.WriteString("status ")
			b.WriteString(strconv.Itoa(e.Status))
		}
		if e.RequestID != "" {
			if e.Status != 0 {
				b.WriteString(", ")
			}
			b.WriteString("request ")
			b.WriteString(e.RequestID)
		}
		b.WriteString(")")
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// Is matches a sentinel with the same code, so errors.Is(err, ErrTimeout) works on any timeout.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code != "" && t.Op == "" && t.Status == 0 && t.Code == e.Code
}

// Transient reports whether the service, not the request, was the problem.
func (e *Error) Transient() bool {
	return e.Code == CodeTimeout || e.Code == CodeNetwork || e.Code == CodeCircuitOpen ||
		e.Status >= 500 || e.Status == 429
}

// IsAPIError reports whether err is an answer from the API with the given code.
func IsAPIError(err error, code string) bool {
	var pe *Error
	return errors.As(err, &pe) && pe.Status != 0 && pe.Code == code
}

// isCallerBug reports errors that must never be hidden behind a fallback: consent and contract
// violations are bugs in the caller, and a consent bug is a privacy bug.
func isCallerBug(err error) bool {
	var pe *Error
	if !errors.As(err, &pe) {
		return false
	}
	switch pe.Code {
	case CodeConsentRequired, CodeConsentViolation, CodeInvalidArgument, CodeNotConfigured,
		CodePIIRejected, CodeModeConflict, CodeModeRequired:
		return true
	}
	return false
}

// abandonReason says why a sent Recommend was given up on, or "" when there is nothing to
// abandon: the request never left (open breaker), the engine refused it (4xx, nothing logged) or
// the caller cancelled.
func abandonReason(err error) AbandonReason {
	var pe *Error
	if !errors.As(err, &pe) || pe.notSent {
		return ""
	}
	switch {
	case pe.Code == CodeTimeout:
		return AbandonTimeout
	case pe.Code == CodeNetwork:
		return AbandonNetwork
	case pe.Status >= 500:
		return AbandonServerError
	case pe.Code == CodeBadResponse:
		return AbandonBadResponse
	}
	return ""
}

func newError(code, op, format string, args ...any) *Error {
	return &Error{Code: code, Op: op, Message: fmt.Sprintf(format, args...)}
}
