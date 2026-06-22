package sysdep

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// ProcessManager manages standalone host processes BY PID — distinct from
// ProcessGroup, which targets a whole agent subtree via a live Process handle.
// The shared mitmproxy is a daemon that outlives the invocation that spawned it
// and is killed later, by PID, from a different invocation that holds no handle
// to it (the last agent out, per the multi-agent lifecycle). So this seam spawns
// a detached daemon and learns its PID, probes an arbitrary PID's liveness, and
// signals a single PID.
//
// Why route this through the seam (for someone coming from PHP/TS): spawning a
// detached process and probing/signalling a PID need real syscalls (Setsid,
// kill), so lifecycle logic that called them directly could not be unit-tested.
// Packages take a ProcessManager and call *that*; production wires
// OSProcessManager, tests wire the fake in sysdeptest.
type ProcessManager interface {
	// Spawn starts name with args as a detached background process (its own
	// session via Setsid) and returns its PID without waiting on it. stdout/stderr
	// are discarded — the proxy writes its own audit log. A non-nil error means
	// the process could not be started.
	Spawn(ctx context.Context, name string, args ...string) (pid int, err error)
	// Alive reports whether pid is a live process, via kill(pid, 0): a nil error
	// means alive; ESRCH means dead; EPERM means alive-but-not-ours (treated as
	// alive — something is there).
	Alive(pid int) bool
	// Signal sends sig to a single pid via kill(pid, sig). A process that is
	// already gone (ESRCH) is not an error. Used to SIGTERM the proxy on last-out.
	Signal(pid int, sig os.Signal) error
	// StartTime returns pid's process start time as unix microseconds — a stable
	// per-process identity that changes when a PID is recycled into a different
	// process. It is the second identity factor for attached agents (the proxy
	// already gets one via a port probe): pruneDead compares the live process's
	// start time against the value recorded in the lock at attach. A non-nil error
	// means the start time could not be read (e.g. the process is gone), which
	// callers treat as "not the recorded process".
	StartTime(pid int) (int64, error)
}

// OSProcessManager is the production ProcessManager.
type OSProcessManager struct{}

var _ ProcessManager = (*OSProcessManager)(nil)

func (OSProcessManager) Spawn(ctx context.Context, name string, args ...string) (int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// Setsid detaches the daemon into its own session so it survives this process
	// and is not in our controlling terminal's signal path.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("sysdep: spawn %q: %w", name, err)
	}
	pid := cmd.Process.Pid
	// Release our handle so no zombie accumulates; the daemon is reparented to init.
	_ = cmd.Process.Release()
	return pid, nil
}

func (OSProcessManager) Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	// EPERM: the process exists but we may not signal it — still alive.
	return errors.Is(err, syscall.EPERM)
}

func (OSProcessManager) Signal(pid int, sig os.Signal) error {
	if pid <= 0 {
		return fmt.Errorf("sysdep: signal pid %d: invalid pid", pid)
	}
	s, ok := sig.(syscall.Signal)
	if !ok {
		return fmt.Errorf("sysdep: signal pid %d: unsupported signal %v", pid, sig)
	}
	if err := syscall.Kill(pid, s); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil // already gone — nothing to kill
		}
		return fmt.Errorf("sysdep: signal pid %d: %w", pid, err)
	}
	return nil
}

func (OSProcessManager) StartTime(pid int) (int64, error) {
	if pid <= 0 {
		return 0, fmt.Errorf("sysdep: start time pid %d: invalid pid", pid)
	}
	// kern.proc.pid returns the process's kinfo_proc; its very first field is
	// extern_proc.p_un, a `struct timeval p_starttime` at offset 0 — the wall-clock
	// time the process started (stable for its lifetime, freshly assigned when the PID
	// is later recycled). We read the raw bytes and parse that leading timeval rather
	// than unix.SysctlKinfoProc, whose strict size check fails with EIO when the
	// running kernel's kinfo_proc differs from x/sys's struct size (newer macOS). The
	// leading p_starttime offset is ABI-stable; a nonexistent pid yields no bytes,
	// which we report as an error (the process is gone). macOS is little-endian.
	raw, err := unix.SysctlRaw("kern.proc.pid", pid)
	if err != nil {
		return 0, fmt.Errorf("sysdep: start time pid %d: %w", pid, err)
	}
	const tvSize = 12 // int64 sec + int32 usec
	if len(raw) < tvSize {
		return 0, fmt.Errorf("sysdep: start time pid %d: no process info (%d bytes)", pid, len(raw))
	}
	sec := int64(binary.LittleEndian.Uint64(raw[0:8]))
	usec := int32(binary.LittleEndian.Uint32(raw[8:12]))
	return sec*1_000_000 + int64(usec), nil
}
