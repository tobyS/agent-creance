# AC-0032: `status` / `clean` commands (WP-6.3)

**Status:** Done
**Estimated Complexity:** Medium
**Created:** 2026-06-04
**Updated:** 2026-06-07
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

- [x] `status` enumerates `~/.cache/agent-creance/projects/*/proxy.lock`, showing per project: proxy alive?, port, attached agent count.
- [x] `clean` stops this project's proxy (if any), removes the lock, and purges the session-overlay; it is idempotent (safe to run twice; safe when nothing is running).
- [x] Neither command corrupts another project's state.

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

- [x] `status` output format (table) and whether to show policy hash / staleness.
      Resolved at the planning checkpoint: a simple aligned table
      (`PROJECT | STATE | PORT | AGENTS`), no policy-hash column, no staleness
      (would require recompiling per project). To show a readable project
      directory instead of the opaque state-dir hash, the project's canonical path
      is now recorded in `proxy.lock` (additive, backward-compatible).

## References

- `docs/design.md` — "Commands" (status/clean), "Multi-agent lifecycle".
- Spec WP-6.3.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.

### 2026-06-07
Implemented (research + plan under `thoughts/shared/`). Both commands are thin
orchestration over the existing lifecycle/diagnosis machinery:

- `status` = `proxy.Manager.Inspect` run across every project (new
  `internal/status` package: `Scanner` + golden-tested `Render`).
- `clean` = new `proxy.Manager.Clean` (CleanOrphan without the orphan guard):
  unconditional, idempotent teardown that **refuses when live agents are attached
  unless `--force`** (warn-never-kill, decided at the checkpoint).

Schema: `proxy.lock` gained a `canonical_path` field (written by `Attach`) so
`status` shows the real project directory; older locks fall back to the hash.
New seam: `FileSystem.ReadDir`. Verified: `make test` + `make lint` green; the new
`clean` real-proxy integration test passes (`make test-integration`). Two unrelated
integration tests fail on this host (mitmproxy enforcer addon can't load; `~/.mitmproxy`
stat blocked) — reproduced identically on the pre-change base commit, so they are
environmental, not from this work.

Closes AC-0032 — completes Milestone M4 (v0.1) with AC-0030/AC-0031.
