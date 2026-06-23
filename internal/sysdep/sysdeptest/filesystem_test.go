package sysdeptest

import (
	"errors"
	"io/fs"
	"testing"
)

func TestFakeFileSystemWriteThenRead(t *testing.T) {
	f := NewFakeFileSystem()
	if err := f.MkdirAll("/p", 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
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
	if err := f.MkdirAll("/p", 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
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
	if err := f.MkdirAll("/p", 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
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
	if err := f.MkdirAll("/p", 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
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

func TestFakeFileSystemWriteMissingParentIsNotExist(t *testing.T) {
	f := NewFakeFileSystem()
	// No MkdirAll of /p first: WriteFile must fail like os.WriteFile does.
	if err := f.WriteFile("/p/policy.json", []byte("x"), 0o600); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("WriteFile(missing parent) error = %v, want fs.ErrNotExist", err)
	}
	if _, ok := f.Files["/p/policy.json"]; ok {
		t.Errorf("failed WriteFile should not have stored content")
	}
}

func TestFakeFileSystemMkdirAllCreatesParents(t *testing.T) {
	f := NewFakeFileSystem()
	if err := f.MkdirAll("/a/b/c", 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	for _, dir := range []string{"/a", "/a/b", "/a/b/c"} {
		fi, err := f.Stat(dir)
		if err != nil {
			t.Fatalf("Stat(%q): %v", dir, err)
		}
		if !fi.IsDir() {
			t.Errorf("Stat(%q).IsDir() = false, want true", dir)
		}
	}
}

func TestFakeFileSystemRemoveMissingIsNotExist(t *testing.T) {
	f := NewFakeFileSystem()
	if err := f.Remove("/absent"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Remove(absent) error = %v, want fs.ErrNotExist", err)
	}
}

func TestFakeFileSystemReadDirListsImmediateChildrenSorted(t *testing.T) {
	f := NewFakeFileSystem()
	// Two project sub-dirs and a file directly under /projects; a grandchild file
	// that must NOT surface as a /projects entry.
	if err := f.MkdirAll("/projects/bbbb", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := f.MkdirAll("/projects/aaaa", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := f.WriteFile("/projects/marker", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.WriteFile("/projects/aaaa/proxy.lock", []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, err := f.ReadDir("/projects")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	want := []string{"aaaa", "bbbb", "marker"} // sorted, no grandchild
	if len(got) != len(want) {
		t.Fatalf("ReadDir names = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ReadDir names = %v, want %v", got, want)
		}
	}
	// Dir vs file is reflected on the entries.
	for _, e := range entries {
		wantDir := e.Name() != "marker"
		if e.IsDir() != wantDir {
			t.Errorf("entry %q IsDir = %v, want %v", e.Name(), e.IsDir(), wantDir)
		}
	}
}

func TestFakeFileSystemReadDirMissingIsNotExist(t *testing.T) {
	f := NewFakeFileSystem()
	if _, err := f.ReadDir("/absent"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadDir(absent) error = %v, want fs.ErrNotExist", err)
	}
}

func TestFakeFileSystemReadDirEmptyDirIsNoError(t *testing.T) {
	f := NewFakeFileSystem()
	if err := f.MkdirAll("/projects", 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := f.ReadDir("/projects")
	if err != nil {
		t.Fatalf("ReadDir(empty dir): %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("ReadDir(empty dir) = %v, want no entries", entries)
	}
}

func TestFakeFileSystemReadDirErrIsScripted(t *testing.T) {
	f := NewFakeFileSystem()
	sentinel := errors.New("io error")
	f.ReadDirErrs["/projects"] = sentinel
	if _, err := f.ReadDir("/projects"); !errors.Is(err, sentinel) {
		t.Errorf("ReadDir error = %v, want %v", err, sentinel)
	}
}
