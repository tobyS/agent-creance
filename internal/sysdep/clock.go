package sysdep

import "time"

// Clock abstracts reading the current time, so logic with time-dependent
// behaviour (the 30-day registry-cache expiry, audit-log timestamps) can be
// unit-tested deterministically instead of racing the real wall clock.
//
// Why route time through the seam (for someone coming from PHP/TS): calling
// time.Now() directly makes a test depend on when it runs. Packages take a Clock
// and call *that*; production wires OSClock, tests wire the fake in sysdeptest.
type Clock interface {
	// Now returns the current local time, mirroring time.Now.
	Now() time.Time
	// Since returns the time elapsed since t, mirroring time.Since (i.e.
	// Now().Sub(t)). Provided as a convenience so callers need not hold a Now().
	Since(t time.Time) time.Duration
}

// OSClock is the production Clock backed by the time package.
//
// The compile-time assertion mirrors the Commander idiom: assigning to the blank
// identifier forces the compiler to verify *OSClock satisfies the interface, so a
// signature drift breaks the build here with a clear message.
type OSClock struct{}

var _ Clock = (*OSClock)(nil)

func (OSClock) Now() time.Time { return time.Now() }

func (OSClock) Since(t time.Time) time.Duration { return time.Since(t) }
