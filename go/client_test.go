package patterna

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestMethods(t *testing.T) {
	published := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		reply     reply
		call      func(context.Context, *Client) error
		admin     bool
		method    string
		path      string
		query     string
		auth      string
		checkBody func(*testing.T, map[string]any)
	}{
		{
			name:   "Recommend",
			reply:  reply{body: slateJSON},
			method: "POST", path: "/v1/recommendations", auth: testServeKey,
			call: func(ctx context.Context, c *Client) error {
				s, err := c.Recommend(ctx, personalized())
				if err == nil && (s.RequestID != "req_1" || len(s.Items) != 2 || s.Items[0].Reason["en"] == "") {
					t.Errorf("slate not decoded: %+v", s)
				}
				return err
			},
			checkBody: func(t *testing.T, b map[string]any) {
				if b["mode"] != "personalized" || b["user_id"] != "u_7f3a" || b["surface"] != "home_articles" {
					t.Errorf("body = %v", b)
				}
			},
		},
		{
			name:   "ReportInteractionsSync",
			reply:  reply{status: 202},
			method: "POST", path: "/v1/interactions", auth: testServeKey,
			call: func(ctx context.Context, c *Client) error {
				return c.ReportInteractionsSync(ctx, "req_1", []Interaction{{ItemID: "doc_1", Position: 0, Type: InteractionClick, OccurredAt: time.Now()}})
			},
			checkBody: func(t *testing.T, b map[string]any) {
				if b["request_id"] != "req_1" || len(b["interactions"].([]any)) != 1 {
					t.Errorf("body = %v", b)
				}
			},
		},
		{
			name:   "IngestEvents",
			reply:  reply{status: 202, body: `{"accepted":2,"duplicates":0,"rejected":[]}`},
			method: "POST", path: "/v1/events", auth: testIngestKey,
			call: func(ctx context.Context, c *Client) error {
				res, err := c.IngestEvents(ctx, []Event{ev("e1"), ev("e2")})
				if err == nil && res.Accepted != 2 {
					t.Errorf("result = %+v", res)
				}
				return err
			},
			checkBody: func(t *testing.T, b map[string]any) {
				if len(b["events"].([]any)) != 2 {
					t.Errorf("body = %v", b)
				}
			},
		},
		{
			name:   "UpsertItems",
			reply:  reply{body: `{"upserted":1,"enrichment_queued":1}`},
			method: "PUT", path: "/v1/catalog/items", auth: testIngestKey,
			call: func(ctx context.Context, c *Client) error {
				res, err := c.UpsertItems(ctx, []ItemUpsert{{ExternalID: "doc_1", Kind: KindDocument, Title: "Magnesium", Locale: "nl", Collection: "recipes"}})
				if err == nil && res.Upserted != 1 {
					t.Errorf("result = %+v", res)
				}
				return err
			},
			checkBody: func(t *testing.T, b map[string]any) {
				item := b["items"].([]any)[0].(map[string]any)
				if item["external_id"] != "doc_1" || item["premium"] != false {
					t.Errorf("item = %v", item)
				}
			},
		},
		{
			name:   "SetItemStatus",
			method: "POST", path: "/v1/catalog/items/a%2Fb%20c/status", query: "kind=document", auth: testIngestKey,
			call: func(ctx context.Context, c *Client) error {
				return c.SetItemStatus(ctx, KindDocument, "a/b c", StatusScheduled, &published)
			},
			checkBody: func(t *testing.T, b map[string]any) {
				if b["status"] != "scheduled" || b["published_at"] != "2026-11-01T08:00:00Z" {
					t.Errorf("body = %v", b)
				}
			},
		},
		{
			name:   "SetItemTags",
			method: "PUT", path: "/v1/catalog/items/p_9/tags", query: "kind=product", auth: testIngestKey,
			call: func(ctx context.Context, c *Client) error {
				return c.SetItemTags(ctx, KindProduct, "p_9", []ItemTag{{Tag: "magnesium"}})
			},
			checkBody: func(t *testing.T, b map[string]any) {
				if b["tags"].([]any)[0].(map[string]any)["tag"] != "magnesium" {
					t.Errorf("body = %v", b)
				}
			},
		},
		{
			name:   "SetUserMode uses the ingest key without an admin key",
			method: "PUT", path: "/v1/users/u_7f3a/mode", auth: testIngestKey,
			call: func(ctx context.Context, c *Client) error { return c.SetUserMode(ctx, "u_7f3a", ModeCohort) },
			checkBody: func(t *testing.T, b map[string]any) {
				if b["mode"] != "cohort" {
					t.Errorf("body = %v", b)
				}
			},
		},
		{
			name:   "SetUserMode prefers the admin key",
			admin:  true,
			method: "PUT", path: "/v1/users/u_7f3a/mode", auth: "pk_admin_test",
			call: func(ctx context.Context, c *Client) error { return c.SetUserMode(ctx, "u_7f3a", ModeNonProfiled) },
		},
		{
			name:   "SetUserProfile",
			method: "PUT", path: "/v1/users/u_7f3a/profile", auth: testIngestKey,
			call: func(ctx context.Context, c *Client) error {
				yes := true
				return c.SetUserProfile(ctx, "u_7f3a", UserProfile{Premium: &yes, Goals: []string{"sleep"}})
			},
			checkBody: func(t *testing.T, b map[string]any) {
				if b["premium"] != true {
					t.Errorf("body = %v", b)
				}
			},
		},
		{
			name:   "DeleteUser",
			reply:  reply{status: 202, body: `{"id":"del_1","user_id":"u_7f3a","requested_at":"2026-10-13T10:00:00Z","rows_deleted_at":null}`},
			method: "DELETE", path: "/v1/users/u_7f3a", auth: testIngestKey,
			call: func(ctx context.Context, c *Client) error {
				d, err := c.DeleteUser(ctx, "u_7f3a")
				if err == nil && (d.ID != "del_1" || d.RowsDeletedAt != nil) {
					t.Errorf("deletion = %+v", d)
				}
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.reply.status == 0 && tt.reply.body == "" {
				tt.reply.status = 200
			}
			r := newRecorder(t, tt.reply)
			c := r.client(t, func(cfg *Config) {
				if tt.admin {
					cfg.AdminKey = "pk_admin_test"
				}
			})
			if err := tt.call(t.Context(), c); err != nil {
				t.Fatalf("call: %v", err)
			}
			reqs := r.requests()
			if len(reqs) != 1 {
				t.Fatalf("requests = %d, want 1", len(reqs))
			}
			got := reqs[0]
			if got.Method != tt.method || got.Path != tt.path || got.Query != tt.query {
				t.Errorf("got %s %s?%s, want %s %s?%s", got.Method, got.Path, got.Query, tt.method, tt.path, tt.query)
			}
			if got.Auth != "Bearer "+tt.auth {
				t.Errorf("auth = %q", got.Auth)
			}
			if tt.checkBody != nil {
				tt.checkBody(t, got.Body)
			}
		})
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name          string
		reply         reply
		wantCode      string
		wantStatus    int
		wantRequestID string
	}{
		{"API code and request id from the body", reply{status: 422, body: `{"code":"pii_rejected","message":"email","request_id":"req_body"}`, requestID: "req_header"}, "pii_rejected", 422, "req_body"},
		{"request id from the header when the body has none", reply{status: 401, body: `nope`, requestID: "req_header"}, "http_401", 401, "req_header"},
		{"2xx that is not JSON", reply{status: 200, body: `<html>`, requestID: "req_x"}, CodeBadResponse, 200, "req_x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRecorder(t, tt.reply)
			_, err := r.client(t).Recommend(t.Context(), personalized())
			var pe *Error
			if !errors.As(err, &pe) {
				t.Fatalf("err = %v", err)
			}
			if pe.Code != tt.wantCode || pe.Status != tt.wantStatus || pe.RequestID != tt.wantRequestID || pe.Op != "recommend" {
				t.Errorf("got %+v", pe)
			}
		})
	}
}

