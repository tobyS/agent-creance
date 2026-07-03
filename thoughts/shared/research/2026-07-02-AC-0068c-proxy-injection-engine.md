---
date: 2026-07-02
ticket: AC-0068c
title: "Proxy injection engine — in-memory fd delivery, overwrite, fail-closed, 472"
status: complete
git_commit: 8e9b802d6a6607fcba74d58076b565a24cc1862c
branch: main
epic: AC-0068
depends_on: [AC-0068a, AC-0068b]
related: [AC-0047, AC-0050, AC-0057]
---

# AC-0068c — Proxy injection engine: research

## Research question

With the `SecretResolver` seam (AC-0068a) and the config/policy credential model
(AC-0068b) merged, how does the proxy actually inject a credential end-to-end:
resolve host-side at spawn, deliver the value to the `mitmdump` addon over an
inherited fd (never argv/env/disk), overwrite the client's auth header on
inject-hosts, honor `in-cage`, fail closed with a new status **472** when a secret
won't resolve, prime clients with a phantom token, and annotate upstream 401/403
rejections with `X-Cage-Injected`? Where does every piece live today, and what is
the smallest change that satisfies the acceptance criteria?

## Summary (the shape of the change)

The two dependencies did all the *static* work: AC-0068a wired `App.SecretResolver`
(unused by any command), AC-0068b put `credentials` + per-rule `inject`/`in_cage`
into `policy.json` (carried through, **ignored by both matchers**). AC-0068c is the
*runtime*: a Go delivery half and a Python injection half that meet at one new
inherited file descriptor.

- **Go (host-side, at proxy spawn):** resolve each credential referenced by an
  `inject` rule via `App.SecretResolver`, serialize `{name: raw-token}` to a small
  JSON payload, and hand it to `mitmdump` over an inherited pipe (fd 3). This needs
  a **new `ProcessManager` seam method** — the current `Spawn(ctx, name, args...)`
  has no channel for env/stdin/`ExtraFiles`. `run.go` becomes the first consumer of
  `App.SecretResolver`. Resolution must be **lazy on the spawn path only** (not on
  proxy reuse), because `op read` can trigger a Touch ID prompt.
- **Python (in the addon):** read the fd once at startup into an in-memory
  `{name: token}` dict; port AC-0068b's Go value-template into the enforcer
  (mirroring the existing dual-language matcher); teach `policy.py` to read
  `inject`/`in_cage`/`credentials` and surface the **matched allow rule** to the
  request hook; in the request hook overwrite the header (or synthesize 472); and in
  the `responseheaders` hook annotate a real upstream 401/403 with `X-Cage-Injected`.
- **Refusal machinery:** add `STATUS_INJECTION_UNAVAILABLE = 472` next to
  470/471 in `responses.py`, a new `CageResponse` builder, reason phrase, and
  `agent_cage_*` error id — then thread the number through the same pinned-literal
  surface AC-0047 enumerated (SKILL.md, briefing.md, design.md, marker tests).
- **Phantom priming needs no new code:** the config `env:` block already flows into
  the cage via `buildEnv` → `--env-pass`. AC-0068c only has to prove the overwrite
  clobbers a client-supplied header; live `gh` priming is AC-0068e (out-of-cage).

Complexity is real but bounded: one new seam method, one Go resolution/wiring path,
one ported pure function in Python, focused edits to `policy.py`/`enforcer.py`, a
new 472 response, and doc/marker updates. No change to `Decide` semantics, the
matcher corpus, or the transport axis.

---

## Detailed findings

### 1. Go proxy lifecycle — how `mitmdump` is spawned today

`internal/proxy/lifecycle.go`:
- `mitmArgs(port, cfg)` (`lifecycle.go:466-475`) builds argv: `--listen-host`,
  `--listen-port`, `-s <enforcerPy>`, `--set creance_policy=<PolicyJSON()>`,
  `--set creance_audit_log=<EgressJSONL()>`, `-q`. **All addon config travels as
  `--set` argv options — no env, no stdin, no fds.**
