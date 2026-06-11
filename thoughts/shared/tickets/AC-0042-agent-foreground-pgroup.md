# AC-0042: run hangs launching the agent — child process group never made foreground

**Status:** Done
**Estimated Complexity:** Small
**Created:** 2026-06-11
**Updated:** 2026-06-11

## Problem Statement

`agent-creance run` hangs forever at `Launching agent…` (observed in a real
monorepo right after the AC-0041 progress output made the phases visible).

Root cause (diagnosed): `OSProcessGroup.Start`
(`internal/sysdep/processgroup.go:57-74`) launches the safehouse/agent child
with `SysProcAttr{Setpgid: true}`, creating a **new process group**, but
nothing ever makes that group the **foreground** process group of the
controlling terminal (no `tcsetpgrp`/`Foreground` anywhere in the codebase).
A TUI agent (Claude Code) immediately calls `tcsetattr` (raw mode) and reads
stdin; from a *background* process group those raise `SIGTTOU`/`SIGTTIN`,
whose default action is **stop the process** (state `T`). The agent freezes
before rendering anything and `cage.Runner` blocks in `proc.Wait()` forever.

Tests never caught this because the cage-verification integration battery runs
non-interactive commands that never touch the tty.

## Desired Outcome

`run` hands the terminal to the agent: the agent's process group becomes the
foreground group, the TUI starts and is fully interactive, and Ctrl-C reaches
the agent's whole subtree. After the agent exits, the wrapper finishes its
teardown (proxy detach, stderr warnings) without being stopped itself.

## User Stories / Use Cases

- As a developer running `agent-creance run`, I want the agent TUI to actually
  appear and accept input so that I can use the caged agent at all.
- As a developer pressing Ctrl-C, I want the signal to reach the agent's
  process group so the session tears down cleanly.

## Acceptance Criteria

- [ ] `agent-creance run` from an interactive terminal launches the agent TUI
  visibly and interactively (no `T`-state stop; manual verification —
  **awaiting user's live run**).
- [x] When stdin is not a terminal (testscript pipes, CI), the child is still
  started successfully — foreground handover is skipped, not failed
  (verified: sysdep + cage integration tests through the skip branch).
- [ ] After the agent exits, the wrapper completes proxy teardown and its own
  stderr output without being stopped by `SIGTTOU` (manual — awaiting user's
  live run).
- [ ] Ctrl-C during an agent session reaches the agent's process group
  (manual — awaiting user's live run).
- [x] Existing tests continue to pass (`make test`, `make lint`).

## Out of Scope

- Any change to the proxy daemon's `Setsid` spawning (`ProcessManager` —
  detached daemon, unrelated).
- Job-control support for suspending/resuming the wrapper itself (Ctrl-Z
  semantics beyond what the foreground handover implies).
- The cage-verification battery gaining a tty-based test (would need a pty
  harness; noted as follow-up if wanted).

## Open Questions

None — this is a well-understood quickfix.

## Questions for Research/Planning

- [ ] Exact mechanics of Go's `SysProcAttr{Foreground: true, Ctty: ...}` on
  macOS: does it imply `Setpgid`, which fd should `Ctty` be, and what happens
  when stdin is not a tty (error vs silent failure)?
- [ ] Where should the non-tty guard live — in `OSProcessGroup.Start` itself
  (probe stdin) or decided by the caller via the existing `sysdep.Terminal`
  seam?
- [ ] Does the wrapper need `signal.Ignore(SIGTTOU)` for its post-exit stderr
  writes, or is that only needed for tty-mode-changing calls? Should it
  restore itself as foreground (`tcsetpgrp` back) after `Wait()` returns?
- [ ] How do the `FakeProcessGroup`-based tests and the testscript runs
  interact with the change (they must stay hermetic and green)?

## References

- Quickfix initiated via `/quickfix` command
- Diagnosis: AC-0041 session conversation (2026-06-11); grep confirmed no
  `tcsetpgrp`/`Foreground`/`SIGTTOU` handling exists anywhere in the repo
- `internal/sysdep/processgroup.go:57-74` — the `Setpgid`-only Start
- `internal/cage/run.go:53-85` — the Wait/signal-forwarding loop that blocks

## Implementation Plan

[Leave empty — will be filled when plan is created]

## Notes & Updates

### 2026-06-11 (implementation)
- Implemented in `internal/sysdep/processgroup.go`: `Foreground: true` +
  `Ctty: stdin` when `isTerminal(os.Stdin)` (non-tty Ctty would fail fork/exec
  with ENOTTY); `osProcess` remembers the handover and `Wait` restores the
  wrapper as foreground (`signal.Ignore(SIGTTOU)` strictly after the child
  exits — SIG_IGN survives execve and must not leak into the agent — then
  `unix.IoctlSetPointerInt(TIOCSPGRP)`; `unix.Tcsetpgrp` does not exist on
  darwin). design.md process-group paragraph updated.
- All automated checks green; the three interactive acceptance criteria are
  inherently manual and await the user's live run (no pty harness in repo —
  consciously out of scope for the quickfix). If the live run fails, reopen.

### 2026-06-11
- Quickfix ticket auto-created from `/quickfix` command
- Root cause already confirmed by code inspection during the AC-0041 session;
  the user observed the hang live with the new progress output
