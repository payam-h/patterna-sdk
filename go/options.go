package patterna

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
)

// Headers the SDK sends. X-Request-Id and X-Trace-Id go on every call; X-Deadline-Ms and
// X-Patterna-Tester only on Recommend.
const (
	HeaderRequestID = "X-Request-Id"
	HeaderTraceID   = "X-Trace-Id"
	HeaderDeadline  = "X-Deadline-Ms"
	HeaderTester    = "X-Patterna-Tester"

	maxHeaderID = 64 // the engine replaces a longer X-Request-Id with its own
)

// CallOption changes one call. Pass it as the last argument of any method.
type CallOption func(*callOpts)

type callOpts struct {
	requestID string
	traceID   string
	tester    bool
}

func collect(opts []CallOption) callOpts {
	var o callOpts
	for _, f := range opts {
		if f != nil {
			f(&o)
		}
	}
	return o
}

// WithTester marks a Recommend call as coming from a tester (X-Patterna-Tester: 1), so a surface
// in status "testers" serves it. Your backend decides who is a tester (engine 0.2).
func WithTester() CallOption { return func(o *callOpts) { o.tester = true } }

// WithRequestID sends id as X-Request-Id instead of a freshly minted one. The engine uses it as
// the slate's request id. Ids longer than 64 characters are ignored and a new one is minted.
func WithRequestID(id string) CallOption {
	return func(o *callOpts) { o.requestID = cleanHeaderID(id) }
}

// WithTraceID sends id as X-Trace-Id on this call, overriding the context and Config.TraceID.
func WithTraceID(id string) CallOption { return func(o *callOpts) { o.traceID = cleanHeaderID(id) } }

type traceKey struct{}

// ContextWithTraceID returns a context whose calls carry id as X-Trace-Id (for example Elvou's
// Sentry trace id), so a Patterna log line can be joined to your own.
func ContextWithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceKey{}, cleanHeaderID(id))
}

// TraceIDFromContext returns the id set with ContextWithTraceID, or "".
func TraceIDFromContext(ctx context.Context) string {
	s, _ := ctx.Value(traceKey{}).(string)
	return s
}

// traceID resolves the trace id of a call: option, then context, then Config.TraceID.
func (c *Client) traceID(ctx context.Context, o callOpts) string {
	if o.traceID != "" {
		return o.traceID
	}
	if s := TraceIDFromContext(ctx); s != "" {
		return s
	}
	if c.cfg.TraceID != nil {
		return cleanHeaderID(c.cfg.TraceID(ctx))
	}
	return ""
}

// NewRequestID returns a random UUIDv4 string, the form the engine mints itself.
func NewRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error on supported platforms
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// cleanHeaderID trims an id and drops it when it cannot travel as a header value.
func cleanHeaderID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > maxHeaderID {
		return ""
	}
	for _, r := range id {
		if r < 0x21 || r > 0x7e {
			return ""
		}
	}
	return id
}
