package patterna

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultRecommendTimeout is the serve budget the customer's backend waits before falling back.
	DefaultRecommendTimeout = 150 * time.Millisecond
	// DefaultRecommendDeadline is the deadline the engine is asked to answer within
	// (X-Deadline-Ms). It sits below DefaultRecommendTimeout so the answer has time to travel back.
	DefaultRecommendDeadline = 120 * time.Millisecond
	// DefaultIngestTimeout bounds every other call, retry included.
	DefaultIngestTimeout = 5 * time.Second
	// DefaultAdminTimeout bounds admin calls (flows, surfaces, experiments, ledger).
	DefaultAdminTimeout = 30 * time.Second

	minServerDeadline = 10 * time.Millisecond

	defaultBreakerThreshold = 5
	defaultBreakerCooldown  = 30 * time.Second
	defaultReportWorkers    = 2
	defaultReportQueue      = 1024
	defaultRetryDelay       = 100 * time.Millisecond

	maxInteractions = 50
	maxEvents       = 500
	maxItems        = 500
	maxExcludes     = 200
	maxControlItems = 50
	maxMemberOf     = 500
	maxBlockLen     = 64
	maxErrorBody    = 64 << 10
)

// Config configures a Client. Only BaseURL and the keys for the scopes you call are required.
type Config struct {
	// BaseURL, e.g. https://api.patterna.ainur.dev/v1.
	BaseURL string
	// Scoped API keys. Read them from the environment; never hard-code them.
	ServeKey  string
	IngestKey string
	// AdminKey is used for SetUserMode, SetUserProfile and DeleteUser when set; otherwise those
	// calls use IngestKey (see docs/ISSUES.md on the scope of /users/*). The admin package and
	// CreateSigningSecret need it.
	AdminKey string

	// HTTPClient defaults to a client with a pooled transport and no client-level timeout; the
	// per-call context deadline is the timeout.
	HTTPClient *http.Client

	RecommendTimeout time.Duration // default 150 ms
	// RecommendDeadline is sent as X-Deadline-Ms: the engine answers within it or serves its
	// fallback. Default 120 ms, or 4/5 of RecommendTimeout when that is shorter; it must be below
	// RecommendTimeout. A shorter caller deadline shrinks it by the same margin.
	RecommendDeadline time.Duration
	IngestTimeout     time.Duration // default 5 s
	AdminTimeout      time.Duration // default 30 s
	RetryDelay        time.Duration // pause before the single retry; default 100 ms

	// TraceID returns the caller's trace id for a call (sent as X-Trace-Id), for hosts whose
	// tracing lives in the context (Sentry, OpenTelemetry). ContextWithTraceID and WithTraceID
	// take precedence.
	TraceID func(ctx context.Context) string

	// SlateMemory is how many recent slates the client remembers to check interaction kinds
	// before sending (see ReportInteractions). Default 4096; negative turns it off.
	SlateMemory int

	BreakerThreshold int           // consecutive failures that open the breaker; default 5
	BreakerCooldown  time.Duration // how long it stays open; default 30 s
	// OnBreakerChange is called when a scope's breaker opens or closes ("serve", "ingest" or
	// "admin").
	OnBreakerChange func(scope string, open bool)

	ReportWorkers   int // goroutines sending ReportInteractions and Abandon; default 2
	ReportQueueSize int // pending reports before ReportInteractions returns ErrQueueFull; default 1024

	// Logger receives one JSON-friendly line per background failure (op, request_id, code).
	// Defaults to discarding.
	Logger *slog.Logger

	now func() time.Time // tests
}

// Client is safe for concurrent use. Create one per process and Close it on shutdown.
type Client struct {
	cfg     Config
	base    string
	http    *http.Client
	log     *slog.Logger
	serveCB *breaker
	ingCB   *breaker
	admCB   *breaker
	kinds   *slateKinds

	reports chan reportJob
	wg      sync.WaitGroup
	mu      sync.RWMutex
	closed  bool
	dropped atomic.Int64
}

type reportJob struct {
	requestID    string
	interactions []Interaction
	abandon      AbandonReason // set for an abandonment instead of interactions
	traceID      string
}

