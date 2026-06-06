package sysdep

import (
	"context"
	"os"
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

func TestOSProcessManagerSignalGoneIsNotError(t *testing.T) {
	var pm OSProcessManager
	// Signalling a non-existent PID (ESRCH) must be swallowed.
	if err := pm.Signal(1<<30, syscall.SIGTERM); err != nil {
		t.Errorf("Signal(dead pid) = %v, want nil", err)
	}
}
