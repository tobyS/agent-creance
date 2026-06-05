package sysdep

import (
	"testing"
	"time"
)

// Smoke tests for the real OSClock against the wall clock. Like the other real
// impls, correctness is verified once here; logic packages use the FakeClock in
// sysdeptest.

func TestOSClockNowIsCurrent(t *testing.T) {
	var c OSClock
	before := time.Now()
	got := c.Now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Errorf("Now() = %v, want within [%v, %v]", got, before, after)
	}
}

func TestOSClockSinceIsNonNegative(t *testing.T) {
	var c OSClock
	start := c.Now()
	d := c.Since(start)
	if d < 0 {
		t.Errorf("Since(now) = %v, want >= 0", d)
	}
	if d > time.Minute {
		t.Errorf("Since(now) = %v, implausibly large", d)
	}
}
