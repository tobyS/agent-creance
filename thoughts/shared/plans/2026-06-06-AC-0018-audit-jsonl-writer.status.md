---
plan: thoughts/shared/plans/2026-06-06-AC-0018-audit-jsonl-writer.md
ticket: AC-0018
updated: 2026-06-06
---

# Implementation status — AC-0018

- [x] Phase 1 — `audit.py` pure module + writer, with tests
- [x] Phase 2 — Wire the writer into `enforcer.py` (four logging points)
- [x] Phase 3 — Integration probes + final verification

**Ticket AC-0018: Done.**

## Notes
- Phase 1: `audit.py` (builders + `scrub_url` + `AuditLog`) and `test_audit.py`
  (goldens, scrub table, 0600, rotation no-drop) added; `make test-enforcer` green
  (86 passed). Goldens: `egress_request_entry.jsonl.golden`,
  `egress_passthrough_entry.jsonl.golden`.
- Phase 2: `enforcer.py` wired — `creance_audit_log` option, `response` hook (full
  intercept entries), `http_connect`/`tls_clienthello` host-only passthrough
  entries, decision stashed in `flow.metadata` from `request`. `test_enforcer.py`
  +6 tests (allow/soft/hard + clean/denied passthrough + disabled-when-empty).
  enforcer suite green (92 passed); `go build ./...` + `make test` green.
- Phase 3: `test_integration.py` wires `creance_audit_log` into `running_proxy` and
  adds 4 live audit probes (soft/hard-deny, allow, host-only passthrough). Final
  verification all green: `make test`, `go build ./...`, `make lint`,
  `make test-enforcer` (92), enforcer integration (`pytest -m integration`, 10
  passed). Ticket ACs ticked; both research questions answered in the ticket.
