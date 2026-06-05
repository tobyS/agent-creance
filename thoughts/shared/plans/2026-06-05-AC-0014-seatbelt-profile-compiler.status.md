---
plan: thoughts/shared/plans/2026-06-05-AC-0014-seatbelt-profile-compiler.md
ticket: AC-0014
started: 2026-06-05
---

# Implementation status — AC-0014

- [x] Phase 1 — Pure renderers + unit/golden tests
- [x] Phase 2 — Compiler (write network.sb out-of-tree)
- [ ] Phase 3 — Integration test (S3 localhost-refusal self-test)
- [ ] Phase 4 — Doc corrections + close ticket

## Log
- 2026-06-05: research + plan committed (9461b76, 1b246d2). Starting Phase 1.
- 2026-06-05: Phase 1 done — internal/profile renderers + golden (network.golden:
  localhost:N, no forbidden literals). `go test -race ./internal/profile/...` green,
  `go build ./...` clean.
- 2026-06-05: Phase 2 done — Compiler (New/Compile/Result) writes network.sb atomically
  to layout.NetworkSB(); no cache. Tests: write+perm, C4 no-in-tree-write, regenerate
  each time, missing-config error. `make test`/`make lint` green. Note: New returns
  *Compiler (no error) — nothing fallible at construction, a minor deviation from the
  plan's (*Compiler, error) signature.
</content>
