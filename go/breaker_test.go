package patterna

import (
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

func TestBreakerStates(t *testing.T) {
	type step struct {
		advance   time.Duration
		wantAllow bool
		record    callOutcome // recorded only when allowed
		wantOpen  bool
	}
	fail, ok, neutral := outcomeFailure, outcomeSuccess, outcomeNeutral
	tests := []struct {
		name  string
		steps []step
	}{
		{"opens after 5 consecutive failures", []step{
			{0, true, fail, false}, {0, true, fail, false}, {0, true, fail, false}, {0, true, fail, false},
			{0, true, fail, true},
			{0, false, 0, true},
			{29 * time.Second, false, 0, true},
		}},
		{"a success resets the count", []step{
			{0, true, fail, false}, {0, true, fail, false}, {0, true, fail, false}, {0, true, fail, false},
			{0, true, ok, false},
			{0, true, fail, false}, {0, true, fail, false}, {0, true, fail, false}, {0, true, fail, false},
			{0, true, fail, true},
		}},
		{"half-open probe success closes", []step{
			{0, true, fail, false}, {0, true, fail, false}, {0, true, fail, false}, {0, true, fail, false},
			{0, true, fail, true},
			{30 * time.Second, true, ok, false},
			{0, true, ok, false},
		}},
		{"half-open probe failure opens for another 30 s", []step{
			{0, true, fail, false}, {0, true, fail, false}, {0, true, fail, false}, {0, true, fail, false},
			{0, true, fail, true},
			{30 * time.Second, true, fail, true},
			{29 * time.Second, false, 0, true},
			{time.Second, true, ok, false},
		}},
		{"a cancelled probe frees the slot without closing", []step{
			{0, true, fail, false}, {0, true, fail, false}, {0, true, fail, false}, {0, true, fail, false},
			{0, true, fail, true},
			{30 * time.Second, true, neutral, true},
			{0, true, ok, false},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := &fakeClock{t: time.Unix(0, 0)}
			var changes []bool
			b := newBreaker(5, 30*time.Second, clock.now, func(open bool) { changes = append(changes, open) })
			for i, s := range tt.steps {
				clock.advance(s.advance)
				allowed := b.allow()
				if allowed != s.wantAllow {
					t.Fatalf("step %d: allow = %v, want %v", i, allowed, s.wantAllow)
				}
				if allowed {
					b.record(s.record)
				}
				if b.isOpen() != s.wantOpen {
					t.Fatalf("step %d: open = %v, want %v", i, b.isOpen(), s.wantOpen)
				}
			}
			_ = changes
		})
	}
}

func TestBreakerOnlyOneProbe(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	b := newBreaker(1, time.Second, clock.now, nil)
	b.allow()
	b.record(outcomeFailure)
	clock.advance(time.Second)
	if !b.allow() {
		t.Fatal("probe not allowed")
	}
	if b.allow() {
		t.Fatal("second concurrent probe allowed")
	}
}

func TestBreakerThroughClient(t *testing.T) {
	r := newRecorder(t, reply{status: 500, body: `{"code":"internal","request_id":"r"}`})
	clock := &fakeClock{t: time.Unix(0, 0)}
	var mu sync.Mutex
	var changes []string
	c := r.client(t, func(cfg *Config) {
		cfg.now = clock.now
		cfg.OnBreakerChange = func(scope string, open bool) {
			mu.Lock()
			defer mu.Unlock()
			if open {
				changes = append(changes, scope+":open")
			} else {
				changes = append(changes, scope+":closed")
			}
		}
	})
	ctx := t.Context()
	for i := range 5 {
		if _, err := c.Recommend(ctx, personalized()); errors.Is(err, ErrCircuitOpen) {
			t.Fatalf("call %d: breaker open too early", i)
		}
	}
	if _, err := c.Recommend(ctx, personalized()); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("6th call: err = %v, want circuit open", err)
	}
	if n := len(r.requests()); n != 5 {
		t.Fatalf("requests = %d, want 5 (the 6th must not reach the server)", n)
	}
	if !c.BreakerOpen("serve") || c.BreakerOpen("ingest") {
		t.Fatal("only the serve breaker should be open")
	}

	// Ingest has its own breaker: still reaches the server.
	if _, err := c.IngestEvents(ctx, []Event{ev("e1")}); errors.Is(err, ErrCircuitOpen) {
		t.Fatal("ingest blocked by the serve breaker")
	}

	r.mu.Lock()
	r.replies = []reply{{body: slateJSON}}
	r.mu.Unlock()
	clock.advance(30 * time.Second)
	if _, err := c.Recommend(ctx, personalized()); err != nil {
		t.Fatalf("probe after cooldown: %v", err)
	}
	if c.BreakerOpen("serve") {
		t.Fatal("breaker should close after a successful probe")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(changes) != 2 || changes[0] != "serve:open" || changes[1] != "serve:closed" {
		t.Errorf("changes = %v", changes)
	}
}
