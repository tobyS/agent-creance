package sysdeptest

import (
	"io/fs"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// FakeFileSystem is a scripted, in-memory FileSystem. You pre-load Files
// (path → contents) and Dirs (created directories); writes mutate that state so
// callers can assert on the result. Each operation has its own error knob
// (Errs for ReadFile, WriteErrs, StatErrs, …) checked before the operation, to
// simulate failures. A path present in no map yields fs.ErrNotExist, so callers
// exercising "absent file" behaviour can rely on errors.Is(err, fs.ErrNotExist).
type FakeFileSystem struct {
	// Files maps an absolute path to its byte contents (ReadFile/WriteFile).
	Files map[string][]byte
	// Dirs records directories created via MkdirAll (path → present).
	Dirs map[string]bool
	// Symlinks records paths that ReadDir surfaces as symlink entries (Type() has
	// fs.ModeSymlink, IsDir() is false), mirroring os.ReadDir's Lstat semantics. The
	// fake does not resolve them — a scanner that skips symlinks never reaches their
	// "target" — so they are the seam for testing symlink-skip behaviour.
	Symlinks map[string]bool
	// Perms records the mode passed to WriteFile/MkdirAll for a path.
	Perms map[string]fs.FileMode

	// Errs optionally maps a path to an error ReadFile should return instead,
	// simulating a file that exists but cannot be read.
	Errs map[string]error
	// WriteErrs / StatErrs / MkdirErrs / RemoveErrs / RenameErrs / ReadDirErrs
	// force the matching operation to fail for a given path (the oldpath for
	// Rename).
	WriteErrs   map[string]error
	StatErrs    map[string]error
	MkdirErrs   map[string]error
	RemoveErrs  map[string]error
	RenameErrs  map[string]error
	ReadDirErrs map[string]error

	// mu guards the maps so the fake is safe under concurrent access (e.g. the
	// proxy lifecycle's -race attach/detach simulation, where MkdirAll runs before
	// the flock is held). Direct map access from a test after the concurrent work
	// has joined needs no locking.
	mu sync.Mutex
}

var _ sysdep.FileSystem = (*FakeFileSystem)(nil)

// NewFakeFileSystem returns an empty, ready-to-populate fake.
func NewFakeFileSystem() *FakeFileSystem {
	return &FakeFileSystem{
		Files:       map[string][]byte{},
		Dirs:        map[string]bool{},
		Symlinks:    map[string]bool{},
		Perms:       map[string]fs.FileMode{},
		Errs:        map[string]error{},
		WriteErrs:   map[string]error{},
		StatErrs:    map[string]error{},
		MkdirErrs:   map[string]error{},
		RemoveErrs:  map[string]error{},
		RenameErrs:  map[string]error{},
		ReadDirErrs: map[string]error{},
	}
}

func (f *FakeFileSystem) ReadFile(name string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.Errs[name]; ok {
		return nil, err
	}
	if b, ok := f.Files[name]; ok {
		return b, nil
	}
	return nil, fs.ErrNotExist
}

// WriteFile mirrors os.WriteFile: it fails with fs.ErrNotExist when the parent
// directory does not exist, so a caller that forgot to MkdirAll first is caught
// in tests rather than in production.
func (f *FakeFileSystem) WriteFile(name string, data []byte, perm fs.FileMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.WriteErrs[name]; ok {
		return err
	}
	if parent := filepath.Dir(name); !f.dirExists(parent) {
		return &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	f.Files[name] = append([]byte(nil), data...)
	f.Perms[name] = perm
	return nil
}

// dirExists reports whether dir is a recorded directory, treating the filesystem
// roots ("/" and ".") as always present — mirroring os, where the root always
// exists. Caller must hold f.mu.
func (f *FakeFileSystem) dirExists(dir string) bool {
	if dir == "/" || dir == "." || dir == "" {
		return true
	}
	return f.Dirs[dir]
}

func (f *FakeFileSystem) Stat(name string) (fs.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.StatErrs[name]; ok {
		return nil, err
	}
	if b, ok := f.Files[name]; ok {
		return fakeFileInfo{name: filepath.Base(name), size: int64(len(b)), mode: f.Perms[name]}, nil
	}
	if f.Dirs[name] {
		return fakeFileInfo{name: filepath.Base(name), mode: f.Perms[name] | fs.ModeDir, dir: true}, nil
	}
	return nil, fs.ErrNotExist
}

// ReadDir returns the immediate children of name (entries whose parent dir is
// name), derived from the Files and Dirs maps and sorted by filename to mirror
// os.ReadDir. An unknown directory — one that is neither a recorded dir nor has
// any children — yields fs.ErrNotExist.
func (f *FakeFileSystem) ReadDir(name string) ([]fs.DirEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.ReadDirErrs[name]; ok {
		return nil, err
	}
	seen := map[string]bool{}
	var entries []fs.DirEntry
	add := func(path string, dir bool) {
		if filepath.Dir(path) != name {
			return
		}
		base := filepath.Base(path)
		if seen[base] {
			return
		}
		seen[base] = true
		mode := fs.FileMode(0)
		switch {
		case f.Symlinks[path]:
			mode, dir = fs.ModeSymlink, false
		case dir:
			mode = fs.ModeDir
		}
		entries = append(entries, fakeDirEntry{name: base, dir: dir, mode: mode})
	}
	for p := range f.Files {
		add(p, false)
	}
	for p := range f.Dirs {
		add(p, true)
	}
	for p := range f.Symlinks {
		add(p, false)
	}
	if len(entries) == 0 && !f.Dirs[name] {
		return nil, fs.ErrNotExist
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

// MkdirAll mirrors os.MkdirAll: it records name and every missing ancestor up to
// the root, so a subsequent WriteFile into a nested path succeeds.
func (f *FakeFileSystem) MkdirAll(name string, perm fs.FileMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.MkdirErrs[name]; ok {
		return err
	}
	for dir := name; dir != "/" && dir != "." && dir != ""; dir = filepath.Dir(dir) {
		f.Dirs[dir] = true
	}
	f.Perms[name] = perm
	return nil
}

// Remove mirrors os.Remove: removing a path that is in none of the maps returns
// fs.ErrNotExist (callers that want a no-op on a missing path use
// sysdep.RemoveIfPresent).
func (f *FakeFileSystem) Remove(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.RemoveErrs[name]; ok {
		return err
	}
	_, isFile := f.Files[name]
	if !isFile && !f.Dirs[name] && !f.Symlinks[name] {
		return &fs.PathError{Op: "remove", Path: name, Err: fs.ErrNotExist}
	}
	delete(f.Files, name)
	delete(f.Dirs, name)
	delete(f.Symlinks, name)
	delete(f.Perms, name)
	return nil
}

func (f *FakeFileSystem) Rename(oldpath, newpath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.RenameErrs[oldpath]; ok {
		return err
	}
	if b, ok := f.Files[oldpath]; ok {
		f.Files[newpath] = b
		f.Perms[newpath] = f.Perms[oldpath]
		delete(f.Files, oldpath)
		delete(f.Perms, oldpath)
		return nil
	}
	if f.Dirs[oldpath] {
		f.Dirs[newpath] = true
		f.Perms[newpath] = f.Perms[oldpath]
		delete(f.Dirs, oldpath)
		delete(f.Perms, oldpath)
		return nil
	}
	return fs.ErrNotExist
}

// fakeFileInfo is a minimal fs.FileInfo for FakeFileSystem.Stat. ModTime is the
// zero time — the fake does not model timestamps.
type fakeFileInfo struct {
	name string
	size int64
	mode fs.FileMode
	dir  bool
}

func (fi fakeFileInfo) Name() string       { return fi.name }
func (fi fakeFileInfo) Size() int64        { return fi.size }
func (fi fakeFileInfo) Mode() fs.FileMode  { return fi.mode }
func (fi fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fi fakeFileInfo) IsDir() bool        { return fi.dir }
func (fi fakeFileInfo) Sys() any           { return nil }

// fakeDirEntry is a minimal fs.DirEntry returned by FakeFileSystem.ReadDir.
type fakeDirEntry struct {
	name string
	dir  bool
	mode fs.FileMode
}

func (e fakeDirEntry) Name() string      { return e.name }
func (e fakeDirEntry) IsDir() bool       { return e.dir }
func (e fakeDirEntry) Type() fs.FileMode { return e.mode }
func (e fakeDirEntry) Info() (fs.FileInfo, error) {
	return fakeFileInfo{name: e.name, mode: e.mode, dir: e.dir}, nil
}
