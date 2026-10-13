package patterna

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/payam-h/patterna-sdk/go/internal/fakeengine"
)

// fake starts the in-memory engine with one surface and a serve client on it.
func fake(t *testing.T, mutate ...func(*Config)) (*Client, *fakeengine.Engine) {
	t.Helper()
	e := fakeengine.New("home_articles")
	t.Cleanup(e.Close)
	cfg := Config{BaseURL: e.URL(), ServeKey: fakeengine.ServeKey, RetryDelay: -1}
	for _, m := range mutate {
		m(&cfg)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(t.Context()) })
	return c, e
}

func abandonCalls(e *fakeengine.Engine) []string {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	var out []string
	for _, c := range e.Calls {
		if strings.HasSuffix(c, "/abandon") {
			out = append(out, c)
		}
	}
	return out
}

// The engine answers shown=false with no items when fewer items than min_slots are eligible. The
// SDK must hand that to the app as "hide the block": no fallback list, and no abandonment (the
// engine would answer 409 not_shown, and the slate already counts in its arm with zero taps).
func TestShownFalseIsNotAbandoned(t *testing.T) {
	c, e := fake(t)
	e.Mu.Lock()
	e.Surfaces["home_articles"].Items = []string{"doc_1", "doc_2"}
	e.Surfaces["home_articles"].MinSlots = 3
	e.Mu.Unlock()

	called := false
	rec, err := c.RecommendWithFallback(t.Context(), personalized(), func() []string { called = true; return []string{"mine"} })
	if err != nil {
		t.Fatal(err)
	}
	if rec.Shown || rec.Fallback || rec.Abandoned || called || len(rec.ItemIDs) != 0 || rec.Cause != nil {
		t.Fatalf("shown=false must hide the block without fallback or abandonment: %+v (fallback called %v)", rec, called)
	}
	if rec.Slate == nil || rec.Slate.Visible() || rec.RequestID == "" || rec.RequestID != rec.Slate.RequestID {
		t.Fatalf("slate = %+v", rec.Slate)
	}
	// An app that abandons anyway is stopped before anything is sent.
	if err := c.Abandon(rec.RequestID, AbandonClientFallback); !errors.Is(err, ErrNotShown) {
		t.Fatalf("Abandon: %v", err)
	}
	if err := c.AbandonSync(t.Context(), rec.RequestID, ""); !errors.Is(err, ErrNotShown) {
		t.Fatalf("AbandonSync: %v", err)
	}
	if err := c.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls := abandonCalls(e); len(calls) != 0 {
		t.Fatalf("abandon reached the engine: %v", calls)
	}
	e.Mu.Lock()
	n := len(e.Abandoned)
	e.Mu.Unlock()
	if n != 0 {
		t.Fatalf("abandoned %d", n)
	}
}

func TestShownFalseAbandonFromAnotherProcess(t *testing.T) {
	// Without the slate in memory (another process served it) the engine's 409 is a typed error.
	c, e := fake(t, func(cfg *Config) { cfg.SlateMemory = -1 })
	e.Mu.Lock()
	e.Surfaces["home_articles"].MinSlots = 1
	e.Mu.Unlock()
	s, err := c.Recommend(t.Context(), personalized())
	if err != nil || s.Visible() {
		t.Fatalf("slate %+v, err %v", s, err)
	}
	err = c.AbandonSync(t.Context(), s.RequestID, AbandonClientFallback)
	if !errors.Is(err, ErrNotShown) || !IsAPIError(err, CodeNotShown) {
		t.Fatalf("err = %v", err)
	}
	var pe *Error
	if !errors.As(err, &pe) || pe.Status != 409 {
		t.Fatalf("status: %v", err)
	}
}

func TestShownTrueAndFallbackAreShown(t *testing.T) {
	c, e := fake(t)
	e.Mu.Lock()
	e.Surfaces["home_articles"].Items = []string{"doc_1", "doc_2", "doc_3"}
	e.Surfaces["home_articles"].MinSlots = 3
	e.Mu.Unlock()
	rec, err := c.RecommendWithFallback(t.Context(), personalized(), nil)
	if err != nil || !rec.Shown || rec.Fallback || !slices.Equal(rec.ItemIDs, []string{"doc_1", "doc_2", "doc_3"}) {
		t.Fatalf("rec %+v, err %v", rec, err)
	}
	// the app may still abandon a shown slate it did not display
	if err := c.AbandonSync(t.Context(), rec.RequestID, ""); err != nil {
		t.Fatal(err)
	}

	r := newRecorder(t, reply{status: 503, body: `{"code":"unavailable"}`})
	rec, err = r.client(t).RecommendWithFallback(t.Context(), personalized(), func() []string { return []string{"mine"} })
	if err != nil || !rec.Shown || !rec.Fallback {
		t.Fatalf("fallback must be shown: %+v, %v", rec, err)
	}
	// an older engine without "shown" means shown
	r = newRecorder(t, reply{body: slateJSON})
	rec, err = r.client(t).RecommendWithFallback(t.Context(), personalized(), nil)
	if err != nil || !rec.Shown {
		t.Fatalf("absent shown: %+v, %v", rec, err)
	}
}

