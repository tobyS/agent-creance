package sysdeptest

import "github.com/tobyS/agent-creance/internal/sysdep"

// FakeTerminal is a Terminal whose interactivity is set by the test: Interactive
// true drives the prompt path, false drives the non-interactive refusal. It lets
// the init bootstrap tests force either answer without a real tty (which
// testscript can never provide).
type FakeTerminal struct {
	// Interactive is what IsInteractive returns. The zero value (false) models a
	// piped/redirected stdin — the safe default for an unattended run.
	Interactive bool
	// StderrTerminal is what IsStderrTerminal returns. The zero value (false)
	// models a piped/redirected stderr, driving the append-only progress path.
	StderrTerminal bool
	// StdoutTerminal is what IsStdoutTerminal returns. The zero value (false)
	// models a piped/redirected stdout, driving plain (uncolored) output.
	StdoutTerminal bool
}

var _ sysdep.Terminal = (*FakeTerminal)(nil)

func (f *FakeTerminal) IsInteractive() bool { return f.Interactive }

func (f *FakeTerminal) IsStderrTerminal() bool { return f.StderrTerminal }

func (f *FakeTerminal) IsStdoutTerminal() bool { return f.StdoutTerminal }
