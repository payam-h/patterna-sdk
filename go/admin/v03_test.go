package admin

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	patterna "github.com/payam-h/patterna-sdk/go"
)

func ptr[T any](v T) *T { return &v }

func TestApplySurface(t *testing.T) {
	a, e := setup(t)
	ctx := t.Context()
	spec := SurfaceSpec{
		Surface: "more_to_read", Slots: ptr(7), ItemKinds: []string{"document"}, MinSlots: ptr(3), DeviceResort: ptr(true),
		Fallback: &SurfaceFallback{Kind: FallbackCurated, ItemIDs: []string{"doc_1", "doc_2"}, ByAnchor: map[string][]string{"doc_9": {"p_1", "p_2"}}},
	}
	plan, err := a.PlanSurface(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"slots", "fallback", "min_slots", "device_resort"}; !slices.Equal(plan.Changes, want) || plan.Applied {
		t.Fatalf("plan %+v, want %v", plan, want)
	}
	plan, err = a.ApplySurface(ctx, spec)
	if err != nil || !plan.Applied {
		t.Fatalf("apply %+v, %v", plan, err)
	}
	e.Mu.Lock()
	sf := *e.Surfaces["more_to_read"]
	last := e.Calls[len(e.Calls)-2] // the PATCH, then the GET that reads the surface back
	e.Mu.Unlock()
	if last != "PATCH /v1/surfaces/more_to_read" {
		t.Fatalf("calls: %v", last)
	}
	if sf.Slots != 7 || sf.MinSlots != 3 || !sf.DeviceResort {
		t.Fatalf("surface %+v", sf)
	}
	var fb SurfaceFallback
	if err := json.Unmarshal(sf.Fallback, &fb); err != nil || fb.Kind != "curated" || !slices.Equal(fb.ByAnchor["doc_9"], []string{"p_1", "p_2"}) {
		t.Fatalf("fallback %s, %v", sf.Fallback, err)
	}
	// applied twice: nothing to do
	plan, err = a.ApplySurface(ctx, spec)
	if err != nil || !plan.Unchanged() || plan.Applied {
		t.Fatalf("second apply %+v, %v", plan, err)
	}
	got, err := a.GetSurface(ctx, "more_to_read")
	if err != nil || got.MinSlots != 3 || !got.DeviceResort {
		t.Fatalf("surface %+v, %v", got, err)
	}
	if f, err := got.FallbackConfig(); err != nil || f.ByAnchor["doc_9"][0] != "p_1" {
		t.Fatalf("fallback config %+v, %v", f, err)
	}
	// min_slots back to 0 is a change, not "left out"
	plan, err = a.ApplySurface(ctx, SurfaceSpec{Surface: "more_to_read", MinSlots: ptr(0)})
	if err != nil || !slices.Equal(plan.Changes, []string{"min_slots"}) {
		t.Fatalf("min_slots 0: %+v, %v", plan, err)
	}
}

func TestUpdateSurfaceReadsBack(t *testing.T) {
	a, _ := setup(t)
	s, err := a.UpdateSurface(t.Context(), "home_products", SurfaceSpec{MinSlots: ptr(2), DeviceResort: ptr(true)})
	if err != nil {
		t.Fatal(err)
	}
	// the engine answers {key, ok}; the client reads the surface back
	if s.Key != "home_products" || s.MinSlots != 2 || !s.DeviceResort || s.Status != StatusShadow {
		t.Fatalf("surface %+v", s)
	}
	if _, err := a.UpdateSurface(t.Context(), "home_products", SurfaceSpec{}); !errors.Is(err, patterna.ErrInvalidArgument) {
		t.Errorf("empty update: %v", err)
	}
}

func TestSurfaceSpecValidate(t *testing.T) {
	bad := []SurfaceSpec{
		{},
		{Surface: "s", Slots: ptr(0)},
		{Surface: "s", MinSlots: ptr(51)},
		{Surface: "s", MinSlots: ptr(-1)},
		{Surface: "s", ItemKinds: []string{"video"}},
		{Surface: "s", Fallback: &SurfaceFallback{Kind: "random"}},
		{Surface: "s", Fallback: &SurfaceFallback{Kind: FallbackCurated}},
		{Surface: "s", Fallback: &SurfaceFallback{Kind: FallbackNewest, ByAnchor: map[string][]string{"doc_1": {}}}},
		{Surface: "s", Exploration: ptr(1.5)},
	}
	for i, s := range bad {
		if err := s.Validate(); !errors.Is(err, patterna.ErrInvalidArgument) {
			t.Errorf("%d: %v", i, err)
		}
	}
	ok := SurfaceSpec{Surface: "s", MinSlots: ptr(0), Fallback: &SurfaceFallback{Kind: FallbackNewest, ByAnchor: map[string][]string{"doc_1": {"p"}}}}
	if err := ok.Validate(); err != nil {
		t.Error(err)
	}
}

