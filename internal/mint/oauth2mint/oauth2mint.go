// Package oauth2mint mints short-lived OAuth2 access tokens from a stored refresh
// token (AC-0069a): one form-encoded refresh-grant POST per RFC 6749 §6. The refresh
// token never leaves the host — only the minted access token reaches the cage. If the
// provider rotates the refresh token (returns a new one), Mint surfaces it so the
// caller can persist the replacement; an invalid_grant (HTTP 400) is terminal and
// signals that re-authorization is required.
package oauth2mint

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/tobyS/agent-creance/internal/mint"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// Config is the resolved OAuth2 minting input: the refresh token is already resolved
// from the secret reference host-side; the rest is non-secret config.
type Config struct {
	RefreshToken  string
	ClientID      string
	TokenEndpoint string
	Scopes        []string
}

// Minter mints access tokens for one OAuth2 credential.
type Minter struct {
	cfg   Config
	http  sysdep.HTTPClient
	clock sysdep.Clock

	// refreshToken is the current refresh token; it is updated in place if the
	// provider rotates it on a grant. RotatedRefreshToken exposes the latest value.
	refreshToken string
	rotated      bool
}

var _ mint.Minter = (*Minter)(nil)

// New builds an OAuth2 minter over the given HTTP and Clock seams.
func New(cfg Config, httpClient sysdep.HTTPClient, clock sysdep.Clock) *Minter {
	return &Minter{cfg: cfg, http: httpClient, clock: clock, refreshToken: cfg.RefreshToken}
}

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"` // seconds
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

// Mint performs one refresh-grant POST and returns the access token and its expiry
// (computed from expires_in relative to the local receipt time — the standard
// clock-skew defense). A returned refresh_token replaces the stored one and is exposed
// via RotatedRefreshToken.
func (m *Minter) Mint(ctx context.Context) (string, time.Time, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", m.refreshToken)
	form.Set("client_id", m.cfg.ClientID)
	body := []byte(form.Encode())

	headers := map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
		"Accept":       "application/json",
	}
	status, respBody, err := m.http.Do(ctx, "POST", m.cfg.TokenEndpoint, headers, body)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("oauth2mint: refresh grant: %w", err)
	}
	if status == 400 {
		// invalid_grant (expired/revoked refresh token) is terminal: only re-auth
		// recovers. Surface a distinct, typed error so the refresher/doctor can guide
		// the user to `credential authorize` rather than retry forever.
		return "", time.Time{}, &InvalidGrantError{Body: snippet(respBody)}
	}
	if status != 200 {
		return "", time.Time{}, &APIError{URL: m.cfg.TokenEndpoint, Status: status, Body: snippet(respBody)}
	}

	var tr tokenResp
	if err := json.Unmarshal(respBody, &tr); err != nil {
		return "", time.Time{}, fmt.Errorf("oauth2mint: decode token response: %w", err)
	}
	if tr.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("oauth2mint: token response carried no access_token")
	}
	if tr.RefreshToken != "" && tr.RefreshToken != m.refreshToken {
		m.refreshToken = tr.RefreshToken
		m.rotated = true
	}
	// A missing expires_in is treated as an immediate expiry (0s) so the refresher
	// re-mints eagerly rather than trusting a token of unknown lifetime.
	exp := m.clock.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	return tr.AccessToken, exp, nil
}

// Revoke is a nil-op: this ticket does not revoke OAuth2 access tokens on teardown
// (they are short-lived and expire on their own).
func (m *Minter) Revoke(_ context.Context, _ string) error { return nil }

// RotatedRefreshToken returns (token, true) if the provider rotated the refresh token
// during a Mint, so the caller can persist the replacement (RFC 6749 §6). Google does
// not rotate today; this is the correct, defensive behavior regardless.
func (m *Minter) RotatedRefreshToken() (string, bool) {
	return m.refreshToken, m.rotated
}

// InvalidGrantError is a terminal refresh failure (HTTP 400 invalid_grant): the
// refresh token is expired/revoked and only re-authorization recovers.
type InvalidGrantError struct {
	Body string
}

func (e *InvalidGrantError) Error() string {
	return fmt.Sprintf("oauth2mint: refresh token rejected (invalid_grant) — re-authorize the credential: %s", e.Body)
}

// APIError is a non-200, non-400 token-endpoint response.
type APIError struct {
	URL    string
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("oauth2mint: POST %s returned %d: %s", e.URL, e.Status, e.Body)
}

func snippet(b []byte) string {
	const limit = 256
	s := strings.TrimSpace(string(b))
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}
