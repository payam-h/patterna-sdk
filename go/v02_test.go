package patterna

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// Cross-language vector, shared with packages/sdk-ts/test/assignment.test.ts and
// packages/sdk-py/tests/test_v02.py.
const (
	assignmentKey    = "test-assignment-key" // a fixture string, not a secret
	assignmentUser   = "u_7f3a"
	assignmentVector = "fd548dfa1627af2ab350f8cebe237014d29004eb6dfb8cfa79cb9c24adbdda42"
)

func TestAssignmentUnit(t *testing.T) {
	if got := AssignmentUnit(assignmentKey, assignmentUser); got != assignmentVector {
		t.Fatalf("got %s", got)
	}
	r := newRecorder(t, reply{body: slateJSON})
	c := r.client(t)
	req := personalized()
	req.AssignmentUnit = AssignmentUnit(assignmentKey, assignmentUser)
	if _, err := c.Recommend(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if r.requests()[0].Body["assignment_unit"] != assignmentVector {
		t.Errorf("body = %v", r.requests()[0].Body)
	}
	for _, bad := range []string{"u_7f3a", strings.ToUpper(assignmentVector), assignmentVector[:63]} {
		req.AssignmentUnit = bad
		if _, err := c.Recommend(t.Context(), req); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
	if n := len(r.requests()); n != 1 {
		t.Errorf("requests = %d", n)
	}
}

func TestRequestHeaders(t *testing.T) {
	t.Run("recommend mints a request id, sends the deadline, no tester by default", func(t *testing.T) {
		r := newRecorder(t, reply{body: `{"request_id":"","surface":"s","arm":"none","policy_version":"p","items":[]}`})
		s, err := r.client(t).Recommend(t.Context(), personalized())
		if err != nil {
			t.Fatal(err)
		}
		h := r.requests()[0].Header
		if !uuidV4.MatchString(h.Get(HeaderRequestID)) {
			t.Errorf("request id = %q", h.Get(HeaderRequestID))
		}
		if s.RequestID != h.Get(HeaderRequestID) {
			t.Errorf("slate request id %q not filled from the sent one", s.RequestID)
		}
		if ms, _ := strconv.Atoi(h.Get(HeaderDeadline)); ms < 110 || ms > 120 {
			t.Errorf("deadline = %q, want about 120", h.Get(HeaderDeadline))
		}
		if h.Get(HeaderTester) != "" || h.Get(HeaderTraceID) != "" {
			t.Errorf("unexpected headers %v", h)
		}
	})
	t.Run("options: request id, tester, trace id", func(t *testing.T) {
		r := newRecorder(t, reply{body: slateJSON})
		_, err := r.client(t).Recommend(t.Context(), personalized(), WithRequestID("req_mine"), WithTester(), WithTraceID("trace-1"))
		if err != nil {
			t.Fatal(err)
		}
		h := r.requests()[0].Header
		if h.Get(HeaderRequestID) != "req_mine" || h.Get(HeaderTester) != "1" || h.Get(HeaderTraceID) != "trace-1" {
			t.Errorf("headers = %v", h)
		}
	})
	t.Run("a shorter caller deadline shrinks X-Deadline-Ms by the same margin", func(t *testing.T) {
		r := newRecorder(t, reply{body: slateJSON})
		ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
		defer cancel()
		if _, err := r.client(t).Recommend(ctx, personalized()); err != nil {
			t.Fatal(err)
		}
		if ms, _ := strconv.Atoi(r.requests()[0].Header.Get(HeaderDeadline)); ms < 40 || ms > 50 {
			t.Errorf("deadline = %d, want about 50", ms)
		}
	})
	t.Run("trace id from the context and from Config.TraceID on every call", func(t *testing.T) {
		r := newRecorder(t, reply{status: 202, body: `{}`})
		c := r.client(t, func(cfg *Config) {
			cfg.TraceID = func(context.Context) string { return "from-config" }
		})
		if _, err := c.IngestEvents(ContextWithTraceID(t.Context(), "from-ctx"), []Event{ev("e1")}); err != nil {
			t.Fatal(err)
		}
		if err := c.DeleteItem(t.Context(), KindDocument, "d1"); err != nil {
			t.Fatal(err)
		}
		reqs := r.requests()
		if reqs[0].Header.Get(HeaderTraceID) != "from-ctx" || reqs[1].Header.Get(HeaderTraceID) != "from-config" {
			t.Errorf("trace ids = %q, %q", reqs[0].Header.Get(HeaderTraceID), reqs[1].Header.Get(HeaderTraceID))
		}
		if reqs[0].Header.Get(HeaderDeadline) != "" || reqs[0].Header.Get(HeaderRequestID) == "" {
			t.Errorf("ingest headers = %v", reqs[0].Header)
		}
	})
	t.Run("ids that cannot travel as a header are dropped", func(t *testing.T) {
		if cleanHeaderID("a b") != "" || cleanHeaderID(strings.Repeat("x", 65)) != "" || cleanHeaderID(" ok ") != "ok" {
			t.Error("cleanHeaderID")
		}
	})
	t.Run("a timeout error carries the sent request id", func(t *testing.T) {
		r := newRecorder(t, reply{hang: true})
		_, err := r.client(t).Recommend(t.Context(), personalized(), WithRequestID("req_t"))
		var pe *Error
		if !errors.As(err, &pe) || pe.Code != CodeTimeout || pe.RequestID != "req_t" {
			t.Errorf("err = %#v", err)
		}
	})
}

func TestNewDeadlineConfig(t *testing.T) {
	if _, err := New(Config{BaseURL: "http://x", RecommendTimeout: 100 * time.Millisecond, RecommendDeadline: 100 * time.Millisecond}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("err = %v", err)
	}
	c, err := New(Config{BaseURL: "http://x", RecommendTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(t.Context())
	if c.cfg.RecommendDeadline != 80*time.Millisecond {
		t.Errorf("default deadline = %v", c.cfg.RecommendDeadline)
	}
}

func TestFallbackAbandons(t *testing.T) {
	abandonOK := func(path string) (reply, bool) {
		if strings.HasSuffix(path, "/abandon") {
			return reply{status: 202}, true
		}
		return reply{}, false
	}
	mine := func() []string { return []string{"own_1"} }
	tests := []struct {
		name        string
		reply       reply
		openBreaker bool
		wantErr     error
		wantReason  string // "" = no abandon
		wantCause   error
	}{
		{"timeout", reply{hang: true}, false, nil, "timeout", ErrTimeout},
		{"5xx", reply{status: 503, body: `{"code":"unavailable"}`}, false, nil, "server_error", nil},
		{"dropped connection", reply{drop: true}, false, nil, "network_error", ErrNetwork},
		{"open breaker sends nothing, abandons nothing", reply{status: 500}, true, nil, "", ErrCircuitOpen},
		{"no_candidates falls back without abandon", reply{status: 422, body: `{"code":"no_candidates"}`}, false, nil, "", ErrNoCandidates},
		{"surface_off falls back", reply{status: 409, body: `{"code":"surface_off"}`}, false, nil, "", ErrSurfaceOff},
		{"unknown_surface falls back", reply{status: 404, body: `{"code":"unknown_surface"}`}, false, nil, "", ErrUnknownSurface},
		{"mode_conflict is returned", reply{status: 422, body: `{"code":"mode_conflict"}`}, false, ErrModeConflict, "", nil},
		{"pii_rejected is returned", reply{status: 422, body: `{"code":"pii_rejected"}`}, false, ErrPIIRejected, "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRoutedRecorder(t, abandonOK, tt.reply)
			c := r.client(t, func(cfg *Config) { cfg.BreakerThreshold = 1 })
			if tt.openBreaker {
				_, _ = c.Recommend(t.Context(), personalized())
				if !c.BreakerOpen("serve") {
					t.Fatal("setup: breaker not open")
				}
			}
			before := len(r.requests())
			rec, err := c.RecommendWithFallback(t.Context(), personalized(), mine, WithTraceID("tr"))
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) || rec.RequestID == "" {
					t.Fatalf("err = %v, rec = %+v", err, rec)
				}
				return
			}
			if err != nil || !rec.Fallback || !slices.Equal(rec.ItemIDs, []string{"own_1"}) {
				t.Fatalf("rec = %+v, err = %v", rec, err)
			}
			if tt.wantCause != nil && !errors.Is(rec.Cause, tt.wantCause) {
				t.Errorf("cause = %v", rec.Cause)
			}
			if err := c.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			ab := r.requestsTo("/abandon")
			if tt.wantReason == "" {
				if len(ab) != 0 || rec.Abandoned {
					t.Fatalf("abandoned %d times", len(ab))
				}
				if tt.openBreaker && len(r.requests()) != before {
					t.Error("open breaker sent a request")
				}
				return
			}
			if len(ab) != 1 || !rec.Abandoned {
				t.Fatalf("abandon requests = %d, rec = %+v", len(ab), rec)
			}
			sent := r.requests()[before].Header.Get(HeaderRequestID)
			if rec.RequestID != sent || ab[0].Path != "/v1/recommendations/"+sent+"/abandon" {
				t.Errorf("abandon path %s, sent id %s, rec id %s", ab[0].Path, sent, rec.RequestID)
			}
			if ab[0].Body["reason"] != tt.wantReason || ab[0].Auth != "Bearer "+testServeKey || ab[0].Header.Get(HeaderTraceID) != "tr" {
				t.Errorf("abandon = %+v", ab[0])
			}
		})
	}
}

func TestAbandonManual(t *testing.T) {
	r := newRecorder(t, reply{status: 404, body: `{"code":"unknown_request"}`})
	c := r.client(t)
	if err := c.AbandonSync(t.Context(), "req_9", ""); !errors.Is(err, ErrUnknownRequest) {
		t.Fatalf("err = %v", err)
	}
	if got := r.requests()[0]; got.Path != "/v1/recommendations/req_9/abandon" || got.Body["reason"] != "client_fallback" {
		t.Errorf("got %+v", got)
	}
	if err := c.Abandon("", AbandonTimeout); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("err = %v", err)
	}
	if err := c.Abandon("req_9", AbandonClientFallback); err != nil {
		t.Fatal(err)
	}
	_ = c.Close(t.Context())
	if c.DroppedReports() != 1 {
		t.Errorf("dropped = %d (the 404 counts)", c.DroppedReports())
	}
}

