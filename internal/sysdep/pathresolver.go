package sysdep

import (
	"os"
	"path/filepath"
)

// PathResolver abstracts the path-canonicalisation and environment primitives
// agent-creance needs to compute a project's stable identity path and the
// out-of-tree cache root. It is deliberately separate from a file *content* I/O
// seam (a future FileSystem interface): this concern only *locates* directories
// — resolving symlinks and reading the home/XDG environment — it never opens,
// reads, or writes their contents.
//
// Why route these through the seam at all (for someone coming from PHP/TS):
// resolving symlinks and reading $HOME touch the real filesystem and process
// environment, so logic that called them directly could not be unit-tested
// hermetically. Packages take a PathResolver and call *that*; production wires
// OSPathResolver, tests wire the fake in sysdeptest.
type PathResolver interface {
	// Abs returns an absolute representation of path, mirroring filepath.Abs.
	// A relative path is resolved against the current working directory.
	Abs(path string) (string, error)
	// EvalSymlinks returns path with all symlinks resolved, mirroring
	// filepath.EvalSymlinks. A non-nil error means the path does not exist:
	// symlink resolution requires the target to be present on disk.
	EvalSymlinks(path string) (string, error)
	// UserHomeDir returns the current user's home directory (os.UserHomeDir).
	UserHomeDir() (string, error)
	// Getenv returns the value of the environment variable named by key, or the
	// empty string if it is unset (os.Getenv).
	Getenv(key string) string
}

// OSPathResolver is the production PathResolver backed by path/filepath and os.
//
// The compile-time assertion mirrors the Commander idiom: assigning to the blank
// identifier forces the compiler to verify *OSPathResolver satisfies the
// interface, so a signature drift breaks the build here with a clear message.
type OSPathResolver struct{}

var _ PathResolver = (*OSPathResolver)(nil)

func (OSPathResolver) Abs(path string) (string, error) {
	return filepath.Abs(path)
}

func (OSPathResolver) EvalSymlinks(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

func (OSPathResolver) UserHomeDir() (string, error) {
	return os.UserHomeDir()
}

func (OSPathResolver) Getenv(key string) string {
	return os.Getenv(key)
}
