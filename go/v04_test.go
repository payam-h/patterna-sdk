package patterna

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func codeOf(err error) string {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

// The round-5 context travels under its contract names and is left out when unset.
func TestContextRound5Wire(t *testing.T) {
	r := newRecorder(t, reply{body: slateJSON})
	c := r.client(t)
	req := personalized()
	req.Context.HourBucket = Ptr(21)
	req.Context.Weekday = Ptr(6)
	req.Context.SubscriptionTier = TierFree
	req.Context.TenureDays = Ptr(0)
	req.Context.Region = "Noord-Holland"
	req.Context.SessionItems = []string{"doc_9", "doc_8"}
	if _, err := c.Recommend(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	ctx := r.requests()[0].Body["context"].(map[string]any)
	want := map[string]any{"locale": "nl", "hour_bucket": 21.0, "weekday": 6.0, "subscription_tier": "free",
		"tenure_days": 0.0, "region": "Noord-Holland", "session_items": []any{"doc_9", "doc_8"}}
	if !reflect.DeepEqual(ctx, want) {
		t.Fatalf("context = %v, want %v", ctx, want)
	}

	if _, err := c.Recommend(t.Context(), personalized()); err != nil {
		t.Fatal(err)
	}
	bare := r.requests()[1].Body["context"].(map[string]any)
	for _, k := range []string{"weekday", "subscription_tier", "tenure_days", "region", "session_items", "hour_bucket"} {
		if _, ok := bare[k]; ok {
			t.Errorf("unset %s was sent: %v", k, bare)
		}
	}
}

func TestContextRound5Validation(t *testing.T) {
	long := strings.Repeat("x", 33)
	ids := make([]string, 21)
	for i := range ids {
		ids[i] = "doc"
	}
	cases := []struct {
		name string
		set  func(*RecommendContext)
		msg  string
	}{
		{"weekday low", func(rc *RecommendContext) { rc.Weekday = Ptr(-1) }, "weekday"},
		{"weekday high", func(rc *RecommendContext) { rc.Weekday = Ptr(7) }, "weekday"},
		{"hour high", func(rc *RecommendContext) { rc.HourBucket = Ptr(24) }, "hour_bucket"},
		{"tenure negative", func(rc *RecommendContext) { rc.TenureDays = Ptr(-3) }, "tenure_days"},
		{"tier long", func(rc *RecommendContext) { rc.SubscriptionTier = long }, "subscription_tier"},
		{"region long", func(rc *RecommendContext) { rc.Region = strings.Repeat("r", 65) }, "region"},
		{"session items many", func(rc *RecommendContext) { rc.SessionItems = ids }, "session_items"},
		{"session item empty", func(rc *RecommendContext) { rc.SessionItems = []string{""} }, "session_items"},
		{"session item long", func(rc *RecommendContext) { rc.SessionItems = []string{strings.Repeat("i", 201)} }, "session_items"},
	}
	r := newRecorder(t, reply{body: slateJSON})
	c := r.client(t)
	for _, tc := range cases {
		req := personalized()
		tc.set(&req.Context)
		_, err := c.Recommend(t.Context(), req)
		if codeOf(err) != CodeInvalidArgument || !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%s: err = %v", tc.name, err)
		}
	}
	// the edges are fine
	req := personalized()
	req.Context.Weekday, req.Context.HourBucket, req.Context.TenureDays = Ptr(0), Ptr(23), Ptr(0)
	req.Context.SubscriptionTier = strings.Repeat("t", 32)
	req.Context.Region = strings.Repeat("r", 64)
	req.Context.SessionItems = ids[:20]
	if _, err := c.Recommend(t.Context(), req); err != nil {
		t.Fatalf("edges: %v", err)
	}
	if n := len(r.requests()); n != 1 {
		t.Fatalf("%d requests reached the engine, want only the valid one", n)
	}
}

func TestLocalTime(t *testing.T) {
	ams, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Skip("no tz database:", err)
	}
	cases := []struct {
		at         time.Time
		loc        *time.Location
		hour, wday int
	}{
		// Sunday 23:30 UTC is Monday 01:30 in Amsterdam (summer time, UTC+2)
		{time.Date(2026, 10, 11, 23, 30, 0, 0, time.UTC), ams, 1, 0},
		// the same instant in UTC: Sunday
		{time.Date(2026, 10, 11, 23, 30, 0, 0, time.UTC), time.UTC, 23, 6},
		// nil keeps the time's own location: Tuesday 2026-10-13 09:00 UTC
		{time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC), nil, 9, 1},
		// winter time (UTC+1): Saturday 2026-12-05 23:10 UTC is Sunday 00:10
		{time.Date(2026, 12, 5, 23, 10, 0, 0, time.UTC), ams, 0, 6},
	}
	for _, tc := range cases {
		h, w := LocalTime(tc.at, tc.loc)
		if h != tc.hour || w != tc.wday {
			t.Errorf("LocalTime(%v, %v) = %d, %d; want %d, %d", tc.at, tc.loc, h, w, tc.hour, tc.wday)
		}
	}
	var rc RecommendContext
	rc.SetLocalTime(cases[0].at, ams)
	var ec EventContext
	ec.SetLocalTime(cases[0].at, ams)
	if *rc.HourBucket != 1 || *rc.Weekday != 0 || *ec.HourBucket != 1 || *ec.Weekday != 0 {
		t.Fatalf("SetLocalTime: rc %d/%d ec %d/%d", *rc.HourBucket, *rc.Weekday, *ec.HourBucket, *ec.Weekday)
	}
}

