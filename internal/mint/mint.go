// Package mint produces short-lived credential tokens host-side (AC-0069a): a
// GitHub App installation token (JWT-sign the app private key → repo-scoped ≤1h
// token) and an OAuth2 refresh-grant access token. It is deliberately self-contained
// — it depends only on the sysdep HTTP and Clock seams, never on the broker — so the
// broker (which owns the refresh loop) can import it without a cycle and every minter
// is unit-tested against a fake transport and a fake clock.
//
// A minter never logs or embeds the token it produces in an error: a mint failure has
// no token yet, and Revoke takes the token as an argument but keeps it out of any
// returned error. Tokens are treated as opaque strings (GitHub's 2026 ~520-char
// `ghs_…` format is just a longer opaque string).
package mint

import (
	"context"
	"time"
)

// Minter produces a short-lived token and, where the provider supports it, revokes
// one. Mint returns the token and its absolute expiry; the refresher (broker) drives
// the schedule and re-Mints before expiry. Revoke is best-effort teardown and is a
// nil-op for providers with no revocation endpoint (OAuth2 here).
type Minter interface {
	Mint(ctx context.Context) (token string, expiresAt time.Time, err error)
	Revoke(ctx context.Context, token string) error
}
