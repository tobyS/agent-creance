package sysdep

import (
	"context"
	"time"
)

// Sleeper abstracts pausing for a duration, used for bounded polling (e.g.
// waiting for the mitmproxy CA file to appear after spawning mitmdump). Routing
// it through a seam keeps poll-loop logic unit-testable: the fake returns
// immediately, so a test exercising "not ready, then ready" does not sleep in
// real time.
//
// Why a seam rather than time.Sleep (for someone coming from PHP/TS): a direct
// time.Sleep makes a test pay the wall-clock delay and cannot be cancelled.
// Packages take a Sleeper and call *that*; production wires OSSleeper, tests wire
// the fake in sysdeptest.
type Sleeper interface {
	// Sleep blocks for d or until ctx is cancelled, whichever comes first. It
	// returns ctx.Err() if the context was cancelled, otherwise nil.
	Sleep(ctx context.Context, d time.Duration) error
}

// OSSleeper is the production Sleeper backed by a timer.
type OSSleeper struct{}

var _ Sleeper = (*OSSleeper)(nil)

func (OSSleeper) Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
