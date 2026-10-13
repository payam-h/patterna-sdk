package patterna

import (
	"context"
	"sync"
)

// ManyRequest is one surface in RecommendMany.
type ManyRequest struct {
	Request  RecommendRequest
	Fallback func() []string
}

// RecommendMany fetches several surfaces at once (Home asks for articles and products together).
// Each request runs as RecommendWithFallback in its own goroutine, all under one deadline: the
// client's RecommendTimeout counted from this call, or ctx's when that is earlier. So two
// surfaces cost one budget, not two, and each one falls back (and is abandoned) on its own.
//
// Results are in the order of reqs. The error is the first caller bug (consent, invalid input);
// the other surfaces still carry their results.
//
// Every request is its own impression with its own request id; WithRequestID is ignored here.
// Prefer this over one surface with mixed candidate_kinds when the placements are separate on
// screen: each keeps its own ledger row.
func (c *Client) RecommendMany(ctx context.Context, reqs []ManyRequest, opts ...CallOption) ([]Recommendation, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.RecommendTimeout)
	defer cancel()
	base := collect(opts)
	base.requestID = ""
	out := make([]Recommendation, len(reqs))
	errs := make([]error, len(reqs))
	var wg sync.WaitGroup
	for i, r := range reqs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o := base
			out[i], errs[i] = c.RecommendWithFallback(ctx, r.Request, r.Fallback, func(co *callOpts) { *co = o })
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return out, err
		}
	}
	return out, nil
}
