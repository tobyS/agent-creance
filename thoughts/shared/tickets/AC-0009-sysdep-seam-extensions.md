# AC-0009: sysdep seam extensions (WP-1.4)

**Status:** Done
**Estimated Complexity:** Small
**Created:** 2026-06-04
**Updated:** 2026-06-05
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

- [x] Interfaces defined for `Clock` (now/since), `FileSystem` (read/write/stat/mkdir/remove/rename as needed), `Keychain` (read item), `Flock` (acquire/release), `ProcessGroup` (start in new pgid, signal group, wait).
- [x] Each interface has a compile-time `var _ Iface = (*RealImpl)(nil)` assertion (real impls may be stubs returning `ErrNotImplemented` until their consumer ticket).
- [x] Each interface has a configurable fake in `sysdeptest` (records calls, returns scripted results/errors).
- [x] The existing `Commander` seam and tests remain green.

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

- [x] Keep one fat `FileSystem` or several narrow interfaces defined at point of use (Go idiom favors the latter)? → **Grow the existing single `FileSystem` in place** with write/stat/mkdir/remove/rename (its own doc delegated this growth to AC-0009); the seam stays concern-scoped (content I/O) rather than fanning out into micro-interfaces.
- [x] Does `Flock` belong here or behind a higher-level lock abstraction in `internal/proxy`? → **Thin `Flock` seam in `sysdep`** (wrapping `unix.Flock`); any higher-level lock abstraction lives in the proxy package with its consumer (WP-3.4).

## References

- `docs/design.md` — "Tech stack", testing conventions; `.claude/tce/profile.md`.
- Spec WP-1.4, cross-cutting concern C2.

## Implementation Plan

- Research: `thoughts/shared/research/2026-06-05-AC-0009-sysdep-seam-extensions.md`
- Plan: `thoughts/shared/plans/2026-06-05-AC-0009-sysdep-seam-extensions.md`

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.

### 2026-06-05
Implemented (WP-1.4). Added five seams to `internal/sysdep`, each with a fake in
`sysdeptest`, a compile-time `var _ Iface = (*Impl)(nil)` assertion on both the
real type and the fake, and a smoke test:

- `Clock` (now/since) — real `OSClock`; `FakeClock` (frozen + `Advance`).
- `FileSystem` grown in place with `WriteFile`/`Stat`/`MkdirAll`/`Remove`/
  `Rename` — real `OSFileSystem`; `FakeFileSystem` extended to a scripted
  in-memory FS with per-op error knobs.
- `Keychain` (`FindGenericPassword`, with `ErrItemNotFound`/`ErrKeychainLocked`
  contract sentinels) — `OSKeychain` stubbed (`ErrNotImplemented`); `FakeKeychain`
  functional with `Locked`/`WithItem`/`Lookups`.
- `Flock` (`Acquire` → release func) — `OSFlock` stubbed; `FakeFlock` records
  acquire/release + `Held`.
- `ProcessGroup`/signals (`Start` → `Process{Signal,Wait,Pgid}`, `Notify`) —
  `Start` stubbed, `Notify` real (`os/signal.Notify`); `FakeProcessGroup`/
  `FakeProcess` record commands/signals.

Decisions (question checkpoint): real stdlib impls now for `Clock` + `FileSystem`
writes; `ErrNotImplemented` stubs (new `sysdep.ErrNotImplemented` sentinel) for
the macOS-specific `Keychain`/`Flock`/`ProcessGroup.Start`, deferred to their
consumers (WP-4.1/WP-3.4/WP-4.3). No `go.mod` change; `cli.App` left unwired
(unused deps). All five verification steps pass (`go build`/`go vet`,
`go test -race ./internal/sysdep/...`, grep guard, `make test`); `make lint`
clean. Commits unsigned (signing key unavailable this session).
