# AC-0061: Proxy-refcount integrity — tear down on signal and survive PID recycling

**Status:** In Progress
**Estimated Complexity:** Medium
**Created:** 2026-06-22
**Updated:** 2026-06-22

## Problem Statement

The 2026-06-22 security review
(`thoughts/shared/reviews/2026-06-22-codebase-quality-review.md`) found two **Medium**
findings in the multi-agent proxy lifecycle. Neither is a confidentiality bypass, but
both leave an orphaned, listening mitmproxy with a corrupt refcount — an operational-
security problem (a stale egress proxy lingering after its session) and a correctness
problem for the refcounted shared-proxy model.

**F5 — Detach is not guaranteed in the post-`Run` teardown window.** `run.go:153`
registers `mgr.Detach` only as a bare `defer`. During `cage.Run`
(`internal/cage/run.go:62`) SIGINT/SIGTERM *are* intercepted and forwarded to the
agent group, so the common Ctrl-C case tears down cleanly and repeated Ctrl-C does
not kill the wrapper — that part is sound. The gap is the window **after** `Run`
returns: `signal.Stop` (`cage/run.go:63`) has unregistered the handler, so a
SIGINT/SIGTERM arriving during the deferred `watcher.Stop()` + `Detach` reverts to
the default disposition (terminate), the `defer` never completes, and the proxy is
leaked with this agent's PID still in the lock's agents array. It self-heals on the
next run in that project (dead-PID prune), but until then an orphan proxy listens and
the refcount is wrong. The design states cleanup runs on "any exit (clean, SIGINT,
crash)", so the current behavior is narrower than documented.

**F13 — A recycled agent PID pins the proxy alive forever.** The lifecycle correctly
distrusts a bare *proxy* PID (it adds a TCP port probe to defend against PID reuse,
`internal/proxy/lifecycle.go`), but it prunes attached *agent* PIDs by `kill -0`
alone (`pruneDead`). On macOS, PIDs recycle; over a long-lived lock (a proxy that
survives many runs) a dead agent's PID can be reassigned to an unrelated live
process. That stale-but-"alive" entry keeps `len(alive) > 0`, which (a) prevents
last-out teardown indefinitely — the proxy never dies, a leak — and (b) makes
`clean` without `--force` refuse with a spurious "live agents attached", stranding
the operator. Agents got no second identity factor where the proxy did.

## Desired Outcome

- **F5:** The proxy refcount is decremented (this agent removed; proxy killed if it
  was the last) on **any** wrapper exit, including a signal that arrives during the
  post-`Run` teardown window — matching the design's "any exit" guarantee. No leaked
  proxy with a stale PID after a Ctrl-C/SIGTERM at any point in the run.
- **F13:** A recycled agent PID can no longer masquerade as a live attached agent: the
  lock records a second identity factor per agent (e.g. a start token / boot-relative
  start time) that `pruneDead` checks, so a reused PID is pruned and last-out teardown
  proceeds. `clean` no longer refuses on a recycled-PID ghost.

## User Stories / Use Cases

- As an operator who Ctrl-C's (or `kill`s) a caged session, I want no mitmproxy left
  listening afterward and no stale refcount, so I'm not unknowingly running an orphan
  egress proxy.
- As an operator running long-lived or many sessions, I want a long-gone agent's
  recycled PID to not keep a proxy pinned alive or block `clean`, so the refcount
  reflects reality.

## Acceptance Criteria

### F5 — teardown on signal
- [ ] A SIGINT/SIGTERM delivered to the wrapper at any point in the run — including
      after `cage.Run` returns, during watcher/proxy teardown — results in this
      agent's PID being removed from the lock and the proxy killed iff it was the last
      agent.
