package sysdep

import "github.com/fsnotify/fsnotify"

// FileOp is a bitmask of filesystem change kinds, mirroring fsnotify.Op without
// leaking the dependency into logic packages. Use Has to test membership.
type FileOp uint32

const (
	// FileCreate reports that a path was created in a watched directory.
	FileCreate FileOp = 1 << iota
	// FileWrite reports that a watched path was written to.
	FileWrite
	// FileRemove reports that a watched path was removed.
	FileRemove
	// FileRename reports that a watched path was renamed (old name in the event).
	FileRename
	// FileChmod reports a metadata change (mode/attributes/truncation).
	FileChmod
)

// Has reports whether o includes the kind x.
func (o FileOp) Has(x FileOp) bool { return o&x != 0 }

// FileEvent is a single filesystem change: the affected path and what happened.
type FileEvent struct {
	Name string
	Op   FileOp
}

// FileWatcher watches filesystem paths and reports change events. It abstracts
// fsnotify so logic packages (the run-session config watcher) stay testable
// against a fake. Watch *directories* and filter events by Name: editors save
// atomically (write temp + rename), which destroys a watch placed on a file
// itself.
//
// Why route file watching through the seam (for someone coming from PHP/TS): a
// real fsnotify watcher touches the OS (kqueue on macOS) and races the wall
// clock, so it cannot be exercised hermetically. Packages take a FileWatcher and
// drive *that*; production wires the fsnotify-backed impl, tests wire the fake in
// sysdeptest.
type FileWatcher interface {
	// Add starts watching path (typically a directory). Watching a path more than
	// once is a no-op.
	Add(path string) error
	// Remove stops watching path. Removing a path that is not watched returns an
	// error (callers reconciling a set should ignore that case).
	Remove(path string) error
	// WatchList returns the paths currently watched, in unspecified order.
	WatchList() []string
	// Events delivers change events until the watcher is closed, at which point
	// the channel is closed.
	Events() <-chan FileEvent
	// Errors delivers watcher errors until the watcher is closed, at which point
	// the channel is closed.
	Errors() <-chan error
	// Close stops all watches and closes the Events and Errors channels.
	Close() error
}

// FileWatcherFactory constructs FileWatchers. It is the injected seam (rather
// than a plain constructor) so the run-session config watcher can be handed a
// fake watcher in unit tests.
type FileWatcherFactory interface {
	NewWatcher() (FileWatcher, error)
}

// OSFileWatcherFactory is the production FileWatcherFactory backed by fsnotify.
type OSFileWatcherFactory struct{}

var _ FileWatcherFactory = OSFileWatcherFactory{}

// NewWatcher constructs an fsnotify-backed watcher. It is exercised only under
// the integration build tag (real kqueue), never in hermetic unit tests.
func (OSFileWatcherFactory) NewWatcher() (FileWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	fw := &osFileWatcher{
		w:      w,
		events: make(chan FileEvent),
		errs:   make(chan error),
	}
	go fw.translate()
	return fw, nil
}

// osFileWatcher wraps *fsnotify.Watcher and translates its events into the
// dependency-free FileEvent/FileOp types. The translation goroutine forwards
// fsnotify's channels onto ours and exits when fsnotify closes them (on Close),
// at which point our channels close too — the consumer's "channel closed" signal.
type osFileWatcher struct {
	w      *fsnotify.Watcher
	events chan FileEvent
	errs   chan error
}

var _ FileWatcher = (*osFileWatcher)(nil)

func (f *osFileWatcher) translate() {
	defer close(f.events)
	defer close(f.errs)
	for {
		select {
		case e, ok := <-f.w.Events:
			if !ok {
				return
			}
			f.events <- FileEvent{Name: e.Name, Op: translateOp(e.Op)}
		case err, ok := <-f.w.Errors:
			if !ok {
				return
			}
			f.errs <- err
		}
	}
}

func (f *osFileWatcher) Add(path string) error    { return f.w.Add(path) }
func (f *osFileWatcher) Remove(path string) error { return f.w.Remove(path) }
func (f *osFileWatcher) WatchList() []string      { return f.w.WatchList() }
func (f *osFileWatcher) Events() <-chan FileEvent { return f.events }
func (f *osFileWatcher) Errors() <-chan error     { return f.errs }
func (f *osFileWatcher) Close() error             { return f.w.Close() }

// translateOp maps fsnotify's Op bits onto FileOp.
func translateOp(op fsnotify.Op) FileOp {
	var out FileOp
	if op.Has(fsnotify.Create) {
		out |= FileCreate
	}
	if op.Has(fsnotify.Write) {
		out |= FileWrite
	}
	if op.Has(fsnotify.Remove) {
		out |= FileRemove
	}
	if op.Has(fsnotify.Rename) {
		out |= FileRename
	}
	if op.Has(fsnotify.Chmod) {
		out |= FileChmod
	}
	return out
}