func TestRecommendManyShown(t *testing.T) {
	c, e := fake(t)
	e.Mu.Lock()
	e.Surfaces["home_articles"].MinSlots = 2
	e.Mu.Unlock()
	out, err := c.RecommendMany(t.Context(), []ManyRequest{{Request: personalized(), Fallback: func() []string { return []string{"x"} }}})
	if err != nil || len(out) != 1 || out[0].Shown || out[0].Fallback || out[0].Abandoned {
		t.Fatalf("out %+v, err %v", out, err)
	}
}

func TestControlItemIDsAndMemberOf(t *testing.T) {
	r := newRecorder(t, reply{body: slateJSON})
	c := r.client(t)
	req := RecommendRequest{Surface: "pairs_with", Mode: ModeNonProfiled, SessionID: "s_1",
		Context: RecommendContext{Locale: "nl", AnchorItemID: "doc_9", ControlItemIDs: []string{"p_1", "p_2"}, MemberOf: []string{"c_1"}}}
	if _, err := c.Recommend(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	ctx := r.requests()[0].Body["context"].(map[string]any)
	if got := ctx["control_item_ids"].([]any); len(got) != 2 || got[0] != "p_1" || got[1] != "p_2" {
		t.Errorf("control_item_ids = %v", ctx["control_item_ids"])
	}
	if got := ctx["member_of"].([]any); len(got) != 1 || got[0] != "c_1" {
		t.Errorf("member_of = %v", ctx["member_of"])
	}
	// absent when empty
	if _, err := c.Recommend(t.Context(), personalized()); err != nil {
		t.Fatal(err)
	}
	ctx = r.requests()[1].Body["context"].(map[string]any)
	if _, ok := ctx["control_item_ids"]; ok {
		t.Error("empty control_item_ids sent")
	}
	if _, ok := ctx["member_of"]; ok {
		t.Error("empty member_of sent")
	}
	// the engine's limits, checked before sending
	big := personalized()
	big.Context.ControlItemIDs = make([]string, 51)
	if _, err := c.Recommend(t.Context(), big); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("51 control ids: %v", err)
	}
	big = personalized()
	big.Context.MemberOf = make([]string, 501)
	if _, err := c.Recommend(t.Context(), big); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("501 communities: %v", err)
	}
	if n := len(r.requests()); n != 2 {
		t.Errorf("requests = %d", n)
	}
}