// New validates cfg and starts the interaction workers.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, newError(CodeNotConfigured, "new", "BaseURL is required")
	}
	if _, err := url.Parse(cfg.BaseURL); err != nil {
		return nil, &Error{Code: CodeNotConfigured, Op: "new", Message: "BaseURL is not a URL", Err: err}
	}
	if cfg.RecommendTimeout <= 0 {
		cfg.RecommendTimeout = DefaultRecommendTimeout
	}
	if cfg.RecommendDeadline <= 0 {
		cfg.RecommendDeadline = min(DefaultRecommendDeadline, cfg.RecommendTimeout*4/5)
	}
	if cfg.RecommendDeadline >= cfg.RecommendTimeout {
		return nil, newError(CodeNotConfigured, "new", "RecommendDeadline must be below RecommendTimeout")
	}
	if cfg.IngestTimeout <= 0 {
		cfg.IngestTimeout = DefaultIngestTimeout
	}
	if cfg.AdminTimeout <= 0 {
		cfg.AdminTimeout = DefaultAdminTimeout
	}
	if cfg.SlateMemory == 0 {
		cfg.SlateMemory = defaultSlateMemory
	}
	if cfg.RetryDelay < 0 {
		cfg.RetryDelay = 0
	} else if cfg.RetryDelay == 0 {
		cfg.RetryDelay = defaultRetryDelay
	}
	if cfg.BreakerThreshold <= 0 {
		cfg.BreakerThreshold = defaultBreakerThreshold
	}
	if cfg.BreakerCooldown <= 0 {
		cfg.BreakerCooldown = defaultBreakerCooldown
	}
	if cfg.ReportWorkers <= 0 {
		cfg.ReportWorkers = defaultReportWorkers
	}
	if cfg.ReportQueueSize <= 0 {
		cfg.ReportQueueSize = defaultReportQueue
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Transport: defaultTransport()}
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	notify := func(scope string) func(bool) {
		return func(open bool) {
			log.Warn("patterna breaker", "op", "breaker", "scope", scope, "open", open)
			if cfg.OnBreakerChange != nil {
				cfg.OnBreakerChange(scope, open)
			}
		}
	}
	c := &Client{
		cfg:     cfg,
		base:    strings.TrimRight(cfg.BaseURL, "/"),
		http:    hc,
		log:     log,
		serveCB: newBreaker(cfg.BreakerThreshold, cfg.BreakerCooldown, cfg.now, notify("serve")),
		ingCB:   newBreaker(cfg.BreakerThreshold, cfg.BreakerCooldown, cfg.now, notify("ingest")),
		admCB:   newBreaker(cfg.BreakerThreshold, cfg.BreakerCooldown, cfg.now, notify("admin")),
		kinds:   newSlateKinds(cfg.SlateMemory),
		reports: make(chan reportJob, cfg.ReportQueueSize),
	}
	for range cfg.ReportWorkers {
		c.wg.Add(1)
		go c.reportWorker()
	}
	return c, nil
}

// BreakerOpen reports whether the breaker for "serve", "ingest" or "admin" is open, for health
// checks.
func (c *Client) BreakerOpen(scope string) bool {
	switch scope {
	case "serve":
		return c.serveCB.isOpen()
	case "admin":
		return c.admCB.isOpen()
	}
	return c.ingCB.isOpen()
}

// DroppedReports counts ReportInteractions and Abandon calls dropped because the queue was full
// or a send failed for good.
func (c *Client) DroppedReports() int64 { return c.dropped.Load() }

// Close stops accepting reports and waits for queued ones to be sent or for ctx to end.
func (c *Client) Close(ctx context.Context) error {
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		close(c.reports)
	}
	c.mu.Unlock()
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return &Error{Code: CodeAborted, Op: "close", Message: "pending reports not drained", Err: ctx.Err()}
	}
}

// ---- serve scope ------------------------------------------------------------------------------

// Recommend ranks a slate. It is never retried: every call logs an impression with propensities,
// and a resend would log a slate nobody saw. Prefer RecommendWithFallback in request paths.
//
// The client mints the request id (X-Request-Id) unless WithRequestID gives one, and asks the
// engine to answer within the deadline (X-Deadline-Ms, see Config.RecommendDeadline).
func (c *Client) Recommend(ctx context.Context, req RecommendRequest, opts ...CallOption) (*Slate, error) {
	o := collect(opts)
	if o.requestID == "" {
		o.requestID = NewRequestID()
	}
	return c.recommend(ctx, req, o)
}

