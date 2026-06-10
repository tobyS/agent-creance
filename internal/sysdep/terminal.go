package sysdep

import (
	"os"

	"golang.org/x/sys/unix"
)

// Terminal abstracts the one question the CLI's interactive paths need to ask
// about standard input: is it a terminal a human can answer a prompt on, or a
// pipe/redirect from a script? The init command uses it to decide whether to
// raise a confirm prompt (interactive) or refuse with an actionable instruction
// (non-interactive), so it never blocks an unattended run waiting on input.
//
// Why a seam rather than calling the tty ioctl inline (for someone coming from
// PHP/TS): under testscript the CLI always runs with a non-tty pipe on stdin, so
// the interactive branch could never be exercised against the real fd. Logic
// packages take a Terminal and call *that*; production wires OSTerminal, tests
// wire the fake in sysdeptest to force either answer.
type Terminal interface {
	// IsInteractive reports whether standard input is a terminal (a human can be
	// prompted) rather than a pipe or redirected file.
	IsInteractive() bool
}

// OSTerminal is the production Terminal. It probes os.Stdin with the termios get
// ioctl (the same check golang.org/x/term performs); success means a tty, the
// ENOTTY failure on a pipe/file means non-interactive — no new dependency beyond
// the golang.org/x/sys/unix already used by flock and fstype.
type OSTerminal struct{}

var _ Terminal = (*OSTerminal)(nil)

func (OSTerminal) IsInteractive() bool {
	_, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TIOCGETA)
	return err == nil
}
