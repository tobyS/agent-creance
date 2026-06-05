package sysdep

import "os"

// FileSystem abstracts reading file contents — the file *content* I/O seam the
// PathResolver doc anticipates (PathResolver only *locates* paths; this *reads*
// them). It is deliberately narrow: its first consumer, include resolution in
// internal/config (AC-0008), only needs ReadFile. Later phases (AC-0009) grow it
// with the write/stat/mkdir/remove/rename methods their consumers require.
//
// Why route file reads through the seam (for someone coming from PHP/TS): reading
// a file touches the real filesystem, so logic that called os.ReadFile directly
// could not be unit-tested hermetically. Packages take a FileSystem and call
// *that*; production wires OSFileSystem, tests wire the fake in sysdeptest.
type FileSystem interface {
	// ReadFile returns the contents of the named file, mirroring os.ReadFile. A
	// non-existent file yields an error satisfying errors.Is(err, fs.ErrNotExist),
	// so callers can distinguish "absent" from a genuine read failure.
	ReadFile(name string) ([]byte, error)
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
