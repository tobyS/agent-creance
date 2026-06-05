package sysdep

import "errors"

// ErrNotImplemented is returned by production sysdep implementations whose real,
// platform-specific behaviour is deferred to the ticket that introduces their
// first consumer. WP-1.4 (AC-0009) seeds the interface + fake now; the macOS
// implementations for Keychain, Flock, and ProcessGroup.Start land with
// internal/cred (WP-4.1), the proxy lifecycle (WP-3.4), and internal/cage
// (WP-4.3) respectively. Callers and tests can errors.Is against it.
var ErrNotImplemented = errors.New("sysdep: not implemented")
