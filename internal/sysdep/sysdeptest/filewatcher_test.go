package sysdeptest

import (
	"errors"
	"testing"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

func TestFakeFileWatcher_AddRemoveWatchList(t *testing.T) {
	w := NewFakeFileWatcher()
	if err := w.Add("/a"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := w.Add("/b"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := w.Remove("/a"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := w.WatchList(); len(got) != 1 || got[0] != "/b" {
		t.Errorf("WatchList = %v, want [/b]", got)
	}
	if got := w.Added(); len(got) != 2 {
		t.Errorf("Added = %v, want two entries", got)
	}
	if got := w.Removed(); len(got) != 1 || got[0] != "/a" {
		t.Errorf("Removed = %v, want [/a]", got)
	}
}

func TestFakeFileWatcher_AddErr(t *testing.T) {
	w := NewFakeFileWatcher()
	w.AddErrs["/bad"] = errors.New("boom")
	if err := w.Add("/bad"); err == nil {
		t.Fatal("Add: want error")
	}
	if got := w.WatchList(); len(got) != 0 {
		t.Errorf("WatchList = %v, want empty (failed add not watched)", got)
	}
}

func TestFakeFileWatcher_CloseClosesChannels(t *testing.T) {
	w := NewFakeFileWatcher()
	w.EventsCh <- sysdep.FileEvent{Name: "/x", Op: sysdep.FileWrite}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !w.Closed() {
		t.Error("Closed() = false after Close")
	}
	// Drain the buffered event, then expect the closed signal.
	if e, ok := <-w.Events(); !ok || e.Name != "/x" {
		t.Errorf("first recv = (%v,%v), want buffered event", e, ok)
	}
	if _, ok := <-w.Events(); ok {
		t.Error("Events channel not closed after Close")
	}
	// Close is safe to call again.
	if err := w.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestFakeFileWatcherFactory(t *testing.T) {
	f := NewFakeFileWatcherFactory()
	w, err := f.NewWatcher()
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	if w != sysdep.FileWatcher(f.Watcher) {
		t.Error("NewWatcher returned a different watcher than f.Watcher")
	}

	f.NewErr = errors.New("nope")
	if _, err := f.NewWatcher(); err == nil {
		t.Error("NewWatcher: want error when NewErr set")
	}
}
