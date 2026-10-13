package patterna

import "context"

// Recommendation is what RecommendWithFallback returns: either a Patterna slate or the
// customer's own list.
type Recommendation struct {
	// ItemIDs are the external ids to show, in order. Always set.
	ItemIDs []string
	// Slate is the Patterna answer; nil when the fallback was used. Report interactions only for
	// a non-nil Slate.
	Slate *Slate
	// RequestID is the id the client minted and sent as X-Request-Id; equal to Slate.RequestID
	// when Patterna answered. Log it next to your own trace.
	RequestID string
	// Fallback is true when ItemIDs came from the fallback function.
	Fallback bool
	// Abandoned is true when the fallback was used after the request reached the engine (timeout,
	// network error, 5xx) and the abandonment was queued, so Patterna does not count a slate
	// nobody saw.
	Abandoned bool
	// Cause says why the fallback was used (timeout, circuit open, API error).
	Cause error
}

// Reason returns the slot's reason text in locale, falling back to Dutch, or "" when the item
// came from the fallback list.
func (r Recommendation) Reason(itemID, locale string) string {
	if r.Slate == nil {
		return ""
	}
	for _, it := range r.Slate.Items {
		if it.ItemID == itemID {
			if s, ok := it.Reason[locale]; ok {
				return s
			}
			return it.Reason["nl"]
		}
	}
	return ""
}

// RecommendWithFallback calls Recommend and, when Patterna cannot answer within the budget, is
// unavailable, or the breaker is open, returns fallback() instead. The error is non-nil only for
// caller bugs (consent violations, invalid input, missing key, pii_rejected, mode_conflict):
// those are never hidden behind a fallback, because a consent bug is a privacy bug.
//
// The request id is minted here, so on a timeout, network error or 5xx the client reports the
// abandonment in the background (see Abandon) and Recommendation.RequestID still names the
// request. An open breaker sends nothing, so there is nothing to abandon.
func (c *Client) RecommendWithFallback(ctx context.Context, req RecommendRequest, fallback func() []string, opts ...CallOption) (Recommendation, error) {
	o := collect(opts)
	if o.requestID == "" {
		o.requestID = NewRequestID()
	}
	o.traceID = c.traceID(ctx, o)
	slate, err := c.recommend(ctx, req, o)
	if err == nil {
		return Recommendation{ItemIDs: slate.ItemIDs(), Slate: slate, RequestID: slate.RequestID}, nil
	}
	if isCallerBug(err) {
		return Recommendation{RequestID: o.requestID}, err
	}
	rec := Recommendation{Fallback: true, Cause: err, RequestID: o.requestID}
	if reason := abandonReason(err); reason != "" {
		rec.Abandoned = c.enqueue("abandon", reportJob{requestID: o.requestID, abandon: reason, traceID: o.traceID}) == nil
	}
	if fallback != nil {
		rec.ItemIDs = fallback()
	}
	return rec, nil
}
