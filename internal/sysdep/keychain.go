package sysdep

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

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

	// FindCertificate returns the PEM bytes of the login-keychain certificate
	// whose common name is commonName — the cheap "is the CA installed?" check the
	// run command uses (setup imports the mitmproxy CA into the login keychain). A
	// missing certificate yields ErrItemNotFound; a locked keychain yields
	// ErrKeychainLocked; callers distinguish these (and genuine failures) via
	// errors.Is. Presence proves setup imported the cert, not that the trust dialog
	// was confirmed — the robust live verification stays setup/doctor's job.
	FindCertificate(commonName string) ([]byte, error)
}

// Contract sentinels the Keychain seam models: these are the real outcomes a
// working implementation and the fake return.
var (
	// ErrItemNotFound means the requested generic-password item is absent.
	ErrItemNotFound = errors.New("sysdep: keychain item not found")
	// ErrKeychainLocked means the login keychain is locked and must be unlocked
	// interactively before the item can be read.
	ErrKeychainLocked = errors.New("sysdep: keychain is locked")
)

// errUnexpectedSecurity wraps a /usr/bin/security failure that is neither
// "item not found" nor a locked-keychain timeout — surfaced to callers (with
// the tool's stderr) so a genuine misconfiguration isn't silently swallowed.
var errUnexpectedSecurity = errors.New("sysdep: keychain lookup failed")

// securityFindTimeout bounds the find-generic-password call. A locked login
// keychain does not fail cleanly — securityd raises a blocking SecurityAgent
// unlock prompt out-of-process (spike S2 §4) — so an unbounded call could hang
// forever. We cap it well above the ~1s observed for a successful read and the
// 8s S2 used to characterize the locked prompt, then map the timeout to
// ErrKeychainLocked.
const securityFindTimeout = 10 * time.Second

// secItemNotFound is the exit code /usr/bin/security returns when the requested
// item is absent (errSecItemNotFound; observed in spike S2 §1).
const secItemNotFound = 44

// OSKeychain is the production Keychain. It shells out to /usr/bin/security
// (the legacy SecKeychain CLI), exactly the access path validated by spike S2 —
// no cgo / Security-framework binding. Its host-side job is detection: callers
// use the returned error (ErrItemNotFound / ErrKeychainLocked) more than the
// secret bytes; the in-cage agent does the actual token refresh.
type OSKeychain struct{}

var _ Keychain = (*OSKeychain)(nil)

func (OSKeychain) FindGenericPassword(service, account string) ([]byte, error) {
	// Service name alone is a unique lookup key (S2); pass -a only when an
	// account is supplied. -w prints the secret to stdout (honors the contract).
	args := []string{"find-generic-password", "-s", service}
	if account != "" {
		args = append(args, "-a", account)
	}
	args = append(args, "-w")
	return runSecurity(args)
}

func (OSKeychain) FindCertificate(commonName string) ([]byte, error) {
	// -c matches by common name; -p prints the matching certificate as PEM. We
	// only need presence, so the PEM bytes are returned as-is for the caller.
	return runSecurity([]string{"find-certificate", "-c", commonName, "-p"})
}

// runSecurity invokes /usr/bin/security with the given arguments under the
// find-timeout, returning the trimmed stdout on success or a mapped Keychain
// sentinel (ErrItemNotFound / ErrKeychainLocked / errUnexpectedSecurity) on
// failure. Both lookup methods share it so the timeout and exit-code mapping
// behave identically.
func runSecurity(args []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), securityFindTimeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "/usr/bin/security", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		// `security` appends a trailing newline after the payload (the secret for
		// -w, the PEM block for -p); strip it so the returned bytes are clean.
		return bytes.TrimSuffix(stdout.Bytes(), []byte("\n")), nil
	}

	if ctx.Err() == context.DeadlineExceeded {
		return nil, interpretSecurityErr(-1, true)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		mapped := interpretSecurityErr(exitErr.ExitCode(), false)
		if errors.Is(mapped, errUnexpectedSecurity) {
			return nil, fmt.Errorf("%w: exit %d: %s", errUnexpectedSecurity,
				exitErr.ExitCode(), bytes.TrimSpace(stderr.Bytes()))
		}
		return nil, mapped
	}
	return nil, fmt.Errorf("%w: %w", errUnexpectedSecurity, err)
}

// interpretSecurityErr maps a failed find-generic-password invocation to a
// Keychain sentinel. timedOut (a locked keychain's blocking prompt, S2 §4) →
// ErrKeychainLocked; exitCode 44 (errSecItemNotFound) → ErrItemNotFound;
// anything else → errUnexpectedSecurity. It is pure so the mapping is
// table-testable without invoking /usr/bin/security.
func interpretSecurityErr(exitCode int, timedOut bool) error {
	switch {
	case timedOut:
		return ErrKeychainLocked
	case exitCode == secItemNotFound:
		return ErrItemNotFound
	default:
		return errUnexpectedSecurity
	}
}
