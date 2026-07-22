package broker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// scriptMinter is a fake mint.Minter driven by a scripted list of results, consumed in
// order (the last result repeats once exhausted). It optionally cancels a context
// after a given number of Mint calls so a FakeSleeper-driven loop terminates
// deterministically.
type scriptMinter struct {
	mu       sync.Mutex
	results  []mintResult
	calls    int
	stopAt   int
	cancel   context.CancelFunc
	rotateTo string // if set, RotatedRefreshToken reports rotation after first mint

	revoked      []string
	rotatedToken string
	didRotate    bool
}

type mintResult struct {
	token string
	exp   time.Time
	err   error
}

func (m *scriptMinter) Mint(ctx context.Context) (string, time.Time, error) {
	// A real minter's HTTP call fails when the context is cancelled; mirror that so a
	// FakeSleeper-driven loop (which never observes cancellation itself) terminates.
	if ctx.Err() != nil {
		return "", time.Time{}, ctx.Err()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.calls
	if idx >= len(m.results) {
		idx = len(m.results) - 1
	}
	res := m.results[idx]
	m.calls++
	if res.err == nil && m.rotateTo != "" && !m.didRotate {
		m.didRotate = true
		m.rotatedToken = m.rotateTo
	}
	if m.stopAt > 0 && m.calls >= m.stopAt && m.cancel != nil {
		m.cancel()
	}
	return res.token, res.exp, res.err
}

func (m *scriptMinter) Revoke(_ context.Context, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revoked = append(m.revoked, token)
	return nil
}

func (m *scriptMinter) RotatedRefreshToken() (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rotatedToken, m.didRotate
}

// noJitter margins make the sleep durations deterministic for assertions.
func noJitter(early, backoff time.Duration) Margins {
	return Margins{Early: early, Jitter: 0, Backoff: backoff}
}

func newTestRefresher(store *Store, clock *sysdeptest.FakeClock, sleeper *sysdeptest.FakeSleeper) *Refresher {
	return NewRefresher(store, clock, sleeper, nil)
}

func TestRefresher_InitialMintSetsTokenAndRefreshesBeforeExpiry(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	clock := sysdeptest.NewFakeClock(now)
	sleeper := &sysdeptest.FakeSleeper{}
	store := NewStore(&sysdeptest.FakeMemory{})

	ctx, cancel := context.WithCancel(context.Background())
	exp := now.Add(time.Hour)
	m := &scriptMinter{
		results: []mintResult{{token: "tok-1", exp: exp}},
		stopAt:  3, // let the loop mint three times, then cancel
		cancel:  cancel,
	}
	r := newTestRefresher(store, clock, sleeper)
	r.Run(ctx, "gh", m, noJitter(5*time.Minute, 30*time.Second))

	// The token is custodied with its expiry, so the Server can enforce it.
	tok, gotExp, ok := store.Get("gh")
	require.True(t, ok)
	require.Equal(t, "tok-1", string(tok))
	require.True(t, gotExp.Equal(exp))

	// It slept toward (expiry − early) each cycle: 1h − 5min = 55min.
	require.NotEmpty(t, sleeper.Sleeps)
	require.Equal(t, 55*time.Minute, sleeper.Sleeps[0], "first refresh scheduled at expiry−early")
}

func TestRefresher_FailedRemintLeavesOldTokenThenServerExpires(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	clock := sysdeptest.NewFakeClock(now)
	sleeper := &sysdeptest.FakeSleeper{}
	store := NewStore(&sysdeptest.FakeMemory{})

	ctx, cancel := context.WithCancel(context.Background())
	exp := now.Add(time.Hour)
	// Mint #1 succeeds; every subsequent mint fails.
	m := &scriptMinter{
		results: []mintResult{{token: "tok-1", exp: exp}, {err: errors.New("boom")}},
		stopAt:  3, // one success + two failures, then cancel
		cancel:  cancel,
	}
	r := newTestRefresher(store, clock, sleeper)
	r.Run(ctx, "gh", m, noJitter(5*time.Minute, 30*time.Second))

	// The old, still-valid token is untouched by the failed re-mints.
	tok, gotExp, ok := store.Get("gh")
	require.True(t, ok)
	require.Equal(t, "tok-1", string(tok))
	require.True(t, gotExp.Equal(exp))

	// A backoff sleep (30s) followed the failure, not another 55min refresh sleep.
	require.Contains(t, sleeper.Sleeps, 30*time.Second, "failed mint retries on backoff")

	// Past expiry, the Server answers `expired` (decision 3), not a dead token.
	clock.Advance(2 * time.Hour)
	resp := NewServer(store, clock).answer(Request{Credential: "gh"})
	require.Equal(t, ErrExpired, resp.Error)
	require.Empty(t, resp.Token)
}

func TestRefresher_InitialMintFailureLeavesCredentialUnknown(t *testing.T) {
	now := time.Now()
	clock := sysdeptest.NewFakeClock(now)
	sleeper := &sysdeptest.FakeSleeper{}
	store := NewStore(&sysdeptest.FakeMemory{})

	ctx, cancel := context.WithCancel(context.Background())
	m := &scriptMinter{
		results: []mintResult{{err: errors.New("down")}},
		stopAt:  2, // two failed attempts, then cancel
		cancel:  cancel,
	}
	newTestRefresher(store, clock, sleeper).Run(ctx, "gh", m, noJitter(5*time.Minute, 30*time.Second))

	// Never minted: the credential is unknown, which the Server maps to 472.
	_, _, ok := store.Get("gh")
	require.False(t, ok)
	require.Equal(t, ErrUnknownCredential, NewServer(store, clock).answer(Request{Credential: "gh"}).Error)
}

func TestRefresher_ContextCancelStopsWithoutWallClock(t *testing.T) {
	now := time.Now()
	clock := sysdeptest.NewFakeClock(now)
	sleeper := &sysdeptest.FakeSleeper{}
	store := NewStore(&sysdeptest.FakeMemory{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before Run

	m := &scriptMinter{results: []mintResult{{token: "t", exp: now.Add(time.Hour)}}}
	done := make(chan struct{})
	go func() {
		newTestRefresher(store, clock, sleeper).Run(ctx, "gh", m, noJitter(5*time.Minute, 30*time.Second))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return promptly on a cancelled context")
	}
}

func TestRefresher_RotatedRefreshTokenPersisted(t *testing.T) {
	now := time.Now()
	clock := sysdeptest.NewFakeClock(now)
	sleeper := &sysdeptest.FakeSleeper{}
	store := NewStore(&sysdeptest.FakeMemory{})

	ctx, cancel := context.WithCancel(context.Background())
	m := &scriptMinter{
		results:  []mintResult{{token: "at", exp: now.Add(time.Hour)}},
		rotateTo: "rt-new",
		stopAt:   2,
		cancel:   cancel,
	}

	var gotName, gotToken string
	r := NewRefresher(store, clock, sleeper, nil).
		WithRotatePersist(func(name, refreshToken string) {
			gotName, gotToken = name, refreshToken
		})
	r.Run(ctx, "drive", m, noJitter(2*time.Minute, 30*time.Second))

	require.Equal(t, "drive", gotName)
	require.Equal(t, "rt-new", gotToken)
}
