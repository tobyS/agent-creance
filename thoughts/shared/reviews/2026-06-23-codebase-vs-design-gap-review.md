---
date: 2026-06-23T14:25:32Z
git_commit: e80eaa8719474d9561f5ffaa973407a249245192
branch: main
ticket: N/A
review_scope: "Whole codebase vs. docs/design.md — gap detection and decision analysis"
status: complete
---

# Code Review: Codebase vs. Design Document (Gap Analysis)

**Date**: 2026-06-23
**Reviewer**: Claude
**Ticket**: N/A (custom-scope, design-conformance review)
**Branch**: main
**Commit**: e80eaa8

## Review Scope

A conformance review of the entire `agent-creance` implementation against
`docs/design.md`. The goal was not bug-hunting but **gap detection**: where does
the shipped code diverge from what the design promises, and — for each gap —
where does the divergence come from (a deliberate decision, or drift)?

Verification was done by reading the design end-to-end and checking ~50 concrete,
falsifiable design claims against the implementation across six subsystems:

1. Egress policy enforcement (`internal/proxy/enforcer/`, `internal/policy/`)
2. Seatbelt profile compilation (`internal/profile/`, `internal/cage/`)
3. Proxy lifecycle / multi-agent / lock file (`internal/proxy/`, `cli/run.go`)
4. Config loading, includes, generators (`internal/config/`, `internal/generator/`)
5. Command surface (`internal/cli/*`, `doctor`, `status`, `setup`, `init`, …)
6. Credential / CA / skill / cage-verification story (`internal/setup`, `cred`, `cage`, `verify`)

Ticket statuses corroborate the maturity: of AC-0001 … AC-0061, every ticket is
**Done** except **AC-0046** (config cage, deliberately Open / deferred past v0.1)
and **AC-0044** (Rejected).

## Executive Summary

**The implementation is an unusually faithful realization of the design.** Across
the ~50 claims checked, the code matches the design on the security-load-bearing
mechanics — the 470/471 refusal contract, the `localhost`-token-only Seatbelt
rules (never literal IP, never `*:N`), deny-shadows-allow precedence, the
out-of-tree state directory, lexical include confinement, the four CA env vars,
the scoped Keychain grant, passthrough's compile-time path/method rejection, and
the parity between the Go matcher and the Python enforcer (both pinned to a shared
decision-vector corpus). The Go/Python dual implementation of the matcher is the
highest-risk duplication in the project and it is handled correctly — line-for-line
equivalent and corpus-pinned.

Only **two genuine gaps** surfaced, and both are the same shape:
**the design claims a behavior happens in more places than the code actually wires
it.** Neither is a security hole or broken core functionality — they are
*diagnostic/coverage* gaps where the design over-states reach. The remaining
deltas are benign supersets (the code does *more* than the design says) and are
not defects.

There is nothing Critical. The codebase does not promise a guarantee it fails to
deliver on the enforcement path.

### Priority Findings

#### Critical (Must Fix)
- None.

#### Important (Should Fix)
- **Prerequisite/version check is not "on every command" and is absent from
  `setup`** — design says it runs everywhere and explicitly names `setup`, but the
  code runs it only in `run` and `doctor`. `internal/cli/setup.go:48`,
  `internal/cli/cli.go:98`. (vs. `docs/design.md:374,376`)
- **`doctor` does not detect the file-based-credential fallback** — design says
  "`run`/`doctor` detect the situation … and refuse"; only `run` does.
  `internal/doctor/doctor.go:39-47` (vs. `docs/design.md:530`)

#### Minor (Consider Fixing)
- Design narrative attributes the "policy-hash differs → touch `policy.json`" step
  to the lifecycle manager's validate phase; in code that lives in `run.go`'s
  pre-`Attach` recompile + the enforcer mtime poll, not in `proxy.Manager`.
  Behaviorally identical; only the *description* of where it lives is off.
  (`docs/design.md:492` vs. `internal/proxy/lifecycle.go` / `internal/cli/run.go:100`)

#### Positive Observations
- Go matcher ↔ Python enforcer parity is corpus-pinned and line-for-line equivalent
  (`internal/policy/glob.go` ↔ `internal/proxy/enforcer/policy.py`).
- The deferred-scope items (AC-0046 config cage, v0.2 secret injection) are
  documented honestly in both the design's "Not prevented" section and the code,
  rather than silently half-built.
