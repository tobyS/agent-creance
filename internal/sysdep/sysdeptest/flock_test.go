package sysdeptest

import (
	"errors"
	"testing"
)

func TestFakeFlockAcquireAndRelease(t *testing.T) {
	fl := NewFakeFlock()
	release, err := fl.Acquire("/p/proxy.lock")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if !fl.Held("/p/proxy.lock") {
		t.Errorf("Held after Acquire = false, want true")
	}
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if fl.Held("/p/proxy.lock") {
		t.Errorf("Held after release = true, want false")
	}
	if len(fl.Acquired) != 1 || fl.Acquired[0] != "/p/proxy.lock" {
		t.Errorf("Acquired = %v, want [/p/proxy.lock]", fl.Acquired)
	}
	if len(fl.Released) != 1 || fl.Released[0] != "/p/proxy.lock" {
		t.Errorf("Released = %v, want [/p/proxy.lock]", fl.Released)
	}
}

func TestFakeFlockAcquireErr(t *testing.T) {
	fl := NewFakeFlock()
	sentinel := errors.New("would block")
	fl.AcquireErr = sentinel
	release, err := fl.Acquire("/p/proxy.lock")
	if !errors.Is(err, sentinel) {
		t.Errorf("Acquire error = %v, want %v", err, sentinel)
	}
	if release != nil {
		t.Error("release != nil, want nil on error")
	}
	if fl.Held("/p/proxy.lock") {
		t.Errorf("Held after failed Acquire = true, want false")
	}
}

func TestFakeFlockReleaseErr(t *testing.T) {
	fl := NewFakeFlock()
	sentinel := errors.New("unlock failed")
	fl.ReleaseErr = sentinel
	release, err := fl.Acquire("/p/proxy.lock")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := release(); !errors.Is(err, sentinel) {
		t.Errorf("release error = %v, want %v", err, sentinel)
	}
}
