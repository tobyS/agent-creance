# AC-0024: Process group & signal forwarding (WP-4.3)

**Status:** Done
**Estimated Complexity:** Medium
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-4.3 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** AC-0009 (WP-1.4, ProcessGroup seam)
**Spike gate:** none

## Problem Statement

Ctrl-C must reliably tear down the agent *and everything it spawned* (`npm run test`, `php artisan ...`), not just the wrapper. Without a dedicated process group and signal forwarding, orphan processes survive inside the sandbox and keep churning.

## Desired Outcome

`internal/cage` starts the child (Safehouse → agent → its children) in a new process group and forwards `SIGINT`/`SIGTERM` to the whole group, waiting for the group to exit before the lock decrement so teardown ordering is deterministic.

## User Stories / Use Cases

- As an operator, I want Ctrl-C to kill the whole caged process tree so that nothing keeps running after I quit.

## Acceptance Criteria

- [x] The child is started in a new process group (`Setpgid: true` / `setsid` equivalent). — `OSProcessGroup.Start` sets `SysProcAttr{Setpgid: true}` (`internal/sysdep/processgroup.go`).
- [x] `SIGINT`/`SIGTERM` to the wrapper are forwarded to the entire group (`kill(-pgid, sig)`). — `osProcess.Signal` does `syscall.Kill(-pgid, sig)`; `cage.Runner` relays caught SIGINT/SIGTERM (and escalates to SIGKILL after a grace period).
- [x] The wrapper waits for the whole group to exit before performing the lock-file decrement (ordering deterministic). — `cage.Runner.Run` returns only after `Process.Wait()`; the caller (AC-0025) sequences `proxy.Detach` after it.

## Verification & Test Steps

1. `go build ./...` → compiles.
2. `go test -race ./internal/cage/...`: unit test with the `ProcessGroup` fake asserts that a forwarded signal calls group-kill with the negative pgid, and that the decrement happens only after the wait returns.
3. Integration (`make test-integration`): start a caged command that spawns a long-lived child (`sleep 300 &`), send SIGINT to the wrapper, then assert no descendant remains (`pgrep -g <pgid>` empty) within a timeout.
4. `make lint` → clean; `make test` → green.

## Out of Scope

- Lock-file refcount logic itself (AC-0020) — this only sequences the decrement.
- Safehouse argv/env construction (AC-0023).

## Dependencies & Sequencing

Phase 4. Pairs with AC-0023 + AC-0020 to make `run` (AC-0025) clean on exit.

## Questions for Research/Planning

- [x] Interaction between our process group and Safehouse's own child handling — does Safehouse already create a group? — **No.** Inspecting safehouse 0.10.1 (a bash script): it runs `sandbox-exec -f policy -- /usr/bin/env -i <cmd>` as a non-`exec`, non-`setsid` foreground subprocess (so it can clean up its rendered policy after). No `setsid`/`setpgid` anywhere; the only `nohup &` is the VS Code special case. So the whole tree (bash → sandbox-exec → env → agent → children) stays in the single `Setpgid` group we create, and `kill(-pgid, sig)` reaches everything. Verified end-to-end by `TestLiveSafehouseGroupTeardown`.

## References

- `docs/design.md` — "Multi-agent lifecycle" (Process group handling).
- Spec WP-4.3.

## Implementation Plan

- Research: `thoughts/shared/research/2026-06-06-AC-0024-process-group-signal-forwarding.md`
- Plan: `thoughts/shared/plans/2026-06-06-AC-0024-process-group-signal-forwarding.md`

## Notes & Updates

### 2026-06-06
Implemented (WP-4.3). `OSProcessGroup.Start` (`Setpgid: true`) + `osProcess`
(`kill(-pgid, sig)` / `Wait` / `Pgid`) fill the seam; `cage.Runner` forwards
SIGINT/SIGTERM to the group, escalates to SIGKILL after a grace period, and returns
only after the group is reaped (giving the caller the wait-before-decrement ordering);
`ProcessGroup` is wired into `App`/`cli.Main`. Real teardown verified at the syscall
layer (`TestOSProcessGroupTearsDownChildTree`, runs on the dev box) and, on a host that
can nest sandbox-exec, through real safehouse (`TestLiveSafehouseGroupTeardown`).
Removed the now-unused `sysdep.ErrNotImplemented` sentinel. The `run` orchestration that
calls `proxy.Detach` after `Runner.Run` is AC-0025.

### 2026-06-04
Created from the v0.1 technical specification.
