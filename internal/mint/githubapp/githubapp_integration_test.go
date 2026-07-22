//go:build integration

// Integration test for the GitHub App installation-token minter (AC-0069a) against
// the REAL GitHub API. Gated behind the `integration` build tag and env vars so
// `make test` never runs it. It mints a real, repo-scoped installation token, proves
// it authenticates a REST call, then revokes it and proves it no longer does.
//
// It needs a GitHub App (webhooks may be off) installed on the target repo, and its
// PKCS#1 PEM private key reachable via a secret reference. Provide:
//
//	AC_TEST_GH_APP_KEY_REF   op:// / keychain:// / env:// reference to the PEM
//	AC_TEST_GH_APP_CLIENT_ID the App's client ID (JWT iss)
//	AC_TEST_GH_APP_REPO      owner/name the App is installed on
//
// Run with: make test-integration
package githubapp_test

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/mint/githubapp"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

func TestMintRevokeRealGitHub(t *testing.T) {
	keyRef := os.Getenv("AC_TEST_GH_APP_KEY_REF")
	clientID := os.Getenv("AC_TEST_GH_APP_CLIENT_ID")
	repo := os.Getenv("AC_TEST_GH_APP_REPO")
	if keyRef == "" || clientID == "" || repo == "" {
		t.Skip("set AC_TEST_GH_APP_KEY_REF, AC_TEST_GH_APP_CLIENT_ID, AC_TEST_GH_APP_REPO to exercise this test")
	}

	r := sysdep.OSSecretResolver{Commander: sysdep.ExecCommander{}, Keychain: sysdep.OSKeychain{}, Paths: sysdep.OSPathResolver{}}
	pem, err := r.Resolve(context.Background(), keyRef)
	require.NoError(t, err, "resolve the app private key")

	m := githubapp.New(githubapp.Config{
		PEM: pem, ClientID: clientID, Repo: repo,
		Permissions: map[string]string{"metadata": "read"},
	}, sysdep.OSHTTPClient{}, sysdep.OSClock{})

	ctx := context.Background()
	token, expiresAt, err := m.Mint(ctx)
	require.NoError(t, err, "mint an installation token")
	require.NotEmpty(t, token) // never logged
	require.True(t, expiresAt.After(time.Now()), "expiry is in the future")
	require.True(t, expiresAt.Before(time.Now().Add(2*time.Hour)), "installation tokens live ~1h")

	// The minted token authenticates a REST call to the repo.
	require.Equal(t, http.StatusOK, authGet(t, "https://api.github.com/repos/"+repo, token),
		"the minted token should authenticate a repo read")

	// After revocation the same token no longer authenticates.
	require.NoError(t, m.Revoke(ctx, token))
	require.Equal(t, http.StatusUnauthorized, authGet(t, "https://api.github.com/repos/"+repo, token),
		"a revoked token must stop authenticating")
}

// authGet issues an authenticated GitHub GET and returns the status code.
func authGet(t *testing.T, url, token string) int {
	t.Helper()
	status, _, err := sysdep.OSHTTPClient{}.Do(context.Background(), "GET", url, map[string]string{
		"Authorization":        "Bearer " + token,
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
		"User-Agent":           "agent-creance-integration-test",
	}, nil)
	require.NoError(t, err)
	return status
}
