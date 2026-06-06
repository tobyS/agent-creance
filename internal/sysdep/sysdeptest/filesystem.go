package sysdeptest

import (
	"io/fs"
	"path/filepath"
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
	// Perms records the mode passed to WriteFile/MkdirAll for a path.
	Perms map[string]fs.FileMode

	// Errs optionally maps a path to an error ReadFile should return instead,
	// simulating a file that exists but cannot be read.
	Errs map[string]error
	// WriteErrs / StatErrs / MkdirErrs / RemoveErrs / RenameErrs force the
	// matching operation to fail for a given path (the oldpath for Rename).
	WriteErrs  map[string]error
	StatErrs   map[string]error
	MkdirErrs  map[string]error
	RemoveErrs map[string]error
	RenameErrs map[string]error

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
		Files:      map[string][]byte{},
		Dirs:       map[string]bool{},
		Perms:      map[string]fs.FileMode{},
		Errs:       map[string]error{},
		WriteErrs:  map[string]error{},
		StatErrs:   map[string]error{},
		MkdirErrs:  map[string]error{},
		RemoveErrs: map[string]error{},
		RenameErrs: map[string]error{},
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

func (f *FakeFileSystem) WriteFile(name string, data []byte, perm fs.FileMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.WriteErrs[name]; ok {
		return err
	}
	f.Files[name] = append([]byte(nil), data...)
	f.Perms[name] = perm
	return nil
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

func (f *FakeFileSystem) MkdirAll(name string, perm fs.FileMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.MkdirErrs[name]; ok {
		return err
	}
	f.Dirs[name] = true
	f.Perms[name] = perm
	return nil
}

func (f *FakeFileSystem) Remove(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.RemoveErrs[name]; ok {
		return err
	}
	delete(f.Files, name)
	delete(f.Dirs, name)
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
