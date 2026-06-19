package sysdeptest

import (
	"sync"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// FakeFileWatcher is a scripted FileWatcher. Tests push synthetic events onto
// EventsCh / ErrorsCh and assert against the recorded Add/Remove calls and the
// current watch list. It is mutex-guarded because the watcher under test reads it
// from a background goroutine while the test mutates and asserts.
type FakeFileWatcher struct {
	// EventsCh and ErrorsCh back Events()/Errors(); tests send on them to drive
	// the watcher. They are buffered so a test can enqueue without blocking.
	EventsCh chan sysdep.FileEvent
	ErrorsCh chan error
	// AddErrs maps a path to an error Add should return for it.
	AddErrs map[string]error
	// RemoveErr, if set, is returned by Remove.
	RemoveErr error

	mu      sync.Mutex
	added   []string
	removed []string
	watched map[string]struct{}
	closed  bool
}

var _ sysdep.FileWatcher = (*FakeFileWatcher)(nil)

// NewFakeFileWatcher returns a ready-to-use fake with buffered channels.
func NewFakeFileWatcher() *FakeFileWatcher {
	return &FakeFileWatcher{
		EventsCh: make(chan sysdep.FileEvent, 64),
		ErrorsCh: make(chan error, 8),
		AddErrs:  map[string]error{},
		watched:  map[string]struct{}{},
	}
}

func (f *FakeFileWatcher) Add(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.added = append(f.added, path)
	if err := f.AddErrs[path]; err != nil {
		return err
	}
	f.watched[path] = struct{}{}
	return nil
}

func (f *FakeFileWatcher) Remove(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, path)
	if f.RemoveErr != nil {
		return f.RemoveErr
	}
	delete(f.watched, path)
	return nil
}

func (f *FakeFileWatcher) WatchList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.watched))
	for p := range f.watched {
		out = append(out, p)
	}
	return out
}

func (f *FakeFileWatcher) Events() <-chan sysdep.FileEvent { return f.EventsCh }
func (f *FakeFileWatcher) Errors() <-chan error            { return f.ErrorsCh }

// Close records the call and closes the channels, mirroring the real watcher's
// "channel closed signals shutdown" contract. Safe to call once.
func (f *FakeFileWatcher) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	close(f.EventsCh)
	close(f.ErrorsCh)
	return nil
}

// Added returns the paths passed to Add, in call order.
func (f *FakeFileWatcher) Added() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.added...)
}

// Removed returns the paths passed to Remove, in call order.
func (f *FakeFileWatcher) Removed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.removed...)
}

// Closed reports whether Close has been called.
func (f *FakeFileWatcher) Closed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// FakeFileWatcherFactory hands out a single FakeFileWatcher (or NewErr).
type FakeFileWatcherFactory struct {
	// Watcher is returned by NewWatcher when NewErr is nil.
	Watcher *FakeFileWatcher
	// NewErr, if set, makes NewWatcher fail.
	NewErr error
}

var _ sysdep.FileWatcherFactory = (*FakeFileWatcherFactory)(nil)

// NewFakeFileWatcherFactory wires a factory over a fresh FakeFileWatcher.
func NewFakeFileWatcherFactory() *FakeFileWatcherFactory {
	return &FakeFileWatcherFactory{Watcher: NewFakeFileWatcher()}
}

func (f *FakeFileWatcherFactory) NewWatcher() (sysdep.FileWatcher, error) {
	if f.NewErr != nil {
		return nil, f.NewErr
	}
	return f.Watcher, nil
}