const mixedSlateJSON = `{"request_id":"req_mix","surface":"home","arm":"none","policy_version":"p","items":[
{"item_id":"doc_1","kind":"document","position":0,"reason_key":"new","reason":{}},
{"item_id":"com_1","kind":"community","position":1,"reason_key":"new","reason":{}}]}`

func TestInteractionValidation(t *testing.T) {
	r := newRecorder(t, reply{body: mixedSlateJSON})
	c := r.client(t)
	s, err := c.Recommend(t.Context(), personalized())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	neg, two := -1, 2
	tests := []struct {
		name string
		in   Interaction
		ok   bool
	}{
		{"document without kind", Interaction{ItemID: "doc_1", Type: InteractionClick, OccurredAt: now}, true},
		{"community without kind", Interaction{ItemID: "com_1", Position: 1, Type: InteractionClick, OccurredAt: now}, false},
		{"community with kind", Interaction{ItemID: "com_1", Kind: "community", Position: 1, Type: InteractionClick, OccurredAt: now}, true},
		{"unknown kind", Interaction{ItemID: "com_1", Kind: "group", Type: InteractionClick, OccurredAt: now}, false},
		{"displayed position and block", Interaction{ItemID: "doc_1", Kind: "document", DisplayedPosition: &two, Block: "end_carousel", Type: InteractionSeen, OccurredAt: now}, true},
		{"negative displayed position", Interaction{ItemID: "doc_1", DisplayedPosition: &neg, Type: InteractionSeen, OccurredAt: now}, false},
		{"block too long", Interaction{ItemID: "doc_1", Block: strings.Repeat("b", 65), Type: InteractionSeen, OccurredAt: now}, false},
		{"missing item id", Interaction{Type: InteractionSeen, OccurredAt: now}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := c.ReportInteractions(s.RequestID, []Interaction{tt.in})
			if tt.ok != (err == nil) {
				t.Errorf("err = %v", err)
			}
			if !tt.ok && !errors.Is(err, ErrInvalidArgument) {
				t.Errorf("err = %v", err)
			}
		})
	}
	in, ok := s.Interaction("com_1", InteractionClick, now)
	if !ok || in.Kind != "community" || in.Position != 1 {
		t.Errorf("Slate.Interaction = %+v", in)
	}
	if _, ok := s.Interaction("missing", InteractionClick, now); ok {
		t.Error("missing item found")
	}
	// Another process's slate is unknown here: an empty kind passes and the engine decides.
	if err := c.ReportInteractions("req_other", []Interaction{{ItemID: "com_1", Type: InteractionSeen, OccurredAt: now}}); err != nil {
		t.Error(err)
	}
	_ = c.Close(t.Context())
	var body map[string]any
	for _, q := range r.requestsTo("/interactions") {
		ins := q.Body["interactions"].([]any)
		if m := ins[0].(map[string]any); m["block"] == "end_carousel" {
			body = m
		}
	}
	if body == nil || body["displayed_position"] != float64(2) || body["kind"] != "document" {
		t.Errorf("sent = %v", body)
	}
}

