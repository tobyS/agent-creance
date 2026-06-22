# AC-0058 Implementation Status

Plan: `thoughts/shared/plans/2026-06-22-AC-0058-egress-boundary-defeats.md`

## Phase A — SBPL injection + host/method validation (F1, F15, F18)
- Status: done (commit pending)
- parseHostService rejects control-char labels; RenderNetworkSB sanitizes the label
  defensively; ValidateHost/ValidateMethods added and applied to hand-authored rules
  (validateRules) and generator-emitted rules (compile.runGenerators).
- Tests: profile label-injection + %q-escaping pin; config validate cases (control-char
  label, lowercase/unknown method, scheme/whitespace host, valid globs); compile
  generator-rule validation. make test / make lint green; no golden diff.

## Phase B — Enforcer fail-closed (F2, F6)
- Status: not started

## Phase C — Host normalization & parity (F3, F4)
- Status: not started

## Final verification
- Status: not started
