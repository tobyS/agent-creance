package sysdep

import (
	"errors"
	"testing"
)

// The real OSKeychain is deferred to WP-4.1; until then it must clearly report
// that via ErrNotImplemented rather than silently returning a zero value.

func TestOSKeychainFindGenericPasswordNotImplemented(t *testing.T) {
	var kc OSKeychain
	_, err := kc.FindGenericPassword("Claude Code-credentials", "toby")
	if !errors.Is(err, ErrNotImplemented) {
		t.Errorf("FindGenericPassword error = %v, want errors.Is(ErrNotImplemented)", err)
	}
}
