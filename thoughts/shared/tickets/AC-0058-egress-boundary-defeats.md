# AC-0058: Close three critical egress-boundary defeats (SBPL injection, enforcer fail-open, host-deny bypass)

**Status:** In Progress
**Estimated Complexity:** Extra Large
**Created:** 2026-06-22
**Updated:** 2026-06-22

## Problem Statement

A whole-codebase security review (`thoughts/shared/reviews/2026-06-22-codebase-quality-review.md`)
found three **Critical** flaws that each silently defeat a guarantee agent-creance
advertises. All three were verified directly against the source. They share a
severity band (must-fix) and a theme (the request never gets correctly filtered),
so they are tracked together; research/planning should phase them by the three
independent work-streams A/B/C below.

**A — SBPL injection via the `host_services` label.** `RenderNetworkSB`
(`internal/profile/profile.go:91`) writes a host-service's label verbatim into the
generated `network.sb` Seatbelt fragment after a `;; ` comment marker, and
`parseHostService` (`internal/config/validate.go:103`) accepts any non-empty label,
including newlines and control characters. A config entry like
`- "x\n(allow network*):3306"` (a YAML double-quoted scalar carries a real newline)
parses to label `x\n(allow network*)`, port `3306`, and renders a live
`(allow network*)` line **after** the `(deny network*)` baseline — which, by
Seatbelt's last-match-wins precedence, re-opens all outbound egress and defeats the
entire network model. The design already treats the project config as an
attacker-influenced surface (that is why `config-ro.sb` exists), so this is in
scope for the threat model. The label is the only value in the package that reaches
SBPL text without `%q`-escaping; every other interpolation is already escaped.

**B — The mitmproxy enforcer fails OPEN on an unhandled exception.** The `request`
hook (`internal/proxy/enforcer/enforcer.py:198`) has no `try/except`. mitmproxy's
addon manager logs an exception raised in a `request` hook and **continues** — and
for the `request` hook, "continue" means the flow is forwarded upstream because no
`flow.response` was set. Any unexpected exception in the decision path (a malformed
flow, a stale `deny_always[result.matched.index]` access at line 227, a future code
change) therefore becomes an *allowed* egress. For a fail-closed security proxy this
is the cardinal sin. Coupled: the enforcer's behavior on a **missing/corrupt/
mid-write `policy.json`** is correct (an empty ruleset soft-denies everything; a
malformed hot-reload keeps the last-good ruleset and retries next tick) but is
guarded only by luck of the empty-ruleset default and has **no test and no health
signal** to the Go launcher when the initial load fails.

**C — Request hosts are not normalized, so a host-level `deny_always` is bypassable.**
The enforcer feeds `flow.request.host` / `pretty_host` / the SNI to the matcher
verbatim (`internal/proxy/enforcer/enforcer.py:175,204`), and `matchHost`
(`internal/policy/glob.go`) does only a lowercased string/suffix compare. A
trailing-dot host (`api.example.com.`, which DNS resolves identically to
`api.example.com`) therefore does not equal `api.example.com`: a host-level
`deny_always` is **bypassed**, and a host-scoped allow spuriously soft-denies. A
port in the CONNECT authority would mismatch the same way. Coupled: the enforcer
makes a *separate* host-only decision at CONNECT time via `policy.host_disposition`
(`enforcer.py:176,191`) whose passthrough rule differs from `Decide`'s, and the
shared cross-language decision-vector corpus — the entire "the Go and Python
matchers never disagree" guarantee — drives only `decide`/`Decide`, never
`host_disposition`, in either language. So a normalization fix risks introducing a
Go/Python divergence that no test would catch.

## Desired Outcome

All three boundary defeats are closed and pinned by tests:

- **A:** A host-service label can never introduce a line into `network.sb` other
  than its intended trailing comment. Untrusted config values that reach any SBPL
  fragment are validated and/or escaped, and the safety is enforced by adversarial
  tests rather than left incidental to `%q`.
- **B:** Any exception in the enforcer's per-request decision results in a
  **hard-deny**, never a forwarded request. An initial `policy.json` load failure is
  surfaced as a hard startup error to the launcher (not a silent run on an empty
  ruleset); a malformed hot-reload provably keeps the last-good policy.
