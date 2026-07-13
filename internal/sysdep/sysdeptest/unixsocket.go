package sysdeptest

import (
	"net"
	"os"
	"sync"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// FakeUnixSocket is a scripted UnixSocket. Probe consults Listening (absent key ⇒
// nothing listening), mirroring FakePortAllocator. Listen delegates to the real
// OSUnixSocket — binding a socket under a test's TempDir is hermetic (no external
// tool, no shared state), and a fake listener would only re-implement net — but it
// records the paths and honours ListenErr so the failure branches stay scriptable.
type FakeUnixSocket struct {
	// Listening is the probe oracle: Probe(path) returns Listening[path].
	Listening map[string]bool
	// ListenErr, if set, makes Listen fail without binding anything.
	ListenErr error
	// Listened records the paths passed to Listen, in order.
	Listened []string
	// Probed records the paths passed to Probe, in order.
	Probed []string

	mu sync.Mutex
}

var _ sysdep.UnixSocket = (*FakeUnixSocket)(nil)

// NewFakeUnixSocket returns an empty, ready-to-use fake.
func NewFakeUnixSocket() *FakeUnixSocket {
	return &FakeUnixSocket{Listening: map[string]bool{}}
}

func (f *FakeUnixSocket) Listen(path string, perm os.FileMode) (net.Listener, error) {
	f.mu.Lock()
	f.Listened = append(f.Listened, path)
	err := f.ListenErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return sysdep.OSUnixSocket{}.Listen(path, perm)
}

func (f *FakeUnixSocket) Probe(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Probed = append(f.Probed, path)
	return f.Listening[path]
}
