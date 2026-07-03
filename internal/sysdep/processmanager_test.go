package sysdep

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The real OSProcessManager is verified once here (logic packages use the fake):
// it spawns a detached process, probes PID liveness, and signals a single PID.

func TestOSProcessManagerAliveForSelf(t *testing.T) {
	var pm OSProcessManager
	if !pm.Alive(os.Getpid()) {
		t.Error("Alive(self) = false, want true")
	}
	if pm.Alive(0) || pm.Alive(-1) {
		t.Error("Alive(<=0) = true, want false")
	}
	// A PID that almost certainly does not exist.
	if pm.Alive(1 << 30) {
		t.Error("Alive(huge pid) = true, want false")
	}
}

func TestOSProcessManagerSpawnAndSignal(t *testing.T) {
	var pm OSProcessManager
	// sleep long enough that the liveness probe sees it, short enough to not linger.
	pid, err := pm.Spawn(context.Background(), "/bin/sleep", "30")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("Spawn pid = %d, want > 0", pid)
	}
	if !pm.Alive(pid) {
		t.Fatalf("Alive(spawned %d) = false, want true", pid)
	}
	if err := pm.Signal(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	// Reap and wait for it to actually go away.
	deadline := time.Now().Add(2 * time.Second)
	for pm.Alive(pid) && time.Now().Before(deadline) {
		_, _ = syscall.Wait4(pid, nil, syscall.WNOHANG, nil)
		time.Sleep(10 * time.Millisecond)
	}
	if pm.Alive(pid) {
		t.Errorf("process %d still alive after SIGKILL", pid)
	}
}

func TestOSProcessManagerSpawnWithSecret(t *testing.T) {
	var pm OSProcessManager
	out := filepath.Join(t.TempDir(), "received")
	const secret = "s3cr3t-value\nwith-newline"
	// The child copies its inherited fd 3 (the pipe read end) into a file — proving
	// the secret reaches it over the descriptor, not via argv/env.
	pid, err := pm.SpawnWithSecret(context.Background(), []byte(secret), "/bin/sh", "-c", "cat <&3 > "+out)
	if err != nil {
		t.Fatalf("SpawnWithSecret: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("SpawnWithSecret pid = %d, want > 0", pid)
	}
	// Wait for the detached child to finish writing.
	deadline := time.Now().Add(2 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		_, _ = syscall.Wait4(pid, nil, syscall.WNOHANG, nil)
		if b, rerr := os.ReadFile(out); rerr == nil && len(b) == len(secret) {
			got = b
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if string(got) != secret {
		t.Errorf("child received %q over fd 3, want %q", string(got), secret)
	}
}

func TestOSProcessManagerSignalGoneIsNotError(t *testing.T) {
	var pm OSProcessManager
	// Signalling a non-existent PID (ESRCH) must be swallowed.
	if err := pm.Signal(1<<30, syscall.SIGTERM); err != nil {
		t.Errorf("Signal(dead pid) = %v, want nil", err)
	}
}

func TestOSProcessManagerStartTime(t *testing.T) {
	var pm OSProcessManager
	// Our own start time is readable, non-zero, and stable across reads.
	st1, err := pm.StartTime(os.Getpid())
	if err != nil {
		t.Fatalf("StartTime(self): %v", err)
	}
	if st1 <= 0 {
		t.Fatalf("StartTime(self) = %d, want > 0", st1)
	}
	st2, err := pm.StartTime(os.Getpid())
	if err != nil {
		t.Fatalf("StartTime(self) again: %v", err)
	}
	if st1 != st2 {
		t.Errorf("StartTime(self) not stable: %d != %d", st1, st2)
	}
	// An invalid or absent PID errors rather than returning a bogus identity.
	if _, err := pm.StartTime(0); err == nil {
		t.Error("StartTime(0) = nil error, want error")
	}
	if _, err := pm.StartTime(1 << 30); err == nil {
		t.Error("StartTime(huge pid) = nil error, want error")
	}
}