- `Manager.Attach` (`lifecycle.go:129-195`) spawns under a flock only when the proxy
  isn't already up (`proxyUp` = PID alive AND port probe, `:149`): `m.proc.Spawn(ctx,
  proxyBin, mitmArgs(port, cfg)...)` at `:165`, then `waitProxyReady`
  (`:203-216`, 100×50ms port probes). Refcount is the `Agents []agentRef` array in
  the flock-guarded `proxy.lock`; `Detach` (`:221-251`) SIGTERMs the proxy by PID
  when the last agent leaves.
- `proxyBin = "mitmdump"` (`:30`) — bare name resolved via PATH.

The real `exec.Cmd` is built in the seam, **not** in `internal/proxy`:
`OSProcessManager.Spawn` (`internal/sysdep/processmanager.go:56-71`):
```go
cmd := exec.CommandContext(ctx, name, args...)
cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
cmd.Stdin = nil; cmd.Stdout = nil; cmd.Stderr = nil
if err := cmd.Start(); err != nil { ... }
pid := cmd.Process.Pid
_ = cmd.Process.Release()
```
`cmd.Env` is never set (child inherits `os.Environ()` implicitly); `cmd.ExtraFiles`
is never set. **The `ProcessManager` interface (`processmanager.go:28-49`) exposes
only `Spawn/Alive/Signal/StartTime` — there is no parameter for env, stdin, or extra
files.** The one place in the repo that sets `cmd.Env`/real stdio is a *different*
seam, `OSProcessGroup.Start` (`internal/sysdep/processgroup.go:88-89`), for the caged
agent — it still uses no `ExtraFiles`.

The addon reads the policy path from the `--set` option: declared in
`Enforcer.load` (`enforcer.py:95-101`), read in `configure`
(`enforcer.py:110-112`), loaded via `policy.load_policy` → `open(path,"rb")`
(`policy.py:438-446`). The four addon modules are `//go:embed`-ed and materialized to
`<cache>/agent-creance/enforcer/` by `internal/proxy/extract.go` (`Extract()`
`:68-86`), passed as `-s <enforcerPy>`.

**Implication:** delivering a secret over an inherited fd requires **extending the
`ProcessManager` seam** (a new method that sets up a pipe / `ExtraFiles`), plus a way
to tell the addon which fd to read (a new `--set creance_secret_fd=3` option, added
in `mitmArgs` only when there are credentials to deliver). Setsid does not close
inherited fds; Go documents `ExtraFiles[i]` → child fd `3+i`, so fd 3 is stable.

### 2. The enforcer request lifecycle & where injection slots in

`internal/proxy/enforcer/enforcer.py` holds all mitmproxy hooks; the pure matcher is
`policy.py`, the wire bodies `responses.py`, the audit writer `audit.py`.

- **`request(flow)` (`enforcer.py:236-278`)** is the per-request decision. It builds
  `policy.Request(host=flow.request.pretty_host, path=flow.request.path,
  method=flow.request.method)` (`:242-246`), calls `policy.decide(self._ruleset,
  req)` (`:247`), stashes `{decision, rule}` in `flow.metadata["creance_audit"]`
  (`:252-255`), then: **allow → early `return`** (forward untouched, `:257-258`);
  soft-deny → `responses.soft_deny(...)`; hard-deny → looks up
  `self._ruleset.deny_always[result.matched.index].reason` and calls
  `responses.hard_deny(url, reason)`; sets `flow.response = _make_response(r)`. Any
  exception → fail-closed `hard_deny("internal enforcer error")` (`:270-278`).
- **The allow branch currently discards *which* allow rule matched.** For injection
  the hook needs the matched allow rule's `inject`/`in_cage`. `policy.decide` computes
  `_best_match` internally (`policy.py:180-197`) but `Result.matched` is populated for
  the deny path. **`policy.py` must surface the matched allow rule** to the hook.
- **`responseheaders(flow)` (`enforcer.py:280-298`)** sets `flow.response.stream =
  True` for every upstream response. Per AC-0057 research this is the **only** hook
  that sees a genuine upstream response, and `flow.response.status_code` is populated
  there; header mutation is safe (only the body is off-limits once streaming). This
  is where `X-Cage-Injected` on a real 401/403 must be added. Synthesized refusals
  short-circuit in `request` and never reach here.
- **`response(flow)` (`enforcer.py:300-323`)** is the single audit-write point; reads
  only `status_code` + metadata, never the body. A 472 and any annotated status audit
  correctly with no extra work (status is a pass-through int, §5).
- **intercept vs passthrough:** passthrough hosts are tunnelled raw at
  `tls_clienthello` (`enforcer.py:218-234`, `data.ignore_connection = True`) and
  never reach `request`. Injection is therefore intrinsically **intercept-only** —
  consistent with AC-0068b's validation that `inject` requires intercept.
- **No auth header is touched anywhere today.** Allowed requests are forwarded
  byte-for-byte.

**Python currently ignores the new fields** (asserted by
`test_policy.py:110-148`): `Rule.from_dict` (`policy.py:73-81`) reads only
host/paths/methods/mode/reason; `RuleSet.from_dict` (`policy.py:91-96`) reads only
allow/deny_always; `load_policy` drops `credentials`. AC-0068c flips these from
"carried, ignored" to "read and used."

