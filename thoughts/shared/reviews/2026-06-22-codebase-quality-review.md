---
date: 2026-06-22T09:00:12Z
git_commit: a71e1480b13c37f97f6064fb26bb46a7babce9af
branch: main
ticket: N/A
review_scope: "Whole-codebase review: code quality, best practices, test coverage and test meaningfulness"
status: complete
---

# Code Review: agent-creance codebase (quality, best practices, test meaningfulness)

**Date**: 2026-06-22
**Reviewer**: Claude
**Ticket**: N/A (custom whole-codebase scope)
**Branch**: main
**Commit**: a71e148

## Review Scope

A full-codebase review of `agent-creance` for code quality, best-practice adherence,
test coverage, and — specifically requested — whether the tests are *meaningful*
(assert real behavior vs. smoke-test). The codebase is ~14.7k lines of non-test Go
across 53 packages, ~15.3k lines of Go tests, a ~1.3k-line Python mitmproxy enforcer
addon with its own ~1k-line test suite, and 23 testscript `.txtar` CLI tests.

The review was conducted by reading the design doc (`docs/design.md`) in full, then
dispatching six parallel deep-dive analyses across the security-critical subsystems:
the policy engine, the Python enforcer, the proxy lifecycle/concurrency, the cage/SBPL
profile generation, config/credential/setup, and a test-meaningfulness sweep of the
remaining packages. **The headline Critical findings (F1–F4) I then verified directly
against the source** rather than relying on the sub-analyses.

## Executive Summary

This is a mature, unusually well-disciplined codebase. The architecture is clean (thin
`main`, `App`-injected composition root, every OS call behind a `sysdep` interface with
fakes), and the test suite is genuinely strong: a 1:1 test-to-code ratio, a
cross-language decision-vector corpus shared by the Go matcher and the Python enforcer,
golden-file tests for every generated artifact, hermetic testscript CLI tests, and — the
rare part — most tests assert real behavior, not tautologies. The deny-precedence is
structurally guaranteed, credential detection is fail-safe, the CA cancelled-dialog trap
is caught, and the generator's path-scoping (the anti-tenant-hijack logic) is tested
adversarially. The team's stated testing philosophy is actually followed.

That said, for a tool whose entire value proposition is a kernel- and proxy-enforced
security boundary, the review found **four genuine fail-open / boundary-bypass issues**
that matter more than their line count suggests, because each one quietly defeats a
guarantee the tool advertises. The two most important are a **Seatbelt (SBPL) injection
via the cosmetic `host_services` label** (a crafted config re-opens all network egress)
and the **Python enforcer `request` hook failing open on an unhandled exception** (any
bug in the decision path forwards the request upstream). Neither is exotic; both are a
few-line fix plus a test.

The secondary theme is that the **fail-closed and adversarial paths are systematically
under-tested** even where the happy paths are well covered: there is no test that a
malformed/mid-write `policy.json` reload keeps the last-good ruleset, no test that an
enforcer exception fails closed, no SBPL-escaping/injection test, and the load-bearing
"real curl validates the CA against the system store" property is only ever exercised
through a fake. The behavior is often correct; it just isn't pinned, so a future
refactor could silently break it.

### Priority Findings

#### Critical (Must Fix)

- **F1 — SBPL injection via `host_services` label.** `internal/profile/profile.go:91`
  writes the label raw into `network.sb` after a `;; ` comment, and
  `internal/config/validate.go:103` accepts any non-empty label including newlines. A
  config entry `- "x\n(allow network*):3306"` (YAML double-quoted, real newline) renders
  a live `(allow network*)` line *after* the deny baseline, re-opening all outbound
  egress and defeating the core network guarantee. **Verified directly.** The design
  already treats the project config as an attacker-influenced surface (that is why
  `config-ro.sb` exists), so this is in-scope for the threat model.

- **F2 — Enforcer `request` hook fails OPEN on an unhandled exception.**
  `internal/proxy/enforcer/enforcer.py:198` has no `try/except`. mitmproxy's addon
  manager logs an exception raised in a `request` hook and **continues** — and "continue"
  for the `request` hook means the flow is forwarded upstream (no `flow.response` was
  set). Any unexpected exception in the decision path (a malformed flow, a stale index, a
  future code change) becomes an *allowed* egress. **Verified directly** (no guard
  present; the `deny_always[result.matched.index]` access at line 227 is one concrete
  raise site). For a fail-closed security proxy this is the cardinal sin.

