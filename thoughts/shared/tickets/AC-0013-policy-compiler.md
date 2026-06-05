# AC-0013: Policy compiler → policy.json with input-hash cache (WP-2.4)

**Status:** Done
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

- [x] Compiled `policy.json` is the union of: explicit `allow`/`deny_always`, generator output, included files, implicit global, and the session-overlay.
- [x] Each rule in the output carries its source annotation (`explicit` / `generated:…` / `global:…` / `once`).
- [x] An input hash over (project YAML + all includes + global + referenced manifests + session-overlay) keys the cache; matching hash skips regeneration.
- [x] The artifact is written under `~/.cache/agent-creance/projects/<hash>/policy.json` (out-of-tree) and never inside the project tree.

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

- [x] `policy.json` schema/version field for forward-compat with `enforcer.py`? **Yes** —
  artifact carries a top-level `version` (`policy.CompiledVersion = 1`) alongside the
  required per-rule `source` annotation (and a preserved `lower_trust` flag).
- [x] Hash algorithm + what exactly is canonicalized before hashing? **sha256** over a
  canonical `json.Marshal` of the three resolved config layers (global / project+includes
  / session-overlay) plus the referenced manifest bytes — deterministic (sorted map keys,
  fixed struct field order) and environment-independent. Stored in the artifact's
  `input_hash` field; the cache check runs before generators, so a hit does zero work.

## References

- `docs/design.md` — "Config compilation", "Session-scoped allows".
- Spec WP-2.4.

## Implementation Plan

Research: `thoughts/shared/research/2026-06-05-AC-0013-policy-compiler.md`
Plan: `thoughts/shared/plans/2026-06-05-AC-0013-policy-compiler.md`

Four phases:
1. Artifact schema (`internal/policy`) — `Rule` gains `Source`/`LowerTrust`; new
   `Compiled` type (`version` + `input_hash` + embedded `RuleSet`); exported
   `RuleFromConfig`.
2. Layered config loading (`internal/config`) — additive `Loader.GlobalPath` and
   `Loader.ResolveLayer` (single file + includes, no implicit global) to recover the
   provenance the fused `Load` discards. `Load`/`merge` untouched.
3. The compiler (new `internal/policy/compile`) — layered load → generators over their
   in-tree manifests → annotated union → input-hash cache → atomic out-of-tree write.
   Hermetic golden/cache-hit/cache-miss/C4-guard/annotation tests via a `generatorRunner`
   seam.
4. Live integration test (real npm) + close-out.

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.

### 2026-06-05
Implemented across four commits. Checkpoint decisions (all accepted as recommended): add
a `version: 1` field; recover provenance via compiler-owned layered load; sha256 over a
canonical serialization of resolved inputs + manifest bytes, stored as in-artifact
`input_hash`. Self-decided: preserve the generator `lower_trust` flag in the artifact;
interpret criterion-4 "touching the overlay" as an overlay *content* change (the fake fs
does not model mtime); normalize generated rules to an explicit `intercept` mode so the
whole artifact is uniformly self-describing. No CLI surface (`policy show`/`run` wiring)
in scope — this ships the library compiler + tests. All four acceptance criteria met; all
six verification steps pass (`go build ./...`, `make test`, `make test-integration`,
`make lint`, golden reviewed). Marked Done.
