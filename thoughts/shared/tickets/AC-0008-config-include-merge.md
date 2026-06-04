# AC-0008: Include resolution & merge semantics (WP-1.3)

**Status:** Open
**Estimated Complexity:** Medium
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-1.3 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** AC-0007 (WP-1.2)
**Spike gate:** none

## Problem Statement

A project config is a *delta* over an implicit global plus any `include:`d files. Users expect predictable layering: scalars override, allow/deny lists union. Without well-defined, cycle-safe resolution, the compiled policy is ambiguous and a malicious include could loop forever.

## Desired Outcome

`internal/config` resolves the implicit global (`~/.config/agent-creance.yaml`) plus recursive `include:` into one effective config, with cycle detection, a depth limit, scalar-override semantics, and additive union of `allow`/`deny_always`.

## User Stories / Use Cases

- As an operator, I want my project config to only declare what differs from my global baseline so that I don't repeat myself.
- As a team, we want a shared `team-shared.yaml` included so that policy is consistent across the team.

## Acceptance Criteria

- [ ] The implicit global is merged when present and silently skipped when absent.
- [ ] `include:` is resolved recursively; later files override earlier ones for scalar fields; `allow`/`deny_always` lists union additively (no dedupe surprises — document the rule).
- [ ] Include cycles are detected and reported (not an infinite loop); an over-depth chain errors cleanly with the offending path.
- [ ] Merge order is deterministic and documented.

## Verification & Test Steps

1. `go build ./...` → compiles.
2. `go test -race ./internal/config/...` → pass. Table cases required: scalar override (project beats global); list union (allow rules from global + project both present); cycle A→B→A errors; depth-limit exceeded errors with the path; missing global is a no-op.
3. Determinism: run the merge twice on the same inputs and assert byte-identical effective config (or stable struct equality).
4. `make lint` → clean.
5. `make test` → green.

## Out of Scope

- Hashing inputs for the compile cache (AC-0013).
- Session-overlay union (that file is unioned by the compiler, AC-0013).

## Dependencies & Sequencing

Phase 1. Depends on AC-0007. Foundation for AC-0013.

## Questions for Research/Planning

- [ ] What is the depth limit value, and is it configurable?
- [ ] Do duplicate identical allow rules from different files collapse, or are they kept (affecting rule-source annotation)?

## References

- `docs/design.md` — "The configuration" (include resolution paragraph).
- Spec WP-1.3.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.
