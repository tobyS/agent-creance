package sysdeptest

import (
	"context"
	"os"
	"sync"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// FakeProcessManager is a scripted ProcessManager. Spawn returns SpawnPID (and
// records the command); Alive consults the AlivePIDs oracle (absent key ⇒ dead);
// Signal records the (pid, sig) pair. This lets the proxy lifecycle be tested
// without spawning or signalling real processes.
type FakeProcessManager struct {
	// SpawnPID is the PID returned by Spawn when SpawnErr is nil.
	SpawnPID int
	// SpawnErr, if set, makes Spawn fail.
	SpawnErr error
	// AlivePIDs is the liveness oracle: Alive(pid) returns AlivePIDs[pid].
	AlivePIDs map[int]bool
	// SignalErr, if set, is returned by Signal.
	SignalErr error
	// Spawned records each Spawn call, in order.
	Spawned []StartedCommand
	// Signaled records each Signal call, in order.
	Signaled []SignaledPID

	mu sync.Mutex
}

// SignaledPID is one recorded Signal call.
type SignaledPID struct {
	PID int
	Sig os.Signal
}

var _ sysdep.ProcessManager = (*FakeProcessManager)(nil)

// NewFakeProcessManager returns an empty, ready-to-use fake.
func NewFakeProcessManager() *FakeProcessManager {
	return &FakeProcessManager{AlivePIDs: map[int]bool{}}
}

func (f *FakeProcessManager) Spawn(_ context.Context, name string, args ...string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Spawned = append(f.Spawned, StartedCommand{Name: name, Args: args})
	if f.SpawnErr != nil {
		return 0, f.SpawnErr
	}
	return f.SpawnPID, nil
}

func (f *FakeProcessManager) Alive(pid int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.AlivePIDs[pid]
}

func (f *FakeProcessManager) Signal(pid int, sig os.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Signaled = append(f.Signaled, SignaledPID{PID: pid, Sig: sig})
	return f.SignalErr
}