// Every typed attribute lands in attributes under its contract name; Region travels top-level.
func TestItemAttributesWire(t *testing.T) {
	r := newRecorder(t, reply{body: `{"upserted":2}`})
	c := r.client(t)
	exp := time.Date(2026, 12, 31, 23, 0, 0, 0, time.FixedZone("CET", 3600))
	starts := time.Date(2026, 11, 1, 18, 0, 0, 0, time.UTC)
	full := ItemUpsert{
		ExternalID: "prod_1", Kind: KindProduct, Title: "Magnesium", Locale: "nl",
		Attributes: map[string]any{"handle": "magnesium", "format": "text"},
		Editorial: &ItemAttributes{
			Format: FormatProduct, DurationMin: Ptr(12.5), ReadingTimeMin: Ptr(4.0), Difficulty: DifficultyEasy,
			Author: "auth_3", Available: Ptr(false), ExpiresAt: &exp, StartsAt: &starts, RepurchaseDays: Ptr(25),
			Region: "Utrecht", PhaseFit: []string{"follicular"},
			PriceCents: Ptr(2495), CompareAtCents: Ptr(2995), Currency: "EUR", ImageURL: "https://cdn/x.jpg",
			URL: "https://shop/x", ProductType: "supplement", Categories: []string{"sleep", "minerals"},
		},
	}
	empty := ItemUpsert{ExternalID: "doc_1", Kind: KindDocument, Title: "T", Locale: "nl", Editorial: &ItemAttributes{}}
	if _, err := c.UpsertItems(t.Context(), []ItemUpsert{full, empty}); err != nil {
		t.Fatal(err)
	}
	items := r.requests()[0].Body["items"].([]any)
	got := items[0].(map[string]any)
	want := map[string]any{
		"handle": "magnesium", "format": "product", "duration_min": 12.5, "reading_time_min": 4.0,
		"difficulty": "easy", "author": "auth_3", "available": false, "expires_at": "2026-12-31T22:00:00Z",
		"starts_at": "2026-11-01T18:00:00Z", "repurchase_days": 25.0, "phase_fit": []any{"follicular"},
		"price_cents": 2495.0, "compare_at_cents": 2995.0, "currency": "EUR", "image_url": "https://cdn/x.jpg",
		"url": "https://shop/x", "product_type": "supplement", "categories": []any{"sleep", "minerals"},
	}
	if !reflect.DeepEqual(got["attributes"], want) {
		t.Fatalf("attributes = %v\nwant %v", got["attributes"], want)
	}
	if got["region"] != "Utrecht" {
		t.Fatalf("region = %v, want the top-level region", got["region"])
	}
	if _, ok := got["Editorial"]; ok {
		t.Fatal("Editorial leaked as a key")
	}
	if _, ok := items[1].(map[string]any)["attributes"]; ok {
		t.Fatalf("empty attributes were sent: %v", items[1])
	}
	// The caller's map is not changed.
	if full.Attributes["format"] != "text" || len(full.Attributes) != 2 {
		t.Fatalf("Attributes mutated: %v", full.Attributes)
	}
	// Without Editorial nothing changes from 0.3.
	b, _ := json.Marshal(ItemUpsert{ExternalID: "d", Kind: KindDocument, Title: "T", Locale: "nl", Attributes: map[string]any{"a": 1}})
	if string(b) != `{"external_id":"d","kind":"document","title":"T","locale":"nl","premium":false,"attributes":{"a":1}}` {
		t.Fatalf("plain upsert = %s", b)
	}
}

