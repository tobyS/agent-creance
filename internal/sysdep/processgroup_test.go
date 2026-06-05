package sysdep

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// Start's real impl is deferred to WP-4.3; it must report that via
// ErrNotImplemented. Notify, by contrast, is portable stdlib and works now.

func TestOSProcessGroupStartNotImplemented(t *testing.T) {
	var pg OSProcessGroup
	proc, err := pg.Start(context.Background(), "true")
	if !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Start error = %v, want errors.Is(ErrNotImplemented)", err)
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