### 3. Refusal / status-code machinery — the 472 blast radius

470/471 are defined **only** in Python — `responses.py:50-51` (`STATUS_SOFT_DENY =
470`, `STATUS_HARD_DENY = 471`). There is **no Go constant**; every Go occurrence is
a string literal, a doc marker, or an opaque audit int. The full `responses.py`
contract (verified verbatim):
- Header: `X_CAGE_REASON = "X-Cage-Reason"`; values `REASON_SOFT_DENY = "soft-deny"`,
  `REASON_HARD_DENY = "hard-deny"` (`:26-28`).
- Error ids: `ERROR_SOFT_DENY = "agent_cage_not_allowlisted"`, `ERROR_HARD_DENY =
  "agent_cage_hard_deny"` (`:31-32`) — the skill keys on the `agent_cage_` prefix.
- Reason phrases `:59-60`; `CageResponse` dataclass `:65-73` (`status, headers, body,
  reason_phrase`); builders `soft_deny` `:83-101`, `hard_deny` `:104-119`.
- `_make_response` (`enforcer.py:59-67`) applies `resp.reason = r.reason_phrase`.

Decision **names** are duplicated in Go: `internal/audit/entry.go:25-29`
(`DecisionAllow/SoftDeny/HardDeny`) mirror `policy.py:46-48`. `Summarize` has an
`Unknown` bucket for future decisions (`summary.go:79-81`) — forward-compatible.

