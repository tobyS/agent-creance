package sysdeptest

import (
	"context"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// FakeListenerScanner is a scripted ListenerScanner. Listeners returns List
// (optionally Err) so a test can drive the exposed-service scan without lsof.
type FakeListenerScanner struct {
	// List is returned by Listeners when Err is nil.
	List []sysdep.Listener
	// Err, if set, is returned by Listeners (the scan could not run).
	Err error
	// Calls counts Listeners invocations.
	Calls int
}

var _ sysdep.ListenerScanner = (*FakeListenerScanner)(nil)

func (f *FakeListenerScanner) Listeners(_ context.Context) ([]sysdep.Listener, error) {
	f.Calls++
	if f.Err != nil {
		return nil, f.Err
	}
	return f.List, nil
}
