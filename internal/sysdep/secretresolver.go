package sysdep

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
)

// SecretResolver resolves a secret *reference* to the secret value it points at,
// host-side, holding the value in memory only — never written to disk, an argv,
// or a logged field. It is the foundation of credential injection (AC-0068): the
// long-lived secret stays in the user's store (1Password / macOS Keychain / the
// process environment) and only the resolved value lives transiently in memory.
//
// Three reference schemes are supported:
//
//   - op://<vault>/<item>[/<section>]/<field> — a 1Password secret reference,
//     resolved via the `op` CLI (`op read`). The reference is forwarded verbatim;
//     `op` validates its own grammar.
//   - keychain://<service>[/<account>] — a macOS login-keychain generic-password
//     item, resolved via the existing Keychain seam.
//   - env://<NAME> — the value of host environment variable NAME.
//
// Why route this through a seam (for someone coming from PHP/TS): resolving op://
// shells out to `op` and keychain:// touches the real Keychain, so logic that did
// this directly could not be unit-tested. Packages take a SecretResolver and call
// *that*; production wires OSSecretResolver, tests wire the fake in sysdeptest.
type SecretResolver interface {
	// Resolve resolves ref to its secret bytes, held in memory only. ctx bounds a
	// slow backend (`op read` is 200–500ms). An unsupported scheme yields
	// ErrUnknownSecretScheme; a backend tool that is not installed yields
	// ErrSecretToolMissing; a reference that does not resolve (item/field/var
	// absent, tool failure) yields ErrSecretNotFound; a locked keychain yields
	// ErrKeychainLocked. Errors never include the resolved secret value.
	Resolve(ctx context.Context, ref string) ([]byte, error)
}

// Contract sentinels the SecretResolver seam models. Callers distinguish these
// (and genuine failures) via errors.Is. keychain:// resolution also reuses the
// Keychain seam's ErrKeychainLocked (a locked store is recoverable by the human,
// which downstream injection surfaces as the 472 "unlock your secret store"
// signal).
var (
	// ErrUnknownSecretScheme means the reference does not start with a supported
	// scheme (op:// / keychain:// / env://).
	ErrUnknownSecretScheme = errors.New("sysdep: unknown secret reference scheme")
	// ErrSecretNotFound means a syntactically valid reference did not resolve to a
	// value: the 1Password item/field, keychain item, or environment variable is
	// absent (or the backend tool failed without a more specific signal).
	ErrSecretNotFound = errors.New("sysdep: secret not found")
	// ErrSecretToolMissing means the backend CLI a scheme needs (`op` for op://)
	// is not installed on PATH.
	ErrSecretToolMissing = errors.New("sysdep: secret backend tool not installed")
)

// Secret reference scheme prefixes.
const (
	opScheme       = "op://"
	keychainScheme = "keychain://"
	envScheme      = "env://"
)

// op CLI invocation. `op read` takes the whole op:// reference as a single
// argument and prints only the secret to stdout; --no-newline suppresses the
// trailing newline it would otherwise append.
const (
	opBinary    = "op"
	opReadCmd   = "read"
	opNoNewline = "--no-newline"
)

// OSSecretResolver is the production SecretResolver. It composes existing sysdep
// seams rather than calling the OS directly: `op read` goes through the Commander
// seam's secret-safe OutputStdout (stdout-only, so an `op` stderr notice never
// corrupts the secret), keychain:// reuses the Keychain seam (inheriting its
// locked/not-found detection), and env:// reads through the PathResolver seam.
type OSSecretResolver struct {
	Commander Commander
	Keychain  Keychain
	Paths     PathResolver
}

var _ SecretResolver = (*OSSecretResolver)(nil)

func (r OSSecretResolver) Resolve(ctx context.Context, ref string) ([]byte, error) {
	switch {
	case strings.HasPrefix(ref, opScheme):
		return r.resolveOp(ctx, ref)
	case strings.HasPrefix(ref, keychainScheme):
		return r.resolveKeychain(ref)
	case strings.HasPrefix(ref, envScheme):
		return r.resolveEnv(ref)
	default:
		return nil, ErrUnknownSecretScheme
	}
}

// resolveOp forwards the whole op:// reference to `op read`. It checks PATH first
// so a missing `op` maps to ErrSecretToolMissing (actionable: install/sign in)
// rather than the generic ErrSecretNotFound. Any read failure maps to
// ErrSecretNotFound; the underlying Commander error already carries `op`'s stderr
// (never the secret), so it is preserved for diagnosis without leaking the value.
func (r OSSecretResolver) resolveOp(ctx context.Context, ref string) ([]byte, error) {
	if _, err := r.Commander.LookPath(opBinary); err != nil {
		return nil, fmt.Errorf("%w: %q", ErrSecretToolMissing, opBinary)
	}
	out, err := r.Commander.OutputStdout(ctx, opBinary, opReadCmd, opNoNewline, ref)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrSecretNotFound, ref, err)
	}
	// Defensive: --no-newline should already suppress it, but trim a stray trailing
	// newline so the returned bytes are exactly the secret.
	return bytes.TrimRight(out, "\n"), nil
}

// resolveKeychain maps keychain://service[/account] onto the Keychain seam. A
// locked keychain propagates as ErrKeychainLocked; an absent item maps to
// ErrSecretNotFound.
func (r OSSecretResolver) resolveKeychain(ref string) ([]byte, error) {
	service, account, err := parseKeychainRef(strings.TrimPrefix(ref, keychainScheme))
	if err != nil {
		return nil, err
	}
	secret, err := r.Keychain.FindGenericPassword(service, account)
	switch {
	case errors.Is(err, ErrItemNotFound):
		return nil, fmt.Errorf("%w: %s", ErrSecretNotFound, ref)
	case errors.Is(err, ErrKeychainLocked):
		return nil, err // recoverable by the human; preserve the signal
	case err != nil:
		return nil, fmt.Errorf("%w: %s: %w", ErrSecretNotFound, ref, err)
	}
	return secret, nil
}

// resolveEnv reads env://NAME through the PathResolver seam. An unset or empty
// variable is unusable as a secret and maps to ErrSecretNotFound (Getenv cannot
// distinguish unset from empty, and neither is a valid secret).
func (r OSSecretResolver) resolveEnv(ref string) ([]byte, error) {
	name, err := parseEnvRef(strings.TrimPrefix(ref, envScheme))
	if err != nil {
		return nil, err
	}
	v := r.Paths.Getenv(name)
	if v == "" {
		return nil, fmt.Errorf("%w: %s (environment variable %s is unset or empty)",
			ErrSecretNotFound, ref, name)
	}
	return []byte(v), nil
}

// parseKeychainRef splits the part after keychain:// into service and optional
// account. "service" or "service/account" are valid; an empty service is an
// error. It is pure so the parsing is table-testable without touching the
// Keychain.
func parseKeychainRef(rest string) (service, account string, err error) {
	service, account, _ = strings.Cut(rest, "/")
	if service == "" {
		return "", "", fmt.Errorf("%w: keychain reference needs a service name", ErrUnknownSecretScheme)
	}
	return service, account, nil
}

// parseEnvRef validates the part after env:// as a non-empty variable name. It is
// pure so the parsing is table-testable without reading the environment.
func parseEnvRef(rest string) (string, error) {
	if rest == "" {
		return "", fmt.Errorf("%w: env reference needs a variable name", ErrUnknownSecretScheme)
	}
	return rest, nil
}
