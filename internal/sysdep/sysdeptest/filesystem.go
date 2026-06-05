package sysdeptest

import "io/fs"

// FakeFileSystem is a scripted, in-memory FileSystem. You pre-load Files
// (path → contents) and optionally Errs (path → error to force a read failure);
// it then answers without touching the real filesystem. A path present in neither
// map yields fs.ErrNotExist, so callers exercising "absent file" behaviour
// (e.g. a skipped implicit global) can rely on errors.Is(err, fs.ErrNotExist).
type FakeFileSystem struct {
	// Files maps an absolute path to the bytes ReadFile should return for it.
	Files map[string][]byte
	// Errs optionally maps a path to an error ReadFile should return instead,
	// simulating a file that exists but cannot be read.
	Errs map[string]error
}

// NewFakeFileSystem returns an empty, ready-to-populate fake.
func NewFakeFileSystem() *FakeFileSystem {
	return &FakeFileSystem{
		Files: map[string][]byte{},
		Errs:  map[string]error{},
	}
}

func (f *FakeFileSystem) ReadFile(name string) ([]byte, error) {
	if err, ok := f.Errs[name]; ok {
		return nil, err
	}
	if b, ok := f.Files[name]; ok {
		return b, nil
	}
	return nil, fs.ErrNotExist
}
