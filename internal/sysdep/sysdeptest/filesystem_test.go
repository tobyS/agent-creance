package sysdeptest

import (
	"errors"
	"io/fs"
	"testing"
)

func TestFakeFileSystemWriteThenRead(t *testing.T) {
	f := NewFakeFileSystem()
	if err := f.WriteFile("/p/policy.json", []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := f.ReadFile("/p/policy.json")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "x" {
		t.Errorf("ReadFile = %q, want %q", got, "x")
	}
	if f.Perms["/p/policy.json"] != 0o600 {
		t.Errorf("Perms = %v, want 0600", f.Perms["/p/policy.json"])
	}
}

func TestFakeFileSystemReadMissingIsNotExist(t *testing.T) {
	f := NewFakeFileSystem()
	if _, err := f.ReadFile("/absent"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadFile(absent) error = %v, want fs.ErrNotExist", err)
	}
}

func TestFakeFileSystemStatReportsFileAndDir(t *testing.T) {
	f := NewFakeFileSystem()
	if err := f.WriteFile("/p/a", []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fi, err := f.Stat("/p/a")
	if err != nil {
		t.Fatalf("Stat(file): %v", err)
	}
	if fi.IsDir() || fi.Size() != 5 || fi.Name() != "a" {
		t.Errorf("Stat(file) = name=%q size=%d dir=%v, want name=a size=5 dir=false", fi.Name(), fi.Size(), fi.IsDir())
	}

	if err := f.MkdirAll("/p/d", 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	di, err := f.Stat("/p/d")
	if err != nil {
		t.Fatalf("Stat(dir): %v", err)
	}
	if !di.IsDir() {
		t.Errorf("Stat(dir).IsDir() = false, want true")
	}
}

func TestFakeFileSystemStatMissingIsNotExist(t *testing.T) {
	f := NewFakeFileSystem()
	if _, err := f.Stat("/absent"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Stat(absent) error = %v, want fs.ErrNotExist", err)
	}
}

func TestFakeFileSystemWriteErrIsScripted(t *testing.T) {
	f := NewFakeFileSystem()
	sentinel := errors.New("disk full")
	f.WriteErrs["/p/x"] = sentinel
	if err := f.WriteFile("/p/x", []byte("y"), 0o644); !errors.Is(err, sentinel) {
		t.Errorf("WriteFile error = %v, want %v", err, sentinel)
	}
	if _, ok := f.Files["/p/x"]; ok {
		t.Errorf("failed WriteFile should not have stored content")
	}
}

func TestFakeFileSystemRemoveDeletes(t *testing.T) {
	f := NewFakeFileSystem()
	if err := f.WriteFile("/p/x", []byte("y"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := f.Remove("/p/x"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := f.ReadFile("/p/x"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadFile after Remove error = %v, want fs.ErrNotExist", err)
	}
}

func TestFakeFileSystemRenameMovesEntry(t *testing.T) {
	f := NewFakeFileSystem()
	if err := f.WriteFile("/p/tmp", []byte("v"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := f.Rename("/p/tmp", "/p/final"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	got, err := f.ReadFile("/p/final")
	if err != nil || string(got) != "v" {
		t.Errorf("ReadFile(final) = %q, %v; want %q, nil", got, err, "v")
	}
	if _, err := f.ReadFile("/p/tmp"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadFile(tmp) after Rename error = %v, want fs.ErrNotExist", err)
	}
}

func TestFakeFileSystemRenameMissingIsNotExist(t *testing.T) {
	f := NewFakeFileSystem()
	if err := f.Rename("/absent", "/dest"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Rename(absent) error = %v, want fs.ErrNotExist", err)
	}
}
