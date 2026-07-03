---
date: 2026-07-03
ticket: AC-0068c
title: "Proxy injection engine — in-memory fd delivery, overwrite, fail-closed, 472"
status: ready
git_commit: d8c8dbf
branch: main
epic: AC-0068
depends_on: [AC-0068a, AC-0068b]
research: thoughts/shared/research/2026-07-02-AC-0068c-proxy-injection-engine.md
---

# Implementation Plan: AC-0068c — Proxy injection engine

## Overview

Turn the static credential model (AC-0068b, in `policy.json`) into live injection by
adding a Go **delivery** half and a Python **injection** half that meet at one
inherited file descriptor:

1. **Go, at proxy spawn:** resolve each credential referenced by an `inject` rule via
   `App.SecretResolver` (AC-0068a) and hand `{name: raw-token}` to `mitmdump` over an
   inherited pipe (fd 3) — never argv/env/disk. Requires a new `ProcessManager` seam
   method (the current `Spawn` has no fd channel). Resolution is **lazy on the spawn
   path only** (never on proxy reuse, so `op read` cannot re-prompt Touch ID).
2. **Python, in the addon:** read the fd once into an in-memory `{name: token}` dict;
   port AC-0068b's Go value-template into the enforcer (mirroring the dual-language
   matcher); teach `policy.py` to read `inject`/`in_cage`/`credentials` and surface the
   matched allow rule; in the request hook **overwrite** the header (or synthesize
   **472**); leave `in-cage` hosts untouched; and annotate a real upstream 401/403 with
   **`X-Cage-Injected`**.
3. **Refusal machinery:** add `STATUS_INJECTION_UNAVAILABLE = 472` next to 470/471,
   with its own reason phrase, `agent_cage_*` error id, and human-recoverable guidance;
   thread the number through the pinned-literal surface (SKILL.md, briefing.md,
   docs/design.md, marker tests, verify battery).

Phantom priming needs **no new code** — the config `env:` block already reaches the
cage; this ticket only proves overwrite clobbers a client-supplied header.

### Decisions locked at the planning checkpoint

- **docs/design.md:** add the 472 entry to the refusal taxonomy (288-327) **now** —
  it is the refusal section, distinct from the credential-injection section AC-0068e
  owns. Keeps "three response types" from going stale for the epic's duration.
- **Delivery channel:** **`ExtraFiles` (fd 3)**, told to the addon via a new
  `--set creance_secret_fd=3` option — not stdin (avoids colliding with mitmdump's
  own stdin/console).

### Locked by the ticket / dependency plans (not re-litigated)

