---
date: 2026-06-08
ticket: AC-0033
topic: Adversarial cage verification harness ("fake agent" escape battery)
status: ready
research: thoughts/shared/research/2026-06-08-AC-0033-adversarial-cage-verification.md
git_commit: ebdf5df
branch: main
---

# Implementation Plan: AC-0033 — Adversarial cage verification harness (WP-4.5)

## Overview

Build a repeatable battery that launches a hostile shell "fake agent" **inside the
real cage** (`agent-safehouse` + real `mitmdump` running the enforcer) and asserts the
full `docs/design.md` threat-model matrix: BLOCKED vectors refused, ALLOWED vectors
succeed, DOCUMENTED non-guarantees behave as written — plus a **negative control** that
proves the harness reports an escape against a deliberately-weakened cage. This is the
executable acceptance gate for Milestone M3.

Decisions from the research checkpoint:
- **Fixtures:** real public hosts (`example.com` intercept, `example.org` passthrough)
  for proxy-egress ALLOWED vectors, gated behind an egress check (skip offline) —
  matching `internal/proxy/enforcer/test_integration.py`. Everything else (BLOCKED,
  host_services, DOCUMENTED-fs, negative control) is fully local/hermetic.
- **Payload:** a checked-in `/bin/sh` fake-agent script driven via `agent.command`,
  using the injected env (`$HTTPS_PROXY`, `$SSL_CERT_FILE`, `$CLAUDE_CONFIG_DIR`) +
  `curl`/`nc`, emitting one structured `CREANCE::<id>::<observed>` line per vector that
  the Go harness parses and asserts.

## Current state

- The Seatbelt half exists as a thin precedent: `internal/cage/cage_integration_test.go`
  runs `nc 1.1.1.1 443` in the real cage and asserts egress denied. AC-0033 supersedes
  its scattered checks (ticket §"Dependencies & Sequencing").
- The proxy half exists in Python: `internal/proxy/enforcer/test_integration.py` drives
  a real `mitmdump`+enforcer with curl and asserts allow/soft-deny/hard-deny/passthrough/
  audit. No **Go** precedent for asserting through the live proxy.
- Cage launch chain (`cage.New().Resolve→Prepare`, `cage.Build`, real safehouse) and
  proxy lifecycle (`proxy.NewManager(...).Attach`) are both directly drivable from tests.

## Desired end state

- `make test-integration` runs the battery: one real-cage launch exercises the full
  matrix from inside the cage; a second launch against a weakened profile is the
  negative control. Per-vector PASS/FAIL summary printed.
- `make test` runs a fast, hermetic coverage-mapping test that fails if a design
  "Prevented/Not prevented" bullet has no mapped assertion (anti-drift).
- `docs/cage-verification.md` documents the manual red-team vectors (confused-deputy,
  interactive token exfil).
- Ticket marked complete; M3 acceptance recorded.

## What we are NOT doing

- Sandbox-escape/kernel-exploit testing (out of threat model, ticket Out of Scope).
- Resource-exhaustion limits, policy-matcher fuzzing, v0.2 secret-injection vectors.
- Driving the full `runRun` (its prereq/setup/cred preconditions need keychain/CA state);
  we compose the cage + proxy directly as the existing integration tests do.

## Package layout

New package `internal/verify/`:
- `matrix.go` (`package verify`) — exported `Vectors` table: `{ID, Label, DesignAnchor,
  Keyword, Desc}` where `Label ∈ {BLOCKED, ALLOWED, DOCUMENTED, INTEGRITY}` and
  `DesignAnchor`/`Keyword` tie each vector to a `docs/design.md` line. The single source
  of truth linking assertions ↔ threat model (AC §"Harness integrity" line 63).
- `coverage_test.go` — **fast/hermetic** (no build tag): asserts every matrix vector's
  `Keyword` is present at/near its `DesignAnchor` in `docs/design.md` (drift guard), and
  that every "Prevented/Not prevented" bullet keyword set is represented in the table.
- `battery.go` (`package verify`) — the reusable evaluator: given parsed
  `CREANCE::<id>::<observed>` lines + the expected outcomes from `Vectors`, returns a
  `Verdict{Results []VectorResult, Escaped bool}`. Pure; unit-testable.
