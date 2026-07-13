package sysdeptest

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// FakeProcessManager is a scripted ProcessManager. Spawn returns SpawnPID (and
// records the command); Alive consults the AlivePIDs oracle (absent key ⇒ dead);
// Signal records the (pid, sig) pair; StartTime consults the StartTimes oracle
// (absent key ⇒ error). This lets the proxy lifecycle be tested without spawning
// or signalling real processes — and lets a test model a recycled PID by marking a
// PID alive while giving it a StartTime that differs from the one recorded in the
// lock.
type FakeProcessManager struct {
	// SpawnPID is the PID returned by Spawn when SpawnErr is nil and SpawnPIDs is
	// exhausted.
	SpawnPID int
	// SpawnPIDs, when non-empty, is consumed one PID per spawn (in order) before
	// falling back to SpawnPID — so a test that spawns two daemons (the credential
	// broker and mitmdump) can tell their PIDs apart.
	SpawnPIDs []int
	// SpawnErr, if set, makes Spawn fail.
	SpawnErr error
	// AlivePIDs is the liveness oracle: Alive(pid) returns AlivePIDs[pid].
	AlivePIDs map[int]bool
	// SignalErr, if set, is returned by Signal.
	SignalErr error
	// StartTimes is the start-time oracle: StartTime(pid) returns StartTimes[pid].
	// An absent key yields an error (the process is "gone"). A value differing from
	// the start time recorded in the lock models a recycled PID.
	StartTimes map[int]int64
	// StartTimeErr, if set, is returned by StartTime regardless of pid.
	StartTimeErr error
	// Spawned records each Spawn / SpawnWithSecret call, in order (name + args).
	Spawned []StartedCommand
	// Secrets records the secret payload of each SpawnWithSecret call, in order. A
	// plain Spawn appends nothing here, so len(Secrets) counts fd-delivery spawns.
	Secrets [][]byte
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
	return &FakeProcessManager{AlivePIDs: map[int]bool{}, StartTimes: map[int]int64{}}
}

func (f *FakeProcessManager) Spawn(_ context.Context, name string, args ...string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Spawned = append(f.Spawned, StartedCommand{Name: name, Args: args})
	if f.SpawnErr != nil {
		return 0, f.SpawnErr
	}
	return f.nextPID(), nil
}

func (f *FakeProcessManager) SpawnWithSecret(_ context.Context, secret []byte, name string, args ...string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Spawned = append(f.Spawned, StartedCommand{Name: name, Args: args})
	f.Secrets = append(f.Secrets, append([]byte(nil), secret...))
	if f.SpawnErr != nil {
		return 0, f.SpawnErr
	}
	return f.nextPID(), nil
}

// nextPID pops the next scripted PID, falling back to SpawnPID. The caller holds mu.
func (f *FakeProcessManager) nextPID() int {
	if len(f.SpawnPIDs) == 0 {
		return f.SpawnPID
	}
	pid := f.SpawnPIDs[0]
	f.SpawnPIDs = f.SpawnPIDs[1:]
	return pid
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

func (f *FakeProcessManager) StartTime(pid int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.StartTimeErr != nil {
		return 0, f.StartTimeErr
	}
	st, ok := f.StartTimes[pid]
	if !ok {
		return 0, fmt.Errorf("fake: no start time for pid %d", pid)
	}
	return st, nil
}
