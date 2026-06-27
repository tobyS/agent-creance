package cli

// prompt.go generalises the hand-rolled confirm() yes/no primitive (init.go) into the
// small set of interactive prompts the config-editing commands need as a fallback when a
// choice is not supplied via a flag (AC-0067): a single-select menu and a free-text line.
// Every prompt reads app.Stdin and writes app.Stdout, so unit tests drive them with a
// preloaded reader and a buffer, and a new sysdep interface is unnecessary.
//
// The "explicit-or-prompt, never-hang" contract is enforced at the call site: a command
// calls requireInteractive first, which fails with a flag-naming hint when stdin is not a
// terminal, so an unattended invocation never blocks on a read that will not come.

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// requireInteractive returns nil when stdin is an interactive terminal, and otherwise an
// error whose message names the flags to supply. Commands call it before prompting.
func requireInteractive(app *App, hint string) error {
	if app.Terminal.IsInteractive() {
		return nil
	}
	return fmt.Errorf("no terminal for interactive input; %s", hint)
}

// promptText writes "label: " to stdout and returns the trimmed line read from stdin.
func promptText(app *App, label string) (string, error) {
	fmt.Fprintf(app.Stdout, "%s: ", label)
	line, err := readLine(app.Stdin)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// promptSelect renders options as a numbered menu and returns the 0-based index of the
// chosen option, re-prompting on invalid input. EOF before a valid choice is an error
// (rather than an infinite loop) so a truncated stdin fails fast.
func promptSelect(app *App, label string, options []string) (int, error) {
	for {
		fmt.Fprintf(app.Stdout, "%s\n", label)
		for i, o := range options {
			fmt.Fprintf(app.Stdout, "  %d) %s\n", i+1, o)
		}
		fmt.Fprint(app.Stdout, "> ")
		line, err := readLine(app.Stdin)
		if n, perr := strconv.Atoi(strings.TrimSpace(line)); perr == nil && n >= 1 && n <= len(options) {
			return n - 1, nil
		}
		if errors.Is(err, io.EOF) {
			return 0, errors.New("no selection provided")
		}
		if err != nil {
			return 0, err
		}
		fmt.Fprintf(app.Stdout, "please enter a number between 1 and %d\n", len(options))
	}
}
