package patterna

import (
	"context"
	"errors"
	"testing"
	"time"
)

func interactions() []Interaction {
	return []Interaction{{ItemID: "doc_1", Position: 0, Type: InteractionSeen, OccurredAt: time.Now()}}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestReportInteractionsIsAsync(t *testing.T) {
	r := newRecorder(t, reply{status: 202, delay: 50 * time.Millisecond})
	c := r.client(t)
	start := time.Now()
	if err := c.ReportInteractions("req_1", interactions()); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > 20*time.Millisecond {
		t.Errorf("ReportInteractions blocked for %v", el)
	}
	waitFor(t, func() bool { return len(r.requests()) == 1 })
	got := r.requests()[0]
	if got.Path != "/v1/interactions" || got.Body["request_id"] != "req_1" {
		t.Errorf("got %+v", got)
	}
}

func TestReportInteractionsBoundedQueue(t *testing.T) {
	r := newRecorder(t, reply{hang: true})
	c := r.client(t, func(cfg *Config) {
		cfg.ReportWorkers = 1
		cfg.ReportQueueSize = 1
		cfg.IngestTimeout = 300 * time.Millisecond
	})
	// The worker takes the first job and blocks on the hanging server; the second fills the
	// queue; the third must be refused without blocking.
	if err := c.ReportInteractions("r1", interactions()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(r.requests()) == 1 })
	if err := c.ReportInteractions("r2", interactions()); err != nil {
		t.Fatal(err)
	}
	err := c.ReportInteractions("r3", interactions())
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("err = %v, want queue full", err)
	}
	if c.DroppedReports() != 1 {
		t.Errorf("dropped = %d", c.DroppedReports())
	}
}

func TestReportInteractionsValidatesAndClose(t *testing.T) {
	r := newRecorder(t, reply{status: 202})
	c := r.client(t)
	tests := []struct {
		name string
		err  error
		want error
	}{
		{"empty request id", c.ReportInteractions("", interactions()), ErrInvalidArgument},
		{"over 50", c.ReportInteractions("r", make([]Interaction, 51)), ErrInvalidArgument},
		{"empty list is a no-op", c.ReportInteractions("r", nil), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !errors.Is(tt.err, tt.want) && !(tt.want == nil && tt.err == nil) {
				t.Errorf("err = %v, want %v", tt.err, tt.want)
			}
		})
	}

	for range 3 {
		if err := c.ReportInteractions("r", interactions()); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := c.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
	if n := len(r.requests()); n != 3 {
		t.Errorf("Close did not drain: %d requests", n)
	}
	if err := c.ReportInteractions("r", interactions()); !errors.Is(err, ErrClosed) {
		t.Errorf("after close: %v", err)
	}
	if err := c.Close(ctx); err != nil {
		t.Errorf("second close: %v", err)
	}
}

func TestReportFailuresAreCounted(t *testing.T) {
	r := newRecorder(t, reply{status: 400, body: `{"code":"bad_request"}`})
	c := r.client(t)
	if err := c.ReportInteractions("r", interactions()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return c.DroppedReports() == 1 })
}
