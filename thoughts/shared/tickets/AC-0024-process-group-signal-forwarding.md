# AC-0024: Process group & signal forwarding (WP-4.3)

**Status:** Open
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

- [ ] The child is started in a new process group (`Setpgid: true` / `setsid` equivalent).
- [ ] `SIGINT`/`SIGTERM` to the wrapper are forwarded to the entire group (`kill(-pgid, sig)`).
- [ ] The wrapper waits for the whole group to exit before performing the lock-file decrement (ordering deterministic).

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

- [ ] Interaction between our process group and Safehouse's own child handling — does Safehouse already create a group?

## References

- `docs/design.md` — "Multi-agent lifecycle" (Process group handling).
- Spec WP-4.3.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.
