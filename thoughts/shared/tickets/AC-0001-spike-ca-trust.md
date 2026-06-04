# AC-0001: Spike S1 — CA trust / certificate pinning (WP-0.1)

**Status:** Open
**Estimated Complexity:** Medium
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-0.1 / Spike S1 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** none
**Spike gate:** this ticket *is* a gate — blocks AC-0017 (WP-3.1), AC-0026 (WP-5.1), and the global baseline default for `api.anthropic.com`
**Kind:** Investigation (time-boxed, produces a findings note + a decision)

## Problem Statement

The entire egress-filtering story depends on the agent and its toolchain accepting mitmproxy's CA for TLS interception. If Claude Code pins `api.anthropic.com`, or `npm`/`pip`/`git` pin their registries, MITM fails and those paths break. This must be validated against the real tools *before* the enforcer and CA bootstrap are built, because the outcome decides the shipped default enforcement mode for `api.anthropic.com` (`intercept` vs `passthrough`).

## Desired Outcome

A findings note that records, per target host, whether a request through the trusted mitmproxy CA validates, and a recorded decision on the `api.anthropic.com` default mode — with any required design fallback (passthrough) folded into `docs/design.md`.

## User Stories / Use Cases

- As the maintainer, I want to know which hosts tolerate interception so that I don't build an enforcer on a false assumption.
- As an operator, I want `api.anthropic.com` to work caged on first try so that `run` isn't dead on arrival.

## Acceptance Criteria

- [ ] A research note exists at `thoughts/shared/research/2026-06-DD-s1-ca-trust.md`.
- [ ] The note records a PASS/FAIL (interception validates / host pins) for each of: `api.anthropic.com`, `registry.npmjs.org`, `pypi.org` + `files.pythonhosted.org`, `github.com`, `codeload.github.com`, `raw.githubusercontent.com`.
- [ ] The note records whether Claude Code itself reaches its API with `HTTPS_PROXY` set and the CA trusted.
- [ ] A `Decision:` line states the shipped default `mode` for `api.anthropic.com` (`intercept` or `passthrough`) with rationale.
- [ ] If the decision is `passthrough` (or any host pins on the critical path), `docs/design.md` is updated to reflect the chosen composition.

## Verification & Test Steps

> Manual/integration spike — the verifiable artifact is the research note; the experiments below must be reproducible from the note.

1. Trust the mitmproxy CA and start a proxy:
   - `mitmdump -p 0` (note the chosen port `P`), CA generated at `~/.mitmproxy/`.
   - Export `SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE`, `GIT_SSL_CAINFO` → `~/.mitmproxy/mitmproxy-ca-cert.pem`.
2. Per host, run through the proxy and record the result:
   - `curl -sS -o /dev/null -w '%{http_code} %{ssl_verify_result}\n' -x http://127.0.0.1:P https://api.anthropic.com/` → **expected (intercept works):** non-zero HTTP code and `ssl_verify_result 0`; **pin observed:** TLS handshake error.
   - Repeat for each target host above.
3. Drive Claude Code through the proxy: `HTTPS_PROXY=http://127.0.0.1:P claude --version` then a trivial prompt; record whether the API call succeeds.
4. Self-check the deliverable:
   - `test -f thoughts/shared/research/2026-06-*-s1-ca-trust.md && grep -q '^Decision:' thoughts/shared/research/2026-06-*-s1-ca-trust.md` → exit 0.

## Out of Scope

- Building the enforcer, CA installer, or any passthrough code (those are AC-0017 / AC-0026).
- Hosts not on the v0.1 baseline.

## Dependencies & Sequencing

Phase 0. Run first / in parallel with other spikes. Gates AC-0017, AC-0026, and the baseline config.

## Questions for Research/Planning

- [ ] If `api.anthropic.com` pins, does passthrough-at-CONNECT still let refresh + normal API calls through cleanly?
- [ ] Do any registries pin in a way that requires per-host passthrough rather than global trust?

## References

- `docs/design.md` — "Open spikes" (S1), "Per-host enforcement modes".
- Spec WP-0.1.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification. This is a gating spike — no dependent enforcement code merges before its decision is recorded.
