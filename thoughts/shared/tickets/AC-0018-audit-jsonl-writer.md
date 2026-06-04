# AC-0018: Audit JSONL writer + rotation (WP-3.2)

**Status:** Open
**Estimated Complexity:** Medium
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-3.2 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** AC-0017 (WP-3.1)
**Spike gate:** none
**Cross-cutting:** C4 (out-of-tree)

## Problem Statement

Every egress decision must be recorded for after-the-fact audit, with sensitive headers stripped, and the log must not grow unbounded. Because the agent has `./` writable, the log lives out-of-tree so it can't be doctored by the process it records.

## Desired Outcome

The enforcer writes a `0600` JSONL audit log at `egress.jsonl` in the state dir, redacting sensitive headers, with size-based rotation capping disk at ~1 GB/project and never silently dropping entries.

## User Stories / Use Cases

- As an operator, I want a tamper-resistant record of what the agent reached so that I can review a session.
- As a security reviewer, I want secrets stripped from the log so that the audit trail isn't itself a leak.

## Acceptance Criteria

- [ ] Each entry records timestamp, method, URL, decision (allow/soft-deny/hard-deny), matching rule, response status; passthrough entries are host-only (no path/method/status).
- [ ] `Authorization`, `Cookie`, `X-Api-Key` (and the documented set) are redacted before write.
- [ ] File mode is `0600`.
- [ ] Rotation: a write that would exceed 500 MB deletes any `.1`, renames current→`.1`, starts a fresh current; no entries are lost across the flip.
- [ ] The log is written under the out-of-tree state dir only.

## Verification & Test Steps

1. Python addon unit tests: feed synthetic requests and assert the emitted JSONL matches a golden schema; assert redacted headers are absent.
2. Rotation test: set a tiny threshold in test config, write past it, assert `egress.jsonl.1` exists, current is fresh, and the union of both files contains every entry written (count preserved).
3. Permissions: assert `stat` reports `0600` on the created file.
4. Integration (`make test-integration`): run the addon live, make a few caged requests, confirm entries appear and a passthrough request logs host-only.
5. C4 guard: assert the path resolves under `~/.cache/agent-creance/projects/<hash>/` and not in the project tree.

## Out of Scope

- Reading/tailing the log (AC-0021).
- The structured deny-decision log (v0.2).

## Dependencies & Sequencing

Phase 3. Builds on AC-0017 (same addon).

## Questions for Research/Planning

- [ ] Full redaction header list — confirm against design + any tokens in query strings.
- [ ] Atomicity of the rotation rename under concurrent writes from one addon process.

## References

- `docs/design.md` — "Audit log".
- Spec WP-3.2.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.
