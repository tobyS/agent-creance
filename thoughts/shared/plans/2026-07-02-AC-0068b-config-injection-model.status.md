---
plan: thoughts/shared/plans/2026-07-02-AC-0068b-config-injection-model.md
ticket: AC-0068b
started: 2026-07-02
---

# Implementation status — AC-0068b

## Phase 1 — Config schema: types, parsing, merge
Status: done (commit pending)

- Added Rule.Inject/InCage (auth axis), the Credential type, Config.Credentials,
  DefaultCredentialHeader, and Config.Warnings.
- rawConfig mirror + Parse conversion + defaultCredentialHeaders; mergeCredentials
  wired into merge (key-wise, over-wins, nil-when-empty).
- Tests: credentials_test.go (parse + auth axis, header default, KnownFields
  rejection inside a credential entry, merge over-wins + empty-is-nil). Green.
- Regenerated compile golden: only the input_hash line moved (inputHash marshals
  config.Config, so the new empty fields perturb the hash; the rule output is
  unchanged — new policy fields land in Phase 4).

## Phase 2 — Value-template rendering (pure)
Status: not started

## Phase 3 — Validation: local structural + cross-reference + warning tier
Status: not started

## Phase 4 — Policy pipeline: compile, render, golden, enforcer
Status: not started