func TestMissingKey(t *testing.T) {
	r := newRecorder(t, reply{body: slateJSON})
	c := r.client(t, func(cfg *Config) { cfg.ServeKey = "" })
	if _, err := c.Recommend(t.Context(), personalized()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v", err)
	}
	if n := len(r.requests()); n != 0 {
		t.Fatalf("requests = %d", n)
	}
}

func TestTimeouts(t *testing.T) {
	tests := []struct {
		name     string
		call     func(context.Context, *Client) error
		ctx      func(*testing.T) (context.Context, context.CancelFunc)
		wantErr  error
		maxWait  time.Duration
		requests int
	}{
		{
			name:     "Recommend gives up after 150 ms",
			call:     func(ctx context.Context, c *Client) error { _, err := c.Recommend(ctx, personalized()); return err },
			wantErr:  ErrTimeout,
			maxWait:  400 * time.Millisecond,
			requests: 1,
		},
		{
			name: "a shorter caller deadline wins",
			call: func(ctx context.Context, c *Client) error { _, err := c.Recommend(ctx, personalized()); return err },
			ctx: func(t *testing.T) (context.Context, context.CancelFunc) {
				return context.WithTimeout(t.Context(), 30*time.Millisecond)
			},
			wantErr:  ErrTimeout,
			maxWait:  120 * time.Millisecond,
			requests: 1,
		},
		{
			name: "a caller cancel is aborted, not a timeout",
			call: func(ctx context.Context, c *Client) error { _, err := c.Recommend(ctx, personalized()); return err },
			ctx: func(t *testing.T) (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(t.Context())
				time.AfterFunc(20*time.Millisecond, cancel)
				return ctx, cancel
			},
			wantErr:  ErrAborted,
			maxWait:  120 * time.Millisecond,
			requests: 1,
		},
		{
			name: "ingest timeouts are not retried",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.IngestEvents(ctx, []Event{ev("e1")})
				return err
			},
			ctx: func(t *testing.T) (context.Context, context.CancelFunc) {
				return context.WithTimeout(t.Context(), 50*time.Millisecond)
			},
			wantErr:  ErrTimeout,
			maxWait:  200 * time.Millisecond,
			requests: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRecorder(t, reply{hang: true})
			c := r.client(t)
			ctx, cancel := t.Context(), context.CancelFunc(func() {})
			if tt.ctx != nil {
				ctx, cancel = tt.ctx(t)
			}
			defer cancel()
			start := time.Now()
			err := tt.call(ctx, c)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if el := time.Since(start); el > tt.maxWait {
				t.Errorf("took %v, want under %v", el, tt.maxWait)
			}
			if n := len(r.requests()); n != tt.requests {
				t.Errorf("requests = %d, want %d", n, tt.requests)
			}
		})
	}
}

