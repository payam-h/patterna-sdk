package admin

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	patterna "github.com/payam-h/patterna-sdk/go"
	"github.com/payam-h/patterna-sdk/go/internal/fakeengine"
)

func setup(t *testing.T) (*Client, *fakeengine.Engine) {
	t.Helper()
	e := fakeengine.New("more_to_read", "home_products")
	t.Cleanup(e.Close)
	pc, err := patterna.New(patterna.Config{BaseURL: e.URL(), AdminKey: fakeengine.AdminKey, RetryDelay: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close(t.Context()) })
	return New(pc), e
}

const cfgA = `{"generators":[{"kind":"item_to_item","weight":1}],"ranker":{"kind":"linear"},"exploration":{"kind":"none"}}`

// Same config, other key order and whitespace.
const cfgA2 = `{ "exploration": {"kind": "none"}, "ranker": {"kind": "linear"}, "generators": [{"weight": 1, "kind": "item_to_item"}] }`

const cfgB = `{"generators":[{"kind":"trending","weight":1}],"ranker":{"kind":"linear"},"exploration":{"kind":"plackett_luce"}}`

func TestApplyFlow(t *testing.T) {
	a, e := setup(t)
	ctx := t.Context()
	steps := []struct {
		name     string
		cfg      string
		activate bool
		want     ApplyResult
	}{
		{"first apply creates and activates", cfgA, true, ApplyResult{Surface: "more_to_read", Version: 1, Created: true, Activated: true}},
		{"same config in another form is unchanged", cfgA2, true, ApplyResult{Surface: "more_to_read", Version: 1, Unchanged: true}},
		{"new config without activate only creates", cfgB, false, ApplyResult{Surface: "more_to_read", Version: 2, Created: true}},
		{"applying it again with activate reuses version 2", cfgB, true, ApplyResult{Surface: "more_to_read", Version: 2, Activated: true}},
		{"going back to A reuses version 1", cfgA, true, ApplyResult{Surface: "more_to_read", Version: 1, Activated: true}},
	}
	for _, s := range steps {
		got, err := a.ApplyFlow(ctx, "more_to_read", json.RawMessage(s.cfg), "ci", s.activate)
		if err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if *got != s.want {
			t.Errorf("%s: got %+v, want %+v", s.name, *got, s.want)
		}
	}
	e.Mu.Lock()
	n := len(e.Flows["more_to_read"])
	e.Mu.Unlock()
	if n != 2 {
		t.Errorf("versions = %d, want 2", n)
	}
	if _, err := a.ApplyFlow(ctx, "nope", json.RawMessage(cfgA), "", true); !errors.Is(err, patterna.ErrUnknownSurface) {
		t.Errorf("err = %v", err)
	}
	if _, err := a.ApplyFlow(ctx, "more_to_read", json.RawMessage(`{`), "", true); !errors.Is(err, patterna.ErrInvalidArgument) {
		t.Errorf("err = %v", err)
	}
}

func TestFlowsAndRollback(t *testing.T) {
	a, _ := setup(t)
	ctx := t.Context()
	if _, err := a.RollbackFlow(ctx, "more_to_read", ""); !errors.Is(err, ErrNothingToRollBack) {
		t.Fatalf("err = %v", err)
	}
	for _, c := range []string{cfgA, cfgB} {
		f, err := a.CreateFlow(ctx, "more_to_read", json.RawMessage(c), "n")
		if err != nil {
			t.Fatal(err)
		}
		if err := a.ActivateFlow(ctx, "more_to_read", f.Version, ""); err != nil {
			t.Fatal(err)
		}
	}
	active, ok, err := a.GetFlow(ctx, "more_to_read", 0)
	if err != nil || !ok || active.Version != 2 {
		t.Fatalf("active = %+v %v %v", active, ok, err)
	}
	v, err := a.RollbackFlow(ctx, "more_to_read", "")
	if err != nil || v != 1 {
		t.Fatalf("rollback = %d, %v", v, err)
	}
	active, _, _ = a.GetFlow(ctx, "more_to_read", 0)
	if active.Version != 1 {
		t.Errorf("active after rollback = %d", active.Version)
	}
	// Rolling back again returns to 2: the previously active one.
	if v, _ := a.RollbackFlow(ctx, "more_to_read", ""); v != 2 {
		t.Errorf("second rollback = %d", v)
	}
	if _, ok, _ := a.GetFlow(ctx, "home_products", 0); ok {
		t.Error("surface without flows has an active flow")
	}
	if _, err := a.CreateFlow(ctx, "more_to_read", json.RawMessage(`[1]`), ""); !errors.Is(err, patterna.ErrInvalidArgument) {
		t.Errorf("err = %v", err)
	}
}

