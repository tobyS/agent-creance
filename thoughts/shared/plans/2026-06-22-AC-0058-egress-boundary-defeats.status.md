# AC-0058 Implementation Status

Plan: `thoughts/shared/plans/2026-06-22-AC-0058-egress-boundary-defeats.md`

## Phase A — SBPL injection + host/method validation (F1, F15, F18)
- Status: done (commit pending)
- parseHostService rejects control-char labels; RenderNetworkSB sanitizes the label
  defensively; ValidateHost/ValidateMethods added and applied to hand-authored rules
  (validateRules) and generator-emitted rules (compile.runGenerators).
- Tests: profile label-injection + %q-escaping pin; config validate cases (control-char
  label, lowercase/unknown method, scheme/whitespace host, valid globs); compile
  generator-rule validation. make test / make lint green; no golden diff.

## Phase B — Enforcer fail-closed (F2, F6)
- Status: done (commit pending)
- B1: request / http_connect / tls_clienthello hooks wrapped to fail closed — any
  exception hard-denies (request, http_connect) or refuses to tunnel (tls_clienthello);
  _safe_url helper keeps the fallback total.
- B2: _load(initial=) split — an initial load failure logs ERROR (trips mitmproxy
  ErrorCheck → non-zero exit); Go Manager gained a Sleeper seam + waitProxyReady poll
  after Spawn that surfaces a proxy that exits / never listens during startup. NewManager
  signature gained a Sleeper (all call sites + test fixtures updated).
- B3: malformed hot-reload keeps last-good and recovers — pinned by pytest.
- Tests: pytest fail-closed (3) + initial-load-ERROR + malformed-reload; Go lifecycle
  readiness success (existing spawn tests now model alive+listening) + two failure tests.
  make test / make test-enforcer / make lint green.

## Phase C — Host normalization & parity (F3, F4)
- Status: done (commit pending)
- C1: canonicalHost (Go glob.go) / canonical_host (policy.py) — lowercase, strip
  unambiguous :port (IPv6 left alone), strip single trailing dot — applied at the entry
  of Decide/decide and HostDisposition/host_disposition. Documented as the matcher
  contract in policy.go package doc (C2).
- C3: Go RuleSet.HostDisposition added, mirroring policy.py host_disposition. Corpus
  `expected` gained an optional host_disposition {passthrough, deny_reason}; both Go and
  Python replays (and the render-side strict decoder) extended in lockstep. 6 new
  vectors: trailing-dot deny, port deny, uppercase allow, passthrough disposition,
  mixed-mode (passthrough+intercept → not passthrough), deny disposition via trailing dot.
- Tests: Go canonicalHost + HostDisposition tables; Python canonical_host table; corpus
  replays both languages. make test / make test-enforcer / make lint green; no golden diff.

## Final verification
- Status: done
- make test / make test-enforcer / make lint all green; make build rebuilt
  bin/agent-creance. All A/B/C acceptance criteria checked; ticket set to Done.
