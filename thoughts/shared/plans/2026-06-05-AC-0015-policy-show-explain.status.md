---
plan: thoughts/shared/plans/2026-06-05-AC-0015-policy-show-explain.md
ticket: AC-0015
started: 2026-06-05
---

# Implementation status — AC-0015 (policy show / explain)

- [x] Phase 1 — `internal/policy/render` package (Show/Explain + JSON, goldens, C1 vector replay) — commit 3ae5df3
- [x] Phase 2 — grow `App` composition root (FS/Paths/Clock/HTTP), wire Main, register command — commit d58b589
- [x] Phase 3 — `internal/cli/policy.go` command tree (compile-on-demand, --json, --method) — commit d58b589
- [x] Phase 4 — testscript scenarios + final verification (build/golden/test/lint) — commit pending

## Notes
- All phases complete. Final verification green: `go build ./...`, `make golden`
  (no diff), `make test` (race), `make lint` all pass.
- Smoke-tested the real binary end-to-end (show/--json, explain allow/passthrough/
  hard-deny/soft-deny/--method/--json, and the no-config error → exit 1).
- testscript scenarios: policy_show, policy_explain, policy_no_config (all hermetic,
  generator-free so fully offline).
</content>
