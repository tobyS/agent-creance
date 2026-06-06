package sysdeptest

import (
	"sync"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// FakeFlock is a scripted Flock that records lock activity and backs each path's
// contents in memory instead of touching a real descriptor. Contents lets tests
// pre-seed and inspect the locked file's bytes; Acquired/Released list the paths
// in call order. Acquire takes a per-path real mutex so concurrent acquirers are
// genuinely serialised (modelling flock exclusion) — which makes -race
// attach/detach simulations meaningful. Set AcquireErr to make Acquire fail.
type FakeFlock struct {
	// Contents backs each path's lock-file bytes. Pre-seed and inspect here.
	Contents map[string][]byte
	// AcquireErr, if set, makes Acquire fail (no LockedFile returned).
	AcquireErr error
	// Acquired lists the paths passed to Acquire, in order.
	Acquired []string
	// Released lists the paths whose LockedFile was released, in order.
	Released []string

	guard sync.Mutex             // guards Contents, Acquired, Released, locks
	locks map[string]*sync.Mutex // per-path real mutex modelling exclusion
}

var (
	_ sysdep.Flock      = (*FakeFlock)(nil)
	_ sysdep.LockedFile = (*fakeLockedFile)(nil)
)

// NewFakeFlock returns an empty, ready-to-use fake.
func NewFakeFlock() *FakeFlock {
	return &FakeFlock{
		Contents: map[string][]byte{},
		locks:    map[string]*sync.Mutex{},
	}
}

func (f *FakeFlock) Acquire(path string) (sysdep.LockedFile, error) {
	if f.AcquireErr != nil {
		return nil, f.AcquireErr
	}
	m := f.lockFor(path)
	m.Lock() // blocks concurrent acquirers of the same path — models flock LOCK_EX
	f.guard.Lock()
	f.Acquired = append(f.Acquired, path)
	f.guard.Unlock()
	return &fakeLockedFile{flock: f, path: path, mu: m}, nil
}

// lockFor returns the per-path mutex, creating it on first use.
func (f *FakeFlock) lockFor(path string) *sync.Mutex {
	f.guard.Lock()
	defer f.guard.Unlock()
	m, ok := f.locks[path]
	if !ok {
		m = &sync.Mutex{}
		f.locks[path] = m
	}
	return m
}

// fakeLockedFile is the in-memory locked handle: ReadAll/Write round-trip through
// the parent's Contents map, Release records the path and drops the mutex.
type fakeLockedFile struct {
	flock    *FakeFlock
	path     string
	mu       *sync.Mutex
	released bool
}

func (l *fakeLockedFile) ReadAll() ([]byte, error) {
	l.flock.guard.Lock()
	defer l.flock.guard.Unlock()
	data := l.flock.Contents[l.path]
	out := make([]byte, len(data))
	copy(out, data)
	return out, nil
}

func (l *fakeLockedFile) Write(data []byte) error {
	l.flock.guard.Lock()
	defer l.flock.guard.Unlock()
	stored := make([]byte, len(data))
	copy(stored, data)
	l.flock.Contents[l.path] = stored
	return nil
}

func (l *fakeLockedFile) Release() error {
	if l.released {
		return nil
	}
	l.released = true
	l.flock.guard.Lock()
	l.flock.Released = append(l.flock.Released, l.path)
	l.flock.guard.Unlock()
	l.mu.Unlock()
	return nil
}