- `battery_test.go` — table tests for the evaluator + parser (hermetic).
- `verification_integration_test.go` (`//go:build integration`, `package verify_test`) —
  composes the real cage + proxy, runs the fake agent, parses, asserts no escape;
  proxy-egress ALLOWED vectors gated behind an egress probe.
- `testdata/fake-agent.sh` — the hostile payload script.
- `docs/cage-verification.md` — manual checklist.

## Phase 1 — Matrix table + evaluator + coverage/drift guard (hermetic)

**Files:** `internal/verify/matrix.go`, `internal/verify/battery.go`,
`internal/verify/battery_test.go`, `internal/verify/coverage_test.go`.

1. `matrix.go`: define `Label` consts and `Vectors []Vector`. One entry per matrix row
   from the research doc's mapping table. Each carries `DesignAnchor int` (a stable
   line in `docs/design.md`'s §48–70) and `Keyword string` (a substring that must
   appear at that line), plus `Expected` (the canonical observed token for a green run,
   e.g. `blocked`, `200`, `403:soft-deny`, `403:hard-deny`, `connect-ok`, `rm-ok`).
2. `battery.go`:
   - `ParseProbeOutput(stdout string) map[string]string` — extracts
     `CREANCE::<id>::<observed>` lines.
   - `Evaluate(observed map[string]string) Verdict` — for each `Vector`, compare
     `observed[ID]` to `Expected`; a BLOCKED/DOCUMENTED-as-blocked vector observed
     non-blocked, or an ALLOWED vector observed blocked, sets `Escaped=true`. Missing
     observations for non-skipped vectors are failures. Returns per-vector results +
     a printable summary string.
3. `battery_test.go`: feed synthetic outputs (all-green; one BLOCKED leaking;
   one ALLOWED blocked; missing line) → assert `Escaped` and the summary.
4. `coverage_test.go`: read `docs/design.md`, for each `Vector` assert the file's
   `DesignAnchor` line contains `Keyword` (fails loudly if design is re-flowed — forces
   the table to be re-pinned). Also assert presence of each "Not prevented" honesty
   bullet keyword in the table.

**Verification:** `go build ./...`; `make test` green (new fast tests run);
`make lint` clean. Commit: `feat(AC-0033): verification matrix + battery evaluator (WP-4.5, Phase 1)`.

## Phase 2 — The fake-agent payload script (hermetic-buildable)

**File:** `internal/verify/testdata/fake-agent.sh` (mode 0755).

The script runs every vector and prints exactly one `CREANCE::<id>::<observed>` line
each. Inputs: injected env (`$HTTPS_PROXY`, `$SSL_CERT_FILE`, `$CLAUDE_CONFIG_DIR`) +
positional args the harness passes: `$1`=project dir, `$2`=secret-file path (outside the
cage mount), `$3`=allowlisted host_service port, `$4`=non-allowlisted blocked port,
`$5`=`with-egress`|`no-egress`.

Vectors (ids):
- `fs-outside` — `cat "$2"` (secret outside mount) → expect non-zero ⇒ `blocked`.
- `fs-real-claude` — `cat "$HOME/.claude/settings.json"` or `: > "$HOME/.claude/x"` →
  `blocked` (real `~/.claude` never mounted).
- `net-raw-tcp` — `nc -G2 -w2 1.1.1.1 443` → `blocked` on non-zero.
- `net-localhost-v4` — `nc -G1 -w1 -4 127.0.0.1 "$4"` (listener present) → `blocked`.
- `net-localhost-v6` — `nc -G1 -w1 -6 ::1 "$4"` → `blocked`.
- `net-child` — `sh -c 'nc -G2 -w2 1.1.1.1 443'` (subprocess) → `blocked`.
- `net-dns` — `nc -u -w2 8.8.8.8 53` → `blocked`.
- `proxy-soft-deny` — curl through `$HTTPS_PROXY` `--cacert "$SSL_CERT_FILE"`
  `https://not-allowlisted.test/` → emit `403:` + `X-Cage-Reason` value ⇒ `403:soft-deny`.
