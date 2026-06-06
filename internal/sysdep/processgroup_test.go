package sysdep

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// Start's success path (Setpgid, kill(-pgid), Wait) needs real processes and is
// covered by the integration tests; here we only assert the fast failure path.
// Notify is portable stdlib and is exercised directly.

func TestOSProcessGroupStartError(t *testing.T) {
	var pg OSProcessGroup
	proc, err := pg.Start(context.Background(), nil, "/nonexistent/agent-creance-xyz")
	if err == nil {
		t.Error("Start error = nil, want a non-nil error for a missing binary")
	}
	if proc != nil {
		t.Errorf("Start Process = %v, want nil on error", proc)
	}
}

func TestOSProcessGroupNotifyDeliversSignal(t *testing.T) {
	var pg OSProcessGroup
	ch := make(chan os.Signal, 1)
	pg.Notify(ch, syscall.SIGUSR1)
	defer signal.Stop(ch)

	// SIGUSR1 to ourselves is caught (not terminating) because Notify registered
	// a handler for it.
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatalf("Kill(self, SIGUSR1): %v", err)
	}

	select {
	case got := <-ch:
		if got != syscall.SIGUSR1 {
			t.Errorf("received signal %v, want SIGUSR1", got)
		}
	case <-time.After(time.Second):
		t.Fatal("Notify did not deliver SIGUSR1 within 1s")
	}
}
