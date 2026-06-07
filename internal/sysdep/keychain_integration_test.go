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

// TestOSKeychainFindCertificateLive exercises the real find-certificate path the
// run command's setup-precondition check uses. The firm assertion is the absent
// case: a bogus common name must map to ErrItemNotFound — this confirms the exit
// code (errSecItemNotFound = 44) the cheap CA check depends on. The positive case
// is best-effort: find-certificate searches the default keychain list (login +
// search list), which is exactly where setup installs the mitmproxy CA, but on a
// host where setup has not run there is no guaranteed-present cert to match, so we
// only assert when the mitmproxy CA happens to be there.
func TestOSKeychainFindCertificateLive(t *testing.T) {
	_, err := sysdep.OSKeychain{}.FindCertificate("definitely-not-a-real-cert-cn-xyzzy")
	if errors.Is(err, sysdep.ErrKeychainLocked) {
		t.Skip("login keychain is locked; unlock it to exercise this test")
	}
	require.ErrorIs(t, err, sysdep.ErrItemNotFound,
		"an absent common name must map to ErrItemNotFound (exit 44)")

	switch pem, err := (sysdep.OSKeychain{}).FindCertificate("mitmproxy"); {
	case errors.Is(err, sysdep.ErrItemNotFound):
		t.Log("mitmproxy CA not in the keychain (setup not run); skipping the found assertion")
	case err != nil:
		t.Fatalf("FindCertificate(mitmproxy): %v", err)
	default:
		require.NotEmpty(t, pem, "expected a PEM certificate block for the mitmproxy CA")
	}
}
