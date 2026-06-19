// Package configwatch watches a project's source config and its include graph
// during an active run session and recompiles the egress policy when any of those
// files change. It is the missing *trigger* for hot-reload: the compiler already
// writes policy.json atomically and the proxy enforcer already polls that file's
// mtime, so a successful recompile here reaches the live proxy with no restart.
//
// Design (see thoughts/shared/research/2026-06-19-AC-0053-config-hot-reload.md):
//   - Watch the parent *directories* of the config files and filter events by
//     name — editors save atomically (write temp + rename), which destroys a
//     watch placed on the file itself.
//   - Debounce a burst of events (one save fans out into several) with a re-armed
//     timer handled in the event loop's select, so the recompile and the
//     watch-set reconcile run on the single loop goroutine — no locking needed.
//   - On an invalid edit the recompile fails; the compiler leaves the previous
//     policy.json untouched (last-good) and we only print a warning.
//   - Feedback goes to the injected writer (run wires it to stderr), never the
//     single-goroutine progress printer.
package configwatch

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// defaultDebounce is the quiet period after the last change event before a
// recompile fires. One editor save can emit several events; this coalesces them.
const defaultDebounce = 100 * time.Millisecond

// ReloadFunc recompiles the policy. It returns changed=false when the recompile
// was a cache hit (the source changed but the compiled policy did not, so
// policy.json was not rewritten and the enforcer will not reload), a human
// summary like "5 allow, 2 deny" on a real recompile, or an error — in which case
// the previous policy stays in force (last-good).
type ReloadFunc func(ctx context.Context) (changed bool, summary string, err error)

// Option customizes a Watcher.
type Option func(*Watcher)

// WithDebounce overrides the default debounce period (used by tests to keep them
// fast).
func WithDebounce(d time.Duration) Option {
	return func(w *Watcher) { w.debounce = d }
}

