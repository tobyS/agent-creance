package sysdep

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
)

// ProcessGroup abstracts running a child in its own process group and tearing the
// whole group down deterministically — the basis for Ctrl-C handling, where a
// SIGINT/SIGTERM must reach everything the agent spawned (npm, test runners, …),
// not just the wrapper's direct child. When stdin is a terminal the child's group
// is also made the terminal's FOREGROUND group, so keyboard signals (Ctrl-C) are
// delivered by the kernel straight to the agent's subtree; the wrapper's own
// signal forwarding then covers signals sent to the wrapper's PID (kill, ctx
// cancel). It also wraps signal subscription (os/signal.Notify) so the wrapper
// can catch the signals it forwards.
//
// Why route this through the seam (for someone coming from PHP/TS): starting a
// new process group and signalling it needs real syscalls (Setpgid, kill), so
// teardown logic that called them directly could not be unit-tested. Packages
// take a ProcessGroup and call *that*; production wires OSProcessGroup, tests
// wire the fake in sysdeptest.
type ProcessGroup interface {
	// Start runs name with args in a NEW process group (Setpgid: true), wired to the
	// controlling terminal's stdio, with env appended to the parent's environment
	// (KEY=VALUE pairs, last-wins — matching the cage Invocation's extra env). When
	// stdin is a terminal, the new group is also made the terminal's foreground
	// group (a TUI child in a background group would be stopped by SIGTTIN/SIGTTOU
	// on its first tty access); with a piped stdin the handover is skipped. It
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
	// session and keep the controlling terminal.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// A new process group is a BACKGROUND group: a TUI agent's first tty access
	// (tcsetattr raw mode, stdin read) would stop it with SIGTTOU/SIGTTIN and the
	// wrapper would wait forever (AC-0042). Foreground hands the terminal to the
	// child's group — Go runs setpgid + ioctl(Ctty, TIOCSPGRP) in the forked child
	// pre-execve with signals blocked, so the child cannot be stopped doing it.
	// Ctty is a PARENT fd number here (Foreground, unlike Setctty, never indexes
	// the child's fd table). Guarded: with a non-tty stdin (go test, testscript,
	// CI pipes) the ioctl would fail the whole fork/exec with ENOTTY, so the
	// handover is skipped and the child runs as today.
	foreground := isTerminal(os.Stdin)
	if foreground {
		cmd.SysProcAttr.Foreground = true
		cmd.SysProcAttr.Ctty = int(os.Stdin.Fd())
	}
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// If ctx is cancelled, tear down the whole group, not just the leader. Cancel
	// runs only after Start, so cmd.Process is non-nil.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGINT) }
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("sysdep: start %q: %w", name, err)
	}
	return &osProcess{cmd: cmd, pgid: cmd.Process.Pid, foreground: foreground}, nil
}

func (OSProcessGroup) Notify(ch chan<- os.Signal, sigs ...os.Signal) {
	signal.Notify(ch, sigs...)
}

// osProcess is the production Process: a child leading its own process group.
// foreground records whether Start handed the terminal to that group, so Wait
// can symmetrically take it back for the wrapper's teardown output.
type osProcess struct {
	cmd        *exec.Cmd
	pgid       int
	foreground bool
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

func (p *osProcess) Wait() error {
	err := p.cmd.Wait()
	if p.foreground {
		// The child's group owned the terminal; with it gone the wrapper is a
		// background group. Take the terminal back so the teardown output (proxy
		// warnings, the error: line) cannot be stopped by a TOSTOP-mode terminal.
		// tcsetpgrp from a background group itself raises SIGTTOU, so ignore it
		// first — safe to do process-wide only NOW, after the child exited
		// (SIG_IGN survives execve and must not leak into the agent). Best
		// effort: if the tty is gone, the shell reclaims it when we exit.
		signal.Ignore(syscall.SIGTTOU)
		_ = unix.IoctlSetPointerInt(int(os.Stdin.Fd()), unix.TIOCSPGRP, unix.Getpgrp())
	}
	return err
}

func (p *osProcess) Pgid() int { return p.pgid }
