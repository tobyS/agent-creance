---
date: 2026-06-22T00:00:00Z
planner: Claude
git_commit: f8d860a
branch: main
repository: agent-creance
topic: "AC-0058 — Close three critical egress-boundary defeats: implementation plan"
tags: [plan, security, seatbelt, mitmproxy, enforcer, policy, host-normalization, parity]
status: ready
ticket: AC-0058
research: thoughts/shared/research/2026-06-22-AC-0058-egress-boundary-defeats.md
---

# AC-0058 — Close three critical egress-boundary defeats: Implementation Plan

## Overview

Close the three Critical egress-boundary defeats from the 2026-06-22 review, each
pinned by adversarial tests, as three independent phases:

- **Phase A — SBPL injection (F1, F15, F18):** stop the `host_services` label (and any
  untrusted egress-rule value) from injecting a live SBPL form, and validate egress-rule
  hosts/methods at config load for both hand-authored and generator-emitted rules.
- **Phase B — Enforcer fail-closed (F2, F6):** any exception in the per-request decision
  hard-denies; an initial `policy.json` load failure becomes a hard, visible startup
  failure to the Go launcher; a malformed hot-reload provably keeps the last-good policy.
- **Phase C — Host normalization & parity (F3, F4):** canonicalize the request host
  inside the matcher entry points in both languages; add a Go `HostDisposition` and drive
  `host_disposition` from the shared decision-vector corpus.

The phases are independent (separate subsystems) and committed separately. Order A → B → C.

## Decisions (resolved at the checkpoint)

1. **Method validation:** uppercase **and** a known-verb allowlist (typos and unknown
   verbs rejected at config load). The verb set is a package-level var so it is trivially
   extensible.
2. **B2 readiness:** a full Go-side readiness wait — `Attach` polls after `Spawn` until
   the port listens (ready) or the process died / a timeout elapses (fail with a clear
   error). New `ProcessManager` capability + fakes.
3. **Canonicalization:** inside the matcher entry points (`Decide`/`decide`,
   `HostDisposition`/`host_disposition`) in both languages, so the shared corpus proves
   parity.
4. **Sequencing:** three independent phases, A → B → C.

## Current State

- `RenderNetworkSB` writes `svc.Label` raw via `WriteString` (`internal/profile/profile.go:91`);
  `parseHostService` checks only label non-emptiness (`internal/config/validate.go:103`).
- `validateRules` (`validate.go:22-45`) checks host **presence** only and never validates
  methods; generator-emitted rules bypass `validateRules` (`internal/policy/compile/compile.go:509-533`).
- The enforcer `request` hook has no `try/except` (`enforcer.py:198-230`); `_load`
  swallows load errors (`enforcer.py:137-147`); `Spawn` discards stderr and `Attach` does
  no readiness check (`processmanager.go:45-60`, `lifecycle.go:122,138`).
- `matchHost` does lowercase + glob/exact with no trailing-dot/port handling
  (`glob.go:10-24`); no Go `HostDisposition`; the 18-vector corpus drives only
  `decide`/`Decide`.

See the research document for full verification and line-level references.

## Desired End State

