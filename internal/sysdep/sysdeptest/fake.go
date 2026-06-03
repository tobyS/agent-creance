// Package sysdeptest provides test doubles for the sysdep interfaces.
//
// It lives in its own package (not a _test.go file) so that the tests of
// *other* packages can import it — the same pattern the standard library uses
// with net/http/httptest. Keeping fakes out of production packages means they
// never get compiled into the shipped binary.
package sysdeptest

import (
	"context"
	"fmt"
)

// FakeCommander is a scripted Commander. You pre-load the executables it should
// "find" and the output each command should return; it records nothing it isn't
// told about. This lets a unit test exercise the prerequisite/version logic
// without a real agent-safehouse or mitmproxy installed.
type FakeCommander struct {
	// Paths maps an executable name to the path LookPath should return. A name
	// absent from the map is treated as "not installed".
	Paths map[string]string
	// Outputs maps an executable name to the bytes Output should return for it.
	Outputs map[string][]byte
	// Errs optionally maps an executable name to an error Output should return,
	// simulating a tool that exists but fails when invoked.
	Errs map[string]error
}

// NewFakeCommander returns an empty, ready-to-populate fake.
func NewFakeCommander() *FakeCommander {
	return &FakeCommander{
		Paths:   map[string]string{},
		Outputs: map[string][]byte{},
		Errs:    map[string]error{},
	}
}

// WithTool is a small builder helper: register an executable as installed, with
// the version output it should emit. Returns the receiver for chaining.
func (f *FakeCommander) WithTool(name, path string, output string) *FakeCommander {
	f.Paths[name] = path
	f.Outputs[name] = []byte(output)
	return f
}

func (f *FakeCommander) LookPath(name string) (string, error) {
	if p, ok := f.Paths[name]; ok {
		return p, nil
	}
	// Mirror os/exec's error shape closely enough for callers that only check
	// for non-nil.
	return "", fmt.Errorf("%s: executable file not found in $PATH", name)
}

func (f *FakeCommander) Output(_ context.Context, name string, _ ...string) ([]byte, error) {
	if err, ok := f.Errs[name]; ok {
		return nil, err
	}
	if out, ok := f.Outputs[name]; ok {
		return out, nil
	}
	return nil, fmt.Errorf("%s: no scripted output", name)
}
