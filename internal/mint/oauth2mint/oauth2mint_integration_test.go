//go:build integration

// Integration test for the OAuth2 refresh-grant minter (AC-0069a) against a REAL
// token endpoint (Google by default). Gated behind the `integration` build tag and
// env vars so `make test` never runs it. It refreshes a real access token from a
// stored refresh token and asserts it is present with a future expiry.
//
// Obtain the refresh token first with `agent-creance credential authorize <name>`,
// then point the env at it:
//
//	AC_TEST_DRIVE_REFRESH_REF  op:// / keychain:// / env:// reference to the refresh token
//	AC_TEST_DRIVE_CLIENT_ID    the OAuth2 client ID
//
// Run with: make test-integration
package oauth2mint_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/mint/oauth2mint"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

func TestRefreshRealGoogle(t *testing.T) {
	refreshRef := os.Getenv("AC_TEST_DRIVE_REFRESH_REF")
	clientID := os.Getenv("AC_TEST_DRIVE_CLIENT_ID")
	if refreshRef == "" || clientID == "" {
		t.Skip("set AC_TEST_DRIVE_REFRESH_REF and AC_TEST_DRIVE_CLIENT_ID to exercise this test")
	}

	r := sysdep.OSSecretResolver{Commander: sysdep.ExecCommander{}, Keychain: sysdep.OSKeychain{}, Paths: sysdep.OSPathResolver{}}
	rt, err := r.Resolve(context.Background(), refreshRef)
	require.NoError(t, err, "resolve the refresh token")

	m := oauth2mint.New(oauth2mint.Config{
		RefreshToken:  string(rt),
		ClientID:      clientID,
		TokenEndpoint: config.DefaultOAuth2TokenEndpoint,
		Scopes:        []string{config.DefaultOAuth2Scope},
	}, sysdep.OSHTTPClient{}, sysdep.OSClock{})

	token, expiresAt, err := m.Mint(context.Background())
	require.NoError(t, err, "refresh an access token")
	require.NotEmpty(t, token) // never logged
	require.True(t, expiresAt.After(time.Now()), "expiry is in the future")
	require.True(t, expiresAt.Before(time.Now().Add(2*time.Hour)), "Google access tokens live ~1h")
}
