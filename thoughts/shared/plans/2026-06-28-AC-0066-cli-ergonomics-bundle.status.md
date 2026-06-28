# AC-0066 implementation status

Plan: thoughts/shared/plans/2026-06-28-AC-0066-cli-ergonomics-bundle.md

- [x] Phase 1 — S5: setup → init next-step pointer (commit pending)
- [x] Phase 2 — S6: run/proxy startup error remediation hints (commit pending)
  - Note: config wraps point at `.agent-creance.yaml`, NOT `doctor` — doctor does
    not inspect the project config, so a doctor pointer there would be dishonest.
    Proxy paths point at `doctor` (doctor does diagnose the proxy).
- [ ] Phase 3 — S6: config-validation corrected-form hints
- [ ] Phase 4 — S7: doctor --json and status --json
- [ ] Phase 5 — S7: run --quiet
- [ ] Phase 6 — S8: document shell completion
