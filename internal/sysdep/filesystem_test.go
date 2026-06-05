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

func TestOSFileSystemWriteFileRoundTrips(t *testing.T) {
	var fsys OSFileSystem
	path := filepath.Join(t.TempDir(), "policy.json")
	want := []byte(`{"egress":[]}`)
	if err := fsys.WriteFile(path, want, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := fsys.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("ReadFile = %q, want %q", got, want)
	}
}

func TestOSFileSystemMkdirAllAndStatDir(t *testing.T) {
	var fsys OSFileSystem
	dir := filepath.Join(t.TempDir(), "a", "b", "c")
	if err := fsys.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	info, err := fsys.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("Stat(%q).IsDir() = false, want true", dir)
	}
}

func TestOSFileSystemStatMissingIsNotExist(t *testing.T) {
	var fsys OSFileSystem
	_, err := fsys.Stat(filepath.Join(t.TempDir(), "absent"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Stat(missing) error = %v, want errors.Is(fs.ErrNotExist)", err)
	}
}

func TestOSFileSystemRemoveThenReadIsNotExist(t *testing.T) {
	var fsys OSFileSystem
	path := filepath.Join(t.TempDir(), "lock")
	if err := fsys.WriteFile(path, []byte("pid"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := fsys.Remove(path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := fsys.ReadFile(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadFile after Remove error = %v, want errors.Is(fs.ErrNotExist)", err)
	}
}

func TestOSFileSystemRenameMovesContent(t *testing.T) {
	var fsys OSFileSystem
	dir := t.TempDir()
	tmp := filepath.Join(dir, "policy.json.tmp")
	final := filepath.Join(dir, "policy.json")
	want := []byte("compiled")
	if err := fsys.WriteFile(tmp, want, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := fsys.Rename(tmp, final); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	got, err := fsys.ReadFile(final)
	if err != nil {
		t.Fatalf("ReadFile(final): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("ReadFile(final) = %q, want %q", got, want)
	}
	if _, err := fsys.ReadFile(tmp); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadFile(tmp) after Rename error = %v, want errors.Is(fs.ErrNotExist)", err)
	}
}