- **F3 — Host not normalized before matching → `deny_always` host bypass.** The enforcer
  feeds `flow.request.host` / `pretty_host` / SNI to the matcher verbatim
  (`enforcer.py:175,204`), and `matchHost` (`internal/policy/glob.go`) does only a
  lowercased string/suffix compare. A trailing-dot host (`api.example.com.`, which DNS
  resolves identically) does not equal `api.example.com`, so a host-level `deny_always`
  is **bypassed**, and an exact-host allow spuriously soft-denies. A port in the host
  authority would similarly mismatch. **Verified directly.** Fail-open on the hard
  boundary.

#### Important (Should Fix)

- **F4 — `host_disposition` (CONNECT-stage decision) has no cross-language parity
  coverage and can diverge from `Decide`.** The enforcer makes a *separate* host-only
  decision at `http_connect`/`tls_clienthello` via `policy.host_disposition`
  (`enforcer.py:176,191`) whose passthrough rule ("every allow in the top host-rank tier
  is passthrough") differs from `Decide`'s ("the single most-specific allow is
  passthrough"). The shared decision-vector corpus — the entire "the two
  implementations never disagree" guarantee — drives only `decide`/`Decide`, never
  `host_disposition`, in either language. **Verified the separate path exists.** A
  mixed-mode host could be tunnelled raw (path denies silently inert) where the operator
  expects interception, with no test catching it.

- **F5 — Detach not guaranteed in the post-`Run` teardown window.**
  `internal/cli/run.go:153` registers `mgr.Detach` only as a bare `defer`. **Verified,
  but narrower than first flagged:** during `cage.Run` (`internal/cage/run.go:62`)
  SIGINT/SIGTERM *are* intercepted and forwarded, so the common Ctrl-C case tears down
  cleanly and repeated Ctrl-C does not kill the wrapper. The residual gap is the window
  *after* `Run` returns — `signal.Stop` (run.go:63) has unregistered the handler, so a
  signal arriving during `watcher.Stop()` + `Detach` reverts to default (terminate) and
  the proxy is leaked with a stale agent PID. Self-heals on the next run (dead-PID
  prune). Defense-in-depth, but untested.

- **F6 — Enforcer behavior on initial policy-load failure / malformed reload is correct
  but unguarded and untested.** On a missing/corrupt initial `policy.json` the addon runs
  on the empty `RuleSet` from `__init__` (`enforcer.py:74,137`) — which fails closed
  (deny-all) only by luck of the default, with no health signal to the Go launcher. A
  malformed hot-reload correctly keeps last-good and retries next tick, but **neither
  property has a test** — the single most important behavior of a fail-closed proxy is
  asserted only by inspection.

- **F7 — Audit URL redaction is a name-denylist; the doc claims header filtering that
  doesn't exist.** `internal/proxy/enforcer/audit.py:56` redacts a fixed set of
  query-param names; a credential under any unlisted param (`session`, `jwt`,
  `x-amz-signature`, …) lands in the audit log in clear. Separately, `docs/design.md:506`
  promises "sensitive headers filtered before logging," but the implementation logs **no
  headers at all** — safer than the spec, but doc and code are out of sync.

- **F8 — `include:` paths are unconfined.** `internal/config/load.go` resolves
  `include: /etc/passwd`, `include: ~/.ssh/...`, and `include: ../../../x` verbatim, and
  this is currently tested *as supported*. For a confinement tool, a repo-supplied
  `.agent-creance.yaml` being able to pull in any user-readable absolute/`~`/`..` path
  (and surface its contents in parse-error messages) deserves an explicit confinement
  check or a documented trust decision. Cycle detection and depth limits themselves are
  correct and well-tested.

- **F9 — `allow`/`deny`/`import` writes are not atomic or locked.** The in-memory append
  + re-validation gate (`internal/config/edit.go`) is robust, but two concurrent
  `agent-creance allow/deny` (or one racing the user's editor) can lose a write via
  read-modify-write — and a dropped `deny_always` is the dangerous direction. Needs an
  atomic temp+rename (and ideally a flock) in the CLI write path.

- **F10 — CA live-verification soundness is untested where it matters.** The cancelled-
  dialog trap (exit 0 but untrusted) is correctly caught and tested through the fake
  prober (`internal/setup/setup.go:287`), but the load-bearing property — that the real
  `OSTLSProber.ProbeViaProxy` validates against the *system* trust store with no
  `-k`/`--cacert`/`--proxy-insecure` — is never exercised by a real curl. If the real
  prober is lenient, the entire CA verification passes spuriously. Audit `OSTLSProber`
  and add an integration test that an untrusted CA yields `ProbeUntrusted`.

- **F11 — Generator: package name not escaped into the registry URL.**
  `internal/generator/registry/registry.go:166` (`validatePackage`) blocks filesystem
  traversal but not the URL charset, and `packagist.go:18` concatenates the name raw into
  the request path. A hostile `require` key in a cloned repo's `composer.json` (`vendor/
  pkg?x=...`) is not `url.PathEscape`d. Tighten to a registry-name charset / escape each
  segment, with an adversarial test.

- **F12 — Generator: bare-host homepage on a shared apex allowlists the whole host.**
  `internal/generator/homepage.go:24` emits a host-wide allow when a package's `homepage`
  is a bare host. That is safe for subdomain-isolated platforms but not for shared-apex
  hosts (`pages.dev`, `surge.sh`, …) — the exact tenant-isolation the path-scoping logic
  exists to prevent. No known-shared-host handling and no adversarial test.

- **F13 — Recycled *agent* PID pins the proxy alive forever.** `lifecycle.go` correctly
  distrusts a bare proxy PID (adds a TCP port probe) but prunes attached agent PIDs by
  `kill -0` alone (`pruneDead`). On macOS PID recycling, a stale-but-"alive" agent entry
  keeps `len(alive) > 0`, preventing last-out teardown indefinitely (proxy leak) and
  blocking `clean` without `--force`. Agents need a second identity factor (e.g. a
  start-token in the lock).

- **F14 — `FakeFileSystem` is too lenient on parent-dir semantics.**
  `internal/sysdep/sysdeptest/filesystem.go`: `WriteFile` succeeds with a missing parent
  and `MkdirAll` doesn't create parents — so a real "forgot to MkdirAll before WriteFile"
  bug (the exact ordering the registry atomic-write depends on) cannot be caught by any
  unit test using the fake. `Remove` is also a silent no-op on a missing path (diverges
  from `os.Remove`).

- **F15 — No adversarial/escaping tests for SBPL generation.**
  `internal/profile/profile_test.go` golden-tests exact output and bans literal IPs, but
  never feeds a label/path containing `"`, `\`, `\n`, `)`, or `;;`. The path escaping
  via Go `%q` is currently correct but incidental — a refactor swapping `%q` for `%s`
  would pass every test. (This is the test gap behind F1.)

#### Minor (Consider Fixing)

- **F16 — `FormatEntry` can panic on an over-width decision string.**
  `internal/audit/format.go:21` does `strings.Repeat(" ", decisionWidth-len(e.Decision))`,
  which panics on a negative count; golden tests only feed known ≤9-char decisions. Clamp
  with `max(0, …)`.
- **F17 — `audit follow` truncate-in-place is unhandled and untested.**
  `internal/audit/follow.go:75` keys on `os.SameFile`; an in-place truncate keeps reading
  past EOF and silently emits nothing. Handle via size-shrink detection or document it as
  unsupported.
- **F18 — Validation barely checks hosts/methods.** `validate.go:25` only rejects an
  empty host; `host: " "`, `host: "http://x/y"`, and `methods: [FOOBAR, get]` all pass.
  Add hostname/glob + uppercase-method checks (the enforcer's method match is
  case-sensitive, so `get` silently never matches).
- **F19 — Several untested error branches:** `status.Scan`'s per-project lock-read error
  (`internal/status/status.go:53`), `doctor/report.go` `renderProxy`'s no-proxy vs
  proxy-down branches (`:147`/`:157` — a label swap would pass), `doctor.checkProxy`'s
  swallowed resolve/inspect errors. All security-adjacent reporting paths.
- **F20 — Weak testscript assertions.** `clean_idempotent.txtar:13` proves idempotency
  only via the repeated message, never that state was purged; `status_lists.txtar:11`
  doesn't assert the agents-count value. Both would pass if the underlying behavior
  regressed.
- **F21 — Same-second mtime reload miss.** `enforcer.py:154` compares `getmtime`
  inequality; two writes in the same wall-clock second on a 1s-resolution path are
  invisible until a later write. Largely mitigated on APFS (sub-second), so Minor.
- **F22 — `Result.Mode` carries a meaningless mode for hard-denies**
  (`internal/policy/match.go:29`), baking a non-behavioral field into the cross-language
  vector contract.

#### Positive Observations

- **Architecture & seams.** Thin `cmd/agent-creance/main.go`; `App`-injected composition
  root; every OS side effect behind a `sysdep` interface with a fake. Commands take
  `*App`, not globals. The discipline is real and consistent.
- **Deny precedence is structurally guaranteed**, not just tested — `deny_always`
  shadows `allow` by construction in both `match.go` and `policy.py`, and merge dedupe is
  pointer-aware so a `deny_always` is never dropped during config union
  (`TestMerge_RuleDedupeIsPointerAware`).
- **Cross-language corpus.** The shared decision-vector corpus replayed by both
  `vectors_test.go` and `test_vectors.py`, plus `TestCorpusNotForked`, is exactly the
  right way to keep two implementations honest (gap: it doesn't yet cover
  `host_disposition` — F4).
- **Generator path-scoping is tested adversarially.** `forge_test.go` pins the exact
  repo-scoped companion paths (`api.github.com/repos/<org>/<repo>/`, `<org>.github.io/
  <repo>/`) and flags `objects.githubusercontent.com` as host-wide/lower-trust — a
  regression that broadened any would fail.
- **Credential detection is fail-safe** (`cred.go`): locked keychain, file-fallback, and
  missing item all refuse with a clear message; only a present keychain item returns OK.
- **CA cancelled-dialog trap** (`setup.go:287`) re-verifies after install and never
  infers trust from `add-trusted-cert`'s exit code — with a test.
- **Config edit safety** (`edit.go`): comment/format-preserving splice + a
  parse-and-diff re-validation gate that refuses any edit it can't prove is exactly
  old+new.
- **Registry caching** is a behaviorally-tested state machine (fresh→0 calls,
  stale/absent→refetch, atomic-write rollback, path-traversal rejection).
- **`audit follow` rotation** has a real end-to-end test (`follow_test.go:91`) with an
  actual rename asserting no lost entries.
- **File mode 0600** on the audit log is enforced with an explicit `fchmod` (defeats
  umask) and tested.

## Detailed Findings

### Completeness Assessment

Not a ticket review, so there is no acceptance-criteria checklist to map. Against the
design doc, the v0.1 scope is substantially implemented and the deliberate v0.1 cuts
(config cage deferred to AC-0046, OAuth-only, no secret injection) are honestly
documented in both the doc and the code comments. The gap between doc and code worth
reconciling is F7 (the "headers filtered" claim).

### Code Quality

High. Functions are small and single-purpose, naming is consistent, comments explain
*why* (often citing the ticket that introduced a constraint) rather than restating the
code. The largest file (`policy/compile/compile.go`, 620 lines) is a justified
orchestration hub. The one systemic code-quality risk is **unescaped/unvalidated
interpolation into generated security artifacts** — F1 (SBPL label) is the live instance,
and F11 (registry URL) is the same class in the network layer. A small "all external
strings crossing into a generated artifact must be escaped/validated" rule, enforced by
adversarial tests (F15), would close the category.

### Integration

Excellent fit with the codebase's own conventions — new behavior reuses the `sysdep`
seam, golden files, and testscript harness rather than working around them. No drive-by
direct `os/exec` or keychain calls were found in logic packages.

### Test Coverage & Meaningfulness

This was the focus of the request. The verdict: **coverage is broad and most tests are
meaningful**, with a consistent blind spot on **fail-closed and adversarial paths**:

- Fail-closed untested: malformed/mid-write policy reload keeping last-good (F6), enforcer
  exception failing closed (F2), SBPL escaping (F15), CA real-curl validation (F10).
- Adversarial inputs untested: trailing-dot/port host (F3), `host_disposition` parity
  (F4), hostile package name (F11), shared-apex homepage (F12), `..`/absolute includes
  (F8, treated as supported).
- Fake fidelity (F14): `FakeFileSystem`'s flat model hides parent-dir-ordering bugs.
- A handful of weak assertions (F20) and untested error branches (F19) that would pass a
  regression.

None of these undermine the suite's overall value — the happy paths and the wire
contracts are well pinned — but for a security tool the *fail-closed* path is the product,
and it is the least-tested.

### Security Review

Covered inline above. Ranked: F1 (SBPL injection), F2 (enforcer fail-open), F3 (host-deny
bypass) are the three that directly defeat a stated guarantee and should be fixed first.
F4, F8, F9, F10, F13 are real but narrower or require an attacker-influenced precondition.

### Performance Considerations

No concerns. The input-hash cache makes steady-state runs near-instant, the enforcer's
mtime poll is cheap, and the one latency note (proxy `Spawn` under the flock serializes
concurrent first-starts) is an accepted, correct trade-off for first-start atomicity.

## Recommendations

### Immediate Actions
- [ ] **F1** — reject control chars/newlines in `host_services` labels in
      `parseHostService`, and defensively sanitize in `RenderNetworkSB`; add an
      injection test.
- [ ] **F2** — wrap the enforcer `request` hook body in `try/except` that sets a
      hard-deny response on any exception; test with a raising matcher.
- [ ] **F3** — normalize the request host (strip trailing dot, lowercase, strip port)
      once at the enforcer boundary before `decide`/`host_disposition`; add corpus
      vectors for `host.` and `host:443`.
- [ ] **F10** — audit `OSTLSProber.ProbeViaProxy` for `-k`/`--cacert`/`--proxy-insecure`
      and add an integration test that an untrusted CA is reported untrusted.

### Future Considerations
- [ ] **F4** — add a Go `HostDisposition` + host-disposition vectors to the shared
      corpus so the CONNECT-stage decision is parity-checked.
- [ ] **F6** — surface a hard startup failure when the *initial* policy load fails; add a
      malformed-reload-keeps-last-good test.
- [ ] **F5/F13** — install a signal-aware teardown so Detach runs in the post-`Run`
      window; give attached agent PIDs a second identity factor.
- [ ] **F9** — atomic + locked write path for `allow`/`deny`/`import`.
- [ ] **F8** — decide and enforce (or explicitly document) `include:` path confinement.
- [ ] **F11/F12** — escape registry package names into URLs; handle shared-apex
      homepages.
- [ ] **F14** — make `FakeFileSystem` model missing-parent semantics.
- [ ] **F15** — add SBPL escaping/injection tests; **F7** — reconcile the audit doc with
      the no-headers reality and broaden param redaction.
- [ ] Minor: F16 (clamp `FormatEntry`), F17–F22 as capacity allows.

## Files Reviewed

- `internal/profile/profile.go`, `compile.go` — SBPL fragment generation (F1, F15)
- `internal/proxy/enforcer/{enforcer,policy,responses,audit}.py` — live egress filter
  (F2, F3, F4, F6, F7, F21)
- `internal/policy/{policy,match,glob}.go`, `compile/compile.go`, `render/render.go` —
  policy engine (F3, F4, F22)
- `internal/proxy/lifecycle.go`, `internal/cli/run.go`, `internal/cage/run.go`,
  `internal/configwatch/configwatch.go` — lifecycle/concurrency (F5, F13)
- `internal/config/{load,merge,validate,edit}.go` — config (F8, F9, F18)
- `internal/cred/cred.go`, `internal/setup/setup.go` — credential/CA (F10)
- `internal/generator/**`, `registry/**` — allowlist generators (F11, F12)
- `internal/audit/{read,follow,format,summary}.go` — audit read side (F16, F17)
- `internal/status/**`, `internal/doctor/**`, `internal/claudeimport/**`,
  `internal/portscan/**` — reporting (F19)
- `internal/sysdep/sysdeptest/**` — fakes (F14)
- `internal/cli/testdata/script/*.txtar` — CLI behavior tests (F20)

## References

- Design: `docs/design.md`
- Decision-vector corpus: `internal/policy/testdata/decision-vectors/`
- Project profile: `.claude/tce/profile.md`
