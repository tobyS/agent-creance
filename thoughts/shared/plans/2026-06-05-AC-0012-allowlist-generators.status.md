# Status: AC-0012 — Allowlist generators (WP-2.3)

Plan: `thoughts/shared/plans/2026-06-05-AC-0012-allowlist-generators.md`

- [x] Phase 1 — `state.GeneratorsRoot()` cache-path helper (commit 239b578)
- [x] Phase 2 — Output rule type, URL normalization, forge table (commit eb7abba)
- [x] Phase 3 — Manifest parsing, generators, dispatch (commit 4e5d6b9)
- [x] Phase 4 — Manifest-hash output cache (commit 2b1be38)
- [x] Phase 5 — Live integration test + ticket close

## Notes
- All phases complete. `make test`, `make test-integration`, `make lint`,
  `go build ./...` all green. Golden rule-sets reviewed.
- GitLab forge row shipped alongside GitHub (planning checkpoint decision).
- Implementation choice over the plan: composer platform/meta filter uses the
  "no `/` → not a Packagist package" rule rather than enumerating prefixes
  (`php`/`ext-*`/…) — strictly more robust, covers all platform constraints.
- Ticket AC-0012 → Done.
