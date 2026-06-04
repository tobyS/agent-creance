# AC-0033: Adversarial cage verification harness ("fake agent" escape battery) (WP-4.5)

**Status:** Open
**Estimated Complexity:** Extra Large
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** new WP-4.5 (verification capstone) — derived from `docs/design.md` "What the cage prevents — and what it doesn't"
**Depends on:** AC-0014 (.sb), AC-0017 (enforcer), AC-0020 (lifecycle), AC-0023 (Safehouse invocation), AC-0025 (run)
**Spike gate:** inherits S1–S5 via dependencies
**Kind:** End-to-end security verification (the executable form of the threat model)

## Problem Statement

The whole value proposition of agent-creance is that the cage *actually* confines a misbehaving agent. Today that claim rests on the design's prose threat model plus a handful of thin, scattered assertions in other tickets — there is no single, repeatable test that runs a hostile payload **inside the real cage** and proves each restriction holds. Without it, a regression (a wrong `.sb` rule, a passthrough misconfig, a Safehouse flag drift) could silently turn the cage into security theater and every other ticket would still go green.

This ticket turns `docs/design.md` → "What the cage prevents — and what it doesn't" into an executable, agent- and human-runnable battery: a "fake agent" that tries to escape, asserting that prevented things are blocked, allowed things still work, and documented-not-prevented things behave exactly as documented.

## Desired Outcome

A reusable verification harness with two faces:

1. **Automated** (`//go:build integration`): a Go integration test launches the real cage (`run`) with a hostile payload as the agent command; the payload executes the escape battery from inside the cage, emits a structured result, and the test asserts every expected outcome — including a **negative control** that fails the battery against a deliberately-weakened cage.
2. **Manual** (checked-in checklist): a human red-team procedure for vectors that are impractical to automate (confused-deputy via a real dev DB, interactive token-exfil demonstration).

This harness is the **acceptance gate for Milestone M3** — "caged run" isn't done until the battery is green.

## User Stories / Use Cases

- As the maintainer, I want one command that proves the cage confines a hostile agent so that "it's isolated" is a tested fact, not a hope.
- As a reviewer, I want the threat model to be executable so that a regression in any isolation rule fails CI loudly.
- As a security-minded operator, I want a documented manual red-team checklist so that I can re-verify on my own machine before trusting the cage.

## Acceptance Criteria

The harness contains one assertion per bullet of the design's threat model. Each is labeled BLOCKED (must be refused), ALLOWED (must succeed — false-negative guard), or DOCUMENTED (must behave as the "Not prevented" section states).

**BLOCKED — kernel/Seatbelt (assert refused from inside the cage):**
- [ ] Read a host file outside `./` (e.g. `~/.ssh/id_rsa`, `~/.aws/credentials`) → permission denied.
- [ ] Read/write the **real** `~/.claude` (settings, hooks, skills) → denied / not writable.
- [ ] Raw outbound TCP to an arbitrary host bypassing the proxy (e.g. `nc`/`curl` direct to `example.com:443`, no proxy) → blocked.
- [ ] Connect to a **non-allowlisted** localhost port over **both** `127.0.0.1` and `::1` → refused.
- [ ] A child process spawned by the payload inherits the same restrictions (re-run a blocked vector from a subprocess) → still blocked.

**BLOCKED — proxy (assert the structured refusal):**
- [ ] Egress via the proxy to a non-allowlisted host → 403 `X-Cage-Reason: soft-deny`.
- [ ] Egress to a `deny_always` host → 403 `X-Cage-Reason: hard-deny` + reason.
- [ ] Disallowed path/method on an intercepted allowlisted host → soft-deny.
- [ ] DNS query to an arbitrary external nameserver directly → blocked (resolution only via the proxy).

