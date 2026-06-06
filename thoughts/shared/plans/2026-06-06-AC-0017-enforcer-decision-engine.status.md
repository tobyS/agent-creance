---
plan: thoughts/shared/plans/2026-06-06-AC-0017-enforcer-decision-engine.md
ticket: AC-0017
updated: 2026-06-06
---

# Status — AC-0017 enforcer.py decision engine

- [x] Phase 1 — Python scaffold, pure matcher, corpus parity green
- [x] Phase 2 — 403 response bodies + goldens + design.md sync
- [x] Phase 3 — mitmproxy addon (hooks, policy load, hot reload)
- [x] Phase 4 — integration test (real mitmproxy + curl), gated S1
- [x] Phase 5 — final verification + close ticket

## Notes

### Phase 1 (done 2026-06-06)
- `policy.py` ports the Go matcher (match.go/glob.go/policy.go) faithfully; 54
  Python tests green incl. all 18 corpus vectors via `make test-enforcer`.
- Go anti-fork guard `internal/policy/corpus_parity_test.go` asserts a single
  canonical corpus dir.
- **Env deviation from plan (user-approved):** the user's only Python is 3.14.5,
  on which the originally-pinned **mitmproxy 12.0.1 cannot install** (it caps
  `Brotli<=1.1.0`; Brotli ships 3.14 wheels only at 1.2.0). Per the planning
  checkpoint the user chose to **bump the pin to mitmproxy 12.2.3** (the version
  the S1 spike was validated against; installs cleanly on 3.14). Updated
  `internal/buildinfo/buildinfo.go`, `requirements.txt`, and the
  `doctor_healthy.txtar` stub (12.0.1 → 12.2.3) to keep that fixture genuinely
  healthy. All Go tests still green.

### Phase 4 (done 2026-06-06)
- Live integration (`make test-enforcer-integration`, folded into
  `make test-integration`): 6 curl-driven probes against a real mitmdump — soft/
  hard-deny (no egress), allow/passthrough/intercept-cert/hot-reload (egress). All
  green locally; skip cleanly without curl/egress.
- **Security finding folded into the addon:** mitmproxy at CONNECT was connecting to
  the upstream eagerly (and sniffing its cert), so denied hosts were being contacted
  (egress leak) and unreachable denied hosts returned a spurious 502. The addon now
  sets `connection_strategy=lazy` + `upstream_cert=False` in `running()`, making the
  proxy a true gate: a denied host is answered with the 403 and never connected to.
