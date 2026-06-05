# AC-0016: `policy refresh` command (WP-2.7)

**Status:** In Progress
**Estimated Complexity:** Small
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-2.7 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** AC-0011 (WP-2.2), AC-0012 (WP-2.3)
**Spike gate:** none

## Problem Statement

Generator metadata is cached for 30 days; occasionally an operator needs to force a re-fetch (a package changed its homepage, or the cache is stale). A simple command must invalidate the per-package cache and recompile.

## Desired Outcome

`agent-creance policy refresh` forces re-fetch of generator registry metadata (invalidating the per-package cache) and recompiles the policy.

## User Stories / Use Cases

- As an operator, I want to force a metadata refresh so that a corrected upstream homepage is picked up before its 30-day TTL.

## Acceptance Criteria

- [ ] `policy refresh` invalidates the per-package metadata cache for the project's generators and triggers a recompile.
- [ ] It does not require the cage to be running.
- [ ] Output reports what was refreshed (counts) and exits 0 on success.

## Verification & Test Steps

1. `go build ./...` → compiles.
2. Hermetic CLI test (`testscript`): seed a fake cache entry, run `agent-creance policy refresh`, assert the entry is invalidated and a recompile occurred (fake registry called again).
3. `go test -race ./internal/cli/...` → green.
4. `make lint` → clean.

## Out of Scope

- Changing refresh TTL or per-generator config (deferred).

## Dependencies & Sequencing

Phase 2. Small follow-on to the generator stack.

## Questions for Research/Planning

- [ ] Refresh all generators or accept a package/generator filter argument in v0.1?

## References

- `docs/design.md` — "Allowlist generators" (Caching), "Commands".
- Spec WP-2.7.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.
