package common

import (
	"testing"
	"time"
)

// fakeClock drives a LogBudget without sleeping, so the limiter's behaviour is
// asserted rather than raced.
func fakeClock() (*time.Time, func() time.Time) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	return &now, func() time.Time { return now }
}

func TestLogBudgetAllowsTheBurstThenRefills(t *testing.T) {
	now, clock := fakeClock()
	budget := NewLogBudget(2, time.Minute)
	budget.now = clock

	if !budget.Allow() || !budget.Allow() {
		t.Fatal("the first burst lines must be allowed")
	}
	if budget.Allow() {
		t.Fatal("the budget must be exhausted after the burst")
	}

	// Time moves on: the site comes back instead of staying silent forever,
	// which is the whole point of a budget over a spent counter.
	*now = now.Add(time.Minute)
	if !budget.Allow() {
		t.Fatal("a refilled token must be allowed")
	}
	if budget.Allow() {
		t.Fatal("the refill is one line per interval, not a new burst")
	}

	// A long silence restores the full burst.
	*now = now.Add(time.Hour)
	if !budget.Allow() || !budget.Allow() {
		t.Fatal("an idle period must restore the burst")
	}
	if budget.Allow() {
		t.Fatal("a restored burst is still a burst, not an exemption")
	}
	// Anchoring the refill at the current time must not hand out a second burst
	// immediately after the first one was drained.
	*now = now.Add(2 * time.Minute)
	if !budget.Allow() || !budget.Allow() {
		t.Fatal("two intervals must refill two tokens")
	}
	if budget.Allow() {
		t.Fatal("the leftover interval must not have banked extra tokens")
	}
}

func TestLogBudgetClampsDegenerateArguments(t *testing.T) {
	now, clock := fakeClock()
	budget := NewLogBudget(0, 0)
	budget.now = clock
	if !budget.Allow() {
		t.Fatal("a zero burst must still allow one line")
	}
	if budget.Allow() {
		t.Fatal("a zero burst must not allow two lines")
	}
	*now = now.Add(time.Nanosecond)
	if !budget.Allow() {
		t.Fatal("a zero interval is raised to one nanosecond, so time alone refills")
	}
}

func TestNilLogBudgetDeniesEverything(t *testing.T) {
	var budget *LogBudget
	if budget.Allow() {
		t.Fatal("a nil budget must never write")
	}
}
