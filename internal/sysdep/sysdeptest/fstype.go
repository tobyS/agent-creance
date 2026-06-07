package sysdeptest

import (
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// FakeFilesystemTyper is a scripted FilesystemTyper. FSType(path) returns
// Types[path] when present, else Default (a local apfs unless overridden), unless
// Err is set. It records the queried paths in Calls.
type FakeFilesystemTyper struct {
	// Types maps a path to the FSInfo FSType should return for it.
	Types map[string]sysdep.FSInfo
	// Default is returned for paths absent from Types. The zero value is overridden
	// by NewFakeFilesystemTyper to a local apfs so unconfigured paths look healthy.
	Default sysdep.FSInfo
	// Err, if set, is returned by every FSType call.
	Err error
	// Calls records the paths passed to FSType, in order.
	Calls []string
}

var _ sysdep.FilesystemTyper = (*FakeFilesystemTyper)(nil)

// NewFakeFilesystemTyper returns a fake that reports a local apfs by default.
func NewFakeFilesystemTyper() *FakeFilesystemTyper {
	return &FakeFilesystemTyper{
		Types:   map[string]sysdep.FSInfo{},
		Default: sysdep.FSInfo{Name: "apfs", Local: true},
	}
}

func (f *FakeFilesystemTyper) FSType(path string) (sysdep.FSInfo, error) {
	f.Calls = append(f.Calls, path)
	if f.Err != nil {
		return sysdep.FSInfo{}, f.Err
	}
	if info, ok := f.Types[path]; ok {
		return info, nil
	}
	return f.Default, nil
}
