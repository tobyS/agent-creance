# AC-0008: Include resolution & merge semantics (WP-1.3)

**Status:** Done
**Estimated Complexity:** Medium
**Created:** 2026-06-04
**Updated:** 2026-06-05
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

- [x] The implicit global is merged when present and silently skipped when absent.
- [x] `include:` is resolved recursively; later files override earlier ones for scalar fields; `allow`/`deny_always` lists union additively (no dedupe surprises — document the rule).
- [x] Include cycles are detected and reported (not an infinite loop); an over-depth chain errors cleanly with the offending path.
- [x] Merge order is deterministic and documented.

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

- [x] What is the depth limit value, and is it configurable? **Resolved:** a
  non-configurable constant `maxIncludeDepth = 10`. Cycle detection already prevents
  infinite loops; the depth limit is a secondary guard against pathological acyclic
  chains. Non-configurable because a security tool's safety limit shouldn't be settable
  from the very config it guards.
- [x] Do duplicate identical allow rules from different files collapse, or are they kept
  (affecting rule-source annotation)? **Resolved: collapse** (keep first occurrence).
  Applied uniformly to every unioned list. Rule equality goes through `reflect.DeepEqual`
  so the `*[]string` Paths/Methods are compared pointer-aware (a nil/omitted Paths stays
  distinct from an empty one). Note for AC-0013: dedupe drops provenance, so rule-source
  annotation will need its own mechanism rather than relying on per-file duplicates.

## References

- `docs/design.md` — "The configuration" (include resolution paragraph).
- Spec WP-1.3.

## Implementation Plan

Plan: `thoughts/shared/plans/2026-06-05-AC-0008-config-include-merge.md`
Research: `thoughts/shared/research/2026-06-05-AC-0008-config-include-merge.md`

Delivered in three phases:

1. **`sysdep.FileSystem` read seam** — narrow `FileSystem{ReadFile}` interface +
   `OSFileSystem` + `FakeFileSystem` (the file-content I/O seam the PathResolver doc
   anticipated; AC-0009 grows it).
2. **`internal/config/merge.go`** — pure `merge(base, over)`: scalar/command override,
   list union + first-occurrence dedupe, key-wise `env` merge. Deterministic.
3. **`internal/config/load.go`** — `Loader.Load`: implicit global + project, recursive
   `include:` with current-path cycle detection (canonical `EvalSymlinks∘Abs` identity,
   so symlink aliases are caught) and depth limit; missing global = no-op, missing
   project/include = error; merged result has `Include` cleared.

**Merge precedence (low→high), documented in `load.go`:** global's includes → global's
own → project's includes (listed order) → project's own. An including file's own values
are applied last, so it overrides what it includes.

## Notes & Updates

### 2026-06-05
Implemented via `/tce:work`. All acceptance criteria and verification steps met:
`go build`, `go vet`, `make lint`, `make test` (race) green; `make golden` no diff (no
schema change). Both open questions resolved (see above). Commits (unsigned per session
constraint — SSH signing key unavailable): research doc, plan, Phase 1 (FileSystem
seam), Phase 2 (merge), Phase 3 (loader).

### 2026-06-04
Created from the v0.1 technical specification.
