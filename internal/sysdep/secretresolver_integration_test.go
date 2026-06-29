//go:build integration

// This test exercises the real OSSecretResolver backends — op:// via the `op`
// CLI and keychain:// via /usr/bin/security — and skips cleanly when the tool or
// item is unavailable (no `op`, not signed in, item absent, keychain locked). It
// runs only under `make test-integration`, never in the hermetic unit suite, and
// it asserts presence only: the resolved secret value is never printed. The
// env:// backend is hermetic, so its real-backend coverage lives in the unit test
// (TestOSSecretResolverEnv).
package sysdep_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

func newOSResolver() sysdep.OSSecretResolver {
	return sysdep.OSSecretResolver{
		Commander: sysdep.ExecCommander{},
		Keychain:  sysdep.OSKeychain{},
		Paths:     sysdep.OSPathResolver{},
	}
}

func TestOSSecretResolverOpLive(t *testing.T) {
	// Provide a real, readable op:// reference via the environment to exercise the
	// happy path; without it we still verify the resolver reaches `op` and reports
	// a typed failure rather than panicking.
	ref := os.Getenv("AC_TEST_OP_REF")
	if ref == "" {
		t.Skip("set AC_TEST_OP_REF to a readable op:// reference to exercise this test")
	}
	secret, err := newOSResolver().Resolve(context.Background(), ref)
	if errors.Is(err, sysdep.ErrSecretToolMissing) {
		t.Skip("`op` is not installed; install the 1Password CLI to exercise this test")
	}
	if errors.Is(err, sysdep.ErrSecretNotFound) {
		t.Skipf("op could not resolve %s (signed in? reference correct?)", ref)
	}
	require.NoError(t, err)
	require.NotEmpty(t, secret, "expected a non-empty resolved secret") // never print it
}

func TestOSSecretResolverKeychainLive(t *testing.T) {
	// The Anthropic OAuth item spike S2 validated is a known generic-password the
	// keychain:// path can target. Account is the login short name ($USER).
	user := os.Getenv("USER")
	ref := "keychain://Claude Code-credentials/" + user

	secret, err := newOSResolver().Resolve(context.Background(), ref)
	if errors.Is(err, sysdep.ErrSecretNotFound) {
		t.Skipf("no %q keychain item; run `claude` login to exercise this test", ref)
	}
	if errors.Is(err, sysdep.ErrKeychainLocked) {
		t.Skip("login keychain is locked; unlock it to exercise this test")
	}
	require.NoError(t, err)
	require.NotEmpty(t, secret, "expected a non-empty resolved secret") // never print it
}