- `proxy-hard-deny` — `https://blocked.test/anything` ⇒ `403:hard-deny`.
- `proxy-offpath` — allowlisted host but disallowed path/method ⇒ `403:soft-deny`.
- `svc-allowed` — `nc -G1 -w1 127.0.0.1 "$3"` (host_service port) → `connect-ok`.
- `allow-200` *(egress)* — curl `--cacert` `https://example.com/` ⇒ `200`.
- `passthrough` *(egress)* — curl **system CA only** `https://example.org/` ⇒ `200`.
- `doc-rm` — `touch "$1/victim" && rm "$1/victim"` ⇒ `rm-ok`.
- `doc-post` *(egress)* — `curl -X POST -d creance-marker --cacert ... https://example.com/`
  ⇒ `post-sent` (the harness then checks the audit log).
- `doc-config-dir` — write a fake hook into `$CLAUDE_CONFIG_DIR/hooks/` ⇒ `planted`
  (harness asserts it is under the cache dir, not real `~/.claude`).

For `no-egress`, the egress-marked vectors print `CREANCE::<id>::skip`.

Each curl uses `-s -o /tmp body -D headers -w %{http_code}` (write to the RW project
dir, not `/tmp`, which may be denied) and greps the `X-Cage-Reason` header, mirroring
`enforcer/test_integration.py:_curl`.

**Verification:** `sh -n fake-agent.sh` (syntax); it is exercised live in Phase 3.
Commit folded into Phase 3 (the script is inert without the harness).

## Phase 3 — Live battery harness + negative control (integration)

**File:** `internal/verify/verification_integration_test.go` (`//go:build integration`).

