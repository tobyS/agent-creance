package cli

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/broker"
	"github.com/tobyS/agent-creance/internal/mint"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// recordingMinter records the token it was asked to revoke.
type recordingMinter struct {
	revoked []string
}

func (m *recordingMinter) Mint(context.Context) (string, time.Time, error) {
	return "", time.Time{}, nil
}
func (m *recordingMinter) Revoke(_ context.Context, token string) error {
	m.revoked = append(m.revoked, token)
	return nil
}

var _ mint.Minter = (*recordingMinter)(nil)

// TestRevokeMintedRevokesGitHubTokens: on teardown, a minted GitHub token currently in
// the store is revoked best-effort; an OAuth2 minter is skipped (its Revoke is a
// nil-op), and a GitHub credential that never minted (absent from the store) is
// skipped.
func TestRevokeMintedRevokesGitHubTokens(t *testing.T) {
	store := broker.NewStore(&sysdeptest.FakeMemory{})
	store.Set("gh", []byte("ghs_live"), time.Now().Add(time.Hour))
	// "gh-never" has a minter but no store entry (its first mint failed).

	ghMinter := &recordingMinter{}
	neverMinter := &recordingMinter{}
	driveMinter := &recordingMinter{}
	minters := map[string]mint.Minter{
		"gh":       ghMinter,
		"gh-never": neverMinter,
		"drive":    driveMinter,
	}
	payload := broker.Payload{
		"gh":       {Kind: broker.KindGitHubApp},
		"gh-never": {Kind: broker.KindGitHubApp},
		"drive":    {Kind: broker.KindOAuth2},
	}

	app := &App{Stderr: &bytes.Buffer{}}
	revokeMinted(app, minters, store, payload)

	require.Equal(t, []string{"ghs_live"}, ghMinter.revoked, "the live GitHub token is revoked")
	require.Empty(t, neverMinter.revoked, "a GitHub credential that never minted is skipped")
	require.Empty(t, driveMinter.revoked, "OAuth2 revoke is skipped (nil-op)")
}

// TestRevokeMintedStaticOnlyRevokesNothing: a broker with no minters revokes nothing.
func TestRevokeMintedStaticOnlyRevokesNothing(t *testing.T) {
	store := broker.NewStore(&sysdeptest.FakeMemory{})
	store.Set("gh", []byte("static"), time.Time{})
	app := &App{Stderr: &bytes.Buffer{}}
	// No panic, no revocation attempted, and the custodied token is left intact.
	revokeMinted(app, map[string]mint.Minter{}, store, broker.Payload{"gh": {Kind: broker.KindStatic}})
	tok, _, ok := store.Get("gh")
	require.True(t, ok)
	require.Equal(t, "static", string(tok))
}
