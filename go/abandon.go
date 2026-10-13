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
func (c *Client) Abandon(requestID string, reason AbandonReason, opts ...CallOption) error {
	const op = "abandon"
	if requestID == "" {
		return newError(CodeInvalidArgument, op, "request id is required")
	}
	if reason == "" {
		reason = AbandonClientFallback
	}
	o := collect(opts)
	return c.enqueue(op, reportJob{requestID: requestID, abandon: reason, traceID: o.traceID})
}

// AbandonSync is Abandon that waits for the answer. A request the engine never logged answers
// ErrUnknownRequest.
func (c *Client) AbandonSync(ctx context.Context, requestID string, reason AbandonReason, opts ...CallOption) error {
	if requestID == "" {
		return newError(CodeInvalidArgument, "abandon", "request id is required")
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
