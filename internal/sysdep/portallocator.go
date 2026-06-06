package sysdep

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"
)

// probeTimeout bounds the TCP dial used by Probe so a dead port fails fast.
const probeTimeout = 200 * time.Millisecond

// PortAllocator allocates ephemeral loopback TCP ports and probes/reclaims a
// specific one — the basis for the proxy's :0 allocation, the best-effort reclaim
// of the recorded port on a crash restart (so already-attached agents, whose
// frozen Seatbelt profile only allows the old port, recover transparently), and
// the "is the proxy actually listening?" check that distinguishes a live proxy
// from a recycled PID.
//
// Why route this through the seam (for someone coming from PHP/TS): binding and
// dialling sockets touches the real network stack, so port logic that called net
// directly could not be unit-tested deterministically. Packages take a
// PortAllocator and call *that*; production wires OSPortAllocator, tests wire the
// fake in sysdeptest.
type PortAllocator interface {
	// Allocate binds 127.0.0.1:0, reads the OS-assigned port, closes the listener
	// and returns the port. mitmproxy is then told the port via --listen-port,
	// which accepts the small allocate-close-rebind race.
	Allocate() (int, error)
	// TryReclaim attempts to bind 127.0.0.1:port. ok is true when the bind
	// succeeded (the port is free and reclaimable), false when a live holder
	// refused it (address in use). The listener is closed before returning. A
	// non-nil err is an unexpected failure, distinct from "held".
	TryReclaim(port int) (ok bool, err error)
	// Probe reports whether something is accepting connections on 127.0.0.1:port
	// (a short-timeout TCP dial succeeds). Used with ProcessManager.Alive to
	// confirm the proxy is genuinely up.
	Probe(port int) bool
}

// OSPortAllocator is the production PortAllocator backed by net.
type OSPortAllocator struct{}

var _ PortAllocator = (*OSPortAllocator)(nil)

func (OSPortAllocator) Allocate() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("sysdep: allocate ephemeral port: %w", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func (OSPortAllocator) TryReclaim(port int) (bool, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return false, nil // a live holder owns the port — not reclaimable
		}
		return false, fmt.Errorf("sysdep: reclaim port %d: %w", port, err)
	}
	_ = ln.Close()
	return true, nil
}

func (OSPortAllocator) Probe(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), probeTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