func TestRetry(t *testing.T) {
	recommend := func(ctx context.Context, c *Client) error { _, err := c.Recommend(ctx, personalized()); return err }
	ingest := func(ctx context.Context, c *Client) error {
		_, err := c.IngestEvents(ctx, []Event{ev("e1")})
		return err
	}
	unavailable := reply{status: 503, body: `{"code":"unavailable","message":"x","request_id":"r1"}`}
	tests := []struct {
		name     string
		replies  []reply
		call     func(context.Context, *Client) error
		wantErr  bool
		wantCode string
		requests int
	}{
		{"idempotent call retried once on 5xx", []reply{unavailable, {status: 202, body: `{"accepted":1}`}}, ingest, false, "", 2},
		{"idempotent call retried once on a dropped connection", []reply{{drop: true}, {status: 202, body: `{}`}}, ingest, false, "", 2},
		{"gives up after one retry", []reply{unavailable, {status: 502, body: `{"code":"bad_gateway","request_id":"r2"}`}}, ingest, true, "bad_gateway", 2},
		{"4xx is not retried", []reply{{status: 400, body: `{"code":"bad_request"}`}, {status: 202}}, ingest, true, "bad_request", 1},
		{"Recommend is never retried on 5xx", []reply{unavailable, {body: slateJSON}}, recommend, true, "unavailable", 1},
		{"Recommend is never retried on a dropped connection", []reply{{drop: true}, {body: slateJSON}}, recommend, true, CodeNetwork, 1},
		{"SetItemStatus retried", []reply{unavailable, {status: 200}}, func(ctx context.Context, c *Client) error {
			return c.SetItemStatus(ctx, KindDocument, "d", StatusArchived, nil)
		}, false, "", 2},
		{"SetItemTags retried", []reply{unavailable, {status: 200}}, func(ctx context.Context, c *Client) error {
			return c.SetItemTags(ctx, KindDocument, "d", nil)
		}, false, "", 2},
		{"UpsertItems retried", []reply{unavailable, {status: 200, body: `{}`}}, func(ctx context.Context, c *Client) error {
			_, err := c.UpsertItems(ctx, nil)
			return err
		}, false, "", 2},
		{"SetUserMode retried", []reply{unavailable, {status: 200}}, func(ctx context.Context, c *Client) error {
			return c.SetUserMode(ctx, "u_1", ModeCohort)
		}, false, "", 2},
		{"SetUserProfile retried", []reply{unavailable, {status: 200}}, func(ctx context.Context, c *Client) error {
			return c.SetUserProfile(ctx, "u_1", UserProfile{})
		}, false, "", 2},
		{"DeleteUser retried", []reply{unavailable, {status: 202, body: `{}`}}, func(ctx context.Context, c *Client) error {
			_, err := c.DeleteUser(ctx, "u_1")
			return err
		}, false, "", 2},
		{"ReportInteractionsSync retried", []reply{unavailable, {status: 202}}, func(ctx context.Context, c *Client) error {
			return c.ReportInteractionsSync(ctx, "r", []Interaction{{ItemID: "d", Type: InteractionSeen, OccurredAt: time.Now()}})
		}, false, "", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRecorder(t, tt.replies...)
			err := tt.call(t.Context(), r.client(t))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantCode != "" {
				var pe *Error
				if !errors.As(err, &pe) || pe.Code != tt.wantCode {
					t.Errorf("err = %v, want code %s", err, tt.wantCode)
				}
			}
			if n := len(r.requests()); n != tt.requests {
				t.Errorf("requests = %d, want %d", n, tt.requests)
			}
		})
	}
}

