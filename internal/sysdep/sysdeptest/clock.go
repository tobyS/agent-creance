package sysdeptest

import (
	"time"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// FakeClock is a Clock frozen at Current. Now returns Current verbatim and Since
// is computed against it, so cache-expiry and timestamp logic become fully
// deterministic. Advance moves the clock forward to exercise "time has passed"
// branches without sleeping.
type FakeClock struct {
	// Current is the instant Now returns. The zero value is the zero time.
	Current time.Time
}

var _ sysdep.Clock = (*FakeClock)(nil)

// NewFakeClock returns a clock frozen at t.
func NewFakeClock(t time.Time) *FakeClock { return &FakeClock{Current: t} }

func (f *FakeClock) Now() time.Time { return f.Current }

func (f *FakeClock) Since(t time.Time) time.Duration { return f.Current.Sub(t) }

// Advance moves the fake clock forward by d.
func (f *FakeClock) Advance(d time.Duration) { f.Current = f.Current.Add(d) }