func TestSlateKindsBounded(t *testing.T) {
	k := newSlateKinds(2)
	for _, id := range []string{"a", "b", "c"} {
		k.remember(&Slate{RequestID: id, Items: []SlateItem{{ItemID: "x", Kind: "product"}}})
	}
	if k.kind("a", "x") != "" || k.kind("c", "x") != "product" || len(k.m) != 2 {
		t.Errorf("m = %v", k.m)
	}
	var off *slateKinds = newSlateKinds(-1)
	off.remember(&Slate{RequestID: "a"})
	if off.kind("a", "x") != "" {
		t.Error("disabled memory remembered")
	}
}

func TestMeasured(t *testing.T) {
	r := newRecorder(t, reply{body: slateJSON})
	c := r.client(t)
	req := personalized()
	if _, err := c.Recommend(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	req.Context.SetMeasured(false)
	if _, err := c.Recommend(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	reqs := r.requests()
	if _, ok := reqs[0].Body["context"].(map[string]any)["measured"]; ok {
		t.Error("measured sent when unset (default true is the engine's)")
	}
	if reqs[1].Body["context"].(map[string]any)["measured"] != false {
		t.Errorf("body = %v", reqs[1].Body)
	}
}

func TestRecommendMany(t *testing.T) {
	// Articles answer, products hang.
	rr := newSurfaceRecorder(t)
	c := rr.client(t)
	start := time.Now()
	articles := personalized()
	products := personalized()
	products.Surface = "home_products"
	out, err := c.RecommendMany(t.Context(), []ManyRequest{
		{Request: articles, Fallback: func() []string { return []string{"own_a"} }},
		{Request: products, Fallback: func() []string { return []string{"own_p"} }},
	}, WithTester())
	if err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > 300*time.Millisecond {
		t.Errorf("took %v: the two calls must share one budget", el)
	}
	if out[0].Fallback || out[0].Slate == nil || !out[1].Fallback || out[1].ItemIDs[0] != "own_p" {
		t.Errorf("out = %+v", out)
	}
	if out[0].RequestID == out[1].RequestID {
		t.Error("requests share an id")
	}
	_ = c.Close(t.Context())
	for _, q := range rr.requestsTo("/recommendations") {
		if q.Header.Get(HeaderTester) != "1" {
			t.Error("tester option not applied to every request")
		}
	}
	if len(rr.requestsTo("/abandon")) != 1 {
		t.Errorf("abandons = %d, want 1 (products timed out)", len(rr.requestsTo("/abandon")))
	}

	// A caller bug in one request is returned; the other still has its result.
	bad := personalized()
	bad.Mode = ""
	c2 := rr.client(t)
	out, err = c2.RecommendMany(t.Context(), []ManyRequest{{Request: articles}, {Request: bad}})
	if !errors.Is(err, ErrConsentRequired) || out[0].Slate == nil {
		t.Errorf("err = %v, out = %+v", err, out)
	}
}

// newSurfaceRecorder answers home_products by hanging, abandon with 202 and everything else with
// a slate.
func newSurfaceRecorder(t *testing.T) *recorder {
	t.Helper()
	var r *recorder
	r = newRoutedRecorder(t, func(path string) (reply, bool) {
		if strings.HasSuffix(path, "/abandon") {
			return reply{status: 202}, true
		}
		reqs := r.reqs // called under r.mu
		last := reqs[len(reqs)-1]
		if last.Body["surface"] == "home_products" {
			return reply{hang: true}, true
		}
		return reply{}, false
	}, reply{body: slateJSON})
	return r
}

func TestCatalogAndAdminHelpers(t *testing.T) {
	t.Run("DeleteItem", func(t *testing.T) {
		r := newRecorder(t, reply{status: 204})
		if err := r.client(t).DeleteItem(t.Context(), KindCommunity, "c/1"); err != nil {
			t.Fatal(err)
		}
		got := r.requests()[0]
		if got.Method != "DELETE" || got.Path != "/v1/catalog/items/c%2F1" || got.Query != "kind=community" || got.Auth != "Bearer "+testIngestKey {
			t.Errorf("got %+v", got)
		}
		if err := r.client(t).DeleteItem(t.Context(), "thing", "x"); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("DeleteItem unknown", func(t *testing.T) {
		r := newRecorder(t, reply{status: 404, body: `{"code":"item_not_found"}`})
		err := r.client(t).DeleteItem(t.Context(), KindDocument, "x")
		if !errors.Is(err, ErrItemNotFound) || !IsAPIError(err, "item_not_found") {
			t.Errorf("err = %v", err)
		}
		if errors.Is(err, ErrUnknownItem) {
			t.Errorf("item_not_found matched ErrUnknownItem (422 from /interactions)")
		}
	})
	t.Run("GetItem", func(t *testing.T) {
		r := newRecorder(t, reply{status: 200, requestID: "req_g", body: `{"external_id":"42","kind":"product","title":"Magnesium","locale":"nl",
			"status":"published","premium":false,"updated_at":"2026-10-13T08:00:00Z",
			"attributes":{"price_cents":1995,"currency":"EUR","available":true,"format":"product"},
			"tags":[{"tag":"sleep","weight":0.8,"source":"enrich","approved":true}],
			"enrichment":{"status":"done","model":"claude-haiku-5-5","updated_at":"2026-10-13T08:00:00Z"}}`})
		it, err := r.client(t).GetItem(t.Context(), KindProduct, "42")
		if err != nil {
			t.Fatal(err)
		}
		got := r.requests()[0]
		if got.Method != "GET" || got.Path != "/v1/catalog/items/42" || got.Query != "kind=product" || got.Auth != "Bearer "+testIngestKey {
			t.Errorf("got %+v", got)
		}
		if it.ExternalID != "42" || it.Kind != KindProduct || it.Title != "Magnesium" || it.Status != StatusPublished {
			t.Errorf("item = %+v", it)
		}
		if v, _ := it.Attributes["price_cents"].(float64); v != 1995 || it.Attributes["available"] != true {
			t.Errorf("attributes = %v", it.Attributes)
		}
		if len(it.Tags) != 1 || it.Tags[0].Tag != "sleep" || it.Tags[0].Source != "enrich" || it.Tags[0].Weight == nil || *it.Tags[0].Weight != 0.8 {
			t.Errorf("tags = %+v", it.Tags)
		}
		if it.Enrichment == nil || it.Enrichment.Status != "done" || it.Enrichment.Model != "claude-haiku-5-5" || it.Enrichment.UpdatedAt == nil {
			t.Errorf("enrichment = %+v", it.Enrichment)
		}
		if it.UpdatedAt == nil || it.UpdatedAt.Format(time.RFC3339) != "2026-10-13T08:00:00Z" {
			t.Errorf("updated_at = %v", it.UpdatedAt)
		}
	})
	t.Run("GetItem escapes the id and refuses bad input", func(t *testing.T) {
		r := newRecorder(t, reply{status: 200, body: `{"external_id":"c/1","kind":"community","title":"x","locale":"nl","status":"published"}`})
		if _, err := r.client(t).GetItem(t.Context(), KindCommunity, "c/1"); err != nil {
			t.Fatal(err)
		}
		if got := r.requests()[0]; got.Path != "/v1/catalog/items/c%2F1" || got.Query != "kind=community" {
			t.Errorf("got %+v", got)
		}
		if _, err := r.client(t).GetItem(t.Context(), "thing", "x"); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("kind: err = %v", err)
		}
		if _, err := r.client(t).GetItem(t.Context(), KindProduct, ""); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("id: err = %v", err)
		}
		if len(r.requests()) != 1 {
			t.Errorf("invalid input reached the wire: %d requests", len(r.requests()))
		}
	})
	t.Run("GetItem not found", func(t *testing.T) {
		r := newRecorder(t, reply{status: 404, requestID: "req_404", body: `{"code":"item_not_found","message":"no item with that id and kind"}`})
		it, err := r.client(t).GetItem(t.Context(), KindProduct, "gone")
		if it != nil || !errors.Is(err, ErrItemNotFound) {
			t.Fatalf("it = %v, err = %v", it, err)
		}
		var pe *Error
		if !errors.As(err, &pe) || pe.Status != 404 || pe.Op != "getItem" || pe.RequestID != "req_404" {
			t.Errorf("err = %+v", pe)
		}
		if errors.Is(err, ErrUnknownItem) {
			t.Errorf("item_not_found matched ErrUnknownItem")
		}
	})
	t.Run("GetItem retries once on 5xx", func(t *testing.T) {
		r := newRecorder(t, reply{status: 503, body: `{"code":"unavailable"}`},
			reply{status: 200, body: `{"external_id":"42","kind":"product","title":"x","locale":"nl","status":"archived"}`})
		it, err := r.client(t).GetItem(t.Context(), KindProduct, "42")
		if err != nil {
			t.Fatal(err)
		}
		if it.Status != StatusArchived || len(r.requests()) != 2 {
			t.Errorf("status = %s, requests = %d", it.Status, len(r.requests()))
		}
	})
	t.Run("ItemsChangedSince pages through", func(t *testing.T) {
		r := newRecorder(t,
			reply{body: `{"items":[{"external_id":"a","kind":"document"},{"external_id":"b","kind":"document"}],"next_cursor":"c2"}`},
			reply{body: `{"items":[{"external_id":"c","kind":"document","updated_at":"2026-10-13T10:00:00Z"}],"next_cursor":null}`})
		var ids []string
		since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
		err := r.client(t).ItemsChangedSince(t.Context(), since, KindDocument, func(it Item) error {
			ids = append(ids, it.ExternalID)
			return nil
		})
		if err != nil || !slices.Equal(ids, []string{"a", "b", "c"}) {
			t.Fatalf("ids = %v, err = %v", ids, err)
		}
		reqs := r.requests()
		if reqs[0].Query != "changed_since=2026-10-01T00%3A00%3A00Z&kind=document&limit=500" || !strings.Contains(reqs[1].Query, "cursor=c2") {
			t.Errorf("queries = %q, %q", reqs[0].Query, reqs[1].Query)
		}
	})
	t.Run("CreateSigningSecret needs the admin key", func(t *testing.T) {
		r := newRecorder(t, reply{status: 201, body: `{"signing_secret":"abc123"}`})
		if _, err := r.client(t).CreateSigningSecret(t.Context()); !errors.Is(err, ErrNotConfigured) {
			t.Fatalf("err = %v", err)
		}
		s, err := r.client(t, func(c *Config) { c.AdminKey = "pk_admin_test" }).CreateSigningSecret(t.Context())
		if err != nil || s.SigningSecret != "abc123" {
			t.Fatalf("s = %+v, err = %v", s, err)
		}
		got := r.requests()[0]
		if got.Method != "POST" || got.Path != "/v1/settings/signing-secret" || got.Auth != "Bearer pk_admin_test" {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("AdminCall validates the path", func(t *testing.T) {
		r := newRecorder(t, reply{status: 200})
		c := r.client(t, func(c *Config) { c.AdminKey = "k" })
		if err := c.AdminCall(t.Context(), AdminRequest{Method: "GET", Path: "surfaces"}, nil); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestSlateVisible(t *testing.T) {
	var s Slate
	if err := json.Unmarshal([]byte(`{"request_id":"r","items":[]}`), &s); err != nil || !s.Visible() {
		t.Fatalf("absent shown must be visible: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"request_id":"r","items":[],"shown":false}`), &s); err != nil || s.Visible() {
		t.Fatalf("shown=false must hide: %v", err)
	}
	var nilSlate *Slate
	if nilSlate.Visible() {
		t.Fatal("nil slate is not visible")
	}
}
