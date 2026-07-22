package oauth2mint

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

const endpoint = "https://oauth2.googleapis.com/token"

func newMinter(http *sysdeptest.FakeHTTPClient, clock *sysdeptest.FakeClock) *Minter {
	return New(Config{
		RefreshToken:  "rt-original",
		ClientID:      "1234.apps.googleusercontent.com",
		TokenEndpoint: endpoint,
		Scopes:        []string{"https://www.googleapis.com/auth/drive.file"},
	}, http, clock)
}

func TestMint_HappyPath(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	clock := sysdeptest.NewFakeClock(now)
	http := sysdeptest.NewFakeHTTPClient().
		WithResponse("POST", endpoint, 200, []byte(`{"access_token":"at-123","expires_in":3600,"scope":"...","token_type":"Bearer"}`))
	m := newMinter(http, clock)

	tok, exp, err := m.Mint(context.Background())
	require.NoError(t, err)
	require.Equal(t, "at-123", tok)
	require.True(t, exp.Equal(now.Add(3600*time.Second)), "expiry = now + expires_in")

	// The refresh-grant body is correct and form-encoded.
	req := http.LastRequest()
	require.Equal(t, "application/x-www-form-urlencoded", req.Headers["Content-Type"])
	form, err := url.ParseQuery(string(req.Body))
	require.NoError(t, err)
	require.Equal(t, "refresh_token", form.Get("grant_type"))
	require.Equal(t, "rt-original", form.Get("refresh_token"))
	require.Equal(t, "1234.apps.googleusercontent.com", form.Get("client_id"))

	// No rotation in this response.
	_, rotated := m.RotatedRefreshToken()
	require.False(t, rotated)
}

func TestMint_RotatedRefreshTokenSurfaced(t *testing.T) {
	now := time.Now()
	http := sysdeptest.NewFakeHTTPClient().
		WithResponse("POST", endpoint, 200, []byte(`{"access_token":"at","expires_in":3600,"refresh_token":"rt-new"}`))
	m := newMinter(http, sysdeptest.NewFakeClock(now))

	_, _, err := m.Mint(context.Background())
	require.NoError(t, err)
	rt, rotated := m.RotatedRefreshToken()
	require.True(t, rotated)
	require.Equal(t, "rt-new", rt)

	// A subsequent Mint uses the rotated token.
	_, _, err = m.Mint(context.Background())
	require.NoError(t, err)
	form, _ := url.ParseQuery(string(http.LastRequest().Body))
	require.Equal(t, "rt-new", form.Get("refresh_token"))
}

func TestMint_InvalidGrantIsTerminal(t *testing.T) {
	http := sysdeptest.NewFakeHTTPClient().
		WithResponse("POST", endpoint, 400, []byte(`{"error":"invalid_grant"}`))
	m := newMinter(http, sysdeptest.NewFakeClock(time.Now()))

	_, _, err := m.Mint(context.Background())
	require.Error(t, err)
	var ige *InvalidGrantError
	require.ErrorAs(t, err, &ige)
}

func TestMint_OtherNon200IsAPIError(t *testing.T) {
	http := sysdeptest.NewFakeHTTPClient().
		WithResponse("POST", endpoint, 500, []byte(`upstream error`))
	m := newMinter(http, sysdeptest.NewFakeClock(time.Now()))

	_, _, err := m.Mint(context.Background())
	require.Error(t, err)
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, 500, apiErr.Status)
}

func TestMint_MissingAccessTokenIsError(t *testing.T) {
	http := sysdeptest.NewFakeHTTPClient().
		WithResponse("POST", endpoint, 200, []byte(`{"expires_in":3600}`))
	m := newMinter(http, sysdeptest.NewFakeClock(time.Now()))

	_, _, err := m.Mint(context.Background())
	require.ErrorContains(t, err, "no access_token")
}

func TestRevoke_IsNilOp(t *testing.T) {
	m := newMinter(sysdeptest.NewFakeHTTPClient(), sysdeptest.NewFakeClock(time.Now()))
	require.NoError(t, m.Revoke(context.Background(), "at"))
}