All three Acceptance-Criteria groups (A/B/C) in the ticket are satisfied and pinned by
tests; `make test`, `make test-enforcer`, `make lint` are green; `make build` rebuilt at
the end. No change to what the audit log records (AC-0060's scope); no proxy
refcount/lock changes (AC-0061's scope).

---

## Phase A — SBPL injection + host/method validation (F1, F15, F18)

### A.1 Reject control characters in `host_services` labels (parse-time)

- `internal/config/validate.go` — in `parseHostService`, after the empty-label check,
  reject any label containing a control character (`r < 0x20 || r == 0x7f`) with
  `fmt.Errorf("host_services entry %q has a control character in its label", s)`. A
  control-char **denylist** (not a charset allowlist) — descriptive labels with spaces,
  `/`, or `:` (before the port) remain valid.

### A.2 Sanitize the label at render (defense in depth)

- `internal/profile/profile.go` — in `RenderNetworkSB`, before writing the label, replace
  any control character so it can never terminate the comment line (e.g. a small
  `sanitizeLabel(string) string` that maps `r < 0x20 || r == 0x7f` to a space or drops
  it). The label can only ever extend the `;; ` comment.

### A.3 Validate egress-rule hosts and methods (F18), covering generator rules

- `internal/config/validate.go` — add a `validateRule(r Rule, ref string, verr *...)`
  (or extend `validateRules`) that:
  - rejects a host containing a control character or whitespace, and an obviously
    malformed host (empty label between dots, scheme like `http://`, embedded `/`); allow
    `*` and `*.suffix` globs.
  - rejects methods that are empty, not uppercase, or not in a package-level
    `knownMethods` set (`GET HEAD POST PUT PATCH DELETE OPTIONS CONNECT TRACE`). Use the
    accumulating `verr.add(...)` style (`(want one of %v)`).
- `internal/policy/compile/compile.go` — run the same host/method validation over
  **generator-emitted** rules in the generator path (`runGenerators`/`buildRuleSet`), so a
  hostile generated rule is caught at compile time rather than silently never-matching.
  (Confirm the generator rule type during implementation; reuse the `config` validator.)

### A.4 Tests (Phase A)

- `internal/profile/profile_test.go`:
  - adversarial table test feeding labels containing `"`, `\`, `\n`, `\r`, `;;`, `(`,
    `)` and asserting exactly one rule line per service and **zero** `(allow`/`(deny`
    forms beyond the intended baseline + per-port rule (mirror
    `TestRenderNetworkSB_NoForbiddenLiterals` / `assertNoForbiddenLiterals`).
  - a test that pins `%q` on `RenderCAReadFragment` / `RenderConfigReadOnlyFragment` so a
    `%s` refactor fails (feed a path containing `"` and assert it is escaped).
  - `make golden` if any golden `.sb` changes; review the diff (expect none for benign
    inputs).
- `internal/config/validate_test.go`:
  - `TestValidate` cases: control-char label rejected; lowercase method rejected; unknown
    method (`FOOBAR`) rejected; malformed host (`http://x/y`, `" "`) rejected; valid glob
    hosts and uppercase known methods accepted.
- `internal/policy/compile` test: a generator-emitted rule with a bad host/method fails
  compilation with a clear error.

### A.5 Success criteria (Phase A)

#### Automated
- [ ] `make test` green (new profile + config + compile tests pass).
- [ ] `make lint` green.
- [ ] `make golden` produces no unexpected `.sb` diff.

#### Manual
- [ ] A `.agent-creance.yaml` with `host_services: ["x\n(allow network*):3306"]` is
      rejected at load with a clear error (and, if forced past parse, renders no live
      SBPL form).

---

## Phase B — Enforcer fail-closed (F2, F6)

### B.1 Hard-deny on any exception in the decision hooks

- `internal/proxy/enforcer/enforcer.py`:
  - Wrap the body of `request` in `try/except Exception`; on exception set
    `flow.response = _make_response(responses.hard_deny(flow.request.pretty_url,
    "internal enforcer error"))`. Guard the `pretty_url` access itself (fall back to a
    constant URL if even that raises) so the except branch cannot re-raise.
  - Wrap `http_connect`: on exception set a hard-deny `flow.response` (refuse the CONNECT).
  - Wrap `tls_clienthello`: on exception **return without** setting
    `data.ignore_connection` — TLS then terminates and the now-hard-closed `request` path
    decides.

### B.2 Surface an initial policy-load failure to the Go launcher

- `internal/proxy/enforcer/enforcer.py`:
  - Distinguish the **initial** load from a hot-reload (e.g. `_load(initial: bool)` or a
    one-shot flag). On an **initial** failure (missing/corrupt `policy.json`), emit
    `logging.error("agent-creance enforcer: failed to load initial policy …")` so
    mitmproxy's `ErrorCheck` exits `mitmdump` non-zero, while still leaving the
    empty-ruleset (soft-deny-all) posture in place. A **hot-reload** failure must **not**
    error/exit (keep last-good — see B.3).
- `internal/sysdep/processmanager.go` + `internal/sysdep/sysdeptest/processmanager.go`:
  - Add the capability `Attach` needs to detect early exit. Minimal shape: a readiness
    wait built from existing `Alive(pid)` + `PortAllocator.Probe(port)` — no stderr
    capture required, since a non-zero exit makes the process not-`Alive` and the port
    never opens. If a new method is needed, add it to the `ProcessManager` interface with
    a `FakeProcessManager` scripted field and the `var _ sysdep.ProcessManager` assertion.
- `internal/proxy/lifecycle.go`:
  - After `m.proc.Spawn(...)` in `Attach`, poll (bounded timeout, short interval) until
    `m.ports.Probe(port)` succeeds (ready) **or** `!m.proc.Alive(pid)` (the enforcer
    exited — return a clear `fmt.Errorf` startup error) **or** the timeout elapses
    (return a timeout error). On failure, do not record a live proxy.
- `internal/cli/run.go`:
  - Surface the startup error to the user instead of printing "ready".

### B.3 Prove malformed hot-reload keeps last-good

- No code change required (the assignment never completes inside the `try`), but confirm
  the initial-vs-reload split in B.2 does not regress it. Optionally avoid retry-log spam.

### B.4 Tests (Phase B)

- `internal/proxy/enforcer/test_enforcer.py` (run via `make test-enforcer`):
  - B1: monkeypatch `policy.decide` to raise → `addon.request(flow)` sets a 471 hard-deny
    (`flow.response` not None, `X-Cage-Reason: hard-deny`); a second case driving the
    `deny_always[index]` mismatch raise site asserts the same.
  - B2: an initial missing/corrupt `policy.json` causes `configure`/`running` to log at
    ERROR (assert via `caplog`), distinct from a hot-reload.
  - B3: fork `test_hot_reload_picks_up_new_allow` — write good policy, capture a decision,
    overwrite with malformed JSON, bump mtime via `os.utime`, call `_maybe_reload()`,
    assert the decision is unchanged; then write a valid policy and assert it recovers.
- `internal/proxy/lifecycle_test.go`:
  - readiness success (port listening → `Attach` succeeds);
  - readiness failure (process not alive after spawn → `Attach` returns a clear error and
    records no live proxy), using `FakeProcessManager`/`FakePortAllocator`.

### B.5 Success criteria (Phase B)

#### Automated
- [ ] `make test-enforcer` green (B1/B2/B3 pytest cases).
- [ ] `make test` green (lifecycle readiness tests).
- [ ] `make lint` green.

#### Manual
- [ ] Pointing the launcher at a corrupt `policy.json` produces a visible startup failure
      (non-zero), not a silent run on an empty ruleset.

---

## Phase C — Host normalization & CONNECT-stage parity (F3, F4)

### C.1 Canonicalize the request host inside the matcher entry points

- `internal/policy/glob.go` (Go) and `internal/proxy/enforcer/policy.py` (Python): add an
  idempotent `canonicalHost`/`canonical_host` — lowercase, strip a single trailing `.`,
  strip a trailing `:port`.
- Apply it to the **request** host at the entry of `Decide`/`decide` and (C.3)
  `HostDisposition`/`host_disposition`. Rule **patterns** are not canonicalized at match
  time (they are config-validated in Phase A); `matchHost` keeps its lowercase compare.
- Add table tests for `canonicalHost`/`canonical_host` in both languages
  (`host.`→`host`, `HOST`→`host`, `host:443`→`host`, idempotency).

### C.2 Document the matcher contract

- Comment at `Decide`/`decide` that the request host is canonicalized at entry, so Go and
  Python cannot diverge; note in `docs/design.md` where the host model is described.

### C.3 Go `HostDisposition` + corpus parity

- `internal/policy/` — add `HostDisposition(host string) HostDisposition` (type with
  `Passthrough bool`, `DenyReason string`) mirroring `policy.py:391-409`, canonicalizing
  the host at entry. Keep the conservative "any intercept in the top host-rank tier →
  not passthrough" rule; document it as the host-only projection of `Decide` (whose
  most-specific rule is canonical).
- Corpus schema — extend `expected` with an **optional**
  `host_disposition: {passthrough, deny_reason}`; update the strict decoders in **both**
  `internal/policy/vectors_test.go` (the `expectation` struct) and
  `internal/proxy/enforcer/test_vectors.py` (`_EXPECTED_KEYS`) in lockstep.
- Replays — when a vector carries `host_disposition`, assert
  `Ruleset.HostDisposition(request.host)` (Go) and `policy.host_disposition(rs,
  request["host"])` (Python) against it.
- New vectors under `internal/policy/testdata/decision-vectors/`:
  - `host_trailing_dot` (`api.example.com.` host-level deny enforced),
  - `host_with_port` (`host:443`),
  - `host_uppercase`,
  - `host_disposition_mixed_mode` (one passthrough + one intercept allow on the same host
    → `passthrough=false`), with both `expected.decision` and `expected.host_disposition`.

### C.4 Tests (Phase C)

- Go: `canonicalHost` table test; `HostDisposition` unit test; `vectors_test.go` replays
  the new `host_disposition` vectors.
- Python: `canonical_host` test; `test_vectors.py` replays the new vectors through
  `host_disposition`.
- `TestCorpusNotForked` still passes (single corpus dir).

### C.5 Success criteria (Phase C)

#### Automated
- [ ] `make test` green (Go canonicalization, `HostDisposition`, corpus replay).
- [ ] `make test-enforcer` green (Python canonicalization + `host_disposition` replay).
- [ ] `make lint` green.
- [ ] A corpus vector for `host_disposition` fails if either language's replay is removed.

#### Manual
- [ ] A host-level `deny_always` blocks `host`, `HOST`, `host.`, and `host:443`
      identically (reason via an enforcer/integration check or a targeted vector).

---

## Final verification (end of ticket)

- [ ] `make test`, `make test-enforcer`, `make lint` all green.
- [ ] `make build` so `bin/agent-creance` reflects the final commit.
- [ ] Ticket acceptance criteria A/B/C all checked; set `**Status:** Done`, bump
      `**Updated:**`, append a dated note.

## Testing Strategy

Per project conventions: pure logic → table-driven; generated `.sb` → golden with
`make golden`; CLI/lifecycle → Go unit tests with `sysdep` fakes; Python enforcer →
pytest via the `addon` fixture; cross-language matcher → the single shared
decision-vector corpus, extended in both replays in lockstep. External tools only under
`//go:build integration`.

## Scope guards

- **No** change to what the audit log records (AC-0060). Host canonicalization may change
  the host string shown for a passthrough deny — keep that incidental, not a
  records-format change.
- **No** proxy refcount/lock changes (AC-0061); the B.2 readiness wait stays within
  spawn-readiness, not refcount.

## References

- Ticket: `thoughts/shared/tickets/AC-0058-egress-boundary-defeats.md`
- Research: `thoughts/shared/research/2026-06-22-AC-0058-egress-boundary-defeats.md`
- Review: `thoughts/shared/reviews/2026-06-22-codebase-quality-review.md`
