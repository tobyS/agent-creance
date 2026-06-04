# AC-0013: Policy compiler → policy.json with input-hash cache (WP-2.4)

**Status:** Open
**Estimated Complexity:** Large
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-2.4 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** AC-0006 (WP-1.1), AC-0008 (WP-1.3), AC-0010 (WP-2.1), AC-0012 (WP-2.3)
**Spike gate:** none
**Cross-cutting:** C3 (golden), C4 (out-of-tree)

## Problem Statement

The runtime proxy enforces a compiled `policy.json`, not the human YAML. The compiler must union explicit rules + generator output + included files + global + the session-overlay into one annotated artifact, and skip regeneration when inputs are unchanged so same-config runs are near-instant.

## Desired Outcome

`internal/policy` compiles the effective config into an out-of-tree `policy.json` with each rule annotated by source, gated by an input-hash cache that includes manifests and the session-overlay.

## User Stories / Use Cases

- As an operator, I want repeat runs with an unchanged config to be instant so that the cage starts fast.
- As a developer of `policy show`, I want a stable annotated artifact so that the UI just renders it.

## Acceptance Criteria

- [ ] Compiled `policy.json` is the union of: explicit `allow`/`deny_always`, generator output, included files, implicit global, and the session-overlay.
- [ ] Each rule in the output carries its source annotation (`explicit` / `generated:…` / `global:…` / `once`).
- [ ] An input hash over (project YAML + all includes + global + referenced manifests + session-overlay) keys the cache; matching hash skips regeneration.
- [ ] The artifact is written under `~/.cache/agent-creance/projects/<hash>/policy.json` (out-of-tree) and never inside the project tree.

## Verification & Test Steps

1. `go build ./...` → compiles.
2. `go test -race ./internal/policy/...` → pass. Golden `policy.json` for a representative config (explicit + generated + global + overlay). `make golden` diff reviewed.
3. Cache hit: a test compiles once, then re-compiles with identical inputs and asserts no regeneration occurred (e.g. mtime unchanged / generator fake call-count == 0).
4. Cache miss: mutating any input (incl. touching the overlay) forces regeneration.
5. C4 guard: a test asserts the output path is under the state dir and that compiling does **not** create any file inside the fixture project dir (`! test -e <project>/policy.json`).
6. `make lint` → clean.

## Out of Scope

- Seatbelt `.sb` compilation (AC-0014).
- The matcher itself (AC-0010) — this consumes it.
- Writing overlay entries (`allow --once` is AC-0030); the compiler only reads the overlay file.

## Dependencies & Sequencing

Phase 2. The convergence point for AC-0006/0008/0010/0012. Reaches Milestone M1 with AC-0015.

## Questions for Research/Planning

- [ ] `policy.json` schema/version field for forward-compat with `enforcer.py`?
- [ ] Hash algorithm + what exactly is canonicalized before hashing (ordering, whitespace)?

## References

- `docs/design.md` — "Config compilation", "Session-scoped allows".
- Spec WP-2.4.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.