**ALLOWED — false-negative guard (assert success):**
- [ ] Egress via the proxy to an allowlisted host/path/method → 200 upstream.
- [ ] A generated-rule host (a dep homepage/repo from a generator) → allowed.
- [ ] A `host_services` entry at `127.0.0.1:<port>` → connects.
- [ ] A `mode: passthrough` host → tunnels and validates against the **real** upstream certificate (not mitmproxy's CA).

**DOCUMENTED — honesty assertions (assert the design's stated non-guarantees):**
- [ ] `rm`/write within `./` succeeds (project files are damageable by design) → the cage does **not** block it.
- [ ] A `POST` with an agent-controlled body to an **allowlisted** host succeeds (residual exfil surface the allowlist narrows but does not eliminate) → goes through, and is recorded in the audit log.
- [ ] The redirected ephemeral `CLAUDE_CONFIG_DIR` is writable but a hook/skill planted there does **not** appear in the real `~/.claude` after the session (config-persistence vector closed).

**Harness integrity:**
- [ ] **Negative control:** running the battery against a cage with the network deny-baseline removed (or a host-service rule widened) makes the battery **report an escape and fail** — proving the harness can detect a broken cage, not just rubber-stamp.
- [ ] Every assertion maps to a named bullet in `docs/design.md` (a comment/table linking assertion → design line), so the threat model and the test never drift.
- [ ] A checked-in human red-team checklist documents the vectors not automated (confused-deputy via a real DB/Redis `REPLICAOF`, interactive token exfil) with step-by-step repro + expected result.

## Verification & Test Steps

1. `go build ./...` → compiles (payload + harness).
2. Automated battery: `make test-integration` (the harness lives under a build tag) launches the real cage with the hostile payload and asserts the full BLOCKED/ALLOWED/DOCUMENTED matrix above. Expected: all assertions hold; the test prints a per-vector PASS/FAIL summary.
3. Negative control: a sub-test runs the same battery against a deliberately-weakened profile fixture and asserts the harness **fails** (i.e. detects the escape). Expected: the weakened-cage run is red; a clear message names the vector that leaked.
4. Determinism: run the battery twice; results are stable (no flakes from port races — reuse the lock/port machinery from AC-0020).
5. Threat-model coverage check: a test (or `make` target) asserts each design "Prevented/Not prevented" bullet has a corresponding labeled assertion (fail if a bullet is unmapped).
6. Manual checklist: `docs/cage-verification.md` exists; a human can follow it end-to-end on a real machine and record outcomes.
7. `make lint` → clean.

## Out of Scope

- Sandbox-escape / kernel-exploit testing (`sandbox-exec` is not a VM — explicitly out of the threat model; documented, not tested).
- Resource-exhaustion limits (forkbombs, disk fill) — macOS Seatbelt doesn't impose them; documented non-goal.
- Fuzzing the policy matcher (that parity is covered by C1 / AC-0010 / AC-0017 decision vectors).
- v0.2 secret-injection vectors.

## Dependencies & Sequencing

New Phase-4 capstone (WP-4.5). Requires a runnable cage, so it lands after AC-0025 and gates M3 sign-off. Several lower tickets should *contribute* fixtures/probes to it rather than duplicating thin checks:
- AC-0014 localhost-port probe, AC-0017 response-type probes, AC-0023/AC-0025 egress-blocked checks → folded into / superseded by this battery.

## Questions for Research/Planning

- [ ] Payload form: a tiny checked-in Go binary built for the battery, a shell script, or a JSON-driven probe runner the Go harness interprets?
- [ ] How to provision the test fixtures the ALLOWED vectors need (a local allowlisted HTTP server, a fake `host_service` listener, a passthrough upstream with a real cert)?
- [ ] Where the manual checklist lives (`docs/cage-verification.md`) and whether it doubles as release-acceptance sign-off.
- [ ] Can the negative-control fixtures live alongside `internal/profile` golden data without risk of being shipped as a real profile?

## References

- `docs/design.md` — **"What the cage prevents — and what it doesn't"** (the source matrix), "Network refusal handling", "Per-host enforcement modes", "The proxy and the credential story".
- Spec `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md` — Milestone M3; this ticket is its acceptance gate.
- Related: AC-0014, AC-0017, AC-0020, AC-0023, AC-0025; spikes AC-0001/0003/0005.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created after review feedback: the per-ticket integration steps verified isolation only piecemeal and host-side. This capstone runs a hostile "fake agent" *inside* the real cage across the full threat-model matrix, with a negative control so the harness can actually fail. Gates Milestone M3.
