---
plan: thoughts/shared/plans/2026-06-06-AC-0021-audit-log-reader.md
ticket: AC-0021
started: 2026-06-06
---

# Status — AC-0021 audit log reader (WP-3.5)

- [x] Phase 1 — state `.1` accessor + pure parse/summarize/format core (commit pending)
- [x] Phase 2 — file-backed Dump + SummarizeFiles over `.1`+current (commit pending)
- [ ] Phase 3 — rotation-aware Follow (fsnotify + poll backstop)
- [ ] Phase 4 — `logs` command + wiring + testscript + close-out
