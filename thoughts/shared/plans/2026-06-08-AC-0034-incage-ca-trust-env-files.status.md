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

## Phase 2 — CA read-grant fix (GREEN) — DONE
- [x] profile.RenderCAReadFragment + golden + unit tests (grants file-read* on the
      cert only, metadata-only on the parent dir)
- [x] state.Layout.CAProfileSB (ca.sb)
- [x] cage Prepare writes ca.sb (EvalSymlinks for firmlinks); Build appends a third
      --append-profile; buildEnv doc updated
- [x] invocation.golden regenerated (one new --append-profile ca.sb)
- [x] make test / make build / make lint green
- [x] Integration GREEN: env-ca-node + env-ca-python → 200; all 18 PASS; negative
      control still detects the escape; stable -count=2
- [x] Empirically verified in-cage: CA private key (mitmproxy-ca.pem) denied, cert
      readable (temp probe, reverted)

## Phase 3 — docs & ticket — DONE
- [x] cage-verification.md: vector count 18; known-limitation #1 rewritten (env-var
      CA files now work in-cage; private key stays unreadable)
- [x] design.md: ca.sb bullet + single-CA-PEM exception note in "What the cage prevents"
- [x] ticket: ACs ticked, Status Done, dated note with red→green commit refs
- [x] drift guards still green after design.md edits