func TestLedgerByBlock(t *testing.T) {
	a, e := setup(t)
	rows, err := a.LedgerByBlock(t.Context(), LedgerQuery{Surface: "more_to_read", Block: "ignored"})
	if err != nil || len(rows) != 2 || rows[0].Block != "top" || rows[1].Block != "end_carousel" || rows[0].ReadableSample != 900 {
		t.Fatalf("rows %+v, %v", rows, err)
	}
	rows, err = a.Ledger(t.Context(), LedgerQuery{Surface: "more_to_read", Block: "top"})
	if err != nil || len(rows) != 1 || rows[0].Block != "top" {
		t.Fatalf("one block: %+v, %v", rows, err)
	}
	rows, err = a.Ledger(t.Context(), LedgerQuery{Surface: "more_to_read"})
	if err != nil || len(rows) != 1 || rows[0].Block != "" {
		t.Fatalf("whole slate: %+v, %v", rows, err)
	}
	if _, err := a.Ledger(t.Context(), LedgerQuery{Block: strings.Repeat("x", 33)}); !errors.Is(err, patterna.ErrInvalidArgument) {
		t.Errorf("long block: %v", err)
	}
	e.Mu.Lock()
	calls := slices.Clone(e.Calls)
	e.Mu.Unlock()
	if calls[0] != "GET /v1/proof/ledger?by=block&surface=more_to_read" || calls[1] != "GET /v1/proof/ledger?block=top&surface=more_to_read" {
		t.Fatalf("calls %v", calls)
	}
}

func TestSurfaceMetrics(t *testing.T) {
	a, e := setup(t)
	days, err := a.SurfaceMetrics(t.Context(), "more_to_read", MetricsQuery{Days: 7, Block: "top"})
	if err != nil || len(days) != 1 {
		t.Fatalf("days %+v, %v", days, err)
	}
	d := days[0]
	if d.NotShownShare == nil || *d.NotShownShare != 0.25 || d.ClientAbandonedRate == nil || *d.ClientAbandonedRate != 0.1 || d.UnmeasuredShare != nil || d.Block != "top" {
		t.Fatalf("day %+v", d)
	}
	e.Mu.Lock()
	call := e.Calls[0]
	e.Mu.Unlock()
	if call != "GET /v1/surfaces/more_to_read/metrics?block=top&days=7" {
		t.Fatalf("call %s", call)
	}
	if _, err := a.SurfaceMetrics(t.Context(), "nope", MetricsQuery{}); !errors.Is(err, patterna.ErrUnknownSurface) {
		t.Errorf("unknown: %v", err)
	}
	if _, err := a.SurfaceMetrics(t.Context(), "more_to_read", MetricsQuery{Days: 181}); !errors.Is(err, patterna.ErrInvalidArgument) {
		t.Errorf("days: %v", err)
	}
}

func TestPhaseTagsAndHealth(t *testing.T) {
	a, e := setup(t)
	s, err := a.GetSettings(t.Context())
	if err != nil || s.PhaseTags || s.Slug != "fake" || len(s.Raw) == 0 {
		t.Fatalf("settings %+v, %v", s, err)
	}
	if err := a.SetPhaseTags(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if s, err = a.GetSettings(t.Context()); err != nil || !s.PhaseTags {
		t.Fatalf("after: %+v, %v", s, err)
	}
	if err := a.UpdateSettings(t.Context(), SettingsPatch{}); !errors.Is(err, patterna.ErrInvalidArgument) {
		t.Errorf("empty patch: %v", err)
	}
	e.Mu.Lock()
	if e.Settings["phase_tags"] != true {
		t.Errorf("fake settings %v", e.Settings)
	}
	e.Mu.Unlock()
	h, err := a.Health(t.Context())
	if err != nil || h.EngineVersion != "0.2.0" || h.Status != "ok" {
		t.Fatalf("health %+v, %v", h, err)
	}
}