- Several places ship a *superset* of the design (start-time PID identity, extra
  proxy env vars, an extra curated shared-apex host) — hardening beyond the spec.

## Detailed Findings

### Gap 1 — Prerequisite & version check is narrower than the design states

**Design claim.** `docs/design.md:374`: "The prerequisite check runs on **every
command** (it's cheap — `exec.LookPath` plus a `--version` invocation)." And
`docs/design.md:376`: "On `run`, **`setup`**, and `doctor`, agent-creance parses
the installed versions and compares against these constants" (driving the
silent/patch/loud-warn behavior).

**Implementation.** `prereq.Check` is called in exactly two places:
`internal/cli/run.go:58` and `internal/doctor/doctor.go:41`. The version-skew
warning (`warnVersionSkew`) is only in `internal/cli/run.go:63,291`. The root
`PersistentPreRunE` (`internal/cli/cli.go:98-111`) resolves color only — it does
no prerequisite check. Crucially, **`setup` runs neither** — `runSetup`
(`internal/cli/setup.go:48`) goes straight to the `Installer` with no `prereq`
reference anywhere in `internal/cli/setup.go` or `internal/setup/`.

**Where the gap comes from (decision analysis).** This reads as a *deliberate but
undocumented narrowing that overshot*. The check was scoped to the commands that
actually touch the external tools at runtime — `run` launches safehouse+mitmproxy,
`doctor` exists to diagnose them. Commands like `allow`, `deny`, `policy`,
`status`, `init`, `include`, `import` never exec either tool, so a `--version`
probe there would be pure overhead with nothing to gate. That is a reasonable
optimization the design's blanket "every command" wording never caught up to.

The part that looks like a genuine *miss* rather than a defensible cut is `setup`:
the design names it explicitly, and `setup` **does** spawn `mitmdump` (for CA
generation and the post-install TLS verification, `internal/setup/setup.go:223`).
A major mitmproxy version skew is exactly the kind of thing that would make that
verification behave surprisingly, and `setup` is the one-time onboarding command
where a "you're on an untested version" warning is most useful. So `setup` having
no version check is the concrete, fixable half of this gap; the "every command"
overstatement is a doc-accuracy issue.

**Resolution options.** Either (a) add `prereq.Check` + `warnVersionSkew` to
`setup` and soften `docs/design.md:374` to "on the commands that use the tools
(`run`, `setup`, `doctor`)", or (b) move the missing-tool check into
`PersistentPreRunE` to genuinely make it universal. (a) matches the apparent
intent with the least cost.

### Gap 2 — `doctor` does not detect the file-based-credential fallback

**Design claim.** `docs/design.md:530`: "`run`/`doctor` detect the situation
(Keychain item absent but a credentials file present) and refuse with a clear
message … instead of a confusing mid-session auth failure."

**Implementation.** `cred.Detect` is wired into the `run` path only
(`internal/cli/run.go:78`). `doctor`'s `Checker.Run` (`internal/doctor/doctor.go:39-47`)
runs version, CA, proxy, exposed-services, and filesystem checks — there is no
`cred.Detect` call, and no credential section in the `Report`.

**Where the gap comes from (decision analysis).** This looks like **incremental
drift**, not a decision. `cred.Detect` was built as a *launch guard* — its job is
to refuse `run` before the user drops into a confusing in-cage auth failure
(`internal/cred/cred.go:90-93,123-136`). When `doctor` was extended (AC-0031), the
credential check wasn't pulled into it, and the design's "run/doctor" phrasing was
written aspirationally. The irony is that `doctor` is *precisely* the command a
user would reach for to answer "why do my caged sessions fail to authenticate" —
which is the exact failure mode this detection exists to explain. So the gap
quietly removes diagnostic value from the command whose whole purpose is diagnosis.

**Resolution.** Add a credential section to `doctor` that calls
`cred.Detect(app.Keychain, app.FS, app.Paths)` and surfaces `StatusFileFallback`
(and the `locked keychain` case from spike S2, `docs/design.md:20`) as a Warn/Problem
finding. Low effort — the detection logic already exists and is side-effect-free.

### Code Quality

High. The `sysdep` seam is respected throughout (no direct OS calls from logic
packages were found in the audited paths), commands take `*App`, and the
testing-convention split (table tests / golden files / testscript) holds. The
enforcer's Python modules mirror the Go policy package deliberately and are kept
honest by the shared decision-vector corpus
(`internal/policy/testdata/decision-vectors`).

### Integration

