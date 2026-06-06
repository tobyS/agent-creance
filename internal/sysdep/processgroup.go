package sysdep

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
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
	// Start runs name with args in a NEW process group (Setpgid: true), wired to the
	// controlling terminal's stdio, with env appended to the parent's environment
	// (KEY=VALUE pairs, last-wins — matching the cage Invocation's extra env). It
	// returns a handle for signalling and waiting on the whole group. A non-nil error
	// means the child could not be started; in that case Process is nil.
	Start(ctx context.Context, env []string, name string, args ...string) (Process, error)
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

// OSProcessGroup is the production ProcessGroup. Start sets
// SysProcAttr{Setpgid: true} so the child leads a new process group, and returns an
// osProcess whose Signal does syscall.Kill(-pgid, sig) and whose Wait reaps the
// group. Notify is portable stdlib.
type OSProcessGroup struct{}

var _ ProcessGroup = (*OSProcessGroup)(nil)

func (OSProcessGroup) Start(ctx context.Context, env []string, name string, args ...string) (Process, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// Setpgid (with Pgid left 0) makes the child the leader of a NEW process group
	// whose pgid equals its own PID — Go performs the setpgid in the forked child
	// before execve, so cmd.Process.Pid IS the pgid (no Getpgid, no fork/setpgid
	// race). Unlike ProcessManager's Setsid (a detached daemon), we stay in the
	// session and keep the controlling terminal so the agent is interactive.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// If ctx is cancelled, tear down the whole group, not just the leader. Cancel
	// runs only after Start, so cmd.Process is non-nil.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGINT) }
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("sysdep: start %q: %w", name, err)
	}
	return &osProcess{cmd: cmd, pgid: cmd.Process.Pid}, nil
}

func (OSProcessGroup) Notify(ch chan<- os.Signal, sigs ...os.Signal) {
	signal.Notify(ch, sigs...)
}

// osProcess is the production Process: a child leading its own process group.
type osProcess struct {
	cmd  *exec.Cmd
	pgid int
}

var _ Process = (*osProcess)(nil)

func (p *osProcess) Signal(sig os.Signal) error {
	s, ok := sig.(syscall.Signal)
	if !ok {
		return fmt.Errorf("sysdep: signal pgid %d: unsupported signal %v", p.pgid, sig)
	}
	// Negative pgid targets the whole process group, so SIGINT/SIGTERM tears down the
	// agent's entire subtree. A group that has already exited (ESRCH) is not an error.
	if err := syscall.Kill(-p.pgid, s); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return fmt.Errorf("sysdep: signal pgid %d: %w", p.pgid, err)
	}
	return nil
}

func (p *osProcess) Wait() error { return p.cmd.Wait() }

func (p *osProcess) Pgid() int { return p.pgid }