func TestItemAttributesValidation(t *testing.T) {
	cases := []struct {
		name string
		it   ItemUpsert
		msg  string
	}{
		{"format", ItemUpsert{Editorial: &ItemAttributes{Format: "podcast"}}, "attributes.format"},
		{"difficulty", ItemUpsert{Editorial: &ItemAttributes{Difficulty: "expert"}}, "attributes.difficulty"},
		{"duration", ItemUpsert{Editorial: &ItemAttributes{DurationMin: Ptr(-1.0)}}, "attributes.duration_min"},
		{"reading time", ItemUpsert{Editorial: &ItemAttributes{ReadingTimeMin: Ptr(-1.0)}}, "attributes.reading_time_min"},
		{"repurchase", ItemUpsert{Editorial: &ItemAttributes{RepurchaseDays: Ptr(-1)}}, "attributes.repurchase_days"},
		{"price", ItemUpsert{Editorial: &ItemAttributes{PriceCents: Ptr(-1)}}, "attributes.price_cents"},
		{"compare at", ItemUpsert{Editorial: &ItemAttributes{CompareAtCents: Ptr(-1)}}, "attributes.compare_at_cents"},
		{"currency", ItemUpsert{Editorial: &ItemAttributes{Currency: "euro"}}, "attributes.currency"},
		{"editorial region", ItemUpsert{Editorial: &ItemAttributes{Region: strings.Repeat("r", 65)}}, "region"},
		{"region", ItemUpsert{Region: strings.Repeat("r", 65)}, "region"},
		{"region conflict", ItemUpsert{Region: "Utrecht", Editorial: &ItemAttributes{Region: "Drenthe"}}, "different regions"},
	}
	r := newRecorder(t, reply{body: `{}`})
	c := r.client(t)
	for _, tc := range cases {
		tc.it.ExternalID, tc.it.Kind, tc.it.Title, tc.it.Locale = "x", KindProduct, "T", "nl"
		_, err := c.UpsertItems(t.Context(), []ItemUpsert{{ExternalID: "ok", Kind: KindDocument, Title: "T", Locale: "nl"}, tc.it})
		if codeOf(err) != CodeInvalidArgument || !strings.Contains(err.Error(), tc.msg) || !strings.Contains(err.Error(), "item 1") {
			t.Errorf("%s: err = %v", tc.name, err)
		}
	}
	if len(r.requests()) != 0 {
		t.Fatal("an invalid batch reached the engine")
	}
	if err := (&ItemAttributes{Format: FormatWorkout, Difficulty: DifficultyHard, Currency: "EUR"}).Validate(); err != nil {
		t.Fatal(err)
	}
	var nilAttrs *ItemAttributes
	if nilAttrs.Validate() != nil || len(nilAttrs.Map()) != 0 {
		t.Fatal("nil attributes must be valid and empty")
	}
}

