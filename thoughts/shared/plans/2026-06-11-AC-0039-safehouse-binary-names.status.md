# AC-0039 Implementation Status

**Plan:** `thoughts/shared/plans/2026-06-11-AC-0039-safehouse-binary-names.md`

| Phase | Status |
|-------|--------|
| 1 — prereq multi-name resolution + buildinfo constants | done |
| 2 — thread resolved binary into cage launch | done |
| 3 — CLI surface (version consts, txtar, goldens, final verify) | done |

## Notes

- Started 2026-06-11; all phases completed 2026-06-11.
- Final verification: `make test` (race), `make lint`, `make golden` (zero
  churn) all green. Literal sweep clean — executable names live only in
  `internal/buildinfo`.
- Manual verification on the user's machine pending (organAIze.eu +
  `/opt/homebrew/bin/safehouse`); `bin/agent-creance` rebuilt at 11be1a6-dirty.
- Ticket AC-0039 marked Done (version-command AC leg descoped at checkpoint —
  it never probed PATH).
