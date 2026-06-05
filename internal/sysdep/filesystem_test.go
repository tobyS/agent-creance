package sysdep

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// Smoke tests for the real OSFileSystem against the actual filesystem. Like the
// OSPathResolver tests, the real impl's correctness is verified once here; logic
// packages (e.g. internal/config) use the in-memory fake in sysdeptest.

func TestOSFileSystemReadFileRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	want := []byte("agent:\n  workdir: .\n")
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatal(err)
	}

	var fsys OSFileSystem
	got, err := fsys.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("ReadFile = %q, want %q", got, want)
	}
}

func TestOSFileSystemReadFileMissingIsNotExist(t *testing.T) {
	var fsys OSFileSystem
	_, err := fsys.ReadFile(filepath.Join(t.TempDir(), "absent.yaml"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadFile(missing) error = %v, want errors.Is(fs.ErrNotExist)", err)
	}
}
