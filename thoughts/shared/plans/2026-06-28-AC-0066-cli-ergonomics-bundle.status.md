# AC-0066 implementation status — COMPLETE (all phases committed; ticket Done)

Plan: thoughts/shared/plans/2026-06-28-AC-0066-cli-ergonomics-bundle.md

- [x] Phase 1 — S5: setup → init next-step pointer
- [x] Phase 2 — S6: run/proxy startup error remediation hints
  - Note: config wraps point at `.agent-creance.yaml`, NOT `doctor` — doctor does
    not inspect the project config, so a doctor pointer there would be dishonest.
    Proxy paths point at `doctor` (doctor does diagnose the proxy).
- [x] Phase 3 — S6: config-validation corrected-form hints
- [x] Phase 4 — S7: doctor --json and status --json
- [x] Phase 5 — S7: run --quiet
- [x] Phase 6 — S8: document shell completion
