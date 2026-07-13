// Package broker is the host-side custodian of injected credentials (AC-0069b).
//
// It replaces Phase 1's one-shot inherited-fd delivery to the Python enforcer.
// The CLI resolves secrets on the proxy-spawn path (where an op:// reference may
// prompt for Touch ID) and hands them to a detached broker daemon over the same
// pipe; the broker holds them in mlock-ed, wipeable memory and serves them to the
// mitmproxy addon over a unix socket that the cage cannot reach. That gives the
// channel two properties the fd could not have: the served value can be *rotated*
// without restarting the proxy (which is what AC-0069a's minting needs), and the
// raw token lives in Go, reaching the Python addon only for as long as one request
// needs it.
//
// Custody is bounded, and the bound is deliberate: see sysdep.Memory for why mlock
// on macOS is hygiene rather than a control. What actually limits the damage of a
// leaked token is its scope and its TTL.
package broker

import (
	"sync"
	"time"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// entry is one custodied credential: the raw token plus the instant it dies.
// A zero expiresAt means "never expires" — the shape a statically resolved
// reference (op://, keychain://, env://) has. Minted tokens (AC-0069a) will carry
// a real expiry.
type entry struct {
	token     []byte
	expiresAt time.Time
}

// Store holds the current token for each credential name.
//
// Tokens are []byte, never string: a Go string is immutable and cannot be wiped,
// and every []byte(s) conversion silently leaves another copy behind.
//
// Set is the rotation entry point — replacing a credential wipes and unlocks the
// buffer it replaces, so a refresh loop can swap a token in place while requests
// are being served. The RWMutex is what makes that safe against the concurrent
// Gets of in-flight requests.
type Store struct {
	mem sysdep.Memory

	mu      sync.RWMutex
	entries map[string]entry
}

// NewStore returns an empty Store that applies mem's hygiene to every token it
// custodies.
func NewStore(mem sysdep.Memory) *Store {
	return &Store{mem: mem, entries: map[string]entry{}}
}

// Set custodies token under name, replacing (and wiping) any previous value.
// A zero expiresAt means the token does not expire.
//
// The store takes ownership of token: callers must not retain or reuse the slice.
// An mlock failure is not an error — it is logged nowhere and tolerated here,
// because failing to pin a page is not a reason to refuse to serve a credential
// the user asked us to inject. Custody is best-effort by construction.
func (s *Store) Set(name string, token []byte, expiresAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_ = s.mem.Lock(token)
	if old, ok := s.entries[name]; ok {
		s.release(old)
	}
	s.entries[name] = entry{token: token, expiresAt: expiresAt}
}

// Get returns the token custodied under name and its expiry. ok is false when no
// such credential is held.
//
// The returned slice aliases the stored buffer — callers must not modify it, and
// must not hold it across a Set of the same name.
func (s *Store) Get(name string) (token []byte, expiresAt time.Time, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	e, ok := s.entries[name]
	if !ok {
		return nil, time.Time{}, false
	}
	return e.token, e.expiresAt, true
}

// Wipe zeroes every custodied token, unlocks its pages, and empties the store.
// Called on broker shutdown so the secrets do not outlive the process image.
func (s *Store) Wipe() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for name, e := range s.entries {
		s.release(e)
		delete(s.entries, name)
	}
}

// release zeroes e's token and unlocks its pages. The caller holds the write lock.
// Order matters: wipe first, unlock second — unlocking first would let the kernel
// page the still-populated buffer out to swap in the window before the wipe.
func (s *Store) release(e entry) {
	wipe(e.token)
	_ = s.mem.Unlock(e.token)
}

// wipe zeroes b in place.
//
// Go offers no *guaranteed* zeroization primitive: the spec permits a compiler to
// elide a store to memory that is about to become unreachable, and stack copying
// can have left derived copies elsewhere regardless. In practice clear() lowers to
// a memclr on a slice whose backing array escapes into the store, which is not
// elided — and it is the honest best available. See sysdep.Memory for why this is
// hygiene, not a control.
func wipe(b []byte) { clear(b) }
