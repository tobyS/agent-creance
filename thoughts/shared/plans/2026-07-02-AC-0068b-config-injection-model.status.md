---
plan: thoughts/shared/plans/2026-07-02-AC-0068b-config-injection-model.md
ticket: AC-0068b
started: 2026-07-02
---

# Implementation status — AC-0068b

## Phase 1 — Config schema: types, parsing, merge
Status: done

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
Status: done

- internal/config/template.go: RenderCredentialValue + validateTemplate. Substitutes
  {token}/{user}, applies an optional single non-nested base64(…) wrapper. Reference
  spec for the Python injector (AC-0068c).
- template_test.go: table-driven, placeholder token only — all five shapes render;
  reject missing {token}, {user} without username, unbalanced/double base64, unknown
  placeholder; accept the valid forms. Green.

## Phase 3 — Validation: local structural + cross-reference + warning tier
Status: done

- sysdep: exported pure ValidateSecretRefSyntax(ref) (op/keychain/env scheme + non-
  empty remainder, no resolution) + table test.
- config validate.go: per-rule auth checks (inject+in_cage exclusive; inject+
  passthrough) via validateRuleAuth; validateCredentials (source scheme, template,
  header) + validateHeaderName; ValidateEffective (post-merge: inject→defined = hard
  error; dangling credential + username-without-{user} = warnings, sorted).
- Loader.Load runs ValidateEffective on the merged view, stores Config.Warnings.
- run.go surfaces cfg.Warnings to stderr (non-blocking). doctor doesn't load the
  effective config, so run is the surface; a full run testscript isn't hermetic
  (needs real safehouse), so the warning population is covered by Load unit tests.
- Tests: 4 new golden-error fixtures (inject_and_in_cage, passthrough_with_inject,
  credential_bad_source, credential_bad_template); Load cross-layer resolves,
  undefined-inject fails, dangling warns; ValidateEffective unit test. Green; lint
  clean.

## Ticket: Done (2026-07-02). All phases complete; make test / test-enforcer / lint
## green; make build refreshed bin/agent-creance. Both Questions for
## Research/Planning resolved on the ticket.

## Phase 4 — Policy pipeline: compile, render, golden, enforcer
Status: done

- policy.go: Rule.Inject/InCage (json, ignored-by-Decide annotations); RuleFromConfig
  copies them; policy.Credential type + CredentialsFromConfig + Compiled.Credentials
  (references only).
- compile.go: build merges credentials across layers (mergeCredentials), runs
  validateInjectRefs (fail-closed on inject→undefined, the compiler's merged-view
  check since it bypasses Loader.Load), and ruleKey now includes inject/in_cage.
- render.go: authField marker (inject:<name> / in-cage), dimmed, in the allow view;
  JSON view carries credentials + fields automatically.
- Goldens regenerated + reviewed: compile policy.golden (credentials block + per-rule
  fields, no value; input_hash moved), render show/show.json/show_color and the
  explain index shift. Enforcer test_policy.py: new load test proves the fields are
  carried but ignored by the matcher. make test / test-enforcer / lint all green.