func (c *Client) recommend(ctx context.Context, req RecommendRequest, o callOpts) (*Slate, error) {
	if err := validateRecommend(req); err != nil {
		return nil, err
	}
	var out Slate
	err := c.do(ctx, call{
		op: "recommend", method: http.MethodPost, path: "/recommendations",
		body: req, scope: scopeServe, timeout: c.cfg.RecommendTimeout, idempotent: false,
		opts: o, deadlineHeader: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.RequestID == "" {
		out.RequestID = o.requestID
	}
	out.markFills()
	c.kinds.remember(&out)
	return &out, nil
}

// ReportInteractionsSync sends seen, click and dismiss for one slate and waits for the answer.
//
// Each interaction needs Kind unless the item is a document: when this client served the slate
// and the item is a community, product, event or tip, an empty Kind is an ErrInvalidArgument
// before anything is sent. Slate.Interaction fills Kind and Position for you. Set
// DisplayedPosition when the app re-sorted the slate on the device.
func (c *Client) ReportInteractionsSync(ctx context.Context, requestID string, interactions []Interaction, opts ...CallOption) error {
	const op = "reportInteractions"
	if err := c.checkInteractions(op, requestID, interactions); err != nil {
		return err
	}
	if len(interactions) == 0 {
		return nil
	}
	return c.sendInteractions(ctx, requestID, interactions, collect(opts))
}

func (c *Client) sendInteractions(ctx context.Context, requestID string, interactions []Interaction, o callOpts) error {
	return c.do(ctx, call{
		op: "reportInteractions", method: http.MethodPost, path: "/interactions",
		body:  map[string]any{"request_id": requestID, "interactions": interactions},
		scope: scopeServe, timeout: c.cfg.IngestTimeout, idempotent: true, opts: o,
	}, nil)
}

// ReportInteractions queues the report for the background workers and returns at once. It
// returns ErrQueueFull when the bounded queue is full (the report is dropped and counted) and
// ErrClosed after Close. Validation (limits, Kind, positions) happens here, before queueing.
func (c *Client) ReportInteractions(requestID string, interactions []Interaction, opts ...CallOption) error {
	const op = "reportInteractions"
	if err := c.checkInteractions(op, requestID, interactions); err != nil {
		return err
	}
	if len(interactions) == 0 {
		return nil
	}
	o := collect(opts)
	return c.enqueue(op, reportJob{requestID: requestID, interactions: append([]Interaction(nil), interactions...), traceID: o.traceID})
}

func (c *Client) enqueue(op string, job reportJob) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed {
		return newError(CodeClosed, op, "client is closed")
	}
	select {
	case c.reports <- job:
		return nil
	default:
		c.dropped.Add(1)
		return &Error{Code: CodeQueueFull, Op: op, RequestID: job.requestID, Message: "report queue is full; dropped"}
	}
}

func (c *Client) reportWorker() {
	defer c.wg.Done()
	for job := range c.reports {
		// The HTTP request that produced the report is long gone; use a fresh context.
		o := callOpts{traceID: job.traceID}
		op := "reportInteractions"
		var err error
		if job.abandon != "" {
			op = "abandon"
			err = c.sendAbandon(context.Background(), job.requestID, job.abandon, o)
		} else {
			err = c.sendInteractions(context.Background(), job.requestID, job.interactions, o)
		}
		if err != nil {
			c.dropped.Add(1)
			var pe *Error
			code := "unknown"
			if errors.As(err, &pe) {
				code = pe.Code
			}
			c.log.Warn("patterna report dropped", "op", op, "request_id", job.requestID, "code", code)
		}
	}
}

func (c *Client) checkInteractions(op, requestID string, interactions []Interaction) error {
	if requestID == "" {
		return newError(CodeInvalidArgument, op, "request id is required")
	}
	if len(interactions) > maxInteractions {
		return newError(CodeInvalidArgument, op, "at most %d interactions per call", maxInteractions)
	}
	for i, in := range interactions {
		if in.ItemID == "" {
			return newError(CodeInvalidArgument, op, "interaction %d: item id is required", i)
		}
		if in.Position < 0 || (in.DisplayedPosition != nil && *in.DisplayedPosition < 0) {
			return newError(CodeInvalidArgument, op, "interaction %d: positions are 0-based and never negative", i)
		}
		if len(in.Block) > maxBlockLen {
			return newError(CodeInvalidArgument, op, "interaction %d: block is limited to %d characters", i, maxBlockLen)
		}
		if in.Kind != "" {
			if !ItemKind(in.Kind).Valid() {
				return newError(CodeInvalidArgument, op, "interaction %d: unknown kind %q", i, in.Kind)
			}
			continue
		}
		if k := c.kinds.kind(requestID, in.ItemID); k != "" && k != string(KindDocument) {
			return newError(CodeInvalidArgument, op,
				"interaction %d: kind is required for %s items (only documents may leave it empty)", i, k)
		}
	}
	return nil
}

// ---- ingest scope -----------------------------------------------------------------------------

// IngestEvents sends up to 500 canonical events. Idempotent on event_id.
func (c *Client) IngestEvents(ctx context.Context, events []Event, opts ...CallOption) (*IngestResult, error) {
	const op = "ingestEvents"
	if len(events) > maxEvents {
		return nil, newError(CodeInvalidArgument, op, "at most %d events per call", maxEvents)
	}
	for _, e := range events {
		if err := validateEvent(op, e); err != nil {
			return nil, err
		}
	}
	var out IngestResult
	err := c.do(ctx, call{
		op: op, method: http.MethodPost, path: "/events",
		body: map[string]any{"events": events}, scope: scopeIngest, timeout: c.cfg.IngestTimeout, idempotent: true, opts: collect(opts),
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// UpsertItems creates or updates up to 500 catalog items.
func (c *Client) UpsertItems(ctx context.Context, items []ItemUpsert, opts ...CallOption) (*UpsertItemsResult, error) {
	const op = "upsertItems"
	if len(items) > maxItems {
		return nil, newError(CodeInvalidArgument, op, "at most %d items per call", maxItems)
	}
	for i, it := range items {
		if err := validateItem(op, i, it); err != nil {
			return nil, err
		}
	}
	var out UpsertItemsResult
	err := c.do(ctx, call{
		op: op, method: http.MethodPut, path: "/catalog/items",
		body: map[string]any{"items": items}, scope: scopeIngest, timeout: c.cfg.IngestTimeout, idempotent: true, opts: collect(opts),
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// SetItemStatus archives, schedules or publishes an item. publishedAt may be nil.
func (c *Client) SetItemStatus(ctx context.Context, kind ItemKind, externalID string, status ItemStatus, publishedAt *time.Time, opts ...CallOption) error {
	const op = "setItemStatus"
	if externalID == "" {
		return newError(CodeInvalidArgument, op, "external id is required")
	}
	body := map[string]any{"status": status}
	if publishedAt != nil {
		body["published_at"] = publishedAt.UTC().Format(time.RFC3339)
	}
	return c.do(ctx, call{
		op: op, method: http.MethodPost, path: "/catalog/items/" + url.PathEscape(externalID) + "/status",
		query: url.Values{"kind": {string(kind)}}, body: body,
		scope: scopeIngest, timeout: c.cfg.IngestTimeout, idempotent: true, opts: collect(opts),
	}, nil)
}

// SetItemTags replaces the editor-approved tags of an item.
func (c *Client) SetItemTags(ctx context.Context, kind ItemKind, externalID string, tags []ItemTag, opts ...CallOption) error {
	const op = "setItemTags"
	if externalID == "" {
		return newError(CodeInvalidArgument, op, "external id is required")
	}
	if tags == nil {
		tags = []ItemTag{}
	}
	return c.do(ctx, call{
		op: op, method: http.MethodPut, path: "/catalog/items/" + url.PathEscape(externalID) + "/tags",
		query: url.Values{"kind": {string(kind)}}, body: map[string]any{"tags": tags},
		scope: scopeIngest, timeout: c.cfg.IngestTimeout, idempotent: true, opts: collect(opts),
	}, nil)
}

// SetUserMode sets a user's consent mode. non_profiled makes Patterna delete the user row.
func (c *Client) SetUserMode(ctx context.Context, userID string, mode Mode, opts ...CallOption) error {
	const op = "setUserMode"
	if err := CheckUserID(op, userID); err != nil {
		return err
	}
	if err := checkMode(op, mode); err != nil {
		return err
	}
	return c.do(ctx, call{
		op: op, method: http.MethodPut, path: "/users/" + url.PathEscape(userID) + "/mode",
		body: map[string]any{"mode": mode}, scope: scopeUsers, timeout: c.cfg.IngestTimeout, idempotent: true, opts: collect(opts),
	}, nil)
}

// SetUserProfile stores declared attributes as tag affinities. Never health values.
//
// A profile never creates consent (engine 0.3): without profile.Mode, a user Patterna has no row
// for answers ErrModeRequired (422 mode_required); set the mode in the profile or call
// SetUserMode first. An existing user keeps their mode.
func (c *Client) SetUserProfile(ctx context.Context, userID string, profile UserProfile, opts ...CallOption) error {
	const op = "setUserProfile"
	if err := CheckUserID(op, userID); err != nil {
		return err
	}
	if profile.Mode == ModeNonProfiled {
		return newError(CodeInvalidArgument, op, "a non_profiled user has no profile; call SetUserMode")
	}
	return c.do(ctx, call{
		op: op, method: http.MethodPut, path: "/users/" + url.PathEscape(userID) + "/profile",
		body: profile, scope: scopeUsers, timeout: c.cfg.IngestTimeout, idempotent: true, opts: collect(opts),
	}, nil)
}

// DeleteUser deletes every row about the user and schedules retraining.
func (c *Client) DeleteUser(ctx context.Context, userID string, opts ...CallOption) (*DeletionRequest, error) {
	const op = "deleteUser"
	if err := CheckUserID(op, userID); err != nil {
		return nil, err
	}
	var out DeletionRequest
	err := c.do(ctx, call{
		op: op, method: http.MethodDelete, path: "/users/" + url.PathEscape(userID),
		scope: scopeUsers, timeout: c.cfg.IngestTimeout, idempotent: true, opts: collect(opts),
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- transport --------------------------------------------------------------------------------

type scope int

const (
	scopeServe scope = iota
	scopeIngest
	scopeUsers
	scopeAdmin
)

type call struct {
	op         string
	method     string
	path       string
	query      url.Values
	body       any
	scope      scope
	timeout    time.Duration
	idempotent bool
	opts       callOpts
	// deadlineHeader sends X-Deadline-Ms (and X-Patterna-Tester when asked): Recommend only.
	deadlineHeader bool
	// background calls (abandonments) go through the ingest breaker, so the failures that made
	// the client abandon a slate cannot also block the report of it, and never open the serve one.
	background bool
}

func (c *Client) key(s scope) string {
	switch s {
	case scopeServe:
		return c.cfg.ServeKey
	case scopeAdmin:
		return c.cfg.AdminKey
	case scopeUsers:
		if c.cfg.AdminKey != "" {
			return c.cfg.AdminKey
		}
	}
	return c.cfg.IngestKey
}

func (c *Client) breakerFor(s scope) *breaker {
	switch s {
	case scopeServe:
		return c.serveCB
	case scopeAdmin:
		return c.admCB
	}
	return c.ingCB
}

// serverDeadline is the X-Deadline-Ms value for a call with `remaining` time left: the configured
// deadline, shrunk by the same network margin when the caller's own deadline is shorter.
func (c *Client) serverDeadline(remaining time.Duration) time.Duration {
	margin := c.cfg.RecommendTimeout - c.cfg.RecommendDeadline
	d := min(c.cfg.RecommendDeadline, remaining-margin)
	if d < minServerDeadline {
		d = max(remaining/2, time.Millisecond)
	}
	return d
}

// do runs one call under its deadline (the earlier of ctx's and the call's budget), with one
// retry for idempotent calls on 5xx and network errors, through the scope's breaker.
func (c *Client) do(ctx context.Context, cl call, out any) error {
	key := c.key(cl.scope)
	if key == "" {
		return newError(CodeNotConfigured, cl.op, "no API key configured for this scope")
	}
	var payload []byte
	if cl.body != nil {
		var err error
		if payload, err = json.Marshal(cl.body); err != nil {
			return &Error{Code: CodeInvalidArgument, Op: cl.op, Message: "body does not encode as JSON", Err: err}
		}
	}
	target := c.base + cl.path
	if len(cl.query) > 0 {
		target += "?" + cl.query.Encode()
	}

	ctx, cancel := context.WithTimeout(ctx, cl.timeout)
	defer cancel()
	// Every call carries a request id the caller can log, even when the engine never answers.
	if cl.opts.requestID == "" {
		cl.opts.requestID = NewRequestID()
	}
	cl.opts.traceID = c.traceID(ctx, cl.opts)

	cb := c.breakerFor(cl.scope)
	if cl.background {
		cb = c.ingCB
	}
	attempts := 1
	if cl.idempotent {
		attempts = 2
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			t := time.NewTimer(c.cfg.RetryDelay)
			select {
			case <-ctx.Done():
				t.Stop()
				e := c.ctxError(ctx, cl.op, lastErr)
				e.RequestID = cl.opts.requestID
				return e
			case <-t.C:
			}
		}
		if !cb.allow() {
			e := newError(CodeCircuitOpen, cl.op, "circuit open after repeated failures")
			e.RequestID = cl.opts.requestID
			e.notSent = true
			return e
		}
		err := c.once(ctx, cl, target, key, payload, out)
		cb.record(outcomeOf(err))
		if err == nil {
			return nil
		}
		lastErr = err
		var pe *Error
		if !errors.As(err, &pe) || !(pe.Code == CodeNetwork || pe.Status >= 500) {
			return err
		}
	}
	return lastErr
}

func outcomeOf(err error) callOutcome {
	if err == nil {
		return outcomeSuccess
	}
	var pe *Error
	if !errors.As(err, &pe) {
		return outcomeFailure
	}
	switch {
	case pe.Code == CodeAborted:
		return outcomeNeutral
	case pe.Transient():
		return outcomeFailure
	default:
		// A 4xx means the service is up and answering.
		return outcomeSuccess
	}
}

func (c *Client) once(ctx context.Context, cl call, target, key string, payload []byte, out any) error {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, cl.method, target, body)
	if err != nil {
		return &Error{Code: CodeInvalidArgument, Op: cl.op, Message: "cannot build request", Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(HeaderRequestID, cl.opts.requestID)
	if cl.opts.traceID != "" {
		req.Header.Set(HeaderTraceID, cl.opts.traceID)
	}
	if cl.deadlineHeader {
		if dl, ok := ctx.Deadline(); ok {
			ms := c.serverDeadline(time.Until(dl)).Milliseconds()
			req.Header.Set(HeaderDeadline, strconv.FormatInt(max(ms, 1), 10))
		}
		if cl.opts.tester {
			req.Header.Set(HeaderTester, "1")
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			e := c.ctxError(ctx, cl.op, err)
			e.RequestID = cl.opts.requestID
			return e
		}
		return &Error{Code: CodeNetwork, Op: cl.op, RequestID: cl.opts.requestID, Message: "request failed", Err: err}
	}
	defer resp.Body.Close()
	requestID := resp.Header.Get(HeaderRequestID)
	if requestID == "" {
		requestID = cl.opts.requestID
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		var eb apiErrorBody
		_ = json.Unmarshal(raw, &eb)
		pe := &Error{Code: eb.Code, Op: cl.op, Status: resp.StatusCode, RequestID: eb.RequestID, Message: eb.Message, Details: eb.Details}
		if pe.Code == "" {
			pe.Code = "http_" + strconv.Itoa(resp.StatusCode)
		}
		if pe.RequestID == "" {
			pe.RequestID = requestID
		}
		return pe
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return nil // 202 with an empty body
		}
		if ctx.Err() != nil {
			e := c.ctxError(ctx, cl.op, err)
			e.RequestID = requestID
			return e
		}
		return &Error{Code: CodeBadResponse, Op: cl.op, Status: resp.StatusCode, RequestID: requestID, Message: "response is not the expected JSON", Err: err}
	}
	return nil
}

// defaultTransport keeps whatever the host installed as http.DefaultTransport (tracing wrappers
// such as Sentry's replace it with a RoundTripper that is not an *http.Transport) and only tunes
// the pool when it is a plain transport: the default of 2 idle connections per host makes
// concurrent Recommend calls pay a TLS handshake inside a 150 ms budget.
func defaultTransport() http.RoundTripper {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultTransport
	}
	t = t.Clone()
	t.MaxIdleConnsPerHost = 32
	return t
}

// ctxError tells a deadline (ours or the caller's) from a caller cancellation.
func (c *Client) ctxError(ctx context.Context, op string, cause error) *Error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &Error{Code: CodeTimeout, Op: op, Message: "deadline exceeded", Err: cause}
	}
	return &Error{Code: CodeAborted, Op: op, Message: "cancelled by caller", Err: cause}
}
