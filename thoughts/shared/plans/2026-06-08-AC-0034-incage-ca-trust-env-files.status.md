# AC-0034 implementation status

Plan: thoughts/shared/plans/2026-06-08-AC-0034-incage-ca-trust-env-files.md

Environment note: safehouse, mitmdump, node, python3, and the mitmproxy CA are all
present on this host, so the live red→green integration proof can be attempted
(subject to sandbox-exec nesting; the harness skips if it can't nest).

## Phase 1 — probes + vectors (RED) — DONE
- [x] Add env-ca-node / env-ca-python ALLOWED vectors (matrix.go)
- [x] Add the two env-var-CA probes (fake-agent.sh)
- [x] make test green
- [x] Integration: reproduction fails in-cage (RED) — env-ca-node →
      UNABLE_TO_VERIFY_LEAF_SIGNATURE, env-ca-python → ERR; both runtimes ran
      in-cage; other 16 vectors PASS; count=18.

## Phase 2 — CA read-grant fix (GREEN) — IN PROGRESS
## Phase 3 — docs & ticket — NOT STARTED