- **C:** Request hosts are canonicalized (lowercased, trailing dot stripped, port
  stripped) once at the enforcer boundary before any allow/deny decision, so
  `api.example.com.` and `api.example.com:443` are treated identically to
  `api.example.com`. The `host_disposition` CONNECT-stage decision is covered by the
  shared decision-vector corpus with a Go counterpart, so the two implementations are
  provably consistent.

## User Stories / Use Cases

- As an operator, I want a teammate's (or a cloned repo's) `.agent-creance.yaml` to
  be unable to silently re-open all egress through a crafted service label, so the
  network guarantee holds even against a hostile config.
- As an operator, I want a bug in the egress proxy to **block** the request, not
  leak it, so a crash in the filter can never become an exfiltration channel.
- As an operator, I want `deny_always: [host]` to actually block that host no matter
  how the agent spells it (trailing dot, port), so a hard-deny cannot be trivially
  evaded.
- As a maintainer, I want the Go and Python matchers proven to agree on **every**
  decision they make (including the CONNECT-stage host decision), so a one-sided
  change cannot open a silent gap.

## Acceptance Criteria

### A — SBPL injection
- [ ] A `host_services` label containing a newline, carriage return, or other
      control character is rejected at config-parse time with a clear error (or
      defensively sanitized at render so it cannot produce a non-comment line) —
      decide and document which, with both layers preferred for a security boundary.
