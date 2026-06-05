# AC-0006: State directory & project identity (WP-1.1)

**Status:** Done
**Estimated Complexity:** Small
**Created:** 2026-06-04
**Updated:** 2026-06-05
**Plan reference:** WP-1.1 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** AC-0009 (WP-1.4, for the filesystem seam) — may proceed in parallel and wire the seam when it lands
**Spike gate:** none

## Problem Statement

Every later component needs a single, stable answer to "what project is this and where does its out-of-tree state live?" The design ties project identity to the canonical absolute path and derives a `<hash>` used by both the lock file and the state directory. Without this foundation, the policy compiler, proxy lifecycle, and audit log have nowhere consistent to write.

## Desired Outcome

A pure `internal/state` package that, given a project directory, returns a stable identity hash and the fully-resolved out-of-tree directory layout, with symlinked aliases collapsing to one identity.

## User Stories / Use Cases

- As a developer of later phases, I want one helper that yields the state-dir paths so that no package re-implements the layout.
- As an operator, I want a symlinked alias of my project to reuse the same cage state so that I don't get a duplicate proxy.

## Acceptance Criteria

- [x] `internal/state` resolves a directory to its canonical absolute path (`realpath`-style) and a deterministic `<hash>`.
- [x] Two paths that resolve to the same physical directory (incl. via symlink) produce the same hash; different directories produce different hashes.
- [x] The package exposes typed accessors for every state-dir artifact: `policy.json`, `network.sb`, `proxy.lock`, `egress.jsonl`, `claude/`, and the session-overlay file, all under `~/.cache/agent-creance/projects/<hash>/`.
- [x] All filesystem access goes through the `internal/sysdep` seam (no direct `os` calls in logic).

## Verification & Test Steps

1. `go build ./...` → compiles.
2. `go test -race ./internal/state/...` → all pass. Table tests must include: a path and its symlink resolving to one hash; two distinct paths giving distinct hashes; every accessor returning a path rooted at the expected `projects/<hash>/`.
3. `make lint` → no new findings.
4. Grep guard (no direct OS in logic): `! grep -rnE '"os"|os\.(Open|Stat|MkdirAll|ReadFile|WriteFile)' internal/state/*.go` → exit 0 (access is via the seam).
5. `make test` → green overall.

## Out of Scope

- Creating/writing any of the artifacts (compiler/proxy/audit own those).
- Lock-file semantics (AC-0020).

## Dependencies & Sequencing

Phase 1. Foundation for AC-0013, AC-0014, AC-0020, AC-0021. Can start immediately.

## Questions for Research/Planning

- [x] Which hash (FNV/SHA-256-truncated) and what length keeps paths short but collision-safe? → **SHA-256 truncated to the first 8 bytes / 16 hex chars (64-bit)**.
- [x] How should the package behave on iCloud/SMB paths (defer the warning to `doctor`, AC-0031)? → **Deferred to `doctor`; `internal/state` only resolves, it performs no reliability checks.**

## References

- `docs/design.md` — "Config compilation", "Multi-agent lifecycle".
- Spec WP-1.1.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.

### 2026-06-05
Implemented via `/tce:work`.

- Research: `thoughts/shared/research/2026-06-05-AC-0006-state-dir-project-identity.md`.
- Plan: `thoughts/shared/plans/2026-06-05-AC-0006-state-dir-project-identity.md`.
- Key finding: AC-0009's planned file-I/O `FileSystem` does **not** cover the
  path-canonicalisation + cache-root derivation this package needs, so there was no
  real blocking dependency — only a "where does the seam live" choice. Resolved by
  adding a narrow `sysdep.PathResolver` seam now (distinct from AC-0009's
  `FileSystem`).
- Delivered:
  - `internal/sysdep.PathResolver` (+ `OSPathResolver`, `sysdeptest.FakePathResolver`,
    real-impl smoke tests) — commit `07fb82a`.
  - `internal/state` (`Resolver`/`Layout`/`Resolve`/hash/accessors) with hermetic
    table tests — commit `b002c4e`.
- Decisions: hash = SHA-256-trunc-16-hex; cache root honours `XDG_CACHE_HOME` then
  `$HOME/.cache`; session-overlay file named `session-overlay.yaml`. No `cli.App`
  wiring (no consumer command yet — wired when `run`/`policy` arrive).
- Verification: `go build ./...`, `make test` (race), `make lint`, and the no-`os`
  grep guard on `internal/state/*.go` all green.
