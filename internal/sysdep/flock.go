package sysdep

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// Flock abstracts an exclusive advisory file lock — the primitive behind the
// atomic read-modify-write of the proxy.lock file in the multi-agent lifecycle
// (which records the proxy PID, port, policy hash, and attached-agent PIDs).
//
// Because an advisory flock lives on a specific file descriptor, the SAME
// descriptor must carry both the read and the write — so Acquire returns a
// LockedFile for in-place I/O, not just an unlock closure. A temp-file + rename
// (the usual atomic-write idiom elsewhere in this codebase) would swap the inode
// out from under the lock and silently break exclusion, which is why proxy.lock
// is the one file written in place.
//
// Why route locking through the seam (for someone coming from PHP/TS): a real
// advisory lock needs a real file descriptor and OS support, so lifecycle logic
// that called flock(2) directly could not be unit-tested. Packages take a Flock
// and call *that*; production wires OSFlock, tests wire the fake in sysdeptest.
type Flock interface {
	// Acquire opens path (creating it if absent), takes an exclusive advisory
	// lock, blocking until the lock is held, and returns a LockedFile for reading
	// and replacing the contents on the locked descriptor. A non-nil error means
	// the lock could not be acquired; in that case LockedFile is nil. The parent
	// directory must already exist.
	Acquire(path string) (LockedFile, error)
}

// LockedFile is a held exclusive lock plus in-place content access on the locked
// descriptor. Always Release it (defer) so the lock is dropped.
type LockedFile interface {
	// ReadAll returns the full current contents (empty for a freshly created file).
	ReadAll() ([]byte, error)
	// Write replaces the contents — truncate to zero, then write from offset 0 —
	// on the same locked descriptor.
	Write(data []byte) error
	// Release unlocks and closes the descriptor.
	Release() error
}

// lockPerm is the mode of a created lock file. It is out-of-tree, host-only state
// (PIDs, port, policy hash), so it gets the same 0600 as the audit log.
const lockPerm = 0o600

// OSFlock is the production Flock, backed by golang.org/x/sys/unix.Flock on an
// open descriptor (LOCK_EX, blocking). Release does LOCK_UN then Close.
type OSFlock struct{}

var _ Flock = (*OSFlock)(nil)

func (OSFlock) Acquire(path string) (LockedFile, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, lockPerm)
	if err != nil {
		return nil, fmt.Errorf("sysdep: open lock %q: %w", path, err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("sysdep: flock %q: %w", path, err)
	}
	return &osLockedFile{f: f}, nil
}

// osLockedFile holds the locked descriptor.
type osLockedFile struct {
	f *os.File
}

var _ LockedFile = (*osLockedFile)(nil)

func (l *osLockedFile) ReadAll() ([]byte, error) {
	if _, err := l.f.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("sysdep: seek lock: %w", err)
	}
	data, err := io.ReadAll(l.f)
	if err != nil {
		return nil, fmt.Errorf("sysdep: read lock: %w", err)
	}
	return data, nil
}

func (l *osLockedFile) Write(data []byte) error {
	if err := l.f.Truncate(0); err != nil {
		return fmt.Errorf("sysdep: truncate lock: %w", err)
	}
	if _, err := l.f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("sysdep: seek lock: %w", err)
	}
	if _, err := l.f.Write(data); err != nil {
		return fmt.Errorf("sysdep: write lock: %w", err)
	}
	return nil
}

func (l *osLockedFile) Release() error {
	// Close releases the advisory lock too; the explicit LOCK_UN documents intent
	// and drops the lock a moment earlier.
	_ = unix.Flock(int(l.f.Fd()), unix.LOCK_UN)
	return l.f.Close()
}
