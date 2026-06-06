package sysdeptest

import (
	"bytes"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestFakeFlockReadModifyWrite(t *testing.T) {
	fl := NewFakeFlock()
	fl.Contents["/p/proxy.lock"] = []byte("seed")

	lf, err := fl.Acquire("/p/proxy.lock")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	got, err := lf.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, []byte("seed")) {
		t.Errorf("ReadAll = %q, want seed", got)
	}
	if err := lf.Write([]byte("updated")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := lf.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	if !bytes.Equal(fl.Contents["/p/proxy.lock"], []byte("updated")) {
		t.Errorf("Contents = %q, want updated", fl.Contents["/p/proxy.lock"])
	}
	if len(fl.Acquired) != 1 || fl.Acquired[0] != "/p/proxy.lock" {
		t.Errorf("Acquired = %v, want [/p/proxy.lock]", fl.Acquired)
	}
	if len(fl.Released) != 1 || fl.Released[0] != "/p/proxy.lock" {
		t.Errorf("Released = %v, want [/p/proxy.lock]", fl.Released)
	}
}

func TestFakeFlockReadAllReturnsCopy(t *testing.T) {
	fl := NewFakeFlock()
	fl.Contents["/p/proxy.lock"] = []byte("seed")
	lf, _ := fl.Acquire("/p/proxy.lock")
	defer lf.Release()
	got, _ := lf.ReadAll()
	got[0] = 'X' // mutating the returned slice must not corrupt the store
	if !bytes.Equal(fl.Contents["/p/proxy.lock"], []byte("seed")) {
		t.Errorf("Contents mutated via ReadAll alias: %q", fl.Contents["/p/proxy.lock"])
	}
}

func TestFakeFlockAcquireErr(t *testing.T) {
	fl := NewFakeFlock()
	sentinel := errors.New("would block")
	fl.AcquireErr = sentinel
	lf, err := fl.Acquire("/p/proxy.lock")
	if !errors.Is(err, sentinel) {
		t.Errorf("Acquire error = %v, want %v", err, sentinel)
	}
	if lf != nil {
		t.Error("LockedFile != nil, want nil on error")
	}
}

func TestFakeFlockSerialisesAcquirers(t *testing.T) {
	fl := NewFakeFlock()
	lf, err := fl.Acquire("/p/proxy.lock")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	acquired := make(chan struct{})
	var once sync.Once
	go func() {
		lf2, err := fl.Acquire("/p/proxy.lock")
		if err != nil {
			t.Errorf("Acquire (goroutine): %v", err)
			return
		}
		once.Do(func() { close(acquired) })
		_ = lf2.Release()
	}()

	select {
	case <-acquired:
		t.Fatal("second Acquire returned while first lock held")
	case <-time.After(50 * time.Millisecond):
	}
	_ = lf.Release()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("second Acquire did not unblock after release")
	}
}