// Watcher watches the resolved config include graph and recompiles on change.
// After a successful Start it must be stopped with Stop to release the underlying
// watcher and join its goroutine.
type Watcher struct {
	loader   *config.Loader
	factory  sysdep.FileWatcherFactory
	reload   ReloadFunc
	out      io.Writer
	debounce time.Duration

	projectPath string
	fw          sysdep.FileWatcher
	// watched is the set of canonical file paths whose events trigger a reload.
	// It is touched only by the loop goroutine (and by Start before that goroutine
	// is launched), so it needs no lock.
	watched map[string]struct{}

	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// New constructs a Watcher. The loader enumerates the include graph; the factory
// supplies the (real or fake) file watcher; reload performs the recompile; out
// receives the concise feedback lines.
func New(loader *config.Loader, factory sysdep.FileWatcherFactory, reload ReloadFunc, out io.Writer, opts ...Option) *Watcher {
	w := &Watcher{
		loader:   loader,
		factory:  factory,
		reload:   reload,
		out:      out,
		debounce: defaultDebounce,
		watched:  map[string]struct{}{},
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

// Start resolves the include graph for projectConfigPath, begins watching the
// parent directories of every contributing file, and launches the event loop. A
// resolution or watcher-creation failure is returned (the caller treats hot-reload
// as advisory); a failure to watch an individual directory is a warning, not an
// error.
func (w *Watcher) Start(ctx context.Context, projectConfigPath string) error {
	w.projectPath = projectConfigPath

	files, err := w.loader.ResolveFiles(projectConfigPath)
	if err != nil {
		return fmt.Errorf("configwatch: enumerate config files: %w", err)
	}
	fw, err := w.factory.NewWatcher()
	if err != nil {
		return fmt.Errorf("configwatch: create watcher: %w", err)
	}
	w.fw = fw

	w.setWatched(files)
	for _, dir := range dirsOf(files) {
		if err := w.fw.Add(dir); err != nil {
			fmt.Fprintf(w.out, "⚠ config watch: cannot watch %s: %v\n", dir, err)
		}
	}

	go w.loop(ctx)
	return nil
}

// Stop signals the event loop, waits for it to exit, and closes the watcher. It
// is safe to call once after a successful Start; calling it when Start was never
// reached is a no-op.
func (w *Watcher) Stop() error {
	w.stopOnce.Do(func() { close(w.stop) })
	if w.fw == nil {
		return nil
	}
	<-w.done
	return w.fw.Close()
}

// loop is the single goroutine driving the watcher: it filters events, debounces
// them with a re-armed timer, and recompiles once the dust settles.
func (w *Watcher) loop(ctx context.Context) {
	defer close(w.done)

	var timer *time.Timer
	var debounceCh <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		case e, ok := <-w.fw.Events():
			if !ok {
				return
			}
			// Only content changes matter; ignore Chmod (Spotlight/backup noise on
			// macOS) and events for files outside the include graph.
			if !e.Op.Has(sysdep.FileCreate) && !e.Op.Has(sysdep.FileWrite) {
				continue
			}
			if _, ok := w.watched[e.Name]; !ok {
				continue
			}
			if timer == nil {
				timer = time.NewTimer(w.debounce)
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(w.debounce)
			}
			debounceCh = timer.C
		case err, ok := <-w.fw.Errors():
			if !ok {
				return
			}
			fmt.Fprintf(w.out, "⚠ config watch: %v\n", err)
		case <-debounceCh:
			debounceCh = nil
			w.doReload(ctx)
		}
	}
}

// doReload recompiles, reports the outcome, and (on success) re-derives the watch
// set so newly added or removed includes are tracked. On failure it leaves the
// watch set untouched — the last-good policy stays in force.
func (w *Watcher) doReload(ctx context.Context) {
	changed, summary, err := w.reload(ctx)
	if err != nil {
		fmt.Fprintf(w.out, "⚠ config reload failed, keeping last-good policy: %v\n", err)
		return
	}
	if changed {
		fmt.Fprintf(w.out, "✓ config reloaded (%s)\n", summary)
	} else {
		fmt.Fprintf(w.out, "config changed; policy unchanged\n")
	}
	w.rederive()
}

// rederive recomputes the include graph after a successful recompile (an edit may
// have added or removed an include) and reconciles the watched directories.
func (w *Watcher) rederive() {
	files, err := w.loader.ResolveFiles(w.projectPath)
	if err != nil {
		fmt.Fprintf(w.out, "⚠ config watch: re-scan failed, keeping previous watch set: %v\n", err)
		return
	}
	w.setWatched(files)
	w.reconcileDirs(dirsOf(files))
}

// reconcileDirs adds directories newly in the watch set and removes ones no longer
// needed, diffing the desired set against what the watcher currently holds.
func (w *Watcher) reconcileDirs(desired []string) {
	want := make(map[string]struct{}, len(desired))
	for _, d := range desired {
		want[d] = struct{}{}
	}
	have := make(map[string]struct{})
	for _, d := range w.fw.WatchList() {
		have[d] = struct{}{}
	}
	for d := range want {
		if _, ok := have[d]; !ok {
			if err := w.fw.Add(d); err != nil {
				fmt.Fprintf(w.out, "⚠ config watch: cannot watch %s: %v\n", d, err)
			}
		}
	}
	for d := range have {
		if _, ok := want[d]; !ok {
			_ = w.fw.Remove(d) // a no-longer-watched dir; ignore not-watched errors
		}
	}
}

func (w *Watcher) setWatched(files []string) {
	m := make(map[string]struct{}, len(files))
	for _, f := range files {
		m[f] = struct{}{}
	}
	w.watched = m
}

// dirsOf returns the deduplicated parent directories of files (the paths we watch,
// since editors save atomically and a directory watch survives the rename).
func dirsOf(files []string) []string {
	seen := map[string]struct{}{}
	var dirs []string
	for _, f := range files {
		d := filepath.Dir(f)
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		dirs = append(dirs, d)
	}
	return dirs
}
