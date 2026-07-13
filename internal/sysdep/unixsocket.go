package sysdep

import (
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

// socketProbeTimeout bounds the dial used by Probe so a dead broker fails fast.
const socketProbeTimeout = 200 * time.Millisecond

// MaxSocketPathLen is the largest unix-socket path the kernel accepts.
// sockaddr_un.sun_path is 104 bytes on darwin, including the NUL terminator — so
// a path of 103 bytes is the longest that can be bound. Exceeding it fails at
// bind(2) with a confusing EINVAL, which is why Listen checks up front.
const MaxSocketPathLen = 103

// ErrSocketPathTooLong is returned by Listen when the path would overflow
// sun_path. Callers treat it as "no broker" (warn, spawn the proxy anyway, let
// the enforcer answer 472) rather than as a fatal error.
var ErrSocketPathTooLong = errors.New("sysdep: unix socket path too long")

// UnixSocket abstracts binding and probing a unix-domain socket — the credential
// broker's listener and the "is the broker actually up?" check that pairs with
// ProcessManager.Alive, mirroring how PortAllocator.Probe pairs with it for the
// proxy.
//
// Why route it through the seam: binding a socket touches the real filesystem and
// network stack. Packages take a UnixSocket and call *that*; production wires
// OSUnixSocket, tests wire the fake in sysdeptest.
type UnixSocket interface {
	// Listen binds path as a unix-domain socket and chmods it to perm. A stale
	// socket file left by a crashed process is removed first. It returns
	// ErrSocketPathTooLong if path would overflow sun_path.
	//
	// perm is applied after bind: the socket file is created subject to the
	// umask, so the chmod is what actually makes it 0600.
	Listen(path string, perm os.FileMode) (net.Listener, error)
	// Probe reports whether something is accepting connections on the socket at
	// path (a short-timeout dial succeeds).
	Probe(path string) bool
}

// OSUnixSocket is the production UnixSocket backed by net and os.
type OSUnixSocket struct{}

var _ UnixSocket = (*OSUnixSocket)(nil)

func (OSUnixSocket) Listen(path string, perm os.FileMode) (net.Listener, error) {
	if len(path) > MaxSocketPathLen {
		return nil, fmt.Errorf("%w: %d bytes, limit %d: %s", ErrSocketPathTooLong, len(path), MaxSocketPathLen, path)
	}
	// A socket file left behind by a crashed broker would make bind fail with
	// EADDRINUSE even though nobody is listening.
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("sysdep: remove stale socket %s: %w", path, err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("sysdep: listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, perm); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("sysdep: chmod socket %s: %w", path, err)
	}
	return ln, nil
}

func (OSUnixSocket) Probe(path string) bool {
	conn, err := net.DialTimeout("unix", path, socketProbeTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
