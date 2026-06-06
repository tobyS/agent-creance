# AC-0018: Audit JSONL writer + rotation (WP-3.2)

**Status:** Done
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

- [x] Each entry records timestamp, method, URL, decision (allow/soft-deny/hard-deny), matching rule, response status; passthrough entries are host-only (no path/method/status).
- [x] `Authorization`, `Cookie`, `X-Api-Key` (and the documented set) are redacted before write. *(See decision below: the entry logs no headers map at all, so these headers can never be written; "redaction" with teeth is implemented as sensitive query-string token scrubbing in the logged URL.)*
- [x] File mode is `0600`.
- [x] Rotation: a write that would exceed 500 MB deletes any `.1`, renames current→`.1`, starts a fresh current; no entries are lost across the flip. *(via atomic `os.replace`.)*
- [x] The log is written under the out-of-tree state dir only. *(path comes from the `creance_audit_log` option, set by AC-0020 from `state.Layout.EgressJSONL()`; C4 path invariant already covered by `state_test.go`.)*

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

- [x] Full redaction header list — confirm against design + any tokens in query strings.
  **Resolved (checkpoint decision):** the audit entry carries *no* headers map (the
  design's field list — ts, method, URL, decision, rule, status — omits headers), so
  no header can leak. "Redaction" is implemented as query-string token scrubbing on
  the logged URL: the value of any of `token`, `access_token`, `api_key`, `apikey`,
  `key`, `secret`, `client_secret`, `password`, `code`, `sig`, `signature`, `auth`
  (case-insensitive name) is replaced with `REDACTED`, the param kept.
- [x] Atomicity of the rotation rename under concurrent writes from one addon process.
  **Resolved:** non-issue. mitmproxy hooks run on a single asyncio event loop, so
  writes are serialized — no concurrent writes from one process. Rotation is
  `os.replace` (atomic on one filesystem), which both removes any prior `.1` and
  renames current→`.1` in one step. No lock needed.

## References

- `docs/design.md` — "Audit log".
- Spec WP-3.2.

## Implementation Plan

- Research: `thoughts/shared/research/2026-06-06-AC-0018-audit-jsonl-writer.md`
- Plan: `thoughts/shared/plans/2026-06-06-AC-0018-audit-jsonl-writer.md`

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.

### 2026-06-06
Implemented (WP-3.2). New pure module `internal/proxy/enforcer/audit.py` (entry
builders, URL query-token scrubbing, compact JSONL encoding, and an `AuditLog`
writer owning `0600` mode + 500 MB rotation via atomic `os.replace`). Wired into
`enforcer.py` via a new `creance_audit_log` mitmproxy option and four logging points:
the `response` hook writes the full intercept entry (fires for synthesized 403s too),
while `http_connect` (host-denied) and `tls_clienthello` (clean tunnel) write
host-only passthrough entries — there is no flow/path/byte-count for an ignored
connection, so byte counts (mentioned in design) are intentionally not recorded.

Decisions: (1) no headers map in the entry — redaction = URL query-token scrubbing;
(2) audit disabled when the option is empty (mirrors `creance_policy`); the Go
launcher wires the path from `state.Layout.EgressJSONL()` in AC-0020.

Verification — all green: `make test` (race), `go build ./...`, `make lint`,
`make test-enforcer` (92 passed), `make test-enforcer` integration probes
(`pytest -m integration`, 10 passed incl. live soft/hard-deny, allow, and host-only
passthrough audit). Commits this session are unsigned (`--no-gpg-sign`): the SSH
signing key was unreadable in the work environment.