**A new 472 must touch (AC-0047's checklist, `research 2026-06-12` §):**
- `responses.py`: `STATUS_INJECTION_UNAVAILABLE = 472`, `REASON_INJECTION_UNAVAILABLE`,
  `ERROR_INJECTION_UNAVAILABLE = "agent_cage_injection_unavailable"`, a
  `HOW_TO_PROCEED_INJECTION` telling the human to unlock the secret store (NOT
  `allow`), a `REASON_PHRASE_*`, and an `injection_unavailable(...)` builder.
- Python tests: `test_responses.py`, `test_enforcer.py` (and a new inject test file).
- Go verify battery: `internal/verify/matrix.go` (Vectors currently `"470:soft-deny"`
  / `"471:hard-deny"`, `:90-102`), `internal/verify/testdata/fake-agent.sh`
  (produces the `code:reason` observation, `:93-119`), `battery_test.go`. A real 472
  vector needs a resolvable-failure scenario → **integration-tagged** (see §6).
- Audit: no change — `status` is a pass-through int (`audit/entry.go:46-54`,
  `format.go:19-26` prints `-> <status>`), 472 flows through; `format_lines.golden`
  only changes if a fixture emits 472 (none needs to).
- Docs: `internal/setup/SKILL.md` (+ marker test `skill_test.go:96-117`),
  `internal/cage/briefing.md` (+ marker test `cage_test.go:175-183`),
  `docs/design.md:288-327` ("three response types" → four).

**WebFetch constraint (AC-0050):** only the status *number* reaches a body-blind
client; the reason phrase and `X-Cage-*` headers do not. So 472 (the number) is the
signal to WebFetch; `X-Cage-Injected` only helps header-visible clients (curl, `gh`)
— which is exactly the upstream-401/403 case.

### 4. Config → cage env, the credential pipeline, and the value-template

- **Phantom priming is already delivered.** `config.Env` (`config.go:34`) →
  `mergeEnv` (`merge.go:197-209`) → `buildEnv` copies it in first, then the ten
  computed proxy/CA vars overwrite (`cage.go:311-336`) → its keys join `--env-pass`
  (`cage.go:152-154`) → safehouse forwards into the cage. A user writing
  `env: {GH_TOKEN: <phantom>}` gets it into the agent with **zero new code**.
  AC-0068c's job is only to prove the proxy overwrites a client-supplied header.
- **Compiled `policy.json` shape** (`policy.go`): top-level `credentials` map of
  `{source, header?, template, username?}` (`Credential` `:112-117`,
  `CredentialsFromConfig` `:122-136`); per-rule `inject`/`in_cage` (`Rule` `:84-94`,
  copied by `RuleFromConfig` `:257-272`); `Compiled.Credentials` (`:144-149`).
  `validateInjectRefs` (`compile.go:402-418`) already fails the compile closed if an
  `inject` names an undefined credential — **no resolved value is ever in the
  artifact** (only the reference).
- **Go value-template = the spec Python must port** (`internal/config/template.go`):
  `RenderCredentialValue(template, username, token)` — locate an optional single
  `base64(...)` wrapper; substitute `{user}` then `{token}` (literal `ReplaceAll`);
  if wrapped, `base64.StdEncoding`-encode the substituted inner and splice.
  `validateTemplate` enforces: must contain `{token}`; `{user}` ⇒ username non-empty;
  at most one balanced `base64(...)`; no unknown `{...}`. Supported shapes: `Bearer
  {token}`, `token {token}`, `{token}`, `Basic base64({user}:{token})`, custom
  header. This mirrors the existing dual-language matcher (Go `policy` ↔ Python
  `policy.py`): **Go delivers the raw token; Python renders the header value** from
  the token + the credential metadata in `policy.json`. That keeps the resolved
  secret out of Go's compiled artifact and localizes rendering next to injection.
- **`App.SecretResolver`** (`cli.go:50`, built in `Main()` `:219-223`) is wired but
  **unused by any command** — `run.go` does not reference it. AC-0068c is its first
  consumer. The seam resolves `op://`/`keychain://`/`env://` (`secretresolver.go`),
  and `FakeSecretResolver` (`sysdeptest/secretresolver.go`, `WithSecret(ref, value)`)
  is ready for the resolution-path tests.

### 5. Prior art to reuse (AC-0047 / AC-0050 / AC-0057)

- **AC-0047** established custom codes because "the status code is the only signal
  WebFetch always surfaces verbatim"; 470/471 chosen clear of ALB's 460/463/464 and
  RFC-9110-safe. **472 is free and in-band.** AC-0047's research §95-138 is the
  exhaustive pinned-literal checklist for adding a status code.
- **AC-0050** added the reason phrases and the `CageResponse.reason_phrase` +
  `_make_response` seam; and recorded the WebFetch constraint (§3).
- **AC-0057** added `responseheaders` streaming; its research pins that
  `responseheaders` is the only place a real upstream status/headers are visible and
  mutable, and that refusals never reach it. It also introduced a local
  `http.server` origin + `curl -N` driver in `test_integration.py` — reusable to
  drive a local 401/403 origin and assert `X-Cage-Injected`.

### 6. Cage note — what runs in-cage vs out-of-cage

- **In-cage (hermetic, the gate):** all Go unit tests (`make test`, race), the Python
  enforcer unit tests (`make test-enforcer`, taddons/`tflow`/`tutils`), golden
  regeneration, `make lint`, `make build`. The `ProcessManager` fake, the
  `FakeSecretResolver`, and `env://` (via `t.Setenv`) cover the whole
  resolve→deliver→inject path without a real tool.
- **Out-of-cage / integration (`//go:build integration` + `make
  test-enforcer-integration`):** a live `mitmdump` spawned with a real inherited fd;
  real `op://`/`keychain://` resolution; a real 472 end-to-end and the
  `X-Cage-Injected` annotation over a live proxy. Real `gh` + GitHub GraphQL is
  **AC-0068e**, not here. Batch these when the cage is down.

---

## Open questions from the ticket — resolved

- **Exact phantom shape `gh` accepts / `GH_TOKEN` vs `GITHUB_TOKEN`:** `gh` reads
  both; `GH_TOKEN` takes precedence and is the more specific, recommended name. With
  it set, `gh` skips the "logged out" check and sends the request; `gh` does **not**
  locally format-validate the token before sending — it puts the string into the
  `Authorization` header and sends it. Because the proxy **overwrites the whole
  header** on inject-hosts, the phantom's exact scheme/shape is immaterial to
  security; any non-empty placeholder primes `gh`. Recommend `GH_TOKEN` with a
  clearly-fake, plausibly-shaped value in the config `env:` block (documented, not
  code). **Confirmed by the go-gh source** (`pkg/auth/auth.go`, `pkg/api/http_client.go`):
  `tokenForHost` returns the raw env value with no prefix/length/charset check (only a
  non-empty/non-whitespace requirement), `GH_TOKEN` outranks `GITHUB_TOKEN` and bypasses
  the keyring, and `gh` makes no startup auth call. `gh` emits `Authorization: token
  <TOKEN>` (classic scheme) for **both** REST and GraphQL via one shared transport —
  but because the proxy overwrites the whole header, the phantom's scheme/shape is
  cosmetic and the credential's own `template` (Bearer/token) governs the upstream
  value. Live `gh`+GraphQL confirmation remains AC-0068e (out-of-cage). One AC-0068e
  topology note: go-gh only attaches the header when the request host matches the
  configured GitHub host (`isSameDomain`), so all `gh` egress must reach canonical
  GitHub hostnames through the proxy.
- **How the addon reads the inherited fd / secret lifetime:** read fd 3 once during
  the first `configure` (or at `running` before serving) to EOF, `json.loads` into an
  in-memory dict held for the proxy's lifetime; Go writes the payload in a goroutine
  and closes the write end to signal EOF. Python holds it in memory (accepted v1
  trade-off; Go-side zeroization is Phase 2 / AC-0069b).
- **Reuse of the refusal-rendering path for 472:** yes — new `CageResponse` builder
  in `responses.py`, emitted through `_make_response` from the `request` hook, header
  set directly in the `CageResponse.headers` dict (it does not pass
  `responseheaders`).
- **Null-byte / normalized-host hardening on injected hosts:** injected hosts are
  ordinary allow rules matched by the same `policy.decide`, which canonicalizes the
  host; the existing normalization/null-byte rejection therefore already applies. No
  new hardening — add a test asserting an embedded-null host to an inject rule is
  refused by the existing matcher (no injection, no bypass).

## tce Config Drift

None found. `profile.md` and `tickets.md` match the repo: the stack (Go 1.26 CLI +
embedded Python mitmproxy addon), the commands (`make test`, `make test-enforcer`,
`make lint`, `make golden`, `make build`), the code map (`internal/proxy/`,
`internal/proxy/enforcer/`, `internal/sysdep/`, `internal/policy/`, `internal/cred/`,
`internal/setup/`, `internal/cage/`), the tmt ticket layout, and the commit
convention are all current.

## Impact analysis — files this ticket will touch

**Go (host-side delivery):**
- `internal/sysdep/processmanager.go` — new seam method for fd/pipe delivery
  (+ `sysdeptest` fake + tests; secret-hygiene assertion: not in argv/env).
- `internal/proxy/lifecycle.go` — `mitmArgs` gains `--set creance_secret_fd=…` when
  secrets present; `Attach` spawn path calls the new method with a lazily-resolved
  payload.
- `internal/proxy/*.go` (`StartConfig`) — carry a lazy secret-provider closure.
- `internal/cli/run.go` — first consumer of `App.SecretResolver`; build the provider
  from the compiled policy's `inject`-referenced credentials.

**Python (in-addon injection):**
- `internal/proxy/enforcer/policy.py` — read `inject`/`in_cage`/`credentials`;
  surface the matched allow rule.
- new enforcer template module — ported `RenderCredentialValue` (+ parity tests).
- `internal/proxy/enforcer/enforcer.py` — read the secret fd; request-hook
  overwrite / in-cage no-op / 472; `responseheaders` 401/403 → `X-Cage-Injected`.
- `internal/proxy/enforcer/responses.py` — 472 builder + constants.
- `internal/proxy/enforcer/test_*.py` — inject/overwrite/in-cage/472/annotation +
  template parity + policy-load (now read).

**Docs + status-code surface:**
- `internal/setup/SKILL.md` (+ `skill_test.go` markers), `internal/cage/briefing.md`
  (+ `cage_test.go` markers), `docs/design.md:288-327`.
- `internal/verify/matrix.go` / `testdata/fake-agent.sh` / `battery_test.go` — a 472
  vector (integration-tagged).

## References

- Ticket: `thoughts/shared/tickets/AC-0068c-proxy-injection-engine.md`
- Epic: `thoughts/shared/tickets/AC-0068-credential-injection-phase1-epic.md`
- Discussion: `thoughts/shared/discussions/2026-06-28-credential-injection.md`
- Dependencies: `thoughts/shared/plans/2026-06-29-AC-0068a-secretresolver-seam.md`,
  `thoughts/shared/plans/2026-07-02-AC-0068b-config-injection-model.md`
- Prior art: AC-0047 (webfetch-visible-refusals), AC-0050 (refusal-reason-phrases),
  AC-0057 (stream-proxied-responses) tickets/research/plans.
- Code anchors: `internal/proxy/lifecycle.go:466-475,129-195`,
  `internal/sysdep/processmanager.go:28-71`, `internal/proxy/enforcer/enforcer.py`
  (hooks), `internal/proxy/enforcer/policy.py:73-96,139-197,438-446`,
  `internal/proxy/enforcer/responses.py:26-119`, `internal/config/template.go`,
  `internal/policy/policy.go:84-149`, `internal/policy/compile/compile.go:402-418`,
  `internal/cli/cli.go:50,219-223`, `internal/cli/run.go`, `internal/cage/cage.go:311-336`,
  `internal/cred/cred.go`, `docs/design.md:288-327`.
