package sysdeptest

import (
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// FakeMemory is a Memory that records what was locked and unlocked instead of
// touching the real VM, so the broker's custody discipline can be asserted
// without mlock's RLIMIT_MEMLOCK and without a process-wide setrlimit.
//
// Locked and Unlocked record the *lengths* of the buffers, never their contents:
// a fake that kept a copy of the secret would defeat the point of the tests that
// use it.
type FakeMemory struct {
	// LockErr, if set, is returned by Lock. Callers must tolerate it.
	LockErr error
	// CoreDumpErr, if set, is returned by DisableCoreDumps.
	CoreDumpErr error

	// Locked / Unlocked record the buffer lengths passed to Lock / Unlock.
	Locked   []int
	Unlocked []int
	// CoreDumpsDisabled counts DisableCoreDumps calls.
	CoreDumpsDisabled int
}

var _ sysdep.Memory = (*FakeMemory)(nil)

func (f *FakeMemory) Lock(b []byte) error {
	f.Locked = append(f.Locked, len(b))
	return f.LockErr
}

func (f *FakeMemory) Unlock(b []byte) error {
	f.Unlocked = append(f.Unlocked, len(b))
	return nil
}

func (f *FakeMemory) DisableCoreDumps() error {
	f.CoreDumpsDisabled++
	return f.CoreDumpErr
}
