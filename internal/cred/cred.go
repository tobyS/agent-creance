// Package cred is agent-creance's host-side credential detector. v0.1 is
// OAuth-only and does no credential injection: the only credential in play is
// Claude Code's own OAuth token, which on macOS lives in the login Keychain
// (the generic-password item "Claude Code-credentials"), not a file.
//
// cred's job is detection, not refresh — the in-cage agent does the actual
// token refresh via its own Keychain ACL. Before launching a caged session,
// run/doctor ask cred whether the credential is reachable so they can refuse
// up front with a clear message instead of failing mid-session with a confusing
// TLS/auth error. The classification (spike S2, design.md "The proxy and the
// credential story"):
//
//   - Keychain item present                       → use it (StatusOK).
//   - login keychain locked                        → refuse (StatusLocked).
//   - item absent, ~/.claude/.credentials.json present → refuse: file-based
//     credentials are out of scope for v0.1 (StatusFileFallback).
//   - neither present                              → refuse: log in on the host
//     (StatusMissing).
//
// All Keychain access goes through the sysdep.Keychain seam, so detection is
// unit-testable without a logged-in macOS session.
package cred

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// KeychainService is the login-Keychain generic-password service name of the
// Anthropic OAuth credential (spike S2). The service name alone is a unique
// lookup key; the account (the login short name) is only a disambiguator.
const KeychainService = "Claude Code-credentials"

// credentialsFileRel is the file-based credential location cred refuses on,
// relative to the user's home directory.
var credentialsFileRel = filepath.Join(".claude", ".credentials.json")

// Status is the outcome of credential detection.
type Status int

const (
	// StatusOK means the Keychain item is present — caged sessions can use it.
	StatusOK Status = iota
	// StatusLocked means the login keychain is locked. S2 found this raises a
	// blocking unlock prompt rather than failing cleanly, so doctor/run must
	// pre-flight the unlock state; cred reports it as a distinct refusal.
	StatusLocked
	// StatusFileFallback means the Keychain item is absent but a file-based
	// ~/.claude/.credentials.json is present — unsupported in v0.1 (it can't be
	// refreshed under a read-only ~/.claude), so cred refuses.
	StatusFileFallback
	// StatusMissing means neither the Keychain item nor a credentials file is
	// present — the user has not logged in on the host.
	StatusMissing
)

// Result is the outcome of Detect.
type Result struct {
	Status Status
}

// OK reports whether the credential is usable (the Keychain item is present).
func (r Result) OK() bool { return r.Status == StatusOK }

// Message returns the human-facing refusal message for the result, or "" when
// the credential is OK. The strings are deterministic so they can be pinned with
// golden files.
func (r Result) Message() string {
	switch r.Status {
	case StatusOK:
		return ""
	case StatusLocked:
		return msgLocked
	case StatusFileFallback:
		return msgFileFallback
	case StatusMissing:
		return msgMissing
	default:
		return ""
	}
}

const (
	msgLocked = "The login keychain is locked, so the Claude credential can't be read. " +
		"Unlock your login keychain and retry."
	msgFileFallback = "A file-based Claude credential (~/.claude/.credentials.json) was found, " +
		"but caged sessions require a Keychain-stored credential. File-based credentials are not " +
		"supported in v0.1 (they can't be refreshed under a read-only ~/.claude). Run `claude` on " +
		"the host to log in to the Keychain."
	msgMissing = "No Claude credential found. Run `claude` on the host and log in before " +
		"starting a caged session."
)

// Detect classifies whether the Anthropic OAuth credential is reachable. Like
// prereq.Check, the expected outcomes (absent / locked / file-fallback) are data
// encoded in Result.Status, not errors; Detect returns a non-nil error only for
// a genuinely unexpected failure (a Keychain error that is neither absent nor
// locked, an unresolvable home directory, or an unexpected stat failure).
func Detect(kc sysdep.Keychain, fsys sysdep.FileSystem, paths sysdep.PathResolver) (Result, error) {
	// The login short name is the item's account (S2). Detection discards the
	// secret bytes — host-side never needs them.
	account := paths.Getenv("USER")
	switch _, err := kc.FindGenericPassword(KeychainService, account); {
	case err == nil:
		return Result{Status: StatusOK}, nil
	case errors.Is(err, sysdep.ErrKeychainLocked):
		return Result{Status: StatusLocked}, nil
	case errors.Is(err, sysdep.ErrItemNotFound):
		return detectFileFallback(fsys, paths)
	default:
		return Result{}, fmt.Errorf("cred: keychain lookup: %w", err)
	}
}

// detectFileFallback resolves the absent-Keychain case: a present
// ~/.claude/.credentials.json is the (unsupported) file-fallback refusal,
// otherwise the credential is simply missing. Presence is checked with Stat —
// cred never reads the file's contents.
func detectFileFallback(fsys sysdep.FileSystem, paths sysdep.PathResolver) (Result, error) {
	home, err := paths.UserHomeDir()
	if err != nil {
		return Result{}, fmt.Errorf("cred: resolve home dir: %w", err)
	}
	path := filepath.Join(home, credentialsFileRel)
	switch _, err := fsys.Stat(path); {
	case err == nil:
		return Result{Status: StatusFileFallback}, nil
	case errors.Is(err, fs.ErrNotExist):
		return Result{Status: StatusMissing}, nil
	default:
		return Result{}, fmt.Errorf("cred: stat %s: %w", path, err)
	}
}
