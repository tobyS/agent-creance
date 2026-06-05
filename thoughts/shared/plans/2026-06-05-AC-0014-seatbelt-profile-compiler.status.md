---
plan: thoughts/shared/plans/2026-06-05-AC-0014-seatbelt-profile-compiler.md
ticket: AC-0014
started: 2026-06-05
---

# Implementation status — AC-0014

- [x] Phase 1 — Pure renderers + unit/golden tests
- [x] Phase 2 — Compiler (write network.sb out-of-tree)
- [x] Phase 3 — Integration test (S3 localhost-refusal self-test)
- [x] Phase 4 — Doc corrections + close ticket

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
- 2026-06-05: Phase 3 done — live_integration_test.go ships the S3 probes against the
  generated rules via sandbox-exec (EPERM on a non-allowlisted port over v4+v6,
  allowlisted port reachable). Added a preflight that SKIPS when the environment can't
  apply nested sandbox profiles (this dev box can't — even (allow default) hits
  "sandbox_apply: Operation not permitted"). Verified out-of-band that our generated
  localhost:N profile COMPILES (reaches sandbox_apply) while the old literal 127.0.0.1:N
  form is rejected at compile time ("host must be * or localhost") — confirms the spike
  correction. Lints clean under -build-tags=integration.
- 2026-06-05: Phase 4 done — corrected design.md (~53 address-family caveat, ~100
  host_services comment, ~295 network.sb two-fragment split), config.go HostService doc,
  and the spec WP-2.5 bullet to the localhost-token form. Ticket marked Done; all ACs
  checked with spike corrections noted. Final battery green (build/test/lint/golden).
  COMPLETE.
</content>
