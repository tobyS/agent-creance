package broker

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/tobyS/agent-creance/internal/mint"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// Refresher drives one minted credential's lifecycle inside the broker (AC-0069a):
// mint the first token, then re-mint before expiry and swap it into the Store while
// requests are served. It is the only writer that gives a Store entry a non-zero
// expiry, which is what arms the Server's stale-then-472-at-expiry behavior (decision
// 3): as long as the loop keeps Set-ing a fresh expiry, requests succeed; once expiry
// passes without a successful refresh, the Server answers `expired` (→ 472) instead of
// serving a dead token upstream.
//
// It follows the internal/configwatch background-goroutine shape: a context-scoped
// loop over the sysdep.Sleeper (context-cancellable) and sysdep.Clock seams, so it is
// unit-testable against a FakeClock/FakeSleeper with no wall-clock time.
type Refresher struct {
	store   *Store
	clock   sysdep.Clock
	sleeper sysdep.Sleeper
	warn    func(string) // stderr; never a token

	// rotatePersist is called when an OAuth2 minter rotates its refresh token, so the
	// replacement can be persisted (Phase 5 wires the keychain write). Nil until then;
	// the rotation is logged either way. The token value is passed only to this
	// callback, never to warn.
	rotatePersist func(name, refreshToken string)
}

// Margins tunes the refresh schedule for one credential kind.
type Margins struct {
	// Early is how far before expiry to proactively re-mint (keep-hot).
	Early time.Duration
	// Jitter is the maximum +/- random offset applied to the sleep, spreading
	// refreshes so concurrent credentials do not stampede the token endpoint.
	Jitter time.Duration
	// Backoff is the retry interval after a failed mint.
	Backoff time.Duration
}

// Default margins per kind (research: ghinstallation re-mints at expiry−60s;
// x/oauth2 defaults to −10s). We keep hot with a wider margin so a slow endpoint
// never makes a request wait: GitHub's token lives 1h, so 5min leaves ten
// backoff-retries before expiry; the OAuth2 access token lives ~1h too but the grant
// is a single fast POST, so 2min suffices.
var (
	GitHubMargins = Margins{Early: 5 * time.Minute, Jitter: 30 * time.Second, Backoff: 30 * time.Second}
	OAuth2Margins = Margins{Early: 2 * time.Minute, Jitter: 30 * time.Second, Backoff: 30 * time.Second}
)

// NewRefresher builds a Refresher over the given store and seams. warn receives
// human-readable, token-free progress/failure messages.
func NewRefresher(store *Store, clock sysdep.Clock, sleeper sysdep.Sleeper, warn func(string)) *Refresher {
	if warn == nil {
		warn = func(string) {}
	}
	return &Refresher{store: store, clock: clock, sleeper: sleeper, warn: warn}
}

// WithRotatePersist sets the callback invoked with a rotated OAuth2 refresh token.
func (r *Refresher) WithRotatePersist(fn func(name, refreshToken string)) *Refresher {
	r.rotatePersist = fn
	return r
}

// rotatingMinter is the optional capability an OAuth2 minter exposes when it may have
// rotated its refresh token during a Mint.
type rotatingMinter interface {
	RotatedRefreshToken() (string, bool)
}

// Run mints name's token and keeps it fresh until ctx is cancelled. It returns when
// ctx is done. On a mint failure it never overwrites the previously-served token
// (leaving it stale-but-valid until its own expiry), warns, and retries on Backoff so
// a transient failure self-heals.
func (r *Refresher) Run(ctx context.Context, name string, m mint.Minter, margins Margins) {
	for {
		token, expiresAt, err := m.Mint(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Do not touch the store: an initial failure leaves the credential
			// unknown (472 unknown_credential); a later failure leaves the previous
			// token in place until its own expiry (472 expired thereafter).
			r.warn(fmt.Sprintf("credential %q: mint failed (%v); retrying in %s", name, err, margins.Backoff))
			if r.sleeper.Sleep(ctx, margins.Backoff) != nil {
				return
			}
			continue
		}

		r.store.Set(name, []byte(token), expiresAt)
		r.handleRotation(name, m)

		if r.sleeper.Sleep(ctx, r.untilRefresh(expiresAt, margins)) != nil {
			return
		}
	}
}

// handleRotation persists (or logs) a rotated OAuth2 refresh token surfaced by the
// minter. The token value is never passed to warn.
func (r *Refresher) handleRotation(name string, m mint.Minter) {
	rm, ok := m.(rotatingMinter)
	if !ok {
		return
	}
	rt, rotated := rm.RotatedRefreshToken()
	if !rotated {
		return
	}
	if r.rotatePersist != nil {
		r.rotatePersist(name, rt)
		return
	}
	r.warn(fmt.Sprintf("credential %q: refresh token was rotated by the provider (persistence not yet configured)", name))
}

// untilRefresh returns how long to sleep before the next re-mint: expiry minus the
// early margin, plus a random jitter in [−Jitter, +Jitter], floored at zero (re-mint
// immediately if already inside the early window).
func (r *Refresher) untilRefresh(expiresAt time.Time, margins Margins) time.Duration {
	d := expiresAt.Sub(r.clock.Now()) - margins.Early
	if margins.Jitter > 0 {
		d += time.Duration(rand.Int64N(int64(2*margins.Jitter))) - margins.Jitter
	}
	if d < 0 {
		return 0
	}
	return d
}
