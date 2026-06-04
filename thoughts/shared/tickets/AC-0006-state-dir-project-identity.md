# AC-0006: State directory & project identity (WP-1.1)

**Status:** Open
**Estimated Complexity:** Small
**Created:** 2026-06-04
**Updated:** 2026-06-04
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

- [ ] `internal/state` resolves a directory to its canonical absolute path (`realpath`-style) and a deterministic `<hash>`.
- [ ] Two paths that resolve to the same physical directory (incl. via symlink) produce the same hash; different directories produce different hashes.
- [ ] The package exposes typed accessors for every state-dir artifact: `policy.json`, `network.sb`, `proxy.lock`, `egress.jsonl`, `claude/`, and the session-overlay file, all under `~/.cache/agent-creance/projects/<hash>/`.
- [ ] All filesystem access goes through the `internal/sysdep` seam (no direct `os` calls in logic).

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

- [ ] Which hash (FNV/SHA-256-truncated) and what length keeps paths short but collision-safe?
- [ ] How should the package behave on iCloud/SMB paths (defer the warning to `doctor`, AC-0031)?

## References

- `docs/design.md` — "Config compilation", "Multi-agent lifecycle".
- Spec WP-1.1.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.
