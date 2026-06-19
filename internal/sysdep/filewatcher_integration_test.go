//go:build integration

// This test exercises the real fsnotify-backed OSFileWatcher on the host (kqueue
// on macOS): it watches a temp directory, writes and renames a file into it, and
// asserts the translated FileEvents arrive with the right Name and op — the
// directory-watch + atomic-save behaviour the run-session config watcher relies
// on. It needs no external tool, so it runs on any macOS host under
// `make test-integration`. The hermetic watcher *logic* (debounce, filtering,
// reconcile) is covered by the fake-driven tests in internal/configwatch.
package sysdep_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

func TestOSFileWatcherReportsDirectoryChanges(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("agent-creance is macOS-only")
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(target, []byte("initial\n"), 0o644))

	w, err := sysdep.OSFileWatcherFactory{}.NewWatcher()
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })

	require.NoError(t, w.Add(dir))
	require.Contains(t, w.WatchList(), dir)

	// A plain write to the watched file shows up as a Write (with Name == target).
	require.NoError(t, os.WriteFile(target, []byte("changed\n"), 0o644))
	requireEventFor(t, w, target, 5*time.Second)

	// An atomic save (write temp + rename over the target) shows up too — the
	// reason we watch the directory rather than the file.
	tmp := filepath.Join(dir, ".config.yaml.tmp")
	require.NoError(t, os.WriteFile(tmp, []byte("atomic\n"), 0o644))
	require.NoError(t, os.Rename(tmp, target))
	requireEventFor(t, w, target, 5*time.Second)

	// Close releases the watch and closes the channels.
	require.NoError(t, w.Close())
	requireChannelClosed(t, w, 5*time.Second)
}

// requireEventFor drains events until one names target, or fails after timeout.
func requireEventFor(t *testing.T, w sysdep.FileWatcher, target string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case e, ok := <-w.Events():
			if !ok {
				t.Fatalf("events channel closed before an event for %s", target)
			}
			if e.Name == target {
				return
			}
		case err := <-w.Errors():
			t.Fatalf("watcher error: %v", err)
		case <-deadline:
			t.Fatalf("timed out waiting for an event for %s", target)
		}
	}
}

// requireChannelClosed asserts the Events channel drains and closes after Close.
func requireChannelClosed(t *testing.T, w sysdep.FileWatcher, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case _, ok := <-w.Events():
			if !ok {
				return // channel closed — the documented shutdown signal
			}
		case <-deadline:
			t.Fatal("events channel was not closed after Close")
		}
	}
}
