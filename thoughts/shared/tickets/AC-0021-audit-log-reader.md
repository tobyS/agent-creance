# AC-0021: Audit log reader — logs --follow / --summary (WP-3.5)

**Status:** Done
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

- [x] `logs --follow` streams new entries live and continues correctly across a rotation (does not get stuck on the renamed file).
- [x] `logs --summary` reads `.1` and current in order as one stream and reports allow/soft-deny/hard-deny counts.
- [x] Implemented natively (fsnotify), not by shelling out to `tail`.

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

- [x] fsnotify behavior on macOS for rename events — does it fire reliably for the rotation pattern?
  **Resolved:** Not reliably enough to depend on alone — so don't. On macOS/kqueue a
  watch placed on the *file* is destroyed by the rename, and events are coalesced /
  best-effort. The follower therefore watches the *parent directory* (which survives
  the rename and delivers the `Create` for the fresh file), detects rotation by inode
  identity (`os.SameFile`), and runs a 1 s stat-poll backstop so a dropped/coalesced
  event can never lose entries. fsnotify is a latency reducer, not a correctness
  dependency. Verified on real macOS by `internal/audit/follow_test.go` (rotate
  mid-stream; stable over `-count=5 -race`).

## References

- `docs/design.md` — "Audit log" (Tooling).
- Spec WP-3.5.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.

### 2026-06-06
Implemented (WP-3.5). New Go package `internal/audit` (the read side of the egress
audit log) + `agent-creance logs` command:

- **Pure, seam-free core** over `io.Reader`/`[]byte` (`entry.go`, `summary.go`,
  `format.go`): `ParseLine` (the two enforcer JSONL shapes — intercepted
  `{ts,method,url,decision,rule,status}` and passthrough `{ts,host,decision}`),
  `Summarize` (tallies allow/soft-deny/hard-deny across `.1`-then-current as one
  stream, skipping + counting malformed lines rather than aborting), `FormatEntry`
  (one human-readable line per entry). Table + golden tested.
- **File layer** (`read.go`): `SummarizeFiles` / `Dump` open `.1` then current (missing
  files skipped, not an error).
- **`Follow`** (`follow.go`): native fsnotify tail, rotation-aware — see the research
  question above for the watch-the-dir + inode-identity + poll-backstop design.
- **`logs` command** (`internal/cli/logs.go`): bare = dump once; `--summary` = counts;
  `--follow` = live stream; `--follow`+`--summary` rejected. Resolves the project's
  out-of-tree state dir from cwd via `state.New(app.Paths).Resolve(".")`. Added
  `state.Layout.EgressJSONLRotated()` so the `.1` contract is named once on the Go side.

Decisions (checkpoint): (1) `--follow` uses concrete fsnotify with a real-temp-file
follow test — a scoped, documented deviation from the "all FS through `sysdep`" rule,
because fsnotify needs real kernel events no fake can produce; the pure core stays
seam-free. (2) Output is human-formatted, dump-once for bare `logs`; `--follow` starts
at end-of-file (history not re-emitted).

Verification — all green: `make test` (race; incl. the audit unit/golden/follow tests
and the `logs.txtar` testscript), `go build ./...`, `make lint`. Real end-to-end
manually confirmed: `logs`/`logs --summary` over a hand-seeded `.1`+current pair render
the combined stream and correct counts. Research: `thoughts/shared/research/2026-06-06-AC-0021-audit-log-reader.md`;
plan: `thoughts/shared/plans/2026-06-06-AC-0021-audit-log-reader.md`.
