# AC-0023 implementation status

Plan: 2026-06-06-AC-0023-safehouse-invocation.md

- [x] Phase 1 — state.Layout.ProxyProfileSB()
- [x] Phase 2 — internal/cage pure Build (argv + env)
- [x] Phase 3 — Builder, Resolve, Prepare (side effects)
- [x] Phase 4 — integration test + final verification

## Log
- 2026-06-06: research + plan committed; starting Phase 1.
- 2026-06-06: Phase 1 committed (c415369).
- 2026-06-06: Phases 2+3 done — pure Build golden-tested, Builder/Resolve/Prepare with fake-FS tests; make lint clean.
- 2026-06-06: Phase 4 done — gated integration test added; it skips cleanly here because this session host cannot nest sandbox-exec (sandbox_apply not permitted), so it must run on an unsandboxed macOS host. make test + make lint green. Ticket marked Done; buildinfo 1.4.2-vs-0.10.1 skew flagged for follow-up.
