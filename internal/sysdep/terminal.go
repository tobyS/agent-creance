package sysdep

import (
	"os"

	"golang.org/x/sys/unix"
)

// Terminal abstracts the tty questions the CLI needs to ask about its standard
// streams. The init command asks about stdin to decide whether to raise a
// confirm prompt (interactive) or refuse with an actionable instruction
// (non-interactive), so it never blocks an unattended run waiting on input. The
// run command asks about stderr to decide whether its progress counter may
// rewrite a line in place (\r) or must degrade to append-only milestone lines
// for pipes and CI logs.
//
// Why a seam rather than calling the tty ioctl inline (for someone coming from
// PHP/TS): under testscript the CLI always runs with non-tty pipes, so the
// interactive branches could never be exercised against the real fds. Logic
// packages take a Terminal and call *that*; production wires OSTerminal, tests
// wire the fake in sysdeptest to force either answer.
type Terminal interface {
	// IsInteractive reports whether standard input is a terminal (a human can be
	// prompted) rather than a pipe or redirected file.
	IsInteractive() bool
	// IsStderrTerminal reports whether standard error is a terminal (in-place
	// line rewrites render sensibly) rather than a pipe or redirected file.
	IsStderrTerminal() bool
}

// OSTerminal is the production Terminal. It probes the stream with the termios
// get ioctl (the same check golang.org/x/term performs); success means a tty,
// the ENOTTY failure on a pipe/file means non-interactive — no new dependency
// beyond the golang.org/x/sys/unix already used by flock and fstype.
type OSTerminal struct{}

var _ Terminal = (*OSTerminal)(nil)

func (OSTerminal) IsInteractive() bool {
	return isTerminal(os.Stdin)
}

func (OSTerminal) IsStderrTerminal() bool {
	return isTerminal(os.Stderr)
}

func isTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TIOCGETA)
	return err == nil
}
