# AC-0042: Make the agent's process group the terminal foreground group

## Overview

`run` hangs at `Launching agent…` because the agent child is started in a new
process group (`Setpgid: true`) that is never made the controlling terminal's
**foreground** group. A TUI agent's first `tcsetattr`/stdin read from a
background group is answered by the kernel with `SIGTTOU`/`SIGTTIN` (default:
stop), so the agent freezes in state `T` and `cage.Runner` waits forever. Fix:
hand the terminal to the child via `SysProcAttr{Foreground: true, Ctty:
stdin}` when stdin is a tty, and take it back after the child exits.

## Current State Analysis

- `OSProcessGroup.Start` (`internal/sysdep/processgroup.go:57-74`) sets only
  `Setpgid: true` and wires the child to `os.Stdin/Stdout/Stderr`. The comment
  at :59-63 claims keeping the controlling terminal makes "the agent
  interactive" — the exact assumption this bug disproves.
- No `tcsetpgrp`/`Foreground`/`SIGTTOU` handling exists anywhere (grep-confirmed).
- Single production consumer: `cage.Runner.Run` (`internal/cage/run.go:54`),
  reached only from `runRun` step 11. The proxy daemon uses the separate
  `ProcessManager` (Setsid) — unrelated.
- All tests that hit the real `Start` run with non-tty stdin
  (`processgroup_test.go:18` unit failure-path;
  `processgroup_integration_test.go:41`, `cage_integration_test.go:141` under
  the integration tag). `FakeProcessGroup` records only `{Name, Args, Env}` —
  no test observes SysProcAttr. The verify battery bypasses the seam entirely.

## Desired End State

From an interactive terminal, `run` launches the agent TUI visibly and
interactively; Ctrl-C is delivered by the kernel straight to the agent's
group; after the agent exits the wrapper prints its teardown output normally.
With piped stdin (tests, CI) the child starts exactly as today.

### Key Discoveries (research: `thoughts/shared/research/2026-06-11-AC-0042-agent-foreground-pgroup.md`)

- `Foreground: true` implies `Setpgid`; Go performs `setpgid` +
  `ioctl(Ctty, TIOCSPGRP)` in the forked child pre-execve with signals
  blocked — no child-side SIGTTOU hazard on modern Go.
- For `Foreground`, `Ctty` is a **parent fd number** (`int(os.Stdin.Fd())`);
  the child-fd-index semantics apply only to `Setctty` (which must NOT be
  combined with Foreground; rejected since Go 1.15).
- A non-tty `Ctty` makes `cmd.Start()` fail with `ENOTTY` → the guard is
  mandatory; reuse the package-local `isTerminal(f *os.File)`
  (`internal/sysdep/terminal.go:47-50`).
- Parent restore pattern after `Wait()`: `signal.Ignore(syscall.SIGTTOU)` then
  `unix.IoctlSetPointerInt(stdinFd, unix.TIOCSPGRP, unix.Getpgrp())` —
  **never before Start** (the agent would inherit `SIG_IGN` through execve).
  `unix.Tcsetpgrp` does not exist on darwin; the IoctlSetPointerInt form does
  (x/sys v0.45.0, already a direct dependency).
- `kill(-pgid)` teardown semantics are unchanged (pgid still == child pid).
- Do NOT add SIGTTOU to `Runner`'s Notify set — `cage/run_test.go:50` asserts
  it is exactly `{SIGINT, SIGTERM}`; the ignore lives inside `sysdep`.

## What We're NOT Doing

- No change to `ProcessManager`/Setsid (proxy daemon spawning).
- No pty-based automated test for the interactive path (no harness in repo;
  the tty path is manually verified — acceptance criterion 1).
- No job-control (Ctrl-Z resume) support for the wrapper itself.
- No changes to `cage.Runner`'s forwarding loop or its Notify set.

## Implementation Approach

One phase — all changes are in `internal/sysdep/processgroup.go` plus comment
and design-doc updates. The osProcess remembers whether the foreground
handover happened so `Wait` can symmetrically restore it.

## Phase 1: Foreground handover in `OSProcessGroup`

### Changes Required:

#### 1. `internal/sysdep/processgroup.go`

