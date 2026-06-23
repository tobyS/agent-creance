# Status: AC-0060 — Untrusted-input hardening

Plan: `thoughts/shared/plans/2026-06-23-AC-0060-untrusted-input-hardening.md`

## Phases

- [x] Phase 1 — F14: FakeFileSystem fidelity + cage.Prepare
- [ ] Phase 2 — F11: registry name charset validation + escaping
- [ ] Phase 3 — F12: shared-apex homepage drop
- [ ] Phase 4 — F7: drop audit query string + reconcile docs

## Log

- 2026-06-23: research + plan committed; ticket → In Progress. Starting Phase 1.
- 2026-06-23: Phase 1 done. Tightened FakeFileSystem (WriteFile ENOENT on missing
  parent, MkdirAll creates ancestors, Remove ErrNotExist on missing); added
  MkdirAll(Layout.Root) to cage.Prepare. Stricter fake surfaced two more call
  sites than research predicted — both test-fidelity gaps, not prod bugs: init_test
  needed the project root seeded as a dir; extract_test's escape check needed to
  allow the enforcer root's ancestors (which os.MkdirAll legitimately creates).
  make test + lint green.
