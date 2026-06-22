---
date: 2026-06-22T00:00:00Z
researcher: Claude
git_commit: a71e1480b13c37f97f6064fb26bb46a7babce9af
branch: main
repository: agent-creance
topic: "AC-0058 — Close three critical egress-boundary defeats (SBPL injection, enforcer fail-open, host-deny bypass)"
tags: [research, security, seatbelt, mitmproxy, enforcer, policy, host-normalization, parity]
status: complete
ticket: AC-0058
---

# Research: AC-0058 — Close three critical egress-boundary defeats

**Date**: 2026-06-22
**Researcher**: Claude
**Git commit**: a71e1480b13c37f97f6064fb26bb46a7babce9af
**Branch**: main

## Research Question

How should agent-creance close the three Critical egress-boundary defeats from the
2026-06-22 codebase review — (A) SBPL injection via the `host_services` label, (B) the
mitmproxy enforcer failing open on an unhandled exception / unguarded policy load, and
(C) un-normalized request hosts defeating a host-level `deny_always` plus the
unparitied `host_disposition` CONNECT-stage decision — consistent with the project's
existing patterns, and what design choices does each work-stream require?

## Summary

All three defeats and the four review findings folded into them (F1, F2, F3, F4, F15,
F18) were re-verified directly against source. Each is a small-to-moderate fix plus
tests, but the three work-streams touch three independent subsystems and one
(B2 — surfacing an initial-load failure to the Go launcher) requires a genuinely new
capability because `Spawn` today is fire-and-forget with discarded stderr and no
readiness wait.

- **A (SBPL injection):** Confirmed. `RenderNetworkSB` writes `svc.Label` **raw** via
  `strings.Builder.WriteString` (`internal/profile/profile.go:91`); it is the **only**
  SBPL interpolation in the package that is neither `%q`-escaped nor regex-escaped.
  `parseHostService` validates only label non-emptiness
  (`internal/config/validate.go:103`). Fix = reject control chars at parse **and**
  sanitize at render (defense in depth, both preferred). F18 (host/method validation)
  is coupled and must cover **both** hand-authored rules (`validateRules`) and
  generator-emitted rules, which currently **bypass** `validateRules` entirely.

- **B (enforcer fail-closed):** Confirmed. The `request` hook has **no** `try/except`
  (`enforcer.py:198-230`); mitmproxy 12.x's addon manager logs an unhandled hook
  exception and **continues**, and "continue" for `request` means *forwarded upstream*
  because no `flow.response` was set. The `deny_always[result.matched.index]` access at
  line 227 is one concrete raise site. The empty-ruleset default already soft-denies
  everything (fail-closed at the *decision* level), but an initial-load failure is
  swallowed (`_load`, lines 140-147) and surfaced nowhere; `Spawn` discards stderr and
  `Attach` does no readiness check. A malformed hot-reload already keeps last-good (the
  assignment never completes inside the `try`) but has no test.

- **C (host normalization & parity):** Confirmed. Raw mitmproxy host strings flow
  straight into the matchers (`enforcer.py:175,191,204`); `matchHost`
  (`internal/policy/glob.go:10-24`) does only lowercase + `*`/`*.suffix`/exact, so
  `api.example.com.` ≠ `api.example.com` and a host-level `deny_always` is bypassable.
  No Go `HostDisposition` exists; the 18-vector shared corpus drives only
  `decide`/`Decide`, never `host_disposition`, whose passthrough rule
  ("all allows in the top host-rank tier") differs from `Decide`'s
  ("single most-specific allow").

The work-streams are independent and can be implemented and committed in any order. The
recommended approach below resolves each ticket "Question for Research/Planning."

## Detailed Findings

### Work-stream A — SBPL injection via the `host_services` label

#### A.1 The injection (verified)

`RenderNetworkSB` (`internal/profile/profile.go:82-96`):

```go
for _, svc := range dedupeByPort(services) {
    b.WriteString(allowRule(svc.Port))
    if svc.Label != "" {
        b.WriteString("  ;; ")
        b.WriteString(svc.Label)   // line 91 — RAW, no %q, no escaping
    }
    b.WriteByte('\n')
}
```

