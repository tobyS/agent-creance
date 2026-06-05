package sysdep

import (
	"errors"
	"testing"
)

// The real OSFlock is deferred to WP-3.4; until then it must report that via
// ErrNotImplemented rather than returning a nil release func with a nil error.

func TestOSFlockAcquireNotImplemented(t *testing.T) {
	var fl OSFlock
	release, err := fl.Acquire("/tmp/proxy.lock")
	if !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Acquire error = %v, want errors.Is(ErrNotImplemented)", err)
	}
	if release != nil {
		t.Error("Acquire release != nil, want nil on error")
	}
}
