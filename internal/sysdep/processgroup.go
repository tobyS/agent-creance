package sysdep

import (
	"context"
	"os"
	"os/signal"
)

// ProcessGroup abstracts running a child in its own process group and tearing the
// whole group down deterministically — the basis for Ctrl-C handling, where a
// SIGINT/SIGTERM must reach everything the agent spawned (npm, test runners, …),
// not just the wrapper's direct child. It also wraps signal subscription
// (os/signal.Notify) so the wrapper can catch the signals it forwards.
//
// Why route this through the seam (for someone coming from PHP/TS): starting a
// new process group and signalling it needs real syscalls (Setpgid, kill), so
// teardown logic that called them directly could not be unit-tested. Packages
// take a ProcessGroup and call *that*; production wires OSProcessGroup, tests
// wire the fake in sysdeptest.
type ProcessGroup interface {
	// Start runs name with args in a NEW process group (Setpgid: true) and returns
	// a handle for signalling and waiting on the whole group. A non-nil error
	// means the child could not be started; in that case Process is nil.
	Start(ctx context.Context, name string, args ...string) (Process, error)
	// Notify relays the given OS signals into ch, mirroring os/signal.Notify, so
	// the wrapper can catch SIGINT/SIGTERM and forward them to the group.
	Notify(ch chan<- os.Signal, sigs ...os.Signal)
}

// Process is a handle to a child started in its own process group.
type Process interface {
	// Signal forwards sig to the entire process group via kill(-pgid, sig), so a
	// SIGINT/SIGTERM tears down the agent's whole subtree, not just the leader.
	Signal(sig os.Signal) error
	// Wait blocks until the group's leader has exited and returns its exit error
	// (nil on a clean exit). The caller waits for the whole group before the
	// lock-file decrement, so cleanup ordering is deterministic.
	Wait() error
	// Pgid returns the process-group id (used to target kill(-pgid, sig)).
	Pgid() int
}

// OSProcessGroup is the production ProcessGroup. Start is deferred to WP-4.3
// (internal/cage): the real impl sets SysProcAttr{Setpgid: true} and returns an
// osProcess whose Signal does syscall.Kill(-pgid, sig) and whose Wait reaps the
// group. Until then Start returns ErrNotImplemented. Notify is portable stdlib
// and is implemented now.
type OSProcessGroup struct{}

var _ ProcessGroup = (*OSProcessGroup)(nil)

func (OSProcessGroup) Start(_ context.Context, _ string, _ ...string) (Process, error) {
	return nil, ErrNotImplemented
}

func (OSProcessGroup) Notify(ch chan<- os.Signal, sigs ...os.Signal) {
	signal.Notify(ch, sigs...)
}
