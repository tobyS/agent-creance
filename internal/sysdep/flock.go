package sysdep

// Flock abstracts an exclusive advisory file lock — the primitive behind the
// atomic read-modify-write of the proxy.lock file in the multi-agent lifecycle
// (which records the proxy PID, port, policy hash, and attached-agent PIDs).
// It is a separate concern from FileSystem (content I/O): Flock only serialises
// access; the holder still reads/writes the file's contents through FileSystem.
//
// Why route locking through the seam (for someone coming from PHP/TS): a real
// advisory lock needs a real file descriptor and OS support, so lifecycle logic
// that called flock(2) directly could not be unit-tested. Packages take a Flock
// and call *that*; production wires OSFlock, tests wire the fake in sysdeptest.
type Flock interface {
	// Acquire takes an exclusive advisory lock on the file at path (creating it
	// if absent), blocking until the lock is held, and returns a release func
	// that unlocks and closes the underlying descriptor. A non-nil error means
	// the lock could not be acquired; in that case release is nil.
	Acquire(path string) (release func() error, err error)
}

// OSFlock is the production Flock. Its real behaviour is deferred to WP-3.4 (the
// proxy lifecycle): the implementation opens path and calls
// golang.org/x/sys/unix.Flock(fd, LOCK_EX), with release doing Flock(LOCK_UN)
// then Close. It is stubbed here (returns ErrNotImplemented) so this ticket adds
// no golang.org/x/sys dependency.
type OSFlock struct{}

var _ Flock = (*OSFlock)(nil)

func (OSFlock) Acquire(_ string) (func() error, error) {
	return nil, ErrNotImplemented
}