- [ ] No leaked proxy / stale agent PID remains after such a signal (verified without
      relying on the next run's prune to clean up).
- [ ] The teardown remains idempotent and does not double-decrement or kill a proxy
      another agent is still using.

### F13 — recycled-PID resistance
- [ ] Each attached agent is recorded with a second identity factor (start token /
      start time) in the lock alongside its PID.
- [ ] `pruneDead` treats an agent entry as dead when the PID is gone **or** the live
      process at that PID does not match the recorded identity factor (a recycled
      PID), so a reused PID no longer pins the proxy.
- [ ] `clean` (without `--force`) no longer refuses on a recycled-PID ghost; last-out
      teardown proceeds once the only "alive" entries are recycled ghosts.

## Testing Protocol

Per `.claude/tce/profile.md`: all OS interaction goes through `sysdep` seams with
`sysdeptest` fakes; the lifecycle has existing race tests under `-race`; external
tools only under the `integration` tag.

- **F5:** add a test (extending `internal/cli/run_test.go` / `internal/cage/run_test.go`)
  that injects a signal during the post-`Run` teardown window and asserts the lock's
  agents array is decremented and the proxy SIGTERM'd — the path currently uncovered.
  If this requires a signal-aware teardown rather than a bare `defer`, the test should
  drive that mechanism through the existing `ProcessGroup`/signal seam.
- **F13:** extend `internal/proxy/lifecycle_test.go` with a `pruneDead` case where an
  agent PID is "alive" (fake `kill -0` succeeds) but its identity factor doesn't match
  — asserting it is pruned and last-out teardown/`clean` proceed. Reuse the
  `FakeProcessManager` liveness oracle; the review notes the fake currently ignores
  identity, so the fake may need a "PID alive but different process" capability.
- **Concurrency:** keep `internal/proxy/lifecycle_race_test.go` green under `-race`;
  if the identity factor changes the lock schema, update the race test to assert no
  leak/double-kill still holds.
- **Gate:** `make test` (with `-race`, as `make test` already does), `make lint`
  green; `make build` at the end.

## Out of Scope

- The port-reclaim / stranded-agent behavior on proxy crash-restart (already
  implemented and tested — `TestCrashRestartReclaimsPort` etc.); this ticket does not
  change it.
- The accepted serialization of concurrent first-starts under the flock (a documented
  latency trade-off, not a correctness bug).
- Lock-file schema versioning/migration beyond what the identity factor requires.

## Open Questions

None blocking. The second identity factor for agents (start token vs. process
start-time) is an implementation choice for planning; the teardown mechanism (signal
handler spanning the whole run vs. re-arming around teardown) likewise.

## Questions for Research/Planning

- [ ] F5 — the cleanest way to keep a signal-aware teardown across the post-`Run`
      window: keep a process-level handler installed in `runRun` until after Detach,
      or re-arm around the teardown defers. How does this compose with `cage.Run`'s
      own handler so signals aren't double-handled?
- [ ] F13 — what second identity factor is cheap and reliable on macOS (a random
      start token written to the lock at attach vs. reading the process start time via
      a `sysdep` seam); the lock-file schema change and whether `FakeProcessManager`
      needs a "different process at same PID" capability.
- [ ] Confirm the exact `pruneDead` / `Clean` call sites and how `status`/`doctor`
      surface attached agents, so the identity check is applied consistently.

## References

- Review: `thoughts/shared/reviews/2026-06-22-codebase-quality-review.md` (F5, F13).
- F5: `internal/cli/run.go:153-157` (deferred `Detach`),
  `internal/cage/run.go:49-85` (`Run`, signal forwarding + `signal.Stop`),
  `internal/cli/run_test.go`, `internal/cage/run_test.go`.
- F13: `internal/proxy/lifecycle.go` (`pruneDead`, `Attach`, `Detach`, `Clean`; the
  proxy-PID port-probe that agents lack), `internal/proxy/lifecycle_test.go`,
  `internal/proxy/lifecycle_race_test.go`,
  `internal/sysdep/sysdeptest/processmanager.go`.
- `docs/design.md` — "Multi-agent lifecycle" (the lock file as single source of
  truth, "any exit" cleanup, recycled-PID hazard for the proxy).

## Implementation Plan

[Leave empty — filled when the plan is created.]

## Notes & Updates

### 2026-06-22

- Created from the 2026-06-22 security review. Groups the two **Medium**
  proxy-lifecycle findings: F5 (Detach skipped in the post-`Run` signal window) and
  F13 (recycled agent PID pins the proxy alive / blocks `clean`).
- Severity is Medium (availability / refcount integrity, not a confidentiality
  bypass) but the user opted to include it in this batch — an orphaned listening
  proxy with a stale refcount is a real operational-security concern.
- F5's severity was refined during the review: the common Ctrl-C path *does* tear
  down correctly (verified); the residual is the narrow post-`Run` window, so this is
  defense-in-depth rather than a routine leak.
