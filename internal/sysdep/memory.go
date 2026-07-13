package sysdep

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Memory abstracts the process-memory hygiene the credential broker applies to a
// resolved secret: pinning the buffer out of swap and keeping it out of a core
// dump.
//
// What this honestly buys, and what it does not (AC-0069b): mlock(2) *is*
// implemented on darwin — only mlockall(2) returns ENOSYS, which is why some Go
// projects list macOS as "mlock unavailable" — so Lock does pin the page. But on
// a modern Mac swap is encrypted by default, the adversary in this system's model
// (a prompt-injected agent in the cage) runs as the *same uid* as the broker, and
// Go copies stacks and spills registers, so a []byte wipe cannot chase every
// derived copy. Go 1.26's runtime/secret, which could, is a no-op on darwin.
//
// So: this is hygiene, not a control. It removes a class of accidents (a token in
// a core dump, a token on a swapped-out page that outlives the process); it does
// not defend against anything that can read this process's memory. The blast
// radius of an injected credential is bounded by its scope and its TTL
// (AC-0069a), not by memory protection. A Lock failure is therefore never fatal —
// callers warn and carry on.
//
// Why route it through the seam: mlock and setrlimit are real syscalls with
// process-wide effects. Packages take a Memory and call *that*; production wires
// OSMemory, tests wire the fake in sysdeptest.
type Memory interface {
	// Lock pins b in physical memory so it cannot be paged out to swap.
	Lock(b []byte) error
	// Unlock releases a Lock on b. Callers wipe the buffer *before* unlocking.
	Unlock(b []byte) error
	// DisableCoreDumps sets RLIMIT_CORE to zero for this process, so a crash
	// cannot spill the held secrets to disk.
	DisableCoreDumps() error
}

// OSMemory is the production Memory backed by golang.org/x/sys/unix.
type OSMemory struct{}

var _ Memory = (*OSMemory)(nil)

func (OSMemory) Lock(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	if err := unix.Mlock(b); err != nil {
		return fmt.Errorf("sysdep: mlock %d bytes: %w", len(b), err)
	}
	return nil
}

func (OSMemory) Unlock(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	if err := unix.Munlock(b); err != nil {
		return fmt.Errorf("sysdep: munlock %d bytes: %w", len(b), err)
	}
	return nil
}

func (OSMemory) DisableCoreDumps() error {
	if err := unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{Cur: 0, Max: 0}); err != nil {
		return fmt.Errorf("sysdep: disable core dumps: %w", err)
	}
	return nil
}
