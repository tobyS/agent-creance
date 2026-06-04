# AC-0001: Spike S1 — CA trust / certificate pinning (WP-0.1)

**Status:** Done
**Decision:** `api.anthropic.com` → `intercept` (resolved 2026-06-04)
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

- [x] A research note exists at `thoughts/shared/research/2026-06-04-s1-ca-trust.md`.
- [x] The note records a PASS/FAIL (interception validates / host pins) for each of: `api.anthropic.com`, `registry.npmjs.org`, `pypi.org` + `files.pythonhosted.org`, `github.com`, `codeload.github.com`, `raw.githubusercontent.com`. (All PASS — none pinned.)
- [x] The note records whether Claude Code itself reaches its API with `HTTPS_PROXY` set and the CA trusted. (Yes — `POST /v1/messages` returned `200 OK` TLS-terminated through the proxy.)
- [x] A `Decision:` line states the shipped default `mode` for `api.anthropic.com` (`intercept` or `passthrough`) with rationale. (`intercept`.)
- [x] If the decision is `passthrough` (or any host pins on the critical path), `docs/design.md` is updated to reflect the chosen composition. (N/A — decision is `intercept`, nothing pinned; design.md S1 bullet updated to record the resolution.)

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

### 2026-06-04 — Resolved
Ran the experiments (mitmproxy 12.2.3, curl 8.7.1, Claude Code 2.1.162). All seven baseline
hosts validated through the trusted mitmproxy CA (`ssl_verify_result 0`); none pinned. A real
Claude Code inference call (`POST /v1/messages`) returned `200 OK` TLS-terminated through the
proxy — Claude Code does not pin `api.anthropic.com`. **Decision: `intercept`** (keeps the
channel audited; follows design rule). The `passthrough` path in WP-3.1/AC-0017 is therefore
**optional**, not load-bearing (resolves spec §14 risk). Findings note:
`thoughts/shared/research/2026-06-04-s1-ca-trust.md`. `docs/design.md` S1 bullet updated to
record the resolution (no fallback/composition change needed). Incidental: Claude also reaches
`mcp-proxy.anthropic.com` — flagged for the baseline-config ticket (AC-0017).

Gates released: AC-0017 (WP-3.1), AC-0026 (WP-5.1), and the global baseline default for
`api.anthropic.com` may now proceed on the `intercept` assumption.
