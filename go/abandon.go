package patterna

import (
	"context"
	"net/http"
	"net/url"
)

// AbandonReason says why the client gave up on a slate it asked for.
type AbandonReason string

const (
	AbandonTimeout     AbandonReason = "timeout"      // the budget ran out before the answer arrived
	AbandonServerError AbandonReason = "server_error" // the engine answered 5xx
	AbandonNetwork     AbandonReason = "network_error"
	AbandonBadResponse AbandonReason = "bad_response" // the answer was not a slate
	// AbandonClientFallback: the slate arrived but the app showed its own list anyway.
	AbandonClientFallback AbandonReason = "client_fallback"
)

// Abandon tells Patterna (engine 0.2: POST /v1/recommendations/{request_id}/abandon) that the
// slate of requestID was not shown, so the impression is flagged client_fallback and counted in
// the fallback rate instead of as a slate with zero taps. Fire-and-forget on the interaction
// queue; ErrQueueFull and ErrClosed as for ReportInteractions.
//
// RecommendWithFallback calls it for you on a timeout, network error or 5xx. Call it yourself when
// you received a slate and still showed something else.
//
// A slate answered shown=false (Slate.Visible false) is never abandoned: it already counts in its
// arm with zero taps. When this client served it, Abandon returns ErrNotShown without sending;
// otherwise the engine answers 409 not_shown (AbandonSync returns it, Abandon drops it).
func (c *Client) Abandon(requestID string, reason AbandonReason, opts ...CallOption) error {
	const op = "abandon"
	if requestID == "" {
		return newError(CodeInvalidArgument, op, "request id is required")
	}
	if c.kinds.notShown(requestID) {
		return notShownError(op, requestID)
	}
	if reason == "" {
		reason = AbandonClientFallback
	}
	o := collect(opts)
	return c.enqueue(op, reportJob{requestID: requestID, abandon: reason, traceID: o.traceID})
}

// AbandonSync is Abandon that waits for the answer. A request the engine never logged answers
// ErrUnknownRequest; a slate answered shown=false answers ErrNotShown.
func (c *Client) AbandonSync(ctx context.Context, requestID string, reason AbandonReason, opts ...CallOption) error {
	if requestID == "" {
		return newError(CodeInvalidArgument, "abandon", "request id is required")
	}
	if c.kinds.notShown(requestID) {
		return notShownError("abandon", requestID)
	}
	if reason == "" {
		reason = AbandonClientFallback
	}
	o := collect(opts)
	if o.traceID == "" {
		o.traceID = c.traceID(ctx, o)
	}
	return c.sendAbandon(ctx, requestID, reason, o)
}

func (c *Client) sendAbandon(ctx context.Context, requestID string, reason AbandonReason, o callOpts) error {
	return c.do(ctx, call{
		op: "abandon", method: http.MethodPost, path: "/recommendations/" + url.PathEscape(requestID) + "/abandon",
		body: map[string]any{"reason": reason}, scope: scopeServe, timeout: c.cfg.IngestTimeout, idempotent: true, opts: o, background: true,
	}, nil)
}

func notShownError(op, requestID string) *Error {
	return &Error{Code: CodeNotShown, Op: op, RequestID: requestID,
		Message: "the slate was answered shown=false: hide the block, do not abandon it", notSent: true}
}
