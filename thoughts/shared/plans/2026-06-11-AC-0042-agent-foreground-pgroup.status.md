# Implementation Status: AC-0042 — agent foreground process group

## Phase 1: Foreground handover in OSProcessGroup
- **Status**: ✅ Complete
- **Started**: 2026-06-11
- **Completed**: 2026-06-11

### Steps Performed
1. `internal/sysdep/processgroup.go`: `Start` sets `Foreground: true` +
   `Ctty: int(os.Stdin.Fd())` when `isTerminal(os.Stdin)` (guard mandatory —
   non-tty Ctty fails fork/exec with ENOTTY); `osProcess` gained a
   `foreground` field; `Wait` restores the wrapper as the terminal's
   foreground group after the child exits (`signal.Ignore(SIGTTOU)` then
   `unix.IoctlSetPointerInt(stdin, TIOCSPGRP, unix.Getpgrp())`, best-effort).
2. Updated the package, interface, and `Start` doc comments (keyboard signals
   now reach the agent's group directly; forwarding loop covers PID-targeted
   signals).
3. Updated `docs/design.md` "Process group handling" paragraph.
4. Ticket: tty-independent acceptance criteria ticked; the three interactive
   criteria are explicitly marked "awaiting user's live run".

### Issues Encountered
- None; implementation matched the research exactly.

### Verification
- ✅ `make test` (full hermetic suite, race)
- ✅ `make lint` (`go vet` + golangci-lint)
- ✅ `go test -race -tags=integration ./internal/sysdep/ ./internal/cage/`
  (real `/bin/sh` + real safehouse group teardown, non-tty skip branch)
- ⚠️ Interactive tty path: not verifiable in-session (no pty harness);
  user live-run pending — `bin/agent-creance` rebuilt for it.

### Commit
- `d9a5d9d` fix(AC-0042): hand the terminal to the agent's process group
