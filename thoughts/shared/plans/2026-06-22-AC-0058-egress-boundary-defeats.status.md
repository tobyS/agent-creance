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
- Status: not started

## Final verification
- Status: not started
