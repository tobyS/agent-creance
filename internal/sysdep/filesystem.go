package sysdep

import (
	"errors"
	"io/fs"
	"os"
)

// FileSystem abstracts file *content* I/O — the seam the PathResolver doc
// anticipates (PathResolver only *locates* paths; this reads and writes them).
// It covers the out-of-tree state operations later phases need: reading config
// includes (internal/config), and writing/inspecting the compiled policy.json,
// network.sb, the proxy lock file, the audit log, and the extracted enforcer.py.
//
// Why route file I/O through the seam (for someone coming from PHP/TS): touching
// the real filesystem makes logic that called os.ReadFile/os.WriteFile directly
// impossible to unit-test hermetically. Packages take a FileSystem and call
// *that*; production wires OSFileSystem, tests wire the fake in sysdeptest.
type FileSystem interface {
	// ReadFile returns the contents of the named file, mirroring os.ReadFile. A
	// non-existent file yields an error satisfying errors.Is(err, fs.ErrNotExist),
	// so callers can distinguish "absent" from a genuine read failure.
	ReadFile(name string) ([]byte, error)
	// WriteFile writes data to name with perm, mirroring os.WriteFile: it creates
	// the file if absent and truncates it otherwise. A non-nil error means the
	// write failed.
	WriteFile(name string, data []byte, perm fs.FileMode) error
	// Stat returns file info for name, mirroring os.Stat. A non-existent path
	// yields an error satisfying errors.Is(err, fs.ErrNotExist).
	Stat(name string) (fs.FileInfo, error)
	// ReadDir returns name's directory entries sorted by filename, mirroring
	// os.ReadDir. A non-existent directory yields an error satisfying
	// errors.Is(err, fs.ErrNotExist), so callers (e.g. `status` enumerating the
	// projects/ root before any project exists) can treat "absent" as empty.
	ReadDir(name string) ([]fs.DirEntry, error)
	// MkdirAll creates name and any missing parents with perm, mirroring
	// os.MkdirAll. It is a no-op (nil) if the directory already exists.
	MkdirAll(name string, perm fs.FileMode) error
	// Chmod changes name's mode, mirroring os.Chmod. It exists because MkdirAll
	// only applies perm to directories it actually *creates*: a state dir left
	// behind at a laxer mode by an earlier binary would silently stay that way,
	// and the broker's socket directory has to be 0700 whether it is new or not.
	Chmod(name string, perm fs.FileMode) error
	// Remove removes the named file or empty directory, mirroring os.Remove.
	Remove(name string) error
	// Rename renames (moves) oldpath to newpath, mirroring os.Rename — the
	// primitive behind atomic "write to a temp file then rename" updates.
	Rename(oldpath, newpath string) error
}

// OSFileSystem is the production FileSystem backed by the os package.
//
// The compile-time assertion mirrors the Commander/PathResolver idiom: assigning
// to the blank identifier forces the compiler to verify *OSFileSystem satisfies
// the interface, so a signature drift breaks the build here with a clear message.
type OSFileSystem struct{}

var _ FileSystem = (*OSFileSystem)(nil)

func (OSFileSystem) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(name)
}

func (OSFileSystem) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return os.WriteFile(name, data, perm)
}

func (OSFileSystem) Stat(name string) (fs.FileInfo, error) {
	return os.Stat(name)
}

func (OSFileSystem) ReadDir(name string) ([]fs.DirEntry, error) {
	return os.ReadDir(name)
}

func (OSFileSystem) MkdirAll(name string, perm fs.FileMode) error {
	return os.MkdirAll(name, perm)
}

func (OSFileSystem) Chmod(name string, perm fs.FileMode) error {
	return os.Chmod(name, perm)
}

func (OSFileSystem) Remove(name string) error {
	return os.Remove(name)
}

func (OSFileSystem) Rename(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}

// RemoveIfPresent removes name when it exists, reporting whether it did. It papers
// over the gap between os.Remove (which errors on a missing path) and a caller that
// treats "already gone" as success: a Stat establishes existence first, so the
// returned boolean is a reliable "was it there?" — an absent path is (false, nil),
// and a genuine Stat or Remove failure is surfaced. The cache-invalidation paths
// (`policy refresh`) use it to count exactly the entries they actually cleared,
// independent of whether the FileSystem is the OS or the in-memory test fake (whose
// Remove is a no-op on a missing path).
func RemoveIfPresent(fsys FileSystem, name string) (bool, error) {
	if _, err := fsys.Stat(name); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if err := fsys.Remove(name); err != nil {
		return false, err
	}
	return true, nil
}