func TestLimits(t *testing.T) {
	r := newRecorder(t, reply{status: 202, body: `{}`})
	c := r.client(t)
	ctx := t.Context()
	many := func(n int) []Interaction { return make([]Interaction, n) }
	tests := []struct {
		name string
		err  error
	}{
		{"51 interactions", c.ReportInteractionsSync(ctx, "r", many(51))},
		{"empty request id", c.ReportInteractionsSync(ctx, "", many(1))},
		{"501 events", func() error { _, err := c.IngestEvents(ctx, make([]Event, 501)); return err }()},
		{"event without id", func() error { _, err := c.IngestEvents(ctx, []Event{{Type: "x"}}); return err }()},
		{"501 items", func() error { _, err := c.UpsertItems(ctx, make([]ItemUpsert, 501)); return err }()},
		{"201 excludes", func() error {
			req := personalized()
			req.ExcludeItemIDs = make([]string, 201)
			_, err := c.Recommend(ctx, req)
			return err
		}()},
		{"empty external id", c.SetItemStatus(ctx, KindDocument, "", StatusArchived, nil)},
		{"non_profiled profile", c.SetUserProfile(ctx, "u_1", UserProfile{Mode: ModeNonProfiled})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !errors.Is(tt.err, ErrInvalidArgument) {
				t.Errorf("err = %v", tt.err)
			}
		})
	}
	if n := len(r.requests()); n != 0 {
		t.Errorf("requests = %d, want 0", n)
	}
}

func TestErrorString(t *testing.T) {
	e := &Error{Code: "pii_rejected", Op: "ingestEvents", Status: 422, RequestID: "req_1", Message: "field email"}
	want := "patterna: ingestEvents: pii_rejected (status 422, request req_1): field email"
	if e.Error() != want {
		t.Errorf("got %q", e.Error())
	}
	if !IsAPIError(e, "pii_rejected") || e.Transient() {
		t.Error("classification wrong")
	}
}

type wrappedTransport struct{ next http.RoundTripper }

func (w wrappedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return w.next.RoundTrip(r)
}

// Hosts like Elvou replace http.DefaultTransport with a tracing RoundTripper; New must not
// assume it is an *http.Transport.
func TestNewWithWrappedDefaultTransport(t *testing.T) {
	orig := http.DefaultTransport
	http.DefaultTransport = wrappedTransport{next: orig}
	defer func() { http.DefaultTransport = orig }()

	r := newRecorder(t, reply{body: slateJSON})
	if _, err := r.client(t).Recommend(t.Context(), personalized()); err != nil {
		t.Fatal(err)
	}
}
