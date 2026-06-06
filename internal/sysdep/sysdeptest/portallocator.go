package sysdeptest

import (
	"sync"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// FakePortAllocator is a scripted PortAllocator. Allocate returns AllocPort;
// TryReclaim consults ReclaimOK (absent key ⇒ not reclaimable); Probe consults
// Listening (absent key ⇒ nothing listening). It records Allocations and Reclaims
// so tests can assert what was attempted.
type FakePortAllocator struct {
	// AllocPort is the port returned by Allocate when AllocErr is nil.
	AllocPort int
	// AllocErr, if set, makes Allocate fail.
	AllocErr error
	// ReclaimOK is the reclaim oracle: TryReclaim(port) returns ReclaimOK[port].
	ReclaimOK map[int]bool
	// ReclaimErr, if set, is returned by TryReclaim (ok is then false).
	ReclaimErr error
	// Listening is the probe oracle: Probe(port) returns Listening[port].
	Listening map[int]bool
	// Allocations counts Allocate calls.
	Allocations int
	// Reclaims records the ports passed to TryReclaim, in order.
	Reclaims []int

	mu sync.Mutex
}

var _ sysdep.PortAllocator = (*FakePortAllocator)(nil)

// NewFakePortAllocator returns an empty, ready-to-use fake.
func NewFakePortAllocator() *FakePortAllocator {
	return &FakePortAllocator{ReclaimOK: map[int]bool{}, Listening: map[int]bool{}}
}

func (f *FakePortAllocator) Allocate() (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Allocations++
	if f.AllocErr != nil {
		return 0, f.AllocErr
	}
	return f.AllocPort, nil
}

func (f *FakePortAllocator) TryReclaim(port int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Reclaims = append(f.Reclaims, port)
	if f.ReclaimErr != nil {
		return false, f.ReclaimErr
	}
	return f.ReclaimOK[port], nil
}

func (f *FakePortAllocator) Probe(port int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Listening[port]
}
