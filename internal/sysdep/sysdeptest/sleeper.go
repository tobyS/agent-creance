package sysdeptest

import (
	"context"
	"time"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// FakeSleeper is a Sleeper that returns immediately (no real delay) and records
// each requested duration in Sleeps, so a poll loop can be unit-tested without
// paying wall-clock time. If SleepErr is set it is returned instead, simulating a
// cancelled context.
type FakeSleeper struct {
	// Sleeps records each duration passed to Sleep, in order.
	Sleeps []time.Duration
	// SleepErr, if set, is returned by Sleep (e.g. to simulate ctx cancellation).
	SleepErr error
}

var _ sysdep.Sleeper = (*FakeSleeper)(nil)

func (f *FakeSleeper) Sleep(_ context.Context, d time.Duration) error {
	f.Sleeps = append(f.Sleeps, d)
	return f.SleepErr
}
