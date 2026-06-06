# AC-0020: Proxy lifecycle manager (WP-3.4)

**Status:** Done
**Estimated Complexity:** Extra Large
**Created:** 2026-06-04
**Updated:** 2026-06-06
**Plan reference:** WP-3.4 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** AC-0006 (WP-1.1), AC-0009 (WP-1.4, Flock seam), AC-0019 (WP-3.3)
**Spike gate:** **S3 (AC-0003)**
**Cross-cutting:** C4 (out-of-tree)

## Problem Statement

Multiple `agent-creance` invocations in one project share a single mitmproxy via a refcounted lock file. Getting the concurrency right — atomic refcount, crash detection, ephemeral-port allocation with best-effort reclaim, never killing attached agents — is the riskiest non-security logic in the system.

## Desired Outcome

`internal/proxy` manages the proxy lifecycle through a `flock`-guarded lock file: start-or-attach, prune dead agents, allocate an ephemeral port (reclaim the recorded one on restart), decrement on exit and kill the proxy only when the last agent leaves, purge the session-overlay on last-out, and warn (never kill) when a restart lands on a different port with agents still attached.

## User Stories / Use Cases

- As an operator running two caged sessions, I want them to share one proxy so that I don't double-spend resources.
- As an operator whose proxy crashed, I want surviving agents to recover (same port) or be clearly warned (new port) rather than silently hang.

## Acceptance Criteria

- [x] Lock file records proxy PID, port, policy hash, and attached-agent PIDs; all read-modify-writes are `flock`-guarded. (`lockState` in `internal/proxy/lifecycle.go`, written in place on the locked descriptor.)
- [x] On run: prune dead agent PIDs (`kill -0`), verify the proxy is alive; start if none, else attach (add own PID); release lock. (`Manager.Attach`.)
- [x] Port: bind `:0`, record the port; on restart attempt best-effort reclaim of the recorded port. (`PortAllocator.Allocate`/`TryReclaim`; `Manager.choosePort`.)
- [x] Teardown: reacquires the lock, removes own PID, kills the proxy iff the agent array is now empty, and purges the session-overlay on last-out. (`Manager.Detach`. The *signal-trap* that calls Detach on SIGINT/exit is the run command's job, AC-0025.)
- [x] If a restart could not reclaim the port **and** agents remain attached: emit the documented warning naming affected PIDs; **never** kill those agents. (`Manager.warnPortChanged`; covered by `TestCrashRestartReclaimFailWarnsNeverKills`.)
- [x] Lock file is out-of-tree. (`state.Layout.ProxyLock()` under `~/.cache/agent-creance/projects/<hash>/`; guarded by `TestLockPathIsOutOfTree`.)

## Verification & Test Steps

1. `go build ./...` → compiles.
2. `go test -race ./internal/proxy/...` with fakes (Flock, process, clock) — required cases:
   - start-then-attach: second invocation does not start a second proxy; agent array length 2.
   - last-out teardown: removing the final PID kills the proxy and purges the overlay; a non-final exit does neither.
   - dead-PID prune: a stale PID is removed on next run.
   - crash restart, reclaim success: port preserved; attached agents untouched.
   - crash restart, reclaim fail with agents attached: warning emitted (assert message names PIDs), agents not signaled.
3. Race detector clean (`-race`) on concurrent attach/detach simulation.
4. Integration (`make test-integration`, gated S3): real mitmproxy start/attach/teardown across two invocations.
5. C4 guard: lock path under the state dir.

## Out of Scope

- Building the `.sb`/policy (AC-0013/0014) — this consumes them.
- Exec-ing Safehouse (AC-0023) and signal forwarding to the agent group (AC-0024).

## Dependencies & Sequencing

Phase 3. Critical path to M2/M3. Gated by S3.

## Questions for Research/Planning

- [x] `flock` semantics on the target filesystems; how does `doctor` (AC-0031) detect unreliable ones? — **Delegated to AC-0031.** The "warn on filesystems with unreliable `flock`" check (notably iCloud Drive and SMB shares, per `docs/design.md` "Multi-agent lifecycle") is `doctor`'s job; AC-0020 assumes a reliable filesystem and does not detect this itself.
- [x] How is "proxy is alive" verified beyond PID liveness (port probe)? — **PID liveness AND a TCP port probe.** A recorded PID can be recycled into an unrelated process, so `Manager.Attach` treats the proxy as up only when `ProcessManager.Alive(pid)` **and** `PortAllocator.Probe(port)` both hold; otherwise it is a crash and we start fresh.

## References

- `docs/design.md` — "Multi-agent lifecycle" (incl. "Crash recovery and the port").
- Spec WP-3.4, §14.

## Implementation Plan

Research: `thoughts/shared/research/2026-06-06-AC-0020-proxy-lifecycle-manager.md`
Plan: `thoughts/shared/plans/2026-06-06-AC-0020-proxy-lifecycle-manager.md`

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification. Must implement the warn-never-kill path, not just reclaim.

### 2026-06-06 — Implemented (Done)

Built in `internal/proxy/lifecycle.go` (`Manager` with `Attach`/`Detach`) on three
seam changes:

- **`Flock` redesigned** (checkpoint decision): `Acquire` now returns a
  `LockedFile{ReadAll/Write/Release}` so the `proxy.lock` read-modify-write happens
  in place on the *same* descriptor the lock is held on — the codebase's usual
  temp+rename idiom would swap the inode out from under an advisory `flock` and
  break exclusion. Real `OSFlock` implemented via `golang.org/x/sys/unix.Flock`
  (the impl its doc comment had deferred to this ticket). No production callers
  existed, so the seam change was contained to flock + its fake + their tests.
- **New `ProcessManager` seam** (`Spawn` a detached daemon via `Setsid` → PID;
  `Alive` via `kill -0`; `Signal` a single PID). The proxy is a standalone daemon
  killed by PID from a *later* invocation (the last agent out holds no live handle),
  which is why this is distinct from the group-targeted `ProcessGroup` (still
  WP-4.3). Exec-ing Safehouse / agent-group signal forwarding stayed out of scope
  (AC-0023/0024).
- **New `PortAllocator` seam** (`Allocate` `:0`; `TryReclaim` a recorded port;
  `Probe` for the alive check).

Decisions (from the research checkpoint): write-in-place on the locked fd;
proxy-alive = PID liveness **and** a TCP port probe; manager + seams + `OS*` impls +
tests landed here while wiring into `cli.App`/`Main()` and the `run` command are
**deferred to AC-0025** (the first caller). `docs/design.md` "Multi-agent
lifecycle" updated to name the port-probe in the alive check.

Tests: the five mandated scenarios + corrupt-lock self-heal, error paths, and a C4
out-of-tree guard (blackbox); white-box helper tests; a `-race` concurrent
attach/detach simulation (`FakeFileSystem` gained a mutex so the sim is sound, since
`MkdirAll` legitimately runs before the flock); and an S3-gated
`//go:build integration` test driving a real mitmproxy across two invocations.