- [ ] `RenderNetworkSB`, `RenderProxyFragment`, `RenderCAReadFragment`,
      `RenderConfigReadOnlyFragment`, and any other SBPL renderer cannot emit a line
      that is not a comment or an intended rule, for any input — verified by tests
      feeding `"`, `\`, `\n`, `\r`, `)`, `(`, and `;;` in labels and paths.
- [ ] Host and HTTP-method values in egress rules are validated (plausible
      hostname/glob; uppercase known method) so malformed input is caught at config
      load rather than silently never-matching (F18).

### B — Enforcer fail-closed
- [ ] An exception raised anywhere in the per-request decision path results in a
      hard-deny response (the request is **not** forwarded upstream).
- [ ] A failed **initial** `policy.json` load is surfaced as a hard, visible startup
      failure to the Go launcher (distinct from a hot-reload failure), rather than
      the addon running on an empty ruleset.
- [ ] A malformed or mid-write `policy.json` encountered during hot-reload keeps the
      previous good ruleset in force and recovers on the next valid write.

### C — Host normalization & parity
- [ ] The request host is canonicalized (lowercase, strip trailing `.`, strip port)
      once at the enforcer boundary before `decide` and `host_disposition`; a
      host-level `deny_always` blocks `host.`, `HOST`, and `host:443` identically.
- [ ] The Go matcher applies the same canonicalization (or the contract is
      documented as "the enforcer canonicalizes; the matcher assumes canonical
      input") so Go and Python cannot diverge on these inputs.
- [ ] `host_disposition` has a Go counterpart and is exercised by the shared
      decision-vector corpus, including a mixed-mode (passthrough + intercept on the
      same host) adversarial vector.

## Testing Protocol

Follow the project conventions (`.claude/tce/profile.md`): pure logic → table-driven;
generated artifacts → golden with `-update`; CLI → testscript; cross-language matcher
→ the shared decision-vector corpus; Python enforcer → pytest in the repo-local venv;
external tools only under the `integration` tag.

- **A (SBPL):** add adversarial table tests in `internal/profile/profile_test.go`
  asserting that hostile labels/paths produce exactly one rule line and zero injected
  SBPL forms; add a config-validation table case in
  `internal/config/validate_test.go` for control-char labels and malformed
  hosts/methods. Pin the `%q` escaping behavior explicitly so a refactor to `%s`
  fails. Regenerate and review any golden `.sb` via `make golden`.
- **B (fail-closed):** add pytest cases in `internal/proxy/enforcer/test_enforcer.py`
  that (1) inject a raising matcher/decision and assert the flow is hard-denied (a
  `flow.response` with the refusal body is set, not forwarded); (2) assert a malformed
  reload keeps the last-good ruleset and recovers on the next tick; (3) assert an
  initial-load failure surfaces as an error rather than a silent empty ruleset. Run
  via `make test-enforcer`.
- **C (host/parity):** add decision-vectors under
  `internal/policy/testdata/decision-vectors/` for `host.`, `host:443`, uppercase
  host, and the mixed-mode `host_disposition` case; ensure both `vectors_test.go`
  (Go) and `test_vectors.py` (Python) replay them. Add `host_disposition` vectors and
  a Go `HostDisposition` implementation. Confirm a single test (or `make` target)
  fails if either language's corpus replay is absent from the run.
- **Gate:** `make test`, `make test-enforcer`, `make lint` all green; `make build` at
  the end so `bin/agent-creance` reflects the final commit.

## Out of Scope

- Closing the passthrough path-deny blind spot (a `deny_always` path-rule does not
  apply on a `mode: passthrough` host) — that is a documented, accepted design
  trade-off, not a bug. A compile-time *warning* when a path-deny overlaps a
  passthrough host may be a follow-up (see AC-0060/review F-notes), not this ticket.
- Reworking the broader passthrough enforcement model.
- Any change to what the audit log records (covered by AC-0060).

## Open Questions

None blocking. The three work-streams are independent and well understood; the only
design choices (reject vs sanitize the label; canonicalize in the enforcer vs both
matchers) are settled in Acceptance Criteria above.

## Questions for Research/Planning

- [ ] A — exact validation surface for labels (charset allowlist vs control-char
      denylist) and whether to also sanitize at render; confirm no current valid
      label (containing spaces, `/`, `:`?) is broken by it.
- [ ] A/F18 — what host/method validation the compiler already applies to
      *generator-emitted* rules vs hand-authored rules; ensure the passthrough+paths
      rejection and new host/method checks cover both paths.
- [ ] B — how mitmproxy surfaces a hook exception in the installed version (confirm
      "logs and forwards" for the `request` hook), and the cleanest launcher channel
      for a hard initial-load failure (exit code, stderr marker the Go side reads).
- [ ] C — confirm `flow.request.host`/`pretty_host`/SNI can each carry a trailing dot
      or port in the target mitmproxy version; choose the canonicalization point;
      reconcile `host_disposition`'s "all-allows-in-top-tier" rule with `Decide`'s
      "most-specific-allow" rule (which becomes the canonical one).

## References

- Review: `thoughts/shared/reviews/2026-06-22-codebase-quality-review.md` (findings
  F1, F2, F3, F4, F6, F15, F18).
- A: `internal/profile/profile.go:82-96` (`RenderNetworkSB`),
  `internal/config/validate.go:95-114` (`parseHostService`),
  `internal/profile/profile_test.go`.
- B: `internal/proxy/enforcer/enforcer.py:74,137,198-230`,
  `internal/proxy/enforcer/policy.py`, `test_enforcer.py`.
- C: `internal/proxy/enforcer/enforcer.py:175,191,204`,
  `internal/policy/glob.go`, `internal/policy/match.go`,
  `internal/proxy/enforcer/policy.py:391` (`host_disposition`),
  `internal/policy/testdata/decision-vectors/`, `vectors_test.go`, `test_vectors.py`.
- `docs/design.md` — "What the cage prevents" (network model, `localhost` token),
  "Network refusal handling", "Config compilation" (`config-ro.sb`).

## Implementation Plan

[Leave empty — filled when the plan is created.]

## Notes & Updates

### 2026-06-22

- Created from the 2026-06-22 whole-codebase security review. Groups the three
  **Critical** findings (F1 SBPL injection, F2 enforcer fail-open, F3 host-deny
  bypass) plus the testing/parity items directly coupled to each fix (F15 SBPL
  escaping tests, F6 enforcer fail-closed tests, F4 `host_disposition` parity, F18
  host/method validation).
- Grouped by severity at the user's request (one Critical ticket rather than three);
  marked Extra Large because it spans three independent subsystems (profile/config,
  Python enforcer, policy engine + corpus) — research/planning should phase it as
  work-streams A/B/C.
- F1, F2, F3 were verified directly against the source during the review; F18 was
  review-reported and should be confirmed during research.