The compiled artifacts (`network.sb`, `ca.sb`, `config-ro.sb`, `policy.json`,
session overlay) all land out-of-tree in `~/.cache/agent-creance/projects/<hash>/`
exactly as the design's integrity argument requires, and none are mounted into the
cage. The append-profile ordering (network → proxy-port → claude-state → ca →
config-ro) is correct for last-match-wins semantics.

### Test Coverage

Strong and matched to the design's risk areas: golden files for every generated
`.sb` fragment, the shared decision-vector corpus across both matcher
implementations, integration tests for the CA-untrusted verdict, and the
adversarial cage-verification matrix (`internal/verify/matrix.go`) covering both
IPv4 and IPv6 localhost-port refusal, the CA-env-var positive paths, and the
Keychain read/write vectors.

### Security Review

No security regressions against the design were found. The load-bearing
guarantees are all present and correctly scoped: `localhost`-token-only network
rules, CA-cert-only read grant (private key metadata-only), lexical include
confinement with the symlink-target caveat honestly preserved, query-string-stripped
headerless audit log at 0600, and passthrough's path/method compile-time rejection.
The honest limits the design documents (token still readable by the agent,
whitelisted-service confused-deputy, config-persistence vector under AC-0046) are
reflected, not papered over.

### Benign deltas (code does *more* than the design — not defects)

- **Start-time PID identity** (AC-0061): `agentRef` carries `StartTime` beyond the
  design's "PIDs of attached agents," hardening against PID recycling.
  (`internal/proxy/lifecycle.go:73-76,416-428`)
- **Extra proxy env vars**: `HTTP_PROXY`/`NO_PROXY` set alongside `HTTPS_PROXY`.
  (`internal/cage/cage.go:317-323`)
- **Packagist endpoint**: uses `repo.packagist.org/p2/<vendor>/<pkg>.json` rather
  than a bare `packagist.org` lookup — within the design's generic wording.
  (`internal/generator/registry/packagist.go:39`)
- **Extra shared-apex host**: `sharedApexHosts` includes `pythonhosted.org` beyond
  the design's two named examples — consistent with "a small, curated set."
  (`internal/generator/sharedapex.go:17-21`)

## Recommendations

### Immediate Actions
- [ ] Add `prereq.Check` + `warnVersionSkew` to `setup` (it spawns `mitmdump`; a
      version-skew warning belongs on the onboarding command). — Gap 1
- [ ] Reconcile `docs/design.md:374` with reality: the prereq check runs on the
      tool-using commands (`run`, `setup`, `doctor`), not literally every command. — Gap 1
- [ ] Add a credential section to `doctor` calling `cred.Detect`, surfacing the
      file-fallback (and locked-keychain) cases. — Gap 2

### Future Considerations
- [ ] Consider whether the missing-tool (not version) check belongs in
      `PersistentPreRunE` so commands that *don't* use the tools still fail fast
      with the install hint, if that's the intended UX.

## Files Reviewed

- `internal/proxy/enforcer/{enforcer,policy,audit,responses}.py` — refusal contract, audit, hot-reload, streaming
- `internal/policy/{match,glob,policy}.go`, `policy/compile/`, `policy/render/` — matcher, compiler, explain
- `internal/profile/profile.go`, `profile/compile.go`, `profile/testdata/*.golden` — Seatbelt fragments
- `internal/cage/cage.go`, `cage/run.go`, `cage/briefing.md` — cage construction, env, briefing
- `internal/proxy/lifecycle.go`, `state/state.go`, `sysdep/processgroup.go` — refcount lifecycle, identity, signals
- `internal/config/{load,merge,validate}.go` — include resolution + confinement + merge
- `internal/generator/`, `generator/registry/` — package_json/composer_json, forge table, caching
- `internal/cli/{cli,run,setup,init,allow,deny,import,include,policy,logs,status,clean}.go` — command surface
- `internal/setup/`, `setupcheck/`, `cred/`, `verify/` — CA bootstrap/verify, credential detection, cage verification
- `internal/doctor/doctor.go` — diagnostics (Gap 1 & 2 locus)
- `docs/design.md` — the reference

## References

- Design: `docs/design.md`
- Deferred-scope tickets: `thoughts/shared/tickets/AC-0046-config-cage-revisit.md` (Open),
  `AC-0044-incage-dx-state.md` (Rejected)
- Decision-vector corpus: `internal/policy/testdata/decision-vectors`