The port goes through `allowRule(port int)` (`profile.go:74-76`), which `%q`-escapes a
`localhost:<port>` token — `port` is an `int`, so no vector. The **label** is the sole
unescaped external string reaching SBPL text. A YAML double-quoted scalar
`- "x\n(allow network*):3306"` carries a real newline; `parseHostService` splits on the
last `:`, yields label `x\n(allow network*)`, and the renderer emits a live
`(allow network*)` line **after** `DenyBaseline` (`profile.go:85`). By Seatbelt
last-match-wins precedence (documented `profile.go:21-24`) that re-opens all egress.

#### A.2 Escaping inventory of every SBPL renderer

| Renderer | Untrusted input | Escaping |
|---|---|---|
| `allowRule` (74-76) | port (int) | `%q` on `localhost:<port>` |
| `RenderNetworkSB` (82-96) | **label (string)** | **RAW `WriteString` — VULNERABLE** |
| `RenderProxyFragment` (102-107) | port (int) | `%q` via `allowRule`; range-checked |
| `RenderCAReadFragment` (116-129) | cert path | `%q` (both lines, abs-checked) |
| `RenderKeychainFragment` (145-155) | home | `sbplRegexEscape` + `%s` (regex ctx) |
| `RenderClaudeStateFragment` (165-175) | home | `%q` literal + `sbplRegexEscape`/`%s` regex |
| `RenderConfigReadOnlyFragment` (185-200) | config paths | `%q`, abs-checked |

