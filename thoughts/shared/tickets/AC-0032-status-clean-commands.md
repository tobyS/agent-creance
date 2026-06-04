# AC-0032: `status` / `clean` commands (WP-6.3)

**Status:** Open
**Estimated Complexity:** Medium
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-6.3 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** AC-0020 (lifecycle/locks)
**Spike gate:** none

## Problem Statement

Operators need to see what's running across all projects and to tear down a project's cage cleanly. `status` reads every lock file; `clean` removes a project's proxy, lock, and session-overlay, and is safe to run when nothing is live (orphan cleanup).

## Desired Outcome

`agent-creance status` lists running cages across all projects; `agent-creance clean` tears down this project's proxy + lock + overlay, idempotently and orphan-safe.

## User Stories / Use Cases

- As an operator, I want `status` to show all my caged sessions so that I know what's running.
- As an operator, I want `clean` to remove a stuck/orphan proxy so that the next `run` starts fresh.

## Acceptance Criteria

- [ ] `status` enumerates `~/.cache/agent-creance/projects/*/proxy.lock`, showing per project: proxy alive?, port, attached agent count.
- [ ] `clean` stops this project's proxy (if any), removes the lock, and purges the session-overlay; it is idempotent (safe to run twice; safe when nothing is running).
- [ ] Neither command corrupts another project's state.

## Verification & Test Steps

1. `go build ./...` → compiles.
2. `go test -race ./internal/cli/...` and `./internal/proxy/...`:
   - `status`: with two fixture lock files (one alive-faked, one dead), assert the rendered list reflects each correctly (golden).
   - `clean`: given a fixture lock + overlay, assert both are removed and a running proxy is signaled to stop (via fakes); running `clean` again is a no-op (idempotent).
3. Integration (`make test-integration`): start a real proxy via `run`, then `clean` it; assert the proxy process is gone and the lock removed.
4. C4 guard: commands only touch out-of-tree state.
5. `make lint` → clean; `make test` → green.

## Out of Scope

- Diagnostics/`--fix` (AC-0031).
- Cross-user/system-wide cleanup.

## Dependencies & Sequencing

Phase 6. Depends on AC-0020. Completes **Milestone M4** ("v0.1 complete") together with AC-0030/AC-0031.

## Questions for Research/Planning

- [ ] `status` output format (table) and whether to show policy hash / staleness.

## References

- `docs/design.md` — "Commands" (status/clean), "Multi-agent lifecycle".
- Spec WP-6.3.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.
