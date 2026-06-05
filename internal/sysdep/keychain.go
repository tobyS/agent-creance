package sysdep

import "errors"

// Keychain abstracts reading a generic-password item from the macOS login
// Keychain — specifically the Anthropic OAuth credential (the login-keychain
// generic-password item "Claude Code-credentials", account = login short name).
// v0.1's host-side job is detection, not refresh: is the item present, absent,
// or is the keychain locked (the one non-interactive doctor failure)? The token
// refresh happens inside the cage via the agent's own ACL, not here.
//
// Why route this through the seam (for someone coming from PHP/TS): touching the
// real Keychain needs a logged-in macOS session, so logic that called the
// Security framework directly could not be unit-tested. Packages take a Keychain
// and call *that*; production wires OSKeychain, tests wire the fake in sysdeptest.
type Keychain interface {
	// FindGenericPassword returns the secret bytes of the login-keychain
	// generic-password item identified by service and account. A missing item
	// yields ErrItemNotFound; a locked keychain yields ErrKeychainLocked; callers
	// distinguish these (and genuine failures) via errors.Is.
	FindGenericPassword(service, account string) ([]byte, error)
}

// Contract sentinels the Keychain seam models (distinct from ErrNotImplemented,
// which marks a deferred production impl): these are the real outcomes a working
// implementation and the fake return.
var (
	// ErrItemNotFound means the requested generic-password item is absent.
	ErrItemNotFound = errors.New("sysdep: keychain item not found")
	// ErrKeychainLocked means the login keychain is locked and must be unlocked
	// interactively before the item can be read.
	ErrKeychainLocked = errors.New("sysdep: keychain is locked")
)

// OSKeychain is the production Keychain. Its real behaviour is deferred to WP-4.1
// (internal/cred): the implementation reads the item via the macOS Security
// framework and maps absent→ErrItemNotFound, locked→ErrKeychainLocked. Until
// then it returns ErrNotImplemented, so the compile-time assertion holds without
// pulling in a Security-framework/cgo dependency.
type OSKeychain struct{}

var _ Keychain = (*OSKeychain)(nil)

func (OSKeychain) FindGenericPassword(_, _ string) ([]byte, error) {
	return nil, ErrNotImplemented
}