`%q` (Go) escapes control bytes (`\n`→`\n`, `"`→`\"`, `\`→`\\`), which is why the
path renderers are not injectable. `sbplRegexEscape` (`profile.go:216-226`) escapes
regex metacharacters only (not quotes/controls) — fine for regex context. The label
uses neither.

#### A.3 Validation today

`parseHostService` (`internal/config/validate.go:97-114`) splits on the **last** `:`,
rejects an empty label and a non-numeric/out-of-range port — **no character-class check
on the label**. The carrier is `config.HostService{Label string; Port int}`
(`config.go:60-63`); it reaches the renderer via `profile.Compiler.Compile` →
`RenderNetworkSB(cfg.Network.HostServices)` (`internal/profile/compile.go:62`), written
atomically to `layout.NetworkSB()`.

`validateRules` (`validate.go:22-45`) checks egress rules: host **presence only**
(`r.Host == ""`, line 25 — no format/charset check); mode enum + passthrough/paths
incompatibility; **methods are never validated for value or case**. The enforcer's
method match is case-sensitive, so `methods: [get]` silently never matches (F18).

#### A.4 F18 — the generator-rules bypass (important)

There are two compilers. The Seatbelt profile compiler (`internal/profile/compile.go`)
carries the `host_services` label. The **egress policy compiler**
(`internal/policy/compile/compile.go`) builds `policy.json`. Hand-authored rules
(explicit/global/overlay) pass through `config.Loader` → `validateRules`.
**Generator-emitted rules bypass `validateRules` entirely** — `runGenerators`
(`compile.go:509-533`) copies `gr.Rule`, sets `Source`/`LowerTrust`, defaults `Mode`,
and never validates host/method. So an F18 host/method validation that lives only in
`config.validate` would not cover generator output. The fix must validate both paths
(e.g. a shared validator the policy compiler also runs over generator rules).

#### A.5 Existing tests (the gap behind F1/F15)

`internal/profile/profile_test.go` golden-tests `RenderNetworkSB` only with benign
labels (`mysql`, `redis`) and never feeds `"`, `\`, `\n`, `;;`, `(`, `)`. No test
asserts the deny baseline is the last network-affecting line or that no `(allow
network*)` appears after it. Golden files: `internal/profile/testdata/*.golden`.
Adversarial-escaping precedent exists for the *home* fragments
(`TestHomeFragments_RegexEscaping`, profile_test.go:285-304) — mirror it for labels.

Config-validation test patterns: `internal/config/validate_test.go` has a pass/fail
table `TestValidate` (asserts `require.Error` + `require.Contains` on a substring) and a
golden-error `TestGoldenErrors`; `TestValidate_ReportsAllIssues` pins the
accumulate-all-problems behavior (use `verr.add(...)` style for multi-issue reporting).

#### A.6 Recommended approach (A)

1. **Parse-time (denylist):** reject any `host_services` label containing a control
   character (rune < 0x20 or 0x7f) with a clear `fmt.Errorf("host_services entry %q …")`
   error. A control-char **denylist** (not a charset allowlist) is recommended: it
   closes the injection without breaking descriptive labels that legitimately contain
   spaces, `/`, or `:` (the colon before the port is already split off). No current
   valid label is broken.
2. **Render-time (defense in depth):** in `RenderNetworkSB`, strip/replace any
   remaining control character in the label so it can never terminate the comment line —
   so even a future parse bypass cannot inject. Pin with an adversarial table test that
   feeds `"`, `\`, `\n`, `\r`, `;;`, `(`, `)` and asserts exactly one rule line and zero
   injected SBPL forms, plus a test that pins `%q` on the path renderers so a `%s`
   refactor fails.
3. **F18:** validate egress-rule hosts (plausible hostname/glob, no control chars,
   single trailing-dot tolerated-or-rejected — see C) and methods (non-empty, uppercase,
   from a known-verb set with room for extension) at config load, via a validator the
   **policy compiler also applies to generator rules**.

**Open design choice for the checkpoint:** how strict method validation should be —
uppercase-only (minimal: fixes the silent `get` miss) vs a closed known-verb allowlist
(stricter, but risks rejecting valid uncommon verbs like `PROPFIND`).

### Work-stream B — enforcer fail-closed

#### B.1 The fail-open (verified)

`request` (`enforcer.py:198-230`) sets `flow.response` only on soft/hard-deny; allow and
any **exception before line 230** leave `flow.response is None`. Confirmed against
mitmproxy source: `addonmanager.safecall()` catches every `Exception` (except
`AddonHalt`/`OptionsError`), logs `"Addon error: …"`, and `trigger_event` continues —
so the flow is forwarded upstream. The known raise site is
`self._ruleset.deny_always[result.matched.index].reason` (line 227): an `IndexError` if
the ruleset is hot-swapped between `decide` and the index read. The CONNECT/TLS hooks
(`http_connect` 167-184, `tls_clienthello` 186-196) are likewise unguarded.

Note: AC-0057 added a `responseheaders` streaming hook (`enforcer.py:232`); it runs only
for already-allowed/forwarded responses, so it is post-decision — but the fail-closed
review should confirm an exception there cannot un-block a denied request (it cannot:
denied flows never reach it).

#### B.2 Policy load / reload (verified)

- `__init__` seeds an **empty** `policy.RuleSet()` (`enforcer.py:73-79`). An empty
  ruleset **soft-denies every request** (`policy.decide` falls through to
  `DECISION_SOFT_DENY`) — fail-closed at the decision level, but silent.
- `_load` (`enforcer.py:137-147`) catches `FileNotFoundError`, `OSError`, `ValueError`
  and **logs only** (WARNING/ERROR). On an initial missing/corrupt `policy.json` the
  addon keeps running on the empty ruleset; nothing is surfaced to Go. `-q` (quiet) is
  passed in the spawn argv, suppressing even those logs.
- Hot-reload (`_maybe_reload`/`_poll_loop`, 149-163) polls `getmtime` every 1s. On a
  **malformed reload** the `self._ruleset = …` assignment (line 141) never completes
  inside the `try`, so **last-good is retained** — but `self._mtime` is not updated
  (line 142 skipped), so it retries (and re-logs) every tick until fixed. No test pins
  this.
- `policy.json` is written by Go atomically (temp + rename, `compile.go:600-620`) to
  `~/.cache/agent-creance/projects/<hash>/policy.json` — so a partial file never occurs
  on the normal path; a malformed file implies manual edit or a future non-atomic
  writer.

#### B.3 The Go launcher (verified) — why B2 needs new capability

`internal/proxy/lifecycle.go:138` spawns `mitmdump` via `m.proc.Spawn`. `Spawn`
(`internal/sysdep/processmanager.go:45-60`) sets `cmd.Stdout = nil; cmd.Stderr = nil`
(discarded), detaches (`Setsid`), `Start()`s, `Release()`s, and returns the PID — **no
wait, no readiness check**. `Attach` returns immediately and `run.go:152` prints "ready"
meaning only "Spawn returned a PID." The only liveness signal is on a **later** call:
`proxyUp := PID!=0 && Alive(PID) && ports.Probe(port)` (lifecycle.go:122) — a 200ms TCP
dial. So an initial-load failure has nowhere to surface today.

mitmproxy provides a clean failure channel: its `ErrorCheck` addon makes `mitmdump`
**exit non-zero** if any addon logs at ERROR during `load`/`configure`/`running`
(alternatively, raising `OptionsError` from `configure` is re-raised, not swallowed).
The reference shape for the Go side is the integration test's `_wait_until_ready`
(`test_integration.py:121-135`): poll for early process exit + TCP listen.

#### B.4 Test harness (reuse)

`test_enforcer.py`: the `addon` fixture (50-57) instantiates `enforcer.Enforcer()` and
drives `configure` via `taddons.context`; `_https_flow` (66-69) builds flows via
`tflow.tflow(req=tutils.treq(...))`; assertions check `flow.response is None` (allow) vs
`status_code`/`X-Cage-Reason` (deny). `test_hot_reload_picks_up_new_allow` (183-200) is
the exact template to fork for the malformed-reload test (write good → decide →
overwrite malformed → `os.utime` mtime bump → `_maybe_reload()` → assert unchanged).
Module guarded by `pytest.importorskip("mitmproxy")`. The Go process seam:
`FakeProcessManager` (`sysdeptest/processmanager.go`) with `SpawnPID`/`SpawnErr`/
`AlivePIDs` and recorded `Spawned`; `FakePortAllocator.Probe` via a `Listening` map.

#### B.5 Recommended approach (B)

1. **B1 — fail closed on exception.** Wrap the body of `request` in `try/except
   Exception` that sets `flow.response = _make_response(responses.hard_deny(
   flow.request.pretty_url, "<internal enforcer error>"))` — both total functions
   (`responses.py:104-119`, `enforcer.py:59-67`), with no ruleset dependency. For
   `http_connect`, on exception set a hard-deny response (refuse the CONNECT). For
   `tls_clienthello`, on exception **return without** setting `ignore_connection` — so
   TLS terminates and the now-hard-closed `request` path decides. Test by monkeypatching
   `policy.decide` to raise (and by an index-mismatch ruleset) and asserting a 471.
2. **B2 — surface initial-load failure.** Split the load path so an **initial** failure
   (distinct from hot-reload) emits `logging.error(...)` (→ `ErrorCheck` → `mitmdump`
   exits non-zero) while keeping the empty-ruleset fail-closed posture; add a Go-side
   **readiness wait** after `Spawn` in `Attach` that polls until the port listens
   (success) or the process is no longer `Alive` / a timeout elapses (fail with a clear
   error). This detects the non-zero exit without needing stderr capture, but it is the
   one genuinely new mechanism in this ticket and touches the `ProcessManager` seam.
3. **B3 — pin malformed-reload-keeps-last-good** with a pytest forked from the hot-reload
   test. (Optionally suppress repeated identical error logs; not required by the AC.)

**Open design choice for the checkpoint:** scope of the Go-side readiness mechanism for
B2. Because Go always writes a valid `policy.json` atomically *before* spawning, an
initial-load failure is a defensive/rare condition — so the choice is between a full
readiness wait (detect early exit + port probe with timeout, new `ProcessManager`
behavior + fakes) and a lighter "enforcer hard-fails; existing next-attach port probe
notices" posture.

### Work-stream C — host normalization & CONNECT-stage parity

#### C.1 No normalization at the boundary (verified)

Three raw host sources feed the matchers with no lowercase/trailing-dot/port handling:
`http_connect` → `flow.request.host` (`enforcer.py:175`); `tls_clienthello` →
`data.client_hello.sni` (`enforcer.py:188-191`); `request` → `flow.request.pretty_host`
(`enforcer.py:204`). Per mitmproxy 12.x source: `host`/`pretty_host` never carry a port
(port is the separate int `flow.request.port`; `pretty_host` strips it), but a
**trailing dot is preserved**, and SNI is `str | None` (can be `None`) and also
un-normalized. So the live risk is trailing-dot (and uppercase via SNI); port is mostly
a defensive concern (only the raw `host_header`/CONNECT authority can carry one).

#### C.2 The matcher (verified)

`matchHost` (`internal/policy/glob.go:10-24`): lowercases both sides, then `*` /
`*.suffix` (`HasSuffix(host, pattern[1:])`) / exact `pattern == host`. **No trailing-dot
or port handling** — `api.example.com.` fails both exact and suffix. Python `_match_host`
(`policy.py:225-234`) is byte-identical. `Decide` (`match.go:15-42`) gates every rule on
`matchHost`. The `Result.Mode` for a hard-deny carries the deny rule's own (behaviorally
inert) mode — the corpus still asserts it exactly (F22, out of scope here but relevant
to vector authoring).

#### C.3 `host_disposition` divergence (verified)

`host_disposition` (`policy.py:391-409`) is a host-only projection used at CONNECT/TLS:
`deny_reason` = most host-specific **host-level** deny (`r.paths is None`);
`passthrough` = the **top host-rank tier** of matching allows is **all** passthrough
(`all(r.mode == MODE_PASSTHROUGH …)`). This differs from `decide`'s "the **single**
most-specific allow across the full host+path+method ladder is passthrough." The
divergence is structural — no path/method is visible at CONNECT — and the current rule
is **conservative**: any intercept allow in the top tier forces `passthrough=False` (TLS
terminates, the `request` hook decides). There is **no Go `HostDisposition`** (confirmed
by symbol search) and the corpus never drives `host_disposition` in either language.

#### C.4 The shared corpus (verified)

`internal/policy/testdata/decision-vectors/` — 18 `*.json` files, schema
`{name, ruleset, request, expected{decision, mode, matched_rule}}`. Both replays
strict-decode (Go `DisallowUnknownFields`; Python `_EXPECTED_KEYS` set-difference), so a
new `expected.host_disposition` key must be added to **both** in lockstep or both fail.
`TestCorpusNotForked` (`corpus_parity_test.go`) asserts exactly one corpus directory
repo-wide. Replays: Go `vectors_test.go` calls `v.Ruleset.Decide(v.Request)`; Python
`test_vectors.py` calls `policy.decide(rs, req)`. No trailing-dot/port/uppercase/host_
disposition vectors exist.

#### C.5 Recommended approach (C)

1. **C1 + C2 — canonicalize inside the matcher entry points (recommended over
   boundary-only).** Implement a shared, idempotent `canonicalHost`/`canonical_host`
   (lowercase, strip a single trailing `.`, strip a trailing `:port`) in **both**
   `internal/policy` (Go) and `policy.py` (Python), and apply it to the **request** host
   at the entry of `Decide`/`decide` **and** `HostDisposition`/`host_disposition` (rule
   *patterns* are config-validated, not canonicalized at match time; `matchHost` already
   lowercases). This makes the decision-vector corpus directly prove Go↔Python agree on
   `host.`, `host:443`, and uppercase hosts — satisfying "the matcher applies the same
   canonicalization" *and* "the corpus covers these inputs." The alternative — canonical
   only at the enforcer boundary with a documented "matcher assumes canonical input"
   contract — cannot be exercised by the corpus (which calls the matcher directly) and so
   weakens the parity guarantee; not recommended.
2. **C3 — Go `HostDisposition` + corpus coverage.** Add a Go `HostDisposition(host)`
   mirroring `policy.py:391-409`; extend the corpus `expected` schema with an optional
   `host_disposition: {passthrough, deny_reason}` (updating both strict-decoders); extend
   both replays to assert it when present; add vectors for trailing-dot, port, uppercase,
   and a **mixed-mode** host (one passthrough + one intercept allow on the same host)
   proving `passthrough=False`. Keep the conservative "any-intercept-in-top-tier →
   terminate" rule (it favors interception) and **document** `host_disposition` as the
   host-only projection of `Decide`, with `Decide`'s most-specific rule canonical — the
   parity test then pins their agreement.

**Open design choice for the checkpoint:** confirm canonicalization placement
(inside-matcher vs enforcer-boundary-only), since the ticket explicitly offers both.

## Code References

- `internal/profile/profile.go:82-96` — `RenderNetworkSB`, raw label at 89-92
- `internal/profile/profile.go:74-76,116-200` — `%q`/regex-escaped renderers
- `internal/config/validate.go:97-114` — `parseHostService` (label non-emptiness only)
- `internal/config/validate.go:22-45` — `validateRules` (host presence, no method check)
- `internal/policy/compile/compile.go:509-533` — generator rules bypass `validateRules`
- `internal/proxy/enforcer/enforcer.py:198-230` — `request` hook, no try/except, raise at 227
- `internal/proxy/enforcer/enforcer.py:167-196` — `http_connect` / `tls_clienthello`
- `internal/proxy/enforcer/enforcer.py:73-79,137-163` — empty ruleset, load/reload
- `internal/proxy/enforcer/policy.py:139-174` — `decide`; `:391-409` — `host_disposition`
- `internal/proxy/enforcer/responses.py:104-119` — `hard_deny` (total, safe fallback)
- `internal/proxy/lifecycle.go:122,138` — readiness probe / `Spawn` call
- `internal/sysdep/processmanager.go:45-60` — `Spawn` discards stderr, fire-and-forget
- `internal/policy/glob.go:10-24` — `matchHost` (no trailing-dot/port handling)
- `internal/policy/match.go:15-42` — `Decide`
- `internal/policy/testdata/decision-vectors/` — 18-vector shared corpus
- `internal/policy/vectors_test.go` / `internal/proxy/enforcer/test_vectors.py` — replays
- `internal/policy/corpus_parity_test.go` — `TestCorpusNotForked`
- `internal/buildinfo/buildinfo.go:52` — mitmproxy pinned at `12.2.3`

## Architecture / Patterns to follow

- **Tests:** profile → golden + sibling property tests (`make golden`); config → table
  `TestValidate` + golden `TestGoldenErrors`; enforcer → pytest via `addon` fixture
  (`make test-enforcer`); cross-language → the shared corpus (extend both replays in
  lockstep). External tools only under `//go:build integration`.
- **Error style:** `fmt.Errorf("host_services entry %q …")` for per-entry parse;
  `verr.add(...)` for accumulating cross-field validation.
- **sysdep seam (B2):** any new `ProcessManager` capability gets an interface method + a
  `FakeProcessManager` scripted field + `var _ sysdep.X = (*FakeX)(nil)`; never call
  `os/exec` inline.
- **Out-of-tree runtime state** stays in `~/.cache/agent-creance/`.

## Related Research

- `thoughts/shared/research/2026-06-05-AC-0014-seatbelt-profile-compiler.md` — the
  compiler producing `network.sb` (label is cosmetic-comment by design).
- `thoughts/shared/research/2026-06-06-AC-0017-enforcer-decision-engine.md` — enforcer
  decision path + the three wire responses.
- `thoughts/shared/research/2026-06-05-AC-0010-rule-model-matcher.md` — Go↔Python matcher
  parity + the decision-vector corpus contract; notes the matcher compares **verbatim**
  (the deliberate non-normalization that is the F3 gap).
- `thoughts/shared/research/2026-06-19-AC-0053-config-hot-reload.md` — policy hot-reload.
- `thoughts/shared/research/2026-06-22-AC-0057-stream-proxied-responses.md` — the
  `responseheaders` streaming hook (post-decision; confirm it can't un-block a deny).

## Scope boundaries with sibling tickets (from the same review)

- **AC-0059** (F8/F9/F10): include-path confinement, atomic/locked config writes, CA
  verification — not touched here.
- **AC-0060** owns **all audit-log record changes** (F7) plus F11/F12/F14. AC-0058 must
  not change what the audit log records. The host-canonicalization may change the *host
  string* an audit entry shows for a passthrough deny — keep that incidental, not a
  records-format change.
- **AC-0061** (F5/F13): proxy refcount/teardown — the B2 readiness wait must not stray
  into refcount/lock changes.

## Open Questions (for the checkpoint)

1. **A/F18 — method-validation strictness:** uppercase-only (fixes the silent `get`
   miss) vs a closed known-verb allowlist (stricter, risks rejecting `PROPFIND` etc.).
2. **B2 — Go readiness-wait scope:** full readiness wait (detect early exit + port probe
   with timeout; new `ProcessManager` behavior + fakes) vs a lighter "enforcer
   hard-fails; existing next-attach port probe notices" posture, given Go always writes a
   valid `policy.json` before spawn.
3. **C2 — canonicalization placement:** inside the matcher entry points (corpus-provable
   parity, recommended) vs enforcer-boundary-only with a documented "matcher assumes
   canonical input" contract.
4. **Sequencing:** implement/commit as three independent phases A → B → C (recommended,
   matches the ticket's work-stream framing) — confirm there is no preferred order.