func TestPreviousVersion(t *testing.T) {
	at := func(m int) *time.Time { x := time.Date(2026, 1, 1, 0, m, 0, 0, time.UTC); return &x }
	three := 3
	flows := []Flow{{Version: 1, ActivatedAt: at(1)}, {Version: 2, ActivatedAt: at(5)}, {Version: 3, ActivatedAt: at(9)}, {Version: 4}}
	if got := previousVersion(flows, &three); got != 2 {
		t.Errorf("got %d", got)
	}
	if got := previousVersion(flows[3:], nil); got != 0 {
		t.Errorf("got %d", got)
	}
}

func TestSurfaces(t *testing.T) {
	a, _ := setup(t)
	ctx := t.Context()
	s, err := a.SetSurfaceStatus(ctx, "more_to_read", StatusTesters)
	if err != nil || s.Status != "testers" {
		t.Fatalf("s = %+v, %v", s, err)
	}
	if _, err := a.SetSurfaceStatus(ctx, "more_to_read", "on"); !errors.Is(err, patterna.ErrInvalidArgument) {
		t.Errorf("err = %v", err)
	}
	s, err = a.SetSurfaceTesters(ctx, "more_to_read", []string{"u_1", "u_2"})
	if err != nil || !slices.Equal(s.Testers, []string{"u_1", "u_2"}) {
		t.Fatalf("s = %+v, %v", s, err)
	}
	if _, err := a.SetSurfaceTesters(ctx, "more_to_read", []string{"anna@example.com"}); !errors.Is(err, patterna.ErrInvalidArgument) {
		t.Errorf("email tester accepted: %v", err)
	}
	if _, err := a.SetSurfaceStatus(ctx, "nope", StatusOff); !errors.Is(err, patterna.ErrUnknownSurface) {
		t.Errorf("err = %v", err)
	}
}

func TestExperimentsLedgerSecret(t *testing.T) {
	a, e := setup(t)
	ctx := t.Context()
	x, err := a.StartExperiment(ctx, ExperimentInput{Surface: "more_to_read", Name: "first holdout", PrimaryMetric: "tap_rate"})
	if err != nil || x.ID == "" || x.Surface != "more_to_read" || x.StartedAt == nil {
		t.Fatalf("x = %+v, %v", x, err)
	}
	if _, err := a.StartExperiment(ctx, ExperimentInput{Surface: "s"}); !errors.Is(err, patterna.ErrInvalidArgument) {
		t.Errorf("err = %v", err)
	}
	if err := a.EndExperiment(ctx, x.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.EndExperiment(ctx, x.ID); err == nil {
		t.Error("ending twice succeeded")
	}
	list, err := a.ListExperiments(ctx)
	if err != nil || len(list) != 1 || list[0].EndedAt == nil {
		t.Errorf("list = %+v, %v", list, err)
	}

	rows, err := a.Ledger(ctx, LedgerQuery{Surface: "more_to_read", From: "2026-09-14", To: "2026-10-12"})
	if err != nil || len(rows) != 1 || rows[0].CILow == nil || !rows[0].Readable {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
	weeks, err := a.LedgerWeeks(ctx, LedgerQuery{Surface: "more_to_read"})
	if err != nil || len(weeks) != 1 {
		t.Fatalf("weeks = %v, %v", weeks, err)
	}
	job, err := a.ExportLedger(ctx, LedgerQuery{From: "2026-09-14", To: "2026-10-12"})
	if err != nil || job.JobID != "job_1" {
		t.Fatalf("job = %+v, %v", job, err)
	}
	job, err = a.ExportStatus(ctx, "job_1")
	if err != nil || job.Status != "done" || len(job.Files) != 2 {
		t.Fatalf("job = %+v, %v", job, err)
	}
	sec, err := a.CreateSigningSecret(ctx)
	if err != nil || sec.SigningSecret != "s3cr3t-hex" {
		t.Fatalf("secret = %+v, %v", sec, err)
	}
	e.Mu.Lock()
	defer e.Mu.Unlock()
	want := []string{
		"GET /v1/proof/ledger?from=2026-09-14&surface=more_to_read&to=2026-10-12",
		"GET /v1/proof/ledger?bucket=week&surface=more_to_read",
	}
	for _, w := range want {
		if !slices.Contains(e.Calls, w) {
			t.Errorf("missing call %s in %v", w, e.Calls)
		}
	}
}

func TestNeedsAdminKey(t *testing.T) {
	pc, err := patterna.New(patterna.Config{BaseURL: "http://127.0.0.1:1/v1", IngestKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close(t.Context())
	if _, err := New(pc).ListSurfaces(t.Context()); !errors.Is(err, patterna.ErrNotConfigured) {
		t.Errorf("err = %v", err)
	}
}
