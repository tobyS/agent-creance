# AC-0017: enforcer.py decision engine (WP-3.1)

**Status:** Open
**Estimated Complexity:** Large
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-3.1 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** AC-0010 (WP-2.1, decision-vector corpus), AC-0013 (WP-2.4, policy.json)
**Spike gate:** **S1 (AC-0001)** — passthrough default for `api.anthropic.com` depends on it
**Cross-cutting:** C1 (must pass the shared decision-vector corpus from the Python side)

## Problem Statement

The mitmproxy addon is the runtime enforcer. It must read `policy.json`, return the three response types (allow / soft-deny 403 / hard-deny 403) with the structured bodies and `X-Cage-Reason` header the agent's skill understands, implement per-host `mode` (including `passthrough` CONNECT tunneling with no TLS termination), and hot-reload on `policy.json` mtime change — all while exactly matching the Go matcher.

## Desired Outcome

`internal/proxy/enforcer/enforcer.py` enforces the compiled policy with correct three-type responses, passthrough handling, and hot reload, provably consistent with AC-0010 via the shared corpus.

## User Stories / Use Cases

- As a caged agent, I want a structured soft-deny/hard-deny body so that I can route around or escalate appropriately.
- As an operator, I want `allow` from another terminal to take effect within milliseconds so that I don't restart the cage.

## Acceptance Criteria

- [ ] Loads `policy.json`; allow → forwards upstream; soft-deny → 403 + `X-Cage-Reason: soft-deny` + the documented JSON body; hard-deny → 403 + `X-Cage-Reason: hard-deny` + reason body.
- [ ] Per-host `mode`: `intercept` matches host+path+method; `passthrough` tunnels via CONNECT without terminating TLS, host-allow only, with `deny_always`-by-host still refused at CONNECT.
- [ ] Polls `policy.json` mtime and reloads on change.
- [ ] Passes 100% of `internal/policy/testdata/decision-vectors/` from the Python side.
- [ ] The three 403 JSON bodies match golden fixtures byte-for-byte (shared with the Go side where practical).

## Verification & Test Steps

1. Python addon unit tests run the shared corpus: a test harness loads every `decision-vectors/*.json` and asserts the addon's decision/mode equals expected. Run via the project's chosen Python test runner (documented in the Makefile target, e.g. `make test-enforcer`).
2. Parity check: the same corpus passes in both `go test ./internal/policy` (AC-0010) and the Python suite — a CI step asserts both consume the identical files.
3. Golden bodies: `policy explain`-equivalent 403 bodies compared to `testdata/` golden JSON.
4. Integration (`make test-integration`, gated by S1): start a real mitmproxy with the addon + a fixture policy; assert via `curl -x` that an allowed host returns upstream, a soft-denied host returns 403 + `X-Cage-Reason: soft-deny`, a hard-denied host returns 403 + reason, and a passthrough host tunnels (TLS validates against the *real* upstream cert, not mitmproxy's CA).
5. Hot reload: modify the policy file mid-run; assert a previously-denied host becomes allowed within ~1s without restart.
6. **Systematic verification:** the host-side response-type probes here are exercised from *inside the real cage* by the full adversarial battery in **AC-0033** (the end-to-end isolation gate).

## Out of Scope

- Audit-log writing (AC-0018, same file but separate ticket).
- Embedding/extraction into the Go binary (AC-0019).
- Lifecycle/lock management (AC-0020).

## Dependencies & Sequencing

Phase 3. Gated by S1. On the critical path to M2/M3. Reuses AC-0010's corpus — do not fork the logic.

## Questions for Research/Planning

- [ ] mitmproxy addon API for CONNECT passthrough (`next_layer`/`tls_passthrough`) — exact hook.
- [ ] How to run Python addon tests in this Go-centric repo (pytest target + CI wiring).

## References

- `docs/design.md` — "Network refusal handling", "Per-host enforcement modes".
- Spec WP-3.1, cross-cutting C1.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification. Python side of the C1 parity contract.
