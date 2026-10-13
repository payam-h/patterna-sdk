package patterna

import (
	"sync"
	"time"
)

// breaker is a consecutive-failure circuit breaker. Closed: calls pass and failures are counted.
// After `threshold` failures in a row it opens for `cooldown`: calls fail fast with
// ErrCircuitOpen. When the cooldown ends one probe call is let through (half-open); success
// closes it, failure opens it for another cooldown.
type breaker struct {
	mu        sync.Mutex
	threshold int
	cooldown  time.Duration
	now       func() time.Time
	onChange  func(open bool)

	failures  int
	open      bool
	openUntil time.Time
	probing   bool
}

func newBreaker(threshold int, cooldown time.Duration, now func() time.Time, onChange func(bool)) *breaker {
	return &breaker{threshold: threshold, cooldown: cooldown, now: now, onChange: onChange}
}

// allow reports whether a call may go out. A true result must be followed by exactly one record.
func (b *breaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.open {
		return true
	}
	if b.now().Before(b.openUntil) || b.probing {
		return false
	}
	b.probing = true
	return true
}

// record reports the outcome of an allowed call. neutral outcomes (the caller cancelled) release
// a probe slot without changing the state.
func (b *breaker) record(outcome callOutcome) {
	b.mu.Lock()
	var changed, nowOpen bool
	switch outcome {
	case outcomeSuccess:
		b.failures = 0
		b.probing = false
		if b.open {
			b.open, changed, nowOpen = false, true, false
		}
	case outcomeFailure:
		b.failures++
		if b.open {
			// The half-open probe failed: another full cooldown.
			b.probing = false
			b.openUntil = b.now().Add(b.cooldown)
		} else if b.failures >= b.threshold {
			b.open, changed, nowOpen = true, true, true
			b.openUntil = b.now().Add(b.cooldown)
		}
	case outcomeNeutral:
		b.probing = false
	}
	b.mu.Unlock()
	if changed && b.onChange != nil {
		b.onChange(nowOpen)
	}
}

func (b *breaker) isOpen() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.open
}

type callOutcome int

const (
	outcomeSuccess callOutcome = iota
	outcomeFailure
	outcomeNeutral
)
