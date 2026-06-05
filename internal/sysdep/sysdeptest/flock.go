package sysdeptest

import "github.com/tobyS/agent-creance/internal/sysdep"

// FakeFlock is a scripted Flock that records lock activity instead of touching a
// real descriptor. Acquired/Released list the paths in call order, and Held
// reports whether a path is currently locked. Set AcquireErr to make Acquire
// fail and ReleaseErr to make the release func fail.
type FakeFlock struct {
	// AcquireErr, if set, makes Acquire fail (and not record the path as held).
	AcquireErr error
	// ReleaseErr, if set, is returned by the release func.
	ReleaseErr error
	// Acquired lists the paths passed to Acquire, in order.
	Acquired []string
	// Released lists the paths whose release func was invoked, in order.
	Released []string

	held map[string]bool
}

var _ sysdep.Flock = (*FakeFlock)(nil)

// NewFakeFlock returns an empty, ready-to-use fake.
func NewFakeFlock() *FakeFlock {
	return &FakeFlock{held: map[string]bool{}}
}

func (f *FakeFlock) Acquire(path string) (func() error, error) {
	f.Acquired = append(f.Acquired, path)
	if f.AcquireErr != nil {
		return nil, f.AcquireErr
	}
	f.held[path] = true
	return func() error {
		f.Released = append(f.Released, path)
		delete(f.held, path)
		return f.ReleaseErr
	}, nil
}

// Held reports whether path is currently locked (acquired and not yet released).
func (f *FakeFlock) Held(path string) bool { return f.held[path] }