func TestActivityEventBuilders(t *testing.T) {
	at := time.Date(2026, 10, 13, 20, 0, 0, 0, time.UTC)
	pers := EventBase{Mode: ModePersonalized, UserID: "u_7f3a", SessionID: "s_1", OccurredAt: at, EventID: "e1"}
	anon := EventBase{Mode: ModeNonProfiled, SessionID: "s_9", OccurredAt: at, EventID: "e2"}
	type build func(EventBase) (Event, error)
	cases := []struct {
		typ   string
		build build
		props map[string]any
	}{
		{EventCommunityPostCreated, func(b EventBase) (Event, error) {
			return CommunityPostCreated(b, CommunityPost{PostID: "p1", CommunityID: "c1"})
		}, map[string]any{"post_id": "p1", "community_id": "c1"}},
		{EventCommunityReply, func(b EventBase) (Event, error) {
			return CommunityReply(b, CommunityPost{PostID: "p1", CommunityID: "c1"})
		}, map[string]any{"post_id": "p1", "community_id": "c1"}},
		{EventCommunityReaction, func(b EventBase) (Event, error) {
			return CommunityReaction(b, CommunityPost{PostID: "p1", CommunityID: "c1", Reaction: "heart"})
		}, map[string]any{"post_id": "p1", "community_id": "c1", "reaction": "heart"}},
		{EventPaywallViewed, func(b EventBase) (Event, error) {
			b.RequestID = "req_5"
			return PaywallViewed(b, "doc_3")
		}, map[string]any{"item_id": "doc_3", "request_id": "req_5"}},
		{EventPushOpened, func(b EventBase) (Event, error) {
			return PushOpened(b, "doc_4", "weekly_digest")
		}, map[string]any{"item_id": "doc_4", "notification_type": "weekly_digest"}},
		{EventSearchPerformed, func(b EventBase) (Event, error) {
			return SearchPerformed(b, Search{QueryHash: "9f86d081884c7d65", ResultCount: Ptr(0), ClickedItemID: "doc_2"})
		}, map[string]any{"query_hash": "9f86d081884c7d65", "result_count": 0, "clicked_item_id": "doc_2"}},
		{EventItemEngaged, func(b EventBase) (Event, error) {
			return ItemEngaged(b, Engagement{ItemID: "doc_1", EngagedSeconds: 42, MaxScrollPercent: 80})
		}, map[string]any{"item_id": "doc_1", "engaged_seconds": 42, "max_scroll_percent": 80}},
		{EventItemCompleted, func(b EventBase) (Event, error) {
			return ItemCompleted(b, "wo_1", KindDocument, "workouts")
		}, map[string]any{"item_id": "wo_1", "kind": "document", "collection": "workouts"}},
		{EventOrderPlaced, func(b EventBase) (Event, error) {
			return OrderPlaced(b, Order{OrderIDHash: "oh_1", ProductIDs: []string{"prod_1", "prod_2"}, ValueCents: Ptr(4990), Currency: "EUR"})
		}, map[string]any{"order_id_hash": "oh_1", "product_ids": []string{"prod_1", "prod_2"}, "value_cents": 4990, "currency": "EUR"}},
	}
	for _, tc := range cases {
		e, err := tc.build(pers)
		if err != nil {
			t.Fatalf("%s: %v", tc.typ, err)
		}
		if e.Type != tc.typ || e.UserID != "u_7f3a" || e.SessionID != "s_1" || e.Source != "backend" || e.EventID != "e1" || !e.OccurredAt.Equal(at) {
			t.Errorf("%s personalized envelope = %+v", tc.typ, e)
		}
		if !reflect.DeepEqual(e.Props, tc.props) {
			t.Errorf("%s props = %v, want %v", tc.typ, e.Props, tc.props)
		}
		// non_profiled: the session id only
		e, err = tc.build(anon)
		if err != nil || e.UserID != "" || e.SessionID != "s_9" {
			t.Errorf("%s non_profiled = %+v, %v", tc.typ, e, err)
		}
		// non_profiled with a user id is refused
		bad := anon
		bad.UserID = "u_7f3a"
		if _, err := tc.build(bad); codeOf(err) != CodeConsentViolation {
			t.Errorf("%s: non_profiled with a user id: %v", tc.typ, err)
		}
		// non_profiled without a session id is refused
		bad = anon
		bad.SessionID = ""
		if _, err := tc.build(bad); codeOf(err) != CodeConsentViolation {
			t.Errorf("%s: non_profiled without a session: %v", tc.typ, err)
		}
		// personalized and cohort need the user id
		bad = EventBase{Mode: ModeCohort, SessionID: "s_1"}
		if _, err := tc.build(bad); codeOf(err) != CodeInvalidArgument {
			t.Errorf("%s: cohort without a user id: %v", tc.typ, err)
		}
		if _, err := tc.build(EventBase{UserID: "u_1"}); codeOf(err) != CodeConsentRequired {
			t.Errorf("%s: no mode: %v", tc.typ, err)
		}
		if _, err := tc.build(EventBase{Mode: ModePersonalized, UserID: "jan@example.com"}); codeOf(err) != CodeInvalidArgument {
			t.Errorf("%s: email as user id: %v", tc.typ, err)
		}
		// defaults
		e, err = tc.build(EventBase{Mode: ModeCohort, UserID: "u_1"})
		if err != nil || e.EventID == "" || e.OccurredAt.IsZero() || e.Source != "backend" || e.SessionID != "" {
			t.Errorf("%s defaults = %+v, %v", tc.typ, e, err)
		}
	}
}