- Imports: add `golang.org/x/sys/unix`.
- `Start`: keep `Setpgid: true`; when `isTerminal(os.Stdin)`, also set
  `Foreground: true` and `Ctty: int(os.Stdin.Fd())`; pass
  `foreground` into the returned `osProcess`.
- `osProcess`: new `foreground bool` field.
- `Wait`: after `cmd.Wait()` returns, when `foreground`: ignore SIGTTOU
  (process-wide, after the child is gone so nothing inherits it), then
  best-effort `unix.IoctlSetPointerInt(int(os.Stdin.Fd()), unix.TIOCSPGRP,
  unix.Getpgrp())` so the wrapper's teardown writes (proxy warning,
  progress Close, `error:` line) happen as the foreground group.
- Comments: rewrite the `Start` comment at :59-63 (foreground handover +
  non-tty guard + why), and amend the interface doc (:25-33) and package doc
  (:13-17) — keyboard-generated SIGINT now reaches the agent's group directly
  from the kernel; the wrapper's forwarding loop covers signals sent to the
  wrapper's own PID.

```go
// Start, core of the change:
cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
foreground := isTerminal(os.Stdin)
if foreground {
    // Hand the controlling terminal to the child's new group. Without this a
    // background-group TUI is stopped by SIGTTIN/SIGTTOU on its first tty
    // access (AC-0042). Foreground implies Setpgid; Ctty is a PARENT fd here.
    // Guarded: a non-tty Ctty makes fork/exec fail with ENOTTY (tests, CI).
    cmd.SysProcAttr.Foreground = true
    cmd.SysProcAttr.Ctty = int(os.Stdin.Fd())
}

// Wait, after p.cmd.Wait():
if p.foreground {
    signal.Ignore(syscall.SIGTTOU)
    _ = unix.IoctlSetPointerInt(int(os.Stdin.Fd()), unix.TIOCSPGRP, unix.Getpgrp())
}
```

#### 2. `docs/design.md` (process-group paragraph, ~:433)

State that the child's group is made the terminal's foreground group when
stdin is a tty (so keyboard Ctrl-C goes straight to the agent subtree and TUIs
are not stopped by SIGTTIN/SIGTTOU), that the wrapper still forwards
PID-targeted signals via `kill(-pgid)`, and that the wrapper reclaims the
terminal after the agent exits.

#### 3. Ticket close + `make build`

Tick acceptance criteria, set status Done, rebuild `bin/agent-creance` (the
end-of-ticket convention in CLAUDE.md).

### Success Criteria:

#### Automated Verification:

- [x] `make test` green (fakes observe no SysProcAttr; unit Start test takes
  the non-tty skip branch unchanged)
- [x] `go build ./...` (typecheck)
- [x] `make lint` clean
- [x] `go test -race -tags=integration ./internal/sysdep/ ./internal/cage/`
  green (real `/bin/sh` and real safehouse group teardown through the skip
  branch)

#### Manual Verification:

- [ ] `agent-creance run` from a real terminal launches the agent TUI,
  interactive (no `T`-state processes in `ps`)
- [ ] Ctrl-C during the session tears down the agent and its children
- [ ] After agent exit, teardown/stderr output appears and the shell prompt
  returns normally
- [ ] Piped run (`echo | agent-creance run` in a throwaway dir) still refuses/
  starts without ENOTTY errors

## Testing Strategy

- Existing unit suites (fake-based) and the sysdep/cage integration tests
  cover the non-tty branch; the tty branch is OS-mediated and covered
  manually (no pty harness — accepted for this quickfix).
- No new unit tests: the only observable new behavior lives behind real
  syscalls that the project's testing model confines to integration/manual.

## Performance Considerations

None — one extra ioctl at spawn and one at exit.

## Migration Notes

None — no persisted state involved.

## References

- Ticket: `thoughts/shared/tickets/AC-0042-agent-foreground-pgroup.md`
- Research: `thoughts/shared/research/2026-06-11-AC-0042-agent-foreground-pgroup.md`
- Change site: `internal/sysdep/processgroup.go:57-74,104`
- Guard helper: `internal/sysdep/terminal.go:47-50`
