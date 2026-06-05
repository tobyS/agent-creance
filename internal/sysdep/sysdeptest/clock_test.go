package sysdeptest

import (
	"testing"
	"time"
)

func TestFakeClockNowReturnsCurrent(t *testing.T) {
	at := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	c := NewFakeClock(at)
	if got := c.Now(); !got.Equal(at) {
		t.Errorf("Now() = %v, want %v", got, at)
	}
}

func TestFakeClockSinceComputesAgainstCurrent(t *testing.T) {
	at := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	c := NewFakeClock(at)
	if got := c.Since(at.Add(-time.Hour)); got != time.Hour {
		t.Errorf("Since(now-1h) = %v, want %v", got, time.Hour)
	}
}

func TestFakeClockAdvanceMovesNow(t *testing.T) {
	at := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	c := NewFakeClock(at)
	c.Advance(90 * time.Minute)
	if got := c.Now(); !got.Equal(at.Add(90 * time.Minute)) {
		t.Errorf("Now() after Advance = %v, want %v", got, at.Add(90*time.Minute))
	}
}
