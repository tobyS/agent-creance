# AC-0021: Audit log reader — logs --follow / --summary (WP-3.5)

**Status:** Open
**Estimated Complexity:** Medium
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-3.5 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** AC-0018 (WP-3.2, log format)
**Spike gate:** none

## Problem Statement

Operators read the egress log live and in summary. A naive `tail -f` breaks across the size-based rotation; the reader must follow through a rotation and treat `.1` + current as one logical stream.

## Desired Outcome

`internal/audit` powers `agent-creance logs --follow` (rotation-aware via `fsnotify`) and `agent-creance logs --summary` (reads `.1` then current as one stream, reporting allowed/blocked counts).

## User Stories / Use Cases

- As an operator, I want `logs --follow` to keep streaming after a rotation so that I don't lose the live view.
- As an operator, I want `logs --summary` to tell me what was allowed vs blocked so that I can spot issues fast.

## Acceptance Criteria

- [ ] `logs --follow` streams new entries live and continues correctly across a rotation (does not get stuck on the renamed file).
- [ ] `logs --summary` reads `.1` and current in order as one stream and reports allow/soft-deny/hard-deny counts.
- [ ] Implemented natively (fsnotify), not by shelling out to `tail`.

## Verification & Test Steps

1. `go build ./...` → compiles.
2. `go test -race ./internal/audit/...`:
   - follow test: write entries, trigger a rotation mid-stream (rename + new file), write more, assert the follower emitted every entry across the flip.
   - summary test: a `.1` + current pair with known decisions yields the expected counts.
3. Hermetic CLI test (`testscript`): `agent-creance logs --summary` over a fixture log → golden output.
4. `make lint` → clean; `make test` → green.

## Out of Scope

- Writing/rotating the log (AC-0018).
- The v0.2 structured deny-decision log.

## Dependencies & Sequencing

Phase 3. Depends on the AC-0018 format. Contributes to M2.

## Questions for Research/Planning

- [ ] fsnotify behavior on macOS for rename events — does it fire reliably for the rotation pattern?

## References

- `docs/design.md` — "Audit log" (Tooling).
- Spec WP-3.5.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.
