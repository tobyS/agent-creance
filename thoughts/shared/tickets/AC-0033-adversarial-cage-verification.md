# AC-0033: Adversarial cage verification harness ("fake agent" escape battery) (WP-4.5)

**Status:** Done
**Estimated Complexity:** Extra Large
**Created:** 2026-06-04
**Updated:** 2026-06-08
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
- [x] Read a host file outside `./` (e.g. `~/.ssh/id_rsa`, `~/.aws/credentials`) → permission denied. *(`fs-outside`)*
- [x] Read/write the **real** `~/.claude` (settings, hooks, skills) → denied / not writable. *(`fs-real-claude`)*
- [x] Raw outbound TCP to an arbitrary host bypassing the proxy → blocked. *(`net-raw-tcp`)*
- [x] Connect to a **non-allowlisted** localhost port over **both** `127.0.0.1` and `::1` → refused. *(`net-localhost-v4` / `net-localhost-v6`)*
- [x] A child process spawned by the payload inherits the same restrictions → still blocked. *(`net-child`)*

**BLOCKED — proxy (assert the structured refusal):**
- [x] Egress via the proxy to a non-allowlisted host → 403 `X-Cage-Reason: soft-deny`. *(`proxy-soft-deny`)*
- [x] Egress to a `deny_always` host → 403 `X-Cage-Reason: hard-deny` + reason. *(`proxy-hard-deny`)*
- [x] Disallowed path/method on an intercepted allowlisted host → soft-deny. *(`proxy-offpath`)*
- [x] DNS query to an arbitrary external nameserver directly → blocked (resolution only via the proxy). *(`net-dns`)*

**ALLOWED — false-negative guard (assert success):**
- [x] Egress via the proxy to an allowlisted host/path/method → 200 upstream. *(`allow-200`)*
- [~] A generated-rule host (a dep homepage/repo from a generator) → allowed. *(Not a distinct cage vector: at the cage/proxy layer a generated allow rule is byte-identical to an explicit one in `policy.json` — exercised by `allow-200`. The generator that produces such rules has its own live integration test, `internal/generator/live_integration_test.go`.)*
- [x] A `host_services` entry at `127.0.0.1:<port>` → connects. *(`svc-allowed`)*
- [x] A `mode: passthrough` host → tunnels and validates against the **real** upstream certificate (not mitmproxy's CA). *(`passthrough`)*

**DOCUMENTED — honesty assertions (assert the design's stated non-guarantees):**
- [x] `rm`/write within `./` succeeds → the cage does **not** block it. *(`doc-rm`)*
- [x] A `POST` with an agent-controlled body to an **allowlisted** host succeeds → goes through, and is recorded in the audit log (body NOT recorded). *(`doc-post` + `assertAuditedPOST`)*
- [x] The redirected ephemeral `CLAUDE_CONFIG_DIR` is writable but a plant there does **not** appear in the real `~/.claude`. *(`doc-config-dir` + structural Go assertion)*

**Harness integrity:**
- [x] **Negative control:** the battery against a cage with the deny-baseline removed **reports an escape and fails** — proving it can detect a broken cage. *(`TestCageVerificationNegativeControl`)*
- [x] Every assertion maps to a named bullet in `docs/design.md` via `internal/verify.Vectors`, with a fast drift guard (`coverage_test.go`) that fails if a mapped keyword leaves the design's threat-model section.
- [x] A checked-in human red-team checklist documents the non-automated vectors. *(`docs/cage-verification.md`)*

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

### 2026-06-08 — Implemented (M3 gate green)
Research → plan → implement via `/tce:work`. New package `internal/verify`:
- `matrix.go` — `Vectors`, the single source of truth mapping 16 probes (BLOCKED / ALLOWED / DOCUMENTED) to `docs/design.md` bullets.
- `battery.go` — `ParseProbeOutput` + `Evaluate` → a `Verdict` distinguishing a security **Escape** (a BLOCKED vector that leaked) from a plain **Failure** (over-blocking / missing line), with a per-vector `Summary`.
- `coverage_test.go` — fast drift guard (runs in `make test`): fails if a mapped keyword leaves the design's threat-model section.
- `testdata/fake-agent.sh` — the hostile `/bin/sh` payload, run as `agent.command` inside the cage.
- `verification_integration_test.go` — composes a real `mitmdump`+enforcer (via `proxy.Manager.Attach`, reusing the AC-0020 lock/port machinery) with a real `agent-safehouse` cage; `TestCageVerificationBattery` (no escape, no failure) + `TestCageVerificationNegativeControl` (deny-baseline stripped → escape detected). Verified on an unsandboxed macOS host: all 16 vectors green, negative control detects the raw-egress escape, stable across `-count=2`.

**Checkpoint decisions:** real public hosts (`example.com` intercept / `example.org` passthrough) for proxy-egress ALLOWED vectors with skip-on-offline (matches `enforcer/test_integration.py`); shell+curl fake-agent (not a Go binary).

**Findings surfaced by the harness (filed as AC-0034 and AC-0035):**
1. *(→ AC-0034)* The injected CA env vars (`SSL_CERT_FILE` / `NODE_EXTRA_CA_CERTS` / `REQUESTS_CA_BUNDLE` / `GIT_SSL_CAINFO`) point at `~/.mitmproxy/mitmproxy-ca-cert.pem`, which is **not readable inside the cage** (safehouse denies `~/.mitmproxy`). So those belt-and-suspenders CA files are non-functional in-cage; CA trust reaches the cage only via the keychain (`trustd`), making `agent-creance setup` a hard prerequisite. The battery works around it with an in-mount `--cacert` copy.
2. *(→ AC-0035)* `CLAUDE_CONFIG_DIR` resolves under `~/.cache/agent-creance`, but safehouse's base policy grants RW only to `/tmp`, `$TMPDIR`, and specific toolchain dirs — **not** a generic `~/.cache`. The battery passes because `XDG_CACHE_HOME=t.TempDir()` lands under `$TMPDIR` (writable); with the real `~/.cache` location the redirected config dir may not be writable in-cage. Worth confirming a real `run` mounts it (or relies on `$TMPDIR`).

Both findings are recorded in `docs/cage-verification.md` ("Known limitations") as host checks for the manual pass.
