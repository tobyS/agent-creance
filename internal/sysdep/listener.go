package sysdep

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Listener is one listening TCP socket discovered on the host. doctor's
// exposed-service scan reports those bound to all interfaces (0.0.0.0 / ::).
type Listener struct {
	// Command is the owning process name (lsof "c" field).
	Command string
	// PID is the owning process id (lsof "p" field).
	PID int
	// Address is the bind address as lsof renders it with -n -P: "*:8080" for a
	// wildcard (0.0.0.0 or ::) bind, "127.0.0.1:7000"/"[::1]:631" for loopback,
	// "192.168.1.5:8080" for a specific interface.
	Address string
}

// ListenerScanner enumerates listening TCP sockets on the host. The seam exists
// because the real implementation shells out to lsof; tests wire the fake in
// sysdeptest. doctor (AC-0031) is the consumer.
type ListenerScanner interface {
	// Listeners returns every listening TCP socket the caller can see. A non-nil
	// error means the scan could not run (e.g. lsof is not installed), distinct from
	// an empty result (no listeners).
	Listeners(ctx context.Context) ([]Listener, error)
}

// OSListenerScanner is the production ListenerScanner backed by the system lsof.
type OSListenerScanner struct{}

var _ ListenerScanner = (*OSListenerScanner)(nil)

func (OSListenerScanner) Listeners(ctx context.Context) ([]Listener, error) {
	// -nP keep addresses numeric (no DNS / port-name lookup) so both 0.0.0.0 and ::
	// render as "*:port"; -iTCP -sTCP:LISTEN restrict to listening TCP sockets; -F
	// emits machine-parseable field output (p=pid, c=command, n=name).
	cmd := exec.CommandContext(ctx, "lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-F", "pcn")
	out, err := cmd.Output()
	if err != nil {
		// lsof exits 1 with no output when there are simply no matching sockets;
		// that is an empty result, not a failure.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(out) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("sysdep: scan listeners: %w", err)
	}
	return ParseLsof(out), nil
}

// ParseLsof parses lsof -F pcn field output into Listeners. It is pure so the
// parsing is table-testable without invoking lsof. Each line is one field: the
// first byte is the field id, the rest its value. A "p" line starts a process
// (carrying its later "c" command); each "n" line is one socket's bind address.
func ParseLsof(out []byte) []Listener {
	var listeners []Listener
	var curPID int
	var curCmd string
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		id, val := line[0], line[1:]
		switch id {
		case 'p':
			curPID, _ = strconv.Atoi(val)
			curCmd = ""
		case 'c':
			curCmd = val
		case 'n':
			listeners = append(listeners, Listener{Command: curCmd, PID: curPID, Address: val})
		}
	}
	return listeners
}

// IsExposed reports whether a bind address is on all interfaces (a wildcard bind),
// i.e. reachable from off-host. True for lsof's "*:port" (0.0.0.0 and ::) and the
// raw "0.0.0.0:"/"[::]:"/":::" forms; false for loopback or a specific interface IP.
func IsExposed(addr string) bool {
	return strings.HasPrefix(addr, "*:") ||
		strings.HasPrefix(addr, "0.0.0.0:") ||
		strings.HasPrefix(addr, "[::]:") ||
		strings.HasPrefix(addr, ":::")
}