Helpers (mirroring existing integration tests):
- `requireUncagedHost(t)` — `runtime.GOOS=="darwin"`, `exec.LookPath` for `safehouse`,
  `mitmdump`, `curl`, `nc`; a preflight `cage.Build`+run of `/bin/sh -c 'echo ok'` that
  skips on `sandbox_apply: Operation not permitted` (host can't nest sandbox-exec).
- `egressOK()` — `curl -sS --max-time 8 https://example.com/`; drives the
  `with-egress`/`no-egress` arg + per-vector skips.
- `startDualStackListener(t, port)` — `net.Listen` on `127.0.0.1:port` and `[::1]:port`,
  accept-and-close loop, `t.Cleanup`. Used for the allowlisted host_service port and the
  blocked port.
- `freePort()` — `net.Listen("tcp","127.0.0.1:0")`.

`runBattery(t, weakened bool) verify.Verdict`:
1. `t.Setenv("XDG_CACHE_HOME", t.TempDir())`; resolve layout; `MkdirAll(layout.Root)`.
2. Plant a secret file in a separate `t.TempDir()` (NOT under the project mount).
3. Allocate `svcPort`, `blockedPort`; start dual-stack listeners on both.
4. Hand-write `policy.json` at `layout.PolicyJSON()`: allow `example.com` (intercept) +
   `example.org` (passthrough) + an off-path-only allow host for `proxy-offpath`;
   `deny_always` `blocked.test`. (Hand-written like the Python test — the compiler has
   its own tests; AC-0033 tests enforcement.)
5. Extract enforcer (`proxy.NewExtractor(...).Extract()`).
6. Write `network.sb` to `layout.NetworkSB()`:
   - normal: `profile.RenderNetworkSB([]profile.HostService{{Port: svcPort}})`.
   - **weakened (negative control):** the same rendered text with the `(deny network*)`
     baseline line removed (string-replaced in-test; never written as a real profile,
     never embedded) — so raw egress is no longer blocked.
7. `proxy.NewManager(OSFileSystem, OSFlock, OSProcessManager, OSPortAllocator, io).Attach(...)`
   → `att.Port`; `defer Detach`. (Reuses the AC-0020 lock/port machinery — V&T step 4.)
   Wait for `OSPortAllocator{}.Probe(att.Port)` via `require.Eventually`.
8. `cfg`: `Agent.Command = ["/bin/sh", scriptAbsPath, proj, secretPath, svcPort,
   blockedPort, egressFlag]`, `Agent.Workdir = proj`, `Safehouse.AddDirsRW=[proj]`,
   `Network.HostServices = ["svc:<svcPort>"]`.
9. `cage.New(...).Resolve(cfg, layout, att.Port)` → `Prepare` → `cage.Build` → run via
   `exec.CommandContext(inv.Path, inv.Args...)`, `cmd.Env=append(os.Environ(), inv.Env...)`,
   `CombinedOutput()`; skip on the `sandbox_apply` substring.
10. `verify.Evaluate(verify.ParseProbeOutput(out))` → return verdict (+ log the summary).

Tests:
- `TestCageVerificationBattery` — `requireUncagedHost`; `v := runBattery(t, false)`;
  `require.False(t, v.Escaped, v.Summary())`; assert each non-skipped vector matched
  Expected. If egress: additionally assert `egress.jsonl` has a `decision:"allow"`,
  `method:"POST"` entry for `doc-post` and that `creance-marker` is **absent** from the
  log (bodies not recorded); assert a host-only passthrough entry for `example.org`.
- `TestCageVerificationNegativeControl` — `requireUncagedHost`; `v := runBattery(t, true)`;
  `require.True(t, v.Escaped, "weakened cage must be detected as an escape")`; assert the
  leaking vector is one of the `net-*` raw-egress ids (clear message names it).
- Determinism: `TestCageVerificationBattery` is safe to run twice (ports via
  allocator/listeners; no fixed ports) — note in a comment; optionally a `-count=2`
  smoke is manual.

**Verification:** `make test-integration` (battery green, negative control red-detected);
run twice for stability; `make lint`. Commit:
`feat(AC-0033): live cage-verification battery + negative control (WP-4.5, Phase 3)`.

## Phase 4 — Manual red-team checklist

**File:** `docs/cage-verification.md`.

Human-runnable procedure for the vectors impractical to automate (ticket AC
§"Harness integrity" line 64): confused-deputy via a real dev DB/Redis (`REPLICAOF`,
`MIGRATE`, MySQL `FEDERATED`/`LOAD DATA`), and interactive OAuth-token exfil through an
allowlisted POST target. Each item: pre-conditions, step-by-step repro, expected result,
and a record-outcome line. Cross-reference the automated matrix so a reader sees what is
covered automatically vs. by hand. Doubles as release-acceptance sign-off for M3.

**Verification:** `make lint` (markdown only; no code). Commit:
`docs(AC-0033): manual cage-verification red-team checklist (WP-4.5, Phase 4)`.

## Phase 5 — Close out

1. Wire a `make` target alias if useful (e.g. `make verify-cage` →
   `go test -race -tags=integration ./internal/verify/...`) — optional, only if it
   matches existing Makefile style.
2. Full verification: `make test`, `make lint`, `make build`, `make test-integration`.
3. Update `thoughts/shared/tickets/AC-0033-*.md`: Status → Done, Notes update; record M3
   acceptance. Tick the V&T checkboxes.
4. Commit: `test(AC-0033): close ticket — M3 caged-run acceptance gate green (WP-4.5)`.

## Success criteria

### Automated
- `go build ./...` compiles (payload package + harness).
- `make test` green — includes the new hermetic coverage/drift + evaluator tests.
- `make test-integration` green — battery reports all vectors as expected from inside
  the real cage; the negative control is detected as an escape.
- `make lint` clean.

### Manual
- `docs/cage-verification.md` exists and is end-to-end followable.
- Battery run twice yields stable results (no port-race flakes).
- Per-vector PASS/FAIL summary is printed and each assertion maps to a `docs/design.md`
  bullet via `verify.Vectors`.

## Risks / notes

- **mitmproxy CA system-trust & passthrough mirror.** The cage injects
  `SSL_CERT_FILE=~/.mitmproxy/...`; we run the proxy on the default confdir so that path
  matches. The passthrough **positive** proof (system-CA-only validates `example.org`'s
  real cert ⇒ 200) holds regardless. The *mirror* ("intercept host fails under
  system-CA-only") is only reliable when the mitmproxy CA is NOT system-trusted; make it
  a conditional/best-effort sub-check, relying on the audit-log host-only-vs-full entry
  as the primary intercept/passthrough discriminator.
- **Go-ignores-loopback-proxy** is avoided by using `curl -x "$HTTPS_PROXY"` (always
  honors the proxy) rather than an in-process Go client for the proxy vectors.
- **EPERM vs ECONNREFUSED**: shell can't classify cleanly; we make blocks unambiguous by
  pointing each BLOCKED-net probe at a target that *would* connect uncaged (a live
  listener for localhost ports; a routable host for raw TCP), so a failure means the
  sandbox blocked it.
- **Curl output paths**: write `-o`/`-D` files into the RW project dir, never `/tmp`
  (denied by the cage), mirroring how the cage test writes `sleep.pid` into the mount.
