// Package sysdep defines small interfaces over the operating-system facilities
// agent-creance depends on (running external commands, later: keychain access,
// the clock, the filesystem). This is the central testability seam.
//
// Why this exists (for someone coming from PHP/TS): the rest of the codebase
// never calls os/exec, the keychain, or time.Now() directly. It takes a sysdep
// interface as a constructor argument and calls *that*. Production code injects
// the real implementation; tests inject a fake. Because Go interfaces are
// satisfied structurally (no `implements` keyword — a type satisfies an
// interface simply by having the right methods), a test fake is just a small
// struct with the same method set. This is dependency injection without a
// container.
package sysdep

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// Commander abstracts process execution: locating an executable on PATH and
// capturing the output of running one. Keeping this tiny is deliberate —
// interfaces in Go are best kept small and defined at the point of use, so a
// fake only has to implement what a given caller actually needs.
type Commander interface {
	// LookPath reports the absolute path of an executable found on PATH,
	// mirroring os/exec.LookPath. A non-nil error means "not installed".
	LookPath(name string) (string, error)
	// Output runs name with args and returns its combined stdout. ctx allows
	// cancellation/timeouts so a hung `--version` call can't wedge the CLI.
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
	// OutputStdout runs name with args and returns ONLY its stdout, keeping
	// stderr out of the returned bytes. Unlike Output — which merges stderr
	// deliberately (some tools print their version banner there) — this is for the
	// case where stdout carries a secret and the tool may emit unrelated notices
	// to stderr (e.g. `op` writes update/deprecation notices to stderr even on a
	// successful read). On a non-zero exit it returns an error wrapping the trimmed
	// stderr — never stdout, which may hold the secret.
	OutputStdout(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecCommander is the production Commander backed by the os/exec package.
//
// The compile-time assertion below is a common Go idiom: assigning to the blank
// identifier `_` of the interface type forces the compiler to verify that
// *ExecCommander satisfies Commander. If a method signature drifts, the build
// breaks here with a clear message instead of somewhere downstream.
type ExecCommander struct{}

var _ Commander = (*ExecCommander)(nil)

func (ExecCommander) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

func (ExecCommander) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	// CommandContext kills the process if ctx is cancelled. We capture combined
	// output because some tools print their version banner to stderr.
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func (ExecCommander) OutputStdout(ctx context.Context, name string, args ...string) ([]byte, error) {
	// Separate buffers so stderr never lands in the returned bytes: stdout may
	// carry a secret while stderr carries unrelated notices. On failure we surface
	// the trimmed stderr in the error (never stdout) so a caller can diagnose
	// without the secret leaking into a logged error.
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("sysdep: run %q: %w: %s",
			name, err, bytes.TrimSpace(stderr.Bytes()))
	}
	return stdout.Bytes(), nil
}
