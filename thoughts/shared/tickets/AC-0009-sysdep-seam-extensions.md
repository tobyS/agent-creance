# AC-0009: sysdep seam extensions (WP-1.4)

**Status:** Open
**Estimated Complexity:** Small
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-1.4 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** none
**Spike gate:** none

## Problem Statement

The project's core testability rule is that no logic package touches the OS directly. Phases 1–4 introduce new OS touchpoints (clock, filesystem, Keychain, flock, process-group/signals). To keep every later ticket hermetic, these seams and their fakes must exist *before* the consuming logic is written.

## Desired Outcome

`internal/sysdep` grows small interfaces for `Clock`, `FileSystem`, `Keychain`, `Flock`, and `ProcessGroup`/signals, each with a fake in `internal/sysdep/sysdeptest`, following the existing `Commander` pattern. Real implementations land with their consumers.

## User Stories / Use Cases

- As a developer of later tickets, I want a ready fake for each OS dependency so that my package's tests stay hermetic.

## Acceptance Criteria

- [ ] Interfaces defined for `Clock` (now/since), `FileSystem` (read/write/stat/mkdir/remove/rename as needed), `Keychain` (read item), `Flock` (acquire/release), `ProcessGroup` (start in new pgid, signal group, wait).
- [ ] Each interface has a compile-time `var _ Iface = (*RealImpl)(nil)` assertion (real impls may be stubs returning `ErrNotImplemented` until their consumer ticket).
- [ ] Each interface has a configurable fake in `sysdeptest` (records calls, returns scripted results/errors).
- [ ] The existing `Commander` seam and tests remain green.

## Verification & Test Steps

1. `go build ./...` → compiles (incl. compile-time interface assertions).
2. `go vet ./...` → clean.
3. `go test -race ./internal/sysdep/...` → pass (fakes have at least a smoke test each).
4. Grep guard: each new interface has a matching fake — `grep -l 'Clock\|FileSystem\|Keychain\|Flock\|ProcessGroup' internal/sysdep/sysdeptest/*.go` returns the fakes file(s).
5. `make test` → green (no regressions in `cli`/`prereq`).

## Out of Scope

- Real implementations beyond the minimal stub + compile assertion (each lands in its consumer ticket: Keychain→AC-0022, Flock/ProcessGroup→AC-0020/AC-0024, etc.).
- Wiring into `cli.App` for unused deps (add as consumers arrive).

## Dependencies & Sequencing

Phase 1. Enables hermetic tests in AC-0006, AC-0020, AC-0022, AC-0024, and others.

## Questions for Research/Planning

- [ ] Keep one fat `FileSystem` or several narrow interfaces defined at point of use (Go idiom favors the latter)?
- [ ] Does `Flock` belong here or behind a higher-level lock abstraction in `internal/proxy`?

## References

- `docs/design.md` — "Tech stack", testing conventions; `.claude/tce/profile.md`.
- Spec WP-1.4, cross-cutting concern C2.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.
