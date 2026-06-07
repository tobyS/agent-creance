# AC-0031: `doctor` extension (WP-6.2)

**Status:** Done
**Estimated Complexity:** Large
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-6.2 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** AC-0026 (CA verify), AC-0020 (lifecycle/locks)
**Spike gate:** none
**Kind:** Extend existing `internal/cli/doctor.go` + `internal/prereq`

## Problem Statement

`doctor` currently reports only version skew. To be the "tell me everything that's wrong" command, it must also live-verify CA trust, find orphan proxies, detect exposed host services, warn on `flock`-unreliable filesystems, and surface the port-changed-under-attached-agents condition — with a `--fix` that remediates what it safely can.

## Desired Outcome

`agent-creance doctor` produces a full diagnostic report covering CA trust, orphan proxies, exposed host services, unreliable-filesystem warnings, the port-change condition, and the existing version report; `--fix` auto-remediates orphans and similar safe issues.

## User Stories / Use Cases

- As an operator with a flaky cage, I want `doctor` to pinpoint the problem so that I can fix it fast.
- As an operator, I want `doctor --fix` to clean orphan proxies so that I don't hunt PIDs by hand.

## Acceptance Criteria

- [x] Report includes: CA live-verify (reusing AC-0026), orphan-proxy scan, exposed-host-service scan, `flock`-unreliable filesystem warning (iCloud/SMB), port-changed-under-attached-agents condition (surfaced as the persistent "stranded agents" state), and the existing version report (every mismatch incl. patch).
- [x] `--fix` cleans what it safely can (orphan proxies) and reports what it changed.
- [x] Exit code reflects whether actionable problems remain (untrusted CA, un-fixed orphan, missing prereq → non-zero; warnings → 0).
- [x] Existing version-report behavior and its golden tests remain green (`internal/prereq` untouched).

## Verification & Test Steps

1. `go build ./...` → compiles.
2. `go test -race ./internal/cli/...` and `./internal/prereq/...` (existing) → still green.
3. Golden report: a `testscript`/golden fixture drives each condition (orphan lock present, CA verify stubbed failing, unreliable-FS flag, version skew) and compares the rendered report to golden output (`make golden` diff reviewed).
4. `--fix` integration (`make test-integration`): create an orphan proxy + lock, run `doctor --fix`, assert the orphan is gone and the lock cleaned.
5. `make lint` → clean.

## Out of Scope

- Re-implementing CA verify or lifecycle (reuse AC-0026/AC-0020).
- `status`/`clean` (AC-0032).

## Dependencies & Sequencing

Phase 6. Reuses AC-0026 + AC-0020.

## Questions for Research/Planning

- [ ] How to detect "exposed host service bound to 0.0.0.0" portably.
- [ ] Filesystem-type detection for the iCloud/SMB warning.

## References

- `docs/design.md` — "Prerequisites and version handling", "Commands" (doctor), "Multi-agent lifecycle".
- Spec WP-6.2.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification. Extends the existing doctor skeleton — keep current version-report tests green.

### 2026-06-07
Implemented (research + plan in thoughts/). Key decisions from the planning checkpoint:
- **Orphan scan scoped to the current project only** — no cross-project enumeration (`ReadDir`/`ProjectsRoot`) built; left to AC-0032.
- **CA not generated → "run setup", never generated** — doctor stays read-only (checks `CAGenerated` before the side-effecting `Verify`).
- **Port-change condition** surfaced as the persistent, detectable "stranded agents" state (live agents but proxy not on the recorded port); AC-0020 persists no old port.
- **Exit non-zero** only on untrusted CA, un-fixed orphan, or missing prereq.
- New seams: `sysdep.FilesystemTyper` (statfs) and `sysdep.ListenerScanner` (lsof). New `internal/doctor` package (Checker/Report/Render). `--fix` reuses the AC-0020 teardown via `proxy.Manager.CleanOrphan`.
