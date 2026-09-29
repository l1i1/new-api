package common

import (
	"sync"
	"time"
)

// LogBudget is a per-site rate limit for diagnostic log lines.
//
// A plain "first N lines" counter is not enough for a long-running process: once
// the quota is spent the site is silent forever, so a fault that starts on day
// two is invisible and a fault that was fixed cannot be confirmed by its log
// line coming back. A budget keeps the same burst — the first N lines are what a
// diagnosis actually needs — and then refills one line per interval, so a
// persistent fault stays visible at a low, bounded cost instead of either
// flooding or disappearing.
//
// The budget is process-local and safe for concurrent use.
type LogBudget struct {
	burst    int
	interval time.Duration

	mu     sync.Mutex
	tokens int
	last   time.Time
	// now is time.Now in production. It is a field so a test can advance the
	// clock deterministically instead of sleeping.
	now func() time.Time
}

// NewLogBudget returns a budget that allows burst lines immediately and then one
// more line per interval. A burst below 1 is raised to 1 so a site can never be
// silenced by a typo; an interval below 1ns is raised to 1ns so a caller cannot
// disable the limiter by passing zero.
func NewLogBudget(burst int, interval time.Duration) *LogBudget {
	if burst < 1 {
		burst = 1
	}
	if interval < time.Nanosecond {
		interval = time.Nanosecond
	}
	// last is left at its zero value on purpose: Allow anchors it to the first
	// call's clock, so a test can swap the clock before the first line without
	// having to know that the constructor read the real one.
	return &LogBudget{burst: burst, interval: interval, tokens: burst, now: time.Now}
}

// Allow reports whether one line may be written now, consuming a token when it
// returns true.
func (b *LogBudget) Allow() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if b.last.IsZero() {
		b.last = now
	}
	// Refill first, so an idle period restores the burst — a rare event must
	// never be suppressed — and so a partially drained budget regains its
	// allowance over time.
	if elapsed := now.Sub(b.last); elapsed >= b.interval {
		refill := int(elapsed / b.interval)
		if refill >= b.burst {
			// Enough time passed to refill completely. Anchoring at now instead
			// of accumulating keeps the leftover from granting a second burst.
			b.tokens = b.burst
			b.last = now
		} else {
			b.tokens += refill
			if b.tokens > b.burst {
				b.tokens = b.burst
			}
			b.last = b.last.Add(time.Duration(refill) * b.interval)
		}
	}
	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
}
