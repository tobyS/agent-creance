package sysdep

import (
	"net"
	"testing"
)

// The real OSPortAllocator is verified once here (logic packages use the fake):
// it allocates an ephemeral port, probes liveness, and distinguishes a free port
// from a held one.

func TestOSPortAllocatorAllocate(t *testing.T) {
	var pa OSPortAllocator
	port, err := pa.Allocate()
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if port <= 0 || port > 65535 {
		t.Fatalf("Allocate port = %d, out of range", port)
	}
	// Nothing is listening on it after Allocate closed its listener.
	if pa.Probe(port) {
		t.Errorf("Probe(freshly allocated %d) = true, want false", port)
	}
}

func TestOSPortAllocatorProbeAndReclaimHeldPort(t *testing.T) {
	var pa OSPortAllocator
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	if !pa.Probe(port) {
		t.Errorf("Probe(held %d) = false, want true", port)
	}
	ok, err := pa.TryReclaim(port)
	if err != nil {
		t.Fatalf("TryReclaim(held): %v", err)
	}
	if ok {
		t.Errorf("TryReclaim(held %d) ok = true, want false (a live holder owns it)", port)
	}
}

func TestOSPortAllocatorReclaimFreePort(t *testing.T) {
	var pa OSPortAllocator
	// Grab a port then release it, so it is (very likely) free to reclaim.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	ok, err := pa.TryReclaim(port)
	if err != nil {
		t.Fatalf("TryReclaim(free): %v", err)
	}
	if !ok {
		t.Errorf("TryReclaim(free %d) ok = false, want true", port)
	}
	if pa.Probe(port) {
		t.Errorf("Probe(%d) = true after reclaim closed its listener, want false", port)
	}
}