func TestControlSlateFromFake(t *testing.T) {
	c, e := fake(t)
	e.Mu.Lock()
	e.Surfaces["home_articles"].Items = []string{"doc_1", "doc_2"}
	e.Mu.Unlock()
	req := personalized()
	req.Context.ControlItemIDs = []string{"ed_1", "ed_2"}
	s, err := c.Recommend(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if s.ControlSource != ControlSourceRequest || s.Arm != "control" || !slices.Equal(s.ItemIDs(), []string{"ed_1", "ed_2"}) {
		t.Fatalf("slate %+v", s)
	}
}

func TestSlateV03Fields(t *testing.T) {
	const body = `{"request_id":"r1","surface":"s","arm":"treatment","policy_version":"p","shown":true,
"fill_count":2,"model_versions":{"persona_bayes":"pb-3","gbdt":"g-7"},
"items":[{"item_id":"a","kind":"document","position":0,"reason_key":"similar","reason":{}},
{"item_id":"b","kind":"document","position":1,"reason_key":"new","reason":{},"generator":"fallback"},
{"item_id":"c","kind":"document","position":2,"reason_key":"new","reason":{},"generator":"fallback"}]}`
	r := newRecorder(t, reply{body: body})
	s, err := r.client(t).Recommend(t.Context(), personalized())
	if err != nil {
		t.Fatal(err)
	}
	if s.FillCount != 2 || s.ControlSource != "" || s.ModelVersions["persona_bayes"] != "pb-3" || s.ModelVersions["gbdt"] != "g-7" {
		t.Fatalf("slate %+v", s)
	}
	if fills := []bool{s.Items[0].Fill, s.Items[1].Fill, s.Items[2].Fill}; !slices.Equal(fills, []bool{false, true, true}) {
		t.Fatalf("fill from fill_count = %v", fills)
	}

	// fill per slot from the engine wins over the count
	var sl Slate
	if err := json.Unmarshal([]byte(`{"fill_count":1,"control_source":"by_anchor","items":[{"item_id":"a","fill":true},{"item_id":"b"}]}`), &sl); err != nil {
		t.Fatal(err)
	}
	sl.markFills()
	if !sl.Items[0].Fill || sl.Items[1].Fill || sl.ControlSource != ControlSourceByAnchor {
		t.Fatalf("explicit fill: %+v", sl)
	}
	// a count larger than the slate marks everything, never panics
	sl = Slate{FillCount: 5, Items: []SlateItem{{ItemID: "a"}}}
	sl.markFills()
	if !sl.Items[0].Fill {
		t.Fatal("fill_count > len")
	}
	for _, src := range []ControlSource{ControlSourceRequest, ControlSourceByAnchor, ControlSourceSurface} {
		var x Slate
		if err := json.Unmarshal([]byte(`{"control_source":"`+string(src)+`"}`), &x); err != nil || x.ControlSource != src {
			t.Errorf("%s: %v", src, err)
		}
	}
}

func TestModeRequiredAndNotShownErrors(t *testing.T) {
	r := newRecorder(t, reply{status: 422, body: `{"code":"mode_required","message":"set the mode first","request_id":"req_9"}`})
	c := r.client(t)
	err := c.SetUserProfile(t.Context(), "u_7f3a", UserProfile{Goals: []string{"sleep"}})
	if !errors.Is(err, ErrModeRequired) || !IsAPIError(err, CodeModeRequired) || errors.Is(err, ErrModeConflict) {
		t.Fatalf("err = %v", err)
	}
	if !isCallerBug(err) {
		t.Error("mode_required is a caller bug")
	}
	r = newRecorder(t, reply{status: 409, body: `{"code":"not_shown","message":"hide the block"}`})
	err = r.client(t).AbandonSync(t.Context(), "req_x", AbandonTimeout)
	if !errors.Is(err, ErrNotShown) || errors.Is(err, ErrSurfaceOff) {
		t.Fatalf("err = %v", err)
	}
}

func TestCoachProductEvents(t *testing.T) {
	at := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	shown, err := CoachProductShown(CoachProduct{Mode: ModePersonalized, UserID: "u_7f3a", ProductID: "p_mag", ConversationID: "conv_1", Position: 0, OccurredAt: at, EventID: "e1"})
	if err != nil {
		t.Fatal(err)
	}
	if shown.Type != EventCoachProductShown || shown.UserID != "u_7f3a" || shown.SessionID != "" || shown.Source != "backend" || !shown.OccurredAt.Equal(at) || shown.EventID != "e1" {
		t.Fatalf("event %+v", shown)
	}
	if shown.Props["product_id"] != "p_mag" || shown.Props["conversation_id"] != "conv_1" || shown.Props["position"] != 0 || len(shown.Props) != 3 {
		t.Fatalf("props %v", shown.Props)
	}
	tapped, err := CoachProductTapped(CoachProduct{Mode: ModeNonProfiled, SessionID: "s_1", ProductID: "p_mag", ConversationID: "conv_1", Position: 2})
	if err != nil {
		t.Fatal(err)
	}
	if tapped.Type != EventCoachProductTapped || tapped.UserID != "" || tapped.SessionID != "s_1" || tapped.EventID == "" || tapped.OccurredAt.IsZero() || tapped.Props["position"] != 2 {
		t.Fatalf("event %+v", tapped)
	}

	bad := []struct {
		name string
		p    CoachProduct
		want error
	}{
		{"no mode", CoachProduct{UserID: "u_1", ProductID: "p", ConversationID: "c"}, ErrConsentRequired},
		{"user id in non_profiled", CoachProduct{Mode: ModeNonProfiled, UserID: "u_1", SessionID: "s", ProductID: "p", ConversationID: "c"}, ErrConsentViolation},
		{"no session in non_profiled", CoachProduct{Mode: ModeNonProfiled, ProductID: "p", ConversationID: "c"}, ErrConsentViolation},
		{"no user in personalized", CoachProduct{Mode: ModePersonalized, SessionID: "s", ProductID: "p", ConversationID: "c"}, ErrInvalidArgument},
		{"email as user id", CoachProduct{Mode: ModeCohort, UserID: "a@b.nl", ProductID: "p", ConversationID: "c"}, ErrInvalidArgument},
		{"no product", CoachProduct{Mode: ModeCohort, UserID: "u_1", ConversationID: "c"}, ErrInvalidArgument},
		{"no conversation", CoachProduct{Mode: ModeCohort, UserID: "u_1", ProductID: "p"}, ErrInvalidArgument},
		{"negative position", CoachProduct{Mode: ModeCohort, UserID: "u_1", ProductID: "p", ConversationID: "c", Position: -1}, ErrInvalidArgument},
	}
	for _, b := range bad {
		if _, err := CoachProductShown(b.p); !errors.Is(err, b.want) {
			t.Errorf("%s: %v, want %v", b.name, err, b.want)
		}
	}

	r := newRecorder(t, reply{status: 202, body: `{"accepted":2,"duplicates":0,"rejected":[]}`})
	c := r.client(t)
	if _, err := c.IngestEvents(t.Context(), []Event{shown, tapped}); err != nil {
		t.Fatal(err)
	}
	evs := r.requests()[0].Body["events"].([]any)
	first := evs[0].(map[string]any)
	if first["type"] != "coach_product_shown" || first["props"].(map[string]any)["position"] != float64(0) {
		t.Fatalf("sent %v", first)
	}
	second := evs[1].(map[string]any)
	if _, ok := second["user_id"]; ok || second["session_id"] != "s_1" {
		t.Fatalf("non_profiled event carried a user id: %v", second)
	}
}
