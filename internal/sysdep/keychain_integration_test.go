//go:build integration

// This test exercises the real OSKeychain against the live login Keychain via
// /usr/bin/security — the access path spike S2 (AC-0002) validated. It satisfies
// AC-0022's verification step 4 ("on a real machine, detection finds the actual
// item"). It runs only under `make test-integration` (the integration build
// tag), never in the hermetic unit suite, and skips cleanly when the Anthropic
// OAuth item is not present (e.g. CI, or a machine where `claude` login hasn't
// run). The service name is hard-coded here rather than imported from
// internal/cred to keep sysdep free of an upward dependency; it is the value
// resolved by S2.
package sysdep_test

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

func TestOSKeychainFindGenericPasswordLive(t *testing.T) {
	const service = "Claude Code-credentials" // spike S2; service name is a unique key.
	account := os.Getenv("USER")              // the login short name (S2: acct = $(id -un)).

	secret, err := sysdep.OSKeychain{}.FindGenericPassword(service, account)
	if errors.Is(err, sysdep.ErrItemNotFound) {
		t.Skipf("no %q credential on this machine; run `claude` login to exercise this test", service)
	}
	if errors.Is(err, sysdep.ErrKeychainLocked) {
		t.Skip("login keychain is locked; unlock it to exercise this test")
	}
	require.NoError(t, err)
	// Assert presence only — never print the secret (S2 secret hygiene).
	require.NotEmpty(t, secret, "expected a non-empty credential payload")
}