- **Python renders the header value; Go delivers the raw token.** AC-0068b's plan
  fixed this ("the Go renderer is the spec; AC-0068c ports the equivalent into the
  Python enforcer, mirroring the dual-implementation matcher"). Keeps the resolved
  secret out of Go's compiled artifact.
- **472 is per-request, not fail-to-spawn.** The ticket's "deny the request" is
  request-scoped: the proxy still starts (non-inject hosts keep working); only a
  request needing an unresolved credential gets 472.
- **All template shapes work in Python (Bearer gold-plated).** Porting the whole
  template function is the same cost as one shape; Bearer/GitHub is the only path
  validated end-to-end (that validation is AC-0068e).

## Current state

Verified in research (`2026-07-02-AC-0068c`), key anchors:

- `internal/sysdep/processmanager.go:28-71` — `ProcessManager` interface exposes only
  `Spawn(ctx, name, args...)` / `Alive` / `Signal` / `StartTime`. `OSProcessManager.Spawn`
  sets `cmd.Stdin/Stdout/Stderr = nil`, never `cmd.Env`/`cmd.ExtraFiles`, `Start()` +
  `Release()`.
- `internal/proxy/lifecycle.go:466-475` — `mitmArgs` builds argv (`--set creance_policy=…`,
  `--set creance_audit_log=…`, `-q`). `Manager.Attach` (`:129-195`) spawns via
  `m.proc.Spawn` (`:165`) only when the proxy isn't already up; refcounted reuse
  otherwise. `StartConfig` (`:~100`) carries `EnforcerPy`, `Layout`, `Port`.
- `internal/cli/cli.go:50,219-223` — `App.SecretResolver` wired, **unused**.
  `internal/cli/run.go` orchestrates compile + `Attach`; does not reference the resolver.
- `internal/proxy/enforcer/policy.py:73-96` — `Rule.from_dict`/`RuleSet.from_dict`
  ignore `inject`/`in_cage`/`credentials`; `decide` (`:139-197`) sets `Result.matched`
  for the deny path; `load_policy` (`:438-446`).
- `internal/proxy/enforcer/enforcer.py:236-278` — request hook: allow → early return
  (untouched); soft/hard-deny → `_make_response`. `:280-298` `responseheaders`
  (`stream=True`, only place upstream status/headers are visible). `:300-323` audit.
  `:95-112` option declaration/read. No auth header touched anywhere.
- `internal/proxy/enforcer/responses.py:26-119` — `X_CAGE_REASON`, 470/471 constants,
  `CageResponse`, `soft_deny`/`hard_deny`, reason phrases; `_make_response`
  (`enforcer.py:59-67`) applies `resp.reason`.
- `internal/config/template.go` — `RenderCredentialValue`/`validateTemplate` (the spec
  to port). `internal/policy/policy.go:84-149` — `Rule.Inject/InCage`, `Credential`,
  `Compiled.Credentials`. `internal/policy/compile/compile.go:402-418` —
  `validateInjectRefs` fails compile closed on a dangling `inject`.
- Docs: `internal/setup/SKILL.md` (+ `skill_test.go:96-117` markers), frontmatter
  "three egress response types"; `internal/cage/briefing.md` (+ `cage_test.go:175-183`);
  `docs/design.md:288-327` ("three response types"). Verify battery:
  `internal/verify/matrix.go:90-102`, `testdata/fake-agent.sh:93-119`.

## Desired end state

An `inject` rule causes the proxy to overwrite the auth header on that host with a
value rendered from a host-side-resolved token the cage never sees; a request needing
an unresolved credential gets **472** (not forwarded); an `in-cage` host's auth headers
are provably untouched; an injected credential rejected upstream returns the upstream
401/403 plus `X-Cage-Injected: <name>`; the resolved secret never appears in argv, env,
disk, or logs. `make test`, `make test-enforcer`, `make lint`, `make golden`,
`make build` all green in-cage; real-tool paths covered behind the integration tags
out-of-cage.

### Verification (automated, from profile.md)

- `make test` (`go test -race ./...`) green.
- `make test-enforcer` (Python addon unit tests, repo-local venv) green.
- `make lint` (`go vet` + `golangci-lint`) clean; `make fmt` applied.
- `make golden` regenerates any affected goldens; diff reviewed and intentional.
- `make build` at the end so `bin/agent-creance` reflects the final commit.
- Out-of-cage: `make test-integration`, `make test-enforcer-integration`.

## What we are NOT doing

- No CLI to author credentials/bindings (`credential add/list/rm`, `allow --inject`) —
  AC-0068d.
- No opening of `/graphql`, no real GitHub end-to-end, no `docs/design.md`
  *credential-injection section* — AC-0068e (only the 472 refusal entry lands here).
- No unix-socket broker, no Go-side zeroization/`mlock` — Phase 2 (AC-0069b); Python
  holds the secret in memory (accepted v1 trade-off).
- No minted/rotating tokens — Phase 2 (AC-0069a).
- No new phantom-delivery code — the config `env:` block already delivers it.
- No change to `Decide`/matcher semantics, the decision-vector corpus, or the
  transport axis.

## Implementation approach

Bottom-up, each phase independently testable and committed. Go delivery first (1-2),
then Python injection (3-4), then docs/status surface (5), then out-of-cage
integration (6). Phases 1-5 are the in-cage hermetic gate; Phase 6 is out-of-cage.

---

## Phase 1 — `ProcessManager` seam: inherited-fd secret delivery

### Changes

**`internal/sysdep/processmanager.go`**
- Add an exported constant for the child fd number:
  ```go
  // SecretFD is the file descriptor the child inherits the secret pipe on when
  // spawned via SpawnWithSecret. It is 3 because ExtraFiles[0] maps to fd 3
  // (after stdin/stdout/stderr). The caller passes this number to the child out
  // of band (e.g. an addon option), since the payload must not ride argv/env.
  const SecretFD = 3
  ```
- Add to the `ProcessManager` interface:
  ```go
  // SpawnWithSecret is Spawn plus one inherited pipe: the child receives secret
  // on fd SecretFD (read end), and secret is written to the write end then closed
  // (EOF) — never on argv, env, or disk. Used to hand a resolved credential to the
  // mitmdump addon. On any start error the pipe is torn down and a non-nil error
  // returned.
  SpawnWithSecret(ctx context.Context, secret []byte, name string, args ...string) (pid int, err error)
  ```
- Implement on `OSProcessManager`: `os.Pipe()`; `cmd.ExtraFiles = []*os.File{r}`;
  `cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}`; `cmd.Stdin/Stdout/Stderr =
  nil`; `Start()`; close the parent's copy of `r`; write `secret` to `w` and close `w`
  in a goroutine (small payload, but goroutine avoids any pipe-buffer deadlock);
  `Release()`; return pid. On `Start` error, close both ends and return the wrapped
  error. Mirror `Spawn`'s error/format style.

**`internal/sysdep/sysdeptest/` (the ProcessManager fake)**
- Implement `SpawnWithSecret` on the fake: record the delivered `secret` bytes and the
  argv (so tests can assert delivery *and* that the secret is absent from argv), reuse
  the same scripted pid/err path as `Spawn`. Add/confirm the compile-time assertion
  `var _ sysdep.ProcessManager = (*Fake…)(nil)`. (Confirm the fake's exact type name at
  implementation — grep `sysdeptest` for the ProcessManager fake.)

### Tests

- `processmanager_test.go` (or the existing OS test): a **hermetic real-fd** test —
  `SpawnWithSecret(ctx, []byte("s3cr3t"), "/bin/sh", "-c", "cat <&3 > "+tmpfile)` then
  poll the process dead and assert `tmpfile` contains `s3cr3t`. Uses only `/bin/sh`
  (no external tool), proving the child truly receives the bytes on fd 3.
- Fake test in `sysdeptest`: assert `SpawnWithSecret` records the secret, and a
  **hygiene** assertion that the secret string is not among the recorded argv.

### Success criteria

#### Automated
- [x] `go build ./...` — both implementers satisfy the widened interface.
- [x] `make test` green (race) — existing `Spawn` callers/fakes unaffected.
- [x] `make lint` clean.

#### Manual
- [x] The real-fd test proves fd-3 delivery; the secret never appears in argv/env.

---

## Phase 2 — Resolve-at-spawn + delivery wiring (Go)

### Changes

**`internal/proxy` (`StartConfig` + `lifecycle.go`)**
- Add to `StartConfig`:
  ```go
  // Secrets, if non-nil, is invoked ONLY when this attach actually spawns the
  // proxy (never on reuse) to resolve the injection payload host-side. It returns
  // the JSON {credential-name: raw-token} to hand the addon over fd SecretFD.
  // Best-effort: an individual unresolvable credential is omitted (→ 472 at
  // request time), not a hard error.
  Secrets func(ctx context.Context) ([]byte, error)
  ```
- `mitmArgs(port, cfg, withSecret bool)` — append `--set creance_secret_fd=<SecretFD>`
  when `withSecret`. (Adjust the single call site.)
- `Manager.Attach` spawn path (`lifecycle.go:~165`): when about to spawn, if
  `cfg.Secrets != nil` call it; if the returned payload is non-empty, spawn via
  `m.proc.SpawnWithSecret(ctx, payload, proxyBin, mitmArgs(port, cfg, true)...)`, else
  `m.proc.Spawn(ctx, proxyBin, mitmArgs(port, cfg, false)...)`. On a `Secrets` error,
  log a warning and spawn plain (fail-closed: inject-hosts then 472). The **reuse path
  never calls `Secrets`** — this is the Touch-ID-prompt guard.

**`internal/cli/run.go` — first consumer of `App.SecretResolver`**
- Extract a testable helper (new file `internal/cli/inject.go` or in `run.go`):
  ```go
  // resolveInjectionSecrets resolves every credential referenced by an inject rule
  // in the compiled policy to its raw token via the resolver, returning the JSON
  // {name: token} payload for the addon. Unresolvable credentials are logged and
  // omitted (→ 472). Returns nil when no credential is referenced.
  func resolveInjectionSecrets(ctx context.Context, r sysdep.SecretResolver, compiled *policy.Compiled, warn func(string)) ([]byte, error)
  ```
  Logic: collect the set of credential names named by any `Allow`/`DenyAlways` rule's
  `Inject`; for each, look up `compiled.Credentials[name].Source`; `r.Resolve(ctx,
  source)`; on success set `m[name] = string(token)`; on failure `warn(...)` and skip;
  `json.Marshal(m)` (nil/empty → return nil). Never embeds a token in the returned
  error or warning text.
- In `runRun`, set `StartConfig.Secrets = func(ctx) ([]byte, error) { return
  resolveInjectionSecrets(ctx, app.SecretResolver, compiled, warnFn) }`, where
  `compiled` is the already-compiled policy for this run (locate the exact compile/load
  site; do not recompile). Warnings go to stderr with a `warning:` prefix (mirroring
  AC-0068b's dangling-credential warning surface).

### Tests

- `lifecycle` (fake ProcessManager): `StartConfig.Secrets` returning a payload on the
  spawn path → assert `SpawnWithSecret` called with that payload and `mitmArgs` contains
  `creance_secret_fd`; on the **reuse** path assert `Secrets` is **not** invoked (guard
  a bool in the closure).
- `resolveInjectionSecrets` unit test with `FakeSecretResolver.WithSecret`: a
  compiled policy with two inject rules (one resolvable, one not) → payload JSON has the
  resolvable token only, the warn callback fired for the other; hygiene: the resolved
  token never appears in the warning text.
- No credentials referenced → returns nil (proxy spawns plain).

### Success criteria

#### Automated
- [x] `make test` green (race).
- [x] `make lint` clean; `go build ./...`.

#### Manual
- [x] Reuse path never resolves (no Touch ID re-prompt); `App.SecretResolver` now has a
      real consumer (grep shows `run.go`/`inject.go`).

---

## Phase 3 — Python: read the new policy fields + port the value-template

### Changes

**`internal/proxy/enforcer/policy.py`**
- `Rule` dataclass gains `inject: str = ""` and `in_cage: bool = False`; `Rule.from_dict`
  reads `d.get("inject", "")` / `d.get("in_cage", False)`.
- A `Credential` dataclass (`source`, `header`, `template`, `username`) +
  `Credential.from_dict`. `RuleSet` gains `credentials: dict[str, Credential]`;
  `RuleSet.from_dict` reads the top-level `credentials` map. `load_policy` passes it
  through.
- `decide`/`Result`: populate `Result.matched` with the matched **allow** rule on the
  allow path (today it is the deny rule on the deny path). Keep the deny-path
  `.index`/reason lookup intact. The request hook reads `result.matched.inject` /
  `.in_cage`. (Verify `Result`/`_best_match` shapes; add the matched-allow-rule return
  without disturbing the decision-vector outputs.)

**`internal/proxy/enforcer/inject.py` (new) — the ported value-template**
- `render_credential_value(template, username, token) -> str` and
  `validate_template(template, username)` ported 1:1 from `internal/config/template.go`:
  single optional `base64(...)` wrapper (`base64.b64encode`, standard alphabet),
  `{user}` then `{token}` literal replacement, same validation (`{token}` required;
  `{user}` ⇒ username non-empty; ≤1 balanced `base64(`; no unknown `{...}`). Pure, no
  mitmproxy import (keeps golden/parity tests light).

### Tests

- `test_policy.py`: update the "carried but ignored" test → assert `inject`/`in_cage`
  now parse onto `Rule` and `credentials` onto `RuleSet`, **and** that `decide`
  outcomes are still unchanged (matcher parity preserved). Assert `Result.matched` on
  an allow carries the rule's `inject`.
- `test_inject.py` (new): a **parity table** mirroring `template_test.go` — Bearer /
  `token` / bare / `Basic base64({user}:{token})` / custom, plus the error cases —
  rendered with a placeholder token, asserting byte-identical output to the Go spec
  (hard-code the same expected strings the Go test uses).

### Success criteria

#### Automated
- [x] `make test-enforcer` green — new fields parsed, template parity holds.
- [x] Matcher decisions unchanged (Go/Python decision-vector parity still green).

#### Manual
- [x] `render_credential_value` output matches `RenderCredentialValue` for every shape
      (same expected strings in both test suites).

---

## Phase 4 — Python: secret intake, injection/overwrite/in-cage/472, upstream annotation

### Changes

**`internal/proxy/enforcer/responses.py`**
- Add: `STATUS_INJECTION_UNAVAILABLE = 472`; `REASON_INJECTION_UNAVAILABLE =
  "injection-unavailable"`; `ERROR_INJECTION_UNAVAILABLE =
  "agent_cage_injection_unavailable"`; `X_CAGE_INJECTED = "X-Cage-Injected"`;
  `HOW_TO_PROCEED_INJECTION` (tell the user to unlock the secret store / 1Password;
  do NOT `allow`, do NOT retry — the human recovers, not the agent);
  `REASON_PHRASE_INJECTION_UNAVAILABLE = "agent-creance injection-unavailable (credential could not be resolved)"`.
- `injection_unavailable(url, name) -> CageResponse`: 472, body `{error:
  ERROR_INJECTION_UNAVAILABLE, url, credential: name, how_to_proceed}`, headers
  `{Content-Type, X-Cage-Reason: injection-unavailable, X-Cage-Injected: name}`,
  reason phrase.

**`internal/proxy/enforcer/enforcer.py`**
- `load`: declare option `creance_secret_fd` (int, default 0).
- `configure` (or first `running`): if `creance_secret_fd > 0` and not yet read,
  `os.read` the fd to EOF, `json.loads` into `self._secrets: dict[str,str]`, then close
  the fd; guard so it happens once; empty/absent → `self._secrets = {}`. Never log the
  values.
- `request` hook allow branch (replaces the bare `return` at `:257-258`): let
  `rule = result.matched`:
  - `rule.in_cage` → `return` (proxy never touches auth on in-cage hosts).
  - `rule.inject` → `cred = self._ruleset.credentials.get(rule.inject)`;
    `token = self._secrets.get(rule.inject)`; if `cred is None or token is None` →
    `flow.response = _make_response(responses.injection_unavailable(url, rule.inject))`;
    `return`. Else `value = inject.render_credential_value(cred.template, cred.username,
    token)`; `flow.request.headers[cred.header] = value` (dict assignment overwrites any
    client-supplied header, incl. the phantom); `flow.metadata["creance_injected"] =
    rule.inject`; `return`.
  - else → `return` (default).
  Keep the outer `except` fail-closed. A `render_credential_value` error (should not
  happen — validated at compile) is caught and turns into the hard-deny fail-closed
  path already present.
- `responseheaders` hook: if `flow.metadata.get("creance_injected")` and
  `flow.response.status_code in (401, 403)`, set
  `flow.response.headers[responses.X_CAGE_INJECTED] = flow.metadata["creance_injected"]`
  before/with `flow.response.stream = True`.

### Tests

- `test_enforcer.py` / `test_inject.py` (taddons/`tflow`/`tutils`):
  - **overwrite**: request carrying `Authorization: token phantom` to an inject host →
    header replaced by the rendered value; `creance_injected` metadata set; forwarded
    (no `flow.response`).
  - **472**: inject host, secret absent from `self._secrets` → `flow.response` is 472
    with `X-Cage-Reason: injection-unavailable`, body `error =
    agent_cage_injection_unavailable`, `X-Cage-Injected: <name>`.
  - **in-cage**: intercept `in_cage` host, request with an `Authorization` header →
    forwarded untouched (header unchanged, no `flow.response`, no metadata).
  - **default allow**: allow rule without inject/in_cage → untouched (regression).
  - **upstream annotation**: an injected flow whose upstream response is 401/403 →
    `responseheaders` adds `X-Cage-Injected`; a 200 does not.
  - **null-byte host**: an inject rule host with an embedded null in the request host →
    refused by the existing matcher (no injection, no bypass) — confirms the SOCKS5
    lesson already covers injected hosts.
  - **fd intake**: feed a JSON payload through an `os.pipe` to the read path → populates
    `self._secrets`; malformed/empty → `{}` without crashing.

### Success criteria

#### Automated
- [ ] `make test-enforcer` green — overwrite, 472, in-cage-untouched, annotation, and
      fd-intake covered.
- [ ] `make test` green (Go side unaffected).

#### Manual
- [ ] The resolved token appears in no log line; a `curl -v` 472 shows the reason
      phrase + `X-Cage-Reason` (verified in Phase 6 integration).

---

## Phase 5 — Docs + status-code surface (472 + X-Cage-Injected)

### Changes

- **`internal/setup/SKILL.md`**: insert a new numbered section (after hard-deny, before
  the body-blind section; renumber — the marker test is order-agnostic) documenting
  **472 / `X-Cage-Reason: injection-unavailable` / `agent_cage_injection_unavailable`**,
  body fields (`error, url, credential, how_to_proceed`), and the action: *tell the user
  to unlock the secret store (e.g. 1Password / login keychain); do NOT `allow`, do NOT
  retry — this is human-recoverable, agent cannot fix it.* Update the frontmatter
  `description`: "three egress response types" → "four", and add the
  `agent_cage_injection_unavailable = 472 = injection-unavailable` mapping and a note
  that a bare 472 to a body-blind client means the same. Mention `X-Cage-Injected` for
  upstream-rejection diagnosis.
- **`internal/setup/skill_test.go`**: add markers `"472"`,
  `"agent_cage_injection_unavailable"`, `"injection-unavailable"` (and, if asserting the
  frontmatter, the 472 mapping) to the required-marker list.
- **`internal/cage/briefing.md`**: add a third inline parenthetical — "… or HTTP 472
  (injection-unavailable: a credential the proxy injects could not be resolved — tell
  the user to unlock the secret store; do not retry or ask to allow)." Match the compact
  one-clause-per-code style.
- **`internal/cage/cage_test.go`**: add `"472"` to the briefing marker list.
- **`docs/design.md:288-327`**: "three response types" → "four"; add a 472 subsection in
  the 470/471 style (status, `X-Cage-Reason`, body, the human-recoverable action) and a
  sentence on the `X-Cage-Injected` upstream-401/403 annotation. This is the refusal
  taxonomy only — not the credential-injection section (AC-0068e).
- **`internal/verify/matrix.go` + `testdata/fake-agent.sh` + `battery_test.go`**: add a
  472 vector for the adversarial battery. Because a real 472 needs a live resolvable
  failure, gate/observe it in the **integration** battery (Phase 6) rather than the
  hermetic unit battery; if the battery has no integration split, leave a `// AC-0068c`
  note and cover 472 purely in the enforcer tests. (Decide the minimal touch at
  implementation; do not force a fragile unit vector.)

### Tests

- `make test` green (skill/briefing marker tests now require the 472 markers).
- `make golden` — regenerate any doc-adjacent goldens if touched (none expected;
  SKILL.md/briefing.md have no golden, only marker tests). Review diff.

### Success criteria

#### Automated
- [ ] `internal/setup/skill_test.go` and `internal/cage/cage_test.go` pass with the new
      472 markers.
- [ ] `make test`, `make lint`, `make golden` green; `make build` runs.

#### Manual
- [ ] SKILL.md/briefing.md/design.md consistently describe 472 as allowlisted,
      transient, human-recoverable (unlock the secret store), distinct from 470/471.

---

## Phase 6 — Integration (OUT OF CAGE, best-effort)

> Cage note: everything below stands up a real `mitmdump` and/or resolves real
> `op://`/`keychain://`, which cannot run inside the dogfooding cage. Batch it: ask the
> user to close the cage, run this phase, then re-activate. Not part of the in-cage gate.

### Changes

- **`internal/proxy/enforcer/test_integration.py`** (live proxy): extend the existing
  local `http.server` origin + `curl` driver to assert, end-to-end through a real
  `mitmdump` fed a secret over the real inherited fd: (a) the injected header overwrites
  a client-supplied one; (b) a missing secret yields a real **472** with the body/header
  contract; (c) an origin returning 401/403 for the injected request gets
  `X-Cage-Injected`. Under `make test-enforcer-integration`.
- **Go `//go:build integration`** (`internal/proxy`): spawn a real `mitmdump` via
  `SpawnWithSecret` and assert the addon received the payload (e.g. an inject route to
  the local origin echoing the received header). Under `make test-integration`.
- If a 472 verify-battery vector was stubbed in Phase 5, realize it here against the
  live proxy.

### Success criteria

#### Manual / out-of-cage
- [ ] `make test-enforcer-integration` and `make test-integration` green on the host.
- [ ] A real `curl` to an inject host with an unresolvable credential returns 472; with
      a resolvable one the header is overwritten; an upstream 401/403 carries
      `X-Cage-Injected`.

---

## Testing strategy

- **Go pure/wiring** → table + fake-based tests: the `ProcessManager` fake (secret
  delivery + hygiene), `resolveInjectionSecrets` with `FakeSecretResolver`, `Attach`
  spawn-vs-reuse with the fake. The real-fd path gets one hermetic `/bin/sh` test.
- **Python pure** → parity table (`render_credential_value` vs the Go spec, identical
  expected strings) + `policy.py` field-parsing tests, no mitmproxy import.
- **Python hooks** → taddons/`tflow`/`tutils` for overwrite / 472 / in-cage-untouched /
  annotation / fd-intake / null-byte, no live upstream.
- **Docs** → substring-marker unit tests (no goldens for SKILL/briefing).
- **Real tools** → integration tags only, out-of-cage (Phase 6). No external tool in the
  unit/enforcer suites.

## Cage note (in-cage vs out-of-cage)

- **In-cage gate (hermetic):** Phases 1-5 — `make test`, `make test-enforcer`,
  `make lint`, `make golden`, `make build`. The `ProcessManager` fake, `FakeSecretResolver`,
  `env://` via `t.Setenv`, and taddons cover the whole resolve→deliver→inject→472 path
  without a real tool.
- **Out-of-cage (batch when the cage is down):** Phase 6 — live `mitmdump` +
  inherited-fd delivery, real `op://`/`keychain://`, real 472 / `X-Cage-Injected` over
  the wire. Real `gh` + GraphQL is AC-0068e, not here.

## Success criteria (ticket acceptance)

- [ ] Proxy resolves the configured credential at spawn and delivers it over an
      inherited fd; the value never appears in argv, env, disk, or logs (asserted).
- [ ] On an inject-host, an agent-supplied auth header is replaced by the injected value
      (test).
- [ ] A non-resolvable secret yields **472** and the request is not forwarded.
- [ ] 472 is wired into the refusal machinery alongside 470/471 (reason phrase,
      `X-Cage-Reason`/body), clear of ALB's 460/463/464.
- [ ] An injected credential rejected upstream returns the upstream 401/403 plus
      `X-Cage-Injected: <name>`.
- [ ] On an `in-cage` host the proxy provably leaves auth headers untouched.
- [ ] Phantom priming via the `env:` block lets a client issue a request the proxy then
      authenticates (mechanism proven by the overwrite test in-cage; live `gh` is
      AC-0068e).
- [ ] `SKILL.md` and `briefing.md` (and the design.md refusal taxonomy) document 472 and
      the human-recoverable action.
- [ ] `make test` green; addon behavior covered by enforcer tests; secret-handling paths
      unit-tested with the AC-0068a fake; real-tool paths behind `integration`.
- [ ] `make build` run at the end.

## References

- Ticket: `thoughts/shared/tickets/AC-0068c-proxy-injection-engine.md`
- Research: `thoughts/shared/research/2026-07-02-AC-0068c-proxy-injection-engine.md`
- Epic: `thoughts/shared/tickets/AC-0068-credential-injection-phase1-epic.md`
- Dependencies: AC-0068a (`internal/sysdep/secretresolver.go`), AC-0068b
  (`internal/config/template.go`, `internal/policy/policy.go`).
- Prior art: AC-0047 (status-code checklist), AC-0050 (reason phrases / WebFetch
  constraint), AC-0057 (`responseheaders` streaming + local-origin integration driver).
- Discussion: `thoughts/shared/discussions/2026-06-28-credential-injection.md`