func TestActivityEventValidation(t *testing.T) {
	b := EventBase{Mode: ModePersonalized, UserID: "u_1"}
	cases := []struct {
		name string
		err  error
	}{
		{"post id", second(CommunityPostCreated(b, CommunityPost{CommunityID: "c"}))},
		{"community id", second(CommunityReply(b, CommunityPost{PostID: "p"}))},
		{"reaction missing", second(CommunityReaction(b, CommunityPost{PostID: "p", CommunityID: "c"}))},
		{"reaction on a post", second(CommunityPostCreated(b, CommunityPost{PostID: "p", CommunityID: "c", Reaction: "x"}))},
		{"paywall item", second(PaywallViewed(b, " "))},
		{"push item", second(PushOpened(b, "", "digest"))},
		{"query text", second(SearchPerformed(b, Search{QueryHash: "magnesium slaap"}))},
		{"query short", second(SearchPerformed(b, Search{QueryHash: "abc123"}))},
		{"query upper", second(SearchPerformed(b, Search{QueryHash: "9F86D081884C7D65"}))},
		{"result count", second(SearchPerformed(b, Search{QueryHash: "9f86d081884c7d65", ResultCount: Ptr(-1)}))},
		{"engaged item", second(ItemEngaged(b, Engagement{EngagedSeconds: 3}))},
		{"engaged kind", second(ItemEngaged(b, Engagement{ItemID: "d", Kind: "video"}))},
		{"engaged seconds", second(ItemEngaged(b, Engagement{ItemID: "d", EngagedSeconds: -1}))},
		{"engaged scroll", second(ItemEngaged(b, Engagement{ItemID: "d", MaxScrollPercent: 101}))},
		{"completed item", second(ItemCompleted(b, "", "", ""))},
		{"completed kind", second(ItemCompleted(b, "d", "workout", ""))},
		{"order hash", second(OrderPlaced(b, Order{ProductIDs: []string{"p"}}))},
		{"order products", second(OrderPlaced(b, Order{OrderIDHash: "h"}))},
		{"order empty product", second(OrderPlaced(b, Order{OrderIDHash: "h", ProductIDs: []string{""}}))},
		{"order value", second(OrderPlaced(b, Order{OrderIDHash: "h", ProductIDs: []string{"p"}, ValueCents: Ptr(-5)}))},
		{"order currency", second(OrderPlaced(b, Order{OrderIDHash: "h", ProductIDs: []string{"p"}, Currency: "eur"}))},
		{"event context weekday", second(PushOpened(EventBase{Mode: ModePersonalized, UserID: "u_1", Context: &EventContext{Weekday: Ptr(7)}}, "d", ""))},
	}
	for _, tc := range cases {
		if codeOf(tc.err) != CodeInvalidArgument {
			t.Errorf("%s: err = %v", tc.name, tc.err)
		}
	}
	// optional props are left out
	e, err := SearchPerformed(b, Search{QueryHash: strings.Repeat("a", 64)})
	if err != nil || len(e.Props) != 1 {
		t.Fatalf("search = %v, %v", e.Props, err)
	}
	e, err = ItemCompleted(b, "d", "", "")
	if err != nil || len(e.Props) != 1 {
		t.Fatalf("completed = %v, %v", e.Props, err)
	}
	e, err = PaywallViewed(b, "d")
	if err != nil || len(e.Props) != 1 || e.RequestID != "" {
		t.Fatalf("paywall = %+v, %v", e, err)
	}
	e, err = OrderPlaced(b, Order{OrderIDHash: "h", ProductIDs: []string{"p"}})
	if err != nil || len(e.Props) != 2 {
		t.Fatalf("order = %v, %v", e.Props, err)
	}
}

