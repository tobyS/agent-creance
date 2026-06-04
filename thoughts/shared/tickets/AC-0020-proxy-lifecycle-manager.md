# AC-0020: Proxy lifecycle manager (WP-3.4)

**Status:** Open
**Estimated Complexity:** Extra Large
**Created:** 2026-06-04
**Updated:** 2026-06-04
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

- [ ] Lock file records proxy PID, port, policy hash, and attached-agent PIDs; all read-modify-writes are `flock`-guarded.
- [ ] On run: prune dead agent PIDs (`kill -0`), verify the proxy is alive; start if none, else attach (add own PID); release lock.
- [ ] Port: bind `:0`, record the port; on restart attempt best-effort reclaim of the recorded port.
- [ ] Teardown: trap reacquires the lock, removes own PID, kills the proxy iff the agent array is now empty, and purges the session-overlay on last-out.
- [ ] If a restart could not reclaim the port **and** agents remain attached: emit the documented warning naming affected PIDs; **never** kill those agents.
- [ ] Lock file is out-of-tree.

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

- [ ] `flock` semantics on the target filesystems; how does `doctor` (AC-0031) detect unreliable ones?
- [ ] How is "proxy is alive" verified beyond PID liveness (port probe)?

## References

- `docs/design.md` — "Multi-agent lifecycle" (incl. "Crash recovery and the port").
- Spec WP-3.4, §14.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification. Must implement the warn-never-kill path, not just reclaim.
