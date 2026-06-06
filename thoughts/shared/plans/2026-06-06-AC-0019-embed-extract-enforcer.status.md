# Status: AC-0019 — Embed & extract the enforcer addon

Plan: `2026-06-06-AC-0019-embed-extract-enforcer.md`

- [x] Phase 1 — `state.EnforcerRoot()` accessor + test (commit 18f85d2)
- [x] Phase 2 — `internal/proxy` embed + extractor + tests (commit 2ea8010)
- [x] Phase 3 — Reconcile docs & close the ticket

## Log
- Phase 1: added enforcerSubdir const + EnforcerRoot() + table test; build/test/lint green.
- Phase 2: internal/proxy embeds 4 modules, extracts via byte-compare + tmp/rename; 12 tests; build/test/lint green.
- Phase 3: design.md:449 reconciled to constant location; ticket ACs ticked, status Done, research Q answered. Done.
