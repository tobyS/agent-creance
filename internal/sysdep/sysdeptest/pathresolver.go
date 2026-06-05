package sysdeptest

import "path/filepath"

// FakePathResolver is a scripted PathResolver. You pre-load the symlink topology
// (which input path resolves to which canonical path), the home directory, and
// the environment; it then answers without touching the real filesystem. This
// lets callers exercise canonical-path/identity logic — including the "a symlink
// and its target collapse to one identity" property — entirely in memory.
type FakePathResolver struct {
	// Cwd is the base directory Abs joins relative paths against. Defaults to
	// "/" via NewFakePathResolver.
	Cwd string
	// Symlinks maps an input path to its resolved (canonical) path. A path
	// absent from the map resolves to itself, i.e. it is already canonical.
	Symlinks map[string]string
	// HomeDir is returned by UserHomeDir.
	HomeDir string
	// HomeErr, if set, is returned by UserHomeDir instead of HomeDir.
	HomeErr error
	// Env backs Getenv; an absent key yields the empty string.
	Env map[string]string
	// AbsErr / EvalErr, if set, make Abs / EvalSymlinks fail, simulating a
	// missing or unreadable path.
	AbsErr  error
	EvalErr error
}

// NewFakePathResolver returns an empty, ready-to-populate fake with Cwd set to
// "/" so relative paths resolve deterministically.
func NewFakePathResolver() *FakePathResolver {
	return &FakePathResolver{
		Cwd:      "/",
		Symlinks: map[string]string{},
		Env:      map[string]string{},
	}
}

func (f *FakePathResolver) Abs(path string) (string, error) {
	if f.AbsErr != nil {
		return "", f.AbsErr
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	return filepath.Join(f.Cwd, path), nil
}

func (f *FakePathResolver) EvalSymlinks(path string) (string, error) {
	if f.EvalErr != nil {
		return "", f.EvalErr
	}
	if resolved, ok := f.Symlinks[path]; ok {
		return resolved, nil
	}
	return path, nil
}

func (f *FakePathResolver) UserHomeDir() (string, error) {
	if f.HomeErr != nil {
		return "", f.HomeErr
	}
	return f.HomeDir, nil
}

func (f *FakePathResolver) Getenv(key string) string {
	return f.Env[key]
}
