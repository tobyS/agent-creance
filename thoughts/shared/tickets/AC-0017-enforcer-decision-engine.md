# AC-0017: enforcer.py decision engine (WP-3.1)

**Status:** Done
**Estimated Complexity:** Large
**Created:** 2026-06-04
**Updated:** 2026-06-06
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

- [x] Loads `policy.json`; allow → forwards upstream; soft-deny → 403 + `X-Cage-Reason: soft-deny` + the documented JSON body; hard-deny → 403 + `X-Cage-Reason: hard-deny` + reason body.
- [x] Per-host `mode`: `intercept` matches host+path+method; `passthrough` tunnels via CONNECT without terminating TLS, host-allow only, with `deny_always`-by-host still refused at CONNECT.
- [x] Polls `policy.json` mtime and reloads on change.
- [x] Passes 100% of `internal/policy/testdata/decision-vectors/` from the Python side.
- [x] The three 403 JSON bodies match golden fixtures byte-for-byte (shared with the Go side where practical).

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

- [x] mitmproxy addon API for CONNECT passthrough (`next_layer`/`tls_passthrough`) — exact hook. → `tls_clienthello` + `data.ignore_connection = True` (the `next_layer`/`TCPLayer` pattern is deprecated). Deny-at-CONNECT via `http_connect` returning a non-2xx `flow.response`.
- [x] How to run Python addon tests in this Go-centric repo (pytest target + CI wiring). → `make test-enforcer` runs pytest in a repo-local venv (`.venv-enforcer`, pinned mitmproxy); kept out of the fast `make test` + pre-commit. Real CI deferred; corpus parity guarded by a Go anti-fork test instead.

## References

- `docs/design.md` — "Network refusal handling", "Per-host enforcement modes".
- Spec WP-3.1, cross-cutting C1.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification. Python side of the C1 parity contract.

### 2026-06-06 — Done
Implemented `internal/proxy/enforcer/` (research + plan in `thoughts/shared/{research,plans}/2026-06-06-AC-0017-*`):

- `policy.py` — faithful port of the Go matcher (`internal/policy`); `responses.py` —
  the three wire 403 bodies + `X-Cage-Reason`; `enforcer.py` — the mitmproxy addon
  (`request` / `http_connect` / `tls_clienthello` hooks + mtime hot reload).
- C1 parity: `test_vectors.py` replays the shared corpus from the Python side (18/18);
  a Go anti-fork guard (`internal/policy/corpus_parity_test.go`) keeps the corpus
  single-sourced. Golden 403 bodies in `internal/proxy/enforcer/testdata/`.
- Tooling: `make test-enforcer` (pytest in a repo-local venv) + `make
  test-enforcer-integration` (folded into `make test-integration`); both kept out of
  the Go-only fast `make test` + pre-commit.

Decisions / deviations (see plan):
- **Soft-deny `how_to_proceed` copy** reworded (no "route around" framing) per the
  planning checkpoint; `docs/design.md` updated to match the golden body.
- **mitmproxy pin bumped 12.0.1 → 12.2.3** (`internal/buildinfo` + `requirements.txt`):
  12.0.1 caps `Brotli<=1.1.0`, which has no Python-3.14 wheel; 12.2.3 (the version S1
  validated against) installs cleanly. Updated `doctor_healthy.txtar` to match.
- **Egress-gate hardening:** the addon sets `connection_strategy=lazy` +
  `upstream_cert=False`, so the proxy never connects to (or sniffs the cert of) a
  denied host — the 403 is synthesized with zero upstream contact.

Follow-ups (out of scope here):
- Real CI workflow is deferred — parity is currently guarded by the Go anti-fork test
  + the explicit Make target, not a CI step.
- **AC-0019 (embed/extract)** will `go:embed` the `internal/proxy/enforcer/`
  *directory* (deliberate multi-file layout: pure logic kept import-clean of mitmproxy
  so the corpus/golden suites run without it).
- **AC-0020 (lifecycle)** supplies the `creance_policy` option at launch; the secure
  proxy options above are set by the addon itself, so they need not be launch flags.
- Incidental from S1: Claude also reaches `mcp-proxy.anthropic.com` — flagged for the
  baseline-config ticket.
