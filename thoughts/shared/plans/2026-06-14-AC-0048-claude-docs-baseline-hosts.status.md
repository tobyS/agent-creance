---
plan: 2026-06-14-AC-0048-claude-docs-baseline-hosts.md
ticket: AC-0048
updated: 2026-06-14
---

# AC-0048 implementation status

## Phase 1 — baseline template + test — DONE
- Added `code.claude.com` (intercept, GET, `paths: ["/docs/"]`) and legacy
  redirectors `docs.anthropic.com` / `docs.claude.com` (intercept, host-wide GET)
  to `globalConfigTemplate` in `internal/cli/setup.go`; updated the constant's
  doc comment.
- Extended `TestSetupScaffoldsGlobalConfig` (`internal/cli/setup_test.go`) with a
  host→rule lookup asserting the new rules' mode/paths/methods (uses `slices`).
- `make test` + `make lint` green.

## Phase 2 — README adoption note — DONE
- Added an `## Egress baseline` section to README: explains the scaffolded global
  baseline now includes Anthropic docs hosts, and gives the copy-paste snippet
  for existing-config users (matches the template's hosts/scoping).

## Phase 3 — build + ticket close — PENDING
