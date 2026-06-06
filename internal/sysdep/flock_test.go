package sysdep

import (
	"bytes"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The real OSFlock is verified once here (logic packages use the fake): it locks a
// real descriptor, round-trips contents in place, and serialises concurrent
// acquirers.

func TestOSFlockReadModifyWriteInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.lock")
	var fl OSFlock

	lf, err := fl.Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	got, err := lf.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll (fresh): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("fresh lock contents = %q, want empty", got)
	}
	if err := lf.Write([]byte("first-longer-content")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Overwrite with shorter content to prove truncation (no leftover tail).
	if err := lf.Write([]byte("second")); err != nil {
		t.Fatalf("Write 2: %v", err)
	}
	if err := lf.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	lf2, err := fl.Acquire(path)
	if err != nil {
		t.Fatalf("Acquire 2: %v", err)
	}
	defer lf2.Release()
	got, err = lf2.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll 2: %v", err)
	}
	if !bytes.Equal(got, []byte("second")) {
		t.Errorf("contents = %q, want %q", got, "second")
	}
}

func TestOSFlockSerialisesAcquirers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.lock")
	var fl OSFlock

	lf, err := fl.Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	acquired := make(chan struct{})
	var once sync.Once
	go func() {
		lf2, err := fl.Acquire(path) // must block until the first releases
		if err != nil {
			t.Errorf("Acquire (goroutine): %v", err)
			return
		}
		once.Do(func() { close(acquired) })
		_ = lf2.Release()
	}()

	select {
	case <-acquired:
		t.Fatal("second Acquire returned while first lock was held")
	case <-time.After(50 * time.Millisecond):
		// expected: still blocked
	}

	if err := lf.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	select {
	case <-acquired:
		// expected: unblocked after release
	case <-time.After(2 * time.Second):
		t.Fatal("second Acquire did not unblock after release")
	}
}