func second(_ Event, err error) error { return err }

// The round-5 event context keys go out; invalid values are refused before sending.
func TestEventContextRound5(t *testing.T) {
	r := newRecorder(t, reply{status: 202, body: `{"accepted":1}`})
	c := r.client(t)
	e := ev("e1")
	e.Context = &EventContext{Locale: "nl", HourBucket: Ptr(7), Weekday: Ptr(2), Region: "Utrecht", SubscriptionTier: "premium", TenureDays: Ptr(400)}
	if _, err := c.IngestEvents(t.Context(), []Event{e}); err != nil {
		t.Fatal(err)
	}
	got := r.requests()[0].Body["events"].([]any)[0].(map[string]any)["context"].(map[string]any)
	want := map[string]any{"locale": "nl", "hour_bucket": 7.0, "weekday": 2.0, "region": "Utrecht", "subscription_tier": "premium", "tenure_days": 400.0}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("context = %v", got)
	}
	for _, bad := range []EventContext{{Weekday: Ptr(-1)}, {HourBucket: Ptr(24)}, {TenureDays: Ptr(-1)}, {SubscriptionTier: strings.Repeat("t", 33)}, {Region: strings.Repeat("r", 65)}} {
		e.Context = &bad
		if _, err := c.IngestEvents(t.Context(), []Event{e}); codeOf(err) != CodeInvalidArgument {
			t.Errorf("context %+v: %v", bad, err)
		}
	}
	if len(r.requests()) != 1 {
		t.Fatal("an invalid event reached the engine")
	}
}

func TestReasonRecommended(t *testing.T) {
	if ReasonRecommended != "recommended" || ReasonKeys[len(ReasonKeys)-1] != ReasonRecommended || len(ReasonKeys) != 11 {
		t.Fatalf("reason keys = %v", ReasonKeys)
	}
	body := strings.Replace(slateJSON, `"reason_key":"trending","reason":{"nl":"Populair"}`, `"reason_key":"recommended","reason":{"nl":"Aanbevolen","en":"Recommended"}`, 1)
	r := newRecorder(t, reply{body: body})
	s, err := r.client(t).Recommend(t.Context(), personalized())
	if err != nil {
		t.Fatal(err)
	}
	if s.Items[1].ReasonKey != ReasonRecommended || s.Items[1].Reason["en"] != "Recommended" {
		t.Fatalf("slot = %+v", s.Items[1])
	}
}
