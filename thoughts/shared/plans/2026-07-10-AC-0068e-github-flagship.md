# AC-0068e: GitHub Flagship — Open `/graphql` Token-Scoped, End-to-End Validation, Docs

## Overview

Close GH-1. The credential-injection substrate (AC-0068a–d) is shipped and complete;
this ticket binds it to real GitHub: allowlist `POST api.github.com/graphql` with an
injected repo-scoped fine-grained PAT, bind the existing `api.github.com` REST rules to
the same credential, prove the whole path against a live upstream and across concurrent
proxies, and write the `docs/design.md` credential-injection section.

No production Go or Python code changes. The deliverables are **config**, **docs**, and
**tests**.

## Current State Analysis

AC-0068a–d are all `Status: Done`. The engine resolves a secret host-side at proxy
spawn, ships it over inherited fd 3, overwrites the auth header on the matched rule,
fails closed with 472, and annotates upstream 401/403 with `X-Cage-Injected`. All of it
is unit-tested, and end-to-end tested in Python against a *local echo origin*
(`internal/proxy/enforcer/test_integration.py:561-598`).

What is missing is exactly the ticket's title:

- `.agent-creance.yaml` has no `credentials:` block, no `env:` block, and no
  `/graphql` rule. Its three `api.github.com` allow rules (lines 22, 89, 92) carry no
  auth axis.
- No test anywhere drives real GitHub.
- No test anywhere covers concurrent proxies holding distinct secrets, despite that
  being an epic-level acceptance criterion.
- `docs/design.md:533` still asserts the project "does no credential injection".

### Key Discoveries:

- **Injection rides the *matched* rule, and the matcher picks the most-specific one.**
  `_apply_injection` returns early on `if not rule.inject`
  (`internal/proxy/enforcer/enforcer.py:358-359`), and `_best_match` selects by
  specificity, not order (`internal/proxy/enforcer/policy.py:221-238`). A host-wide
  `api.github.com` inject rule would be shadowed by the existing path-scoped rules and
  never fire.
- **`gh` sends one `Authorization` header for both REST and GraphQL.** Once
  `GH_TOKEN=<phantom>` primes the cage, any `api.github.com` rule without `inject`
  forwards the phantom and earns a real 401. Every rule `gh` traverses must be bound.
- **Secrets resolve on the spawn path only** (`internal/proxy/lifecycle.go:173-185`).
  Editing the config while a cage runs hot-reloads the rules but not the secret map, so
  `api.github.com` answers 472 until the proxy respawns.
- **`gh` performs no token format check** — `go-gh`'s `tokenForHost` requires only a
  non-empty, non-whitespace value, and `GH_TOKEN` outranks both `GITHUB_TOKEN` and the
  keyring (`thoughts/shared/research/2026-07-02-AC-0068c-proxy-injection-engine.md:252-266`).
  The phantom's shape is cosmetic.
- **`gh issue create` needs `Contents: Read`** because its GraphQL query selects
  `defaultBranchRef` ([cli/cli#12798](https://github.com/cli/cli/issues/12798)) — a
  `gh`-specific over-ask beyond the REST equivalent.
- **The Manager-spawned proxy uses the default mitmproxy confdir.** `mitmArgs` sets no
  `confdir` (`internal/proxy/lifecycle.go:495-507`), so the CA a Go test must trust is
  `$HOME/.mitmproxy/mitmproxy-ca-cert.pem` — the pattern
  `internal/sysdep/tlsprober_integration_test.go:57-69` already uses.
- **Test gating conventions**: `exec.LookPath` for tools, `AC_TEST_*` env var to supply
  a live reference (`internal/sysdep/secretresolver_integration_test.go:31-45`), and
  assert-presence-only on secrets (`require.NotEmpty(..., "…") // never print it`).

## Desired End State

Running `agent-creance run` in this repo yields a cage in which the tce workflow's `gh`
operations succeed against `tobyS/agent-creance` over GraphQL, using a fine-grained PAT
the agent never holds. A prompt-injected agent that sets its own `Authorization` header
on `api.github.com` has it clobbered. A locked 1Password produces a 472 telling the
human to unlock, not a silent unauthenticated request. Two concurrent project cages
inject their own tokens with no shared state. `docs/design.md` describes all of it.

Verification: the ticket's six acceptance criteria, checked by `make test-integration`
(with `AC_TEST_GITHUB_TOKEN_REF` set) plus the manual `gh` matrix in Phase 5.

## Resolved Decisions (from the question checkpoint)

1. **Rule binding**: bind each existing `api.github.com` rule with `inject: github`, and
   add a new `/graphql` POST rule. Keeps the path allowlist as an independent defense
   layer on top of token scope.
2. **Token source**: `op://Personal/fjv2nwlg4tdjpzfafo5ditavxa/token`.
3. **Dogfooding config**: commit both the inject rules and the `credentials:` block to
   the project config. A contributor without the secret item gets a 472 with the
   actionable unlock message — the designed failure mode.
4. **Tests**: Go `//go:build integration` for the real-GitHub path (it exercises the Go
   `Secrets` closure → `SpawnWithSecret` path the Python tests only mirror); Python
   `pytest -m integration` for concurrency (needs no real GitHub).
5. **Help text**: the criterion is satisfied by the existing `Long:`/`Example:` doc
   surfaces AC-0068d shipped. Do **not** add a literal `docs/design.md` path to help —
   no command in this codebase does that, and doing so would set a precedent the rest
   do not follow.

## What We're NOT Doing

- No minted short-lived tokens (GitHub App installation / OAuth2 refresh) — AC-0069a.
- No unix-socket secret broker — AC-0069b.
- No gold-plating of the custom-header or Basic shapes; they stay template-only.
- No non-GitHub flagship.
- **No removal or narrowing of the existing REST paths.** They are bound, not tightened.
- No change to `agent-creance init`'s generated git allowlist (AC-0055) to emit a
  `/graphql` + `inject` rule for new projects. That is a genuine follow-up — file it.
- No change to `internal/cred/` credential detection or `doctor`'s credential
  preconditions (AC-0062).
- No production Go/Python code changes: the engine is done.

## Implementation Approach

Order is dictated by one constraint: **development happens inside an agent-creance cage,
and Phase 4's config edit severs that cage's own GitHub egress until it respawns.** So
all hermetic work (docs, test authoring) lands first, the config edit is the last in-cage
step, and everything requiring a real proxy, a real `op`, or real GitHub is batched into
a single out-of-cage phase.

Phases 1–3 are independently committable and leave the tree green. Phase 4 is committable
but changes runtime behavior. Phase 5 runs nothing new — it executes and records.

---

## Phase 1: `docs/design.md` — the credential-injection section

### Overview

Correct the false claim at line 533 and add the section AC-0068c/d explicitly deferred
here. Cross-reference the 472 refusal entry rather than duplicating it.

### Changes Required:

#### 1. Correct the stale v0.1 claim

**File**: `docs/design.md` (line 533)
**Changes**: Rewrite the paragraph that opens **"v0.1 is OAuth-only (Claude Pro/Max), and
does no credential injection."** It must now say that proxy-side injection ships (Phase 1:
static references, Bearer gold-plated, GitHub validated), point forward to the new
section, and keep the true remainder — that Claude Code's own OAuth token is *not*
injected and remains readable in the cage, because the agent is the thing using it.

#### 2. Add the section

**File**: `docs/design.md`
**Changes**: New `## Credential injection` section between `## The proxy and the
credential story` (529) and `## Tech stack` (551). Contents, in order:

- **The two orthogonal axes** — transport (`intercept|passthrough`) × auth
  (`inject: <name>` XOR `in_cage: true`), the auth axis meaningful only on intercepted
  hosts. `inject` on a passthrough rule is rejected at validation
  (`internal/config/validate.go:65-67`).
- **`inject` semantics** — overwrite (clobbers any client-supplied value of that header)
  and fail-closed. Overwrite is what neutralizes the cage-wide keychain read: a
  prompt-injected agent that reads a broad token cannot exceed the injected scope
  against that host.
- **`in_cage` semantics** — the proxy guarantees it never adds, strips, or modifies auth
  headers. The honest marker for SigV4, `op`, and SDK-minted credentials.
- **The value template** — `Bearer {token}`, `token {token}`, bare `{token}`,
  `Basic base64({user}:{token})`, custom header names. Bearer is the validated shape.
- **Host-side, in-memory delivery** — resolved once at proxy spawn via `SecretResolver`
  (`op://` / `keychain://` / `env://`), handed to the addon over inherited fd 3; never
  argv, never env, never disk. Never re-resolved on the reuse path (Touch ID).
- **Failure signalling** — 472 + `X-Cage-Injected`, cross-referencing the refusal
  taxonomy at `docs/design.md:323-334` rather than restating the JSON body.
- **Phantom priming** — why the cage needs `GH_TOKEN=<phantom>`, and that `gh` performs
  no format check so the phantom may be self-describing.
- **⚠ Binding a host with narrower rules** — the most-specific-rule gotcha: because the
  auth axis rides the matched rule, *every* allow rule for an injected host must carry
  `inject`, or the unbound one forwards the phantom. This is new documentation of a real
  trap and belongs here prominently.
- **Per-project scoping** — the proxy is refcounted per project (state-dir hash); N cages
  = N proxies = N independently resolved tokens, no shared state.
- **The GitHub recipe** — fine-grained PAT with repository access limited to the target
  repo, **Metadata: Read** (mandatory/auto) + **Issues: Read and write** + **Contents:
  Read** (for `gh issue create`'s `defaultBranchRef`, cli/cli#12798). Note that
  `gh auth status` works but cannot report scopes (fine-grained PATs emit no
  `X-OAuth-Scopes`), and that `GH_NO_UPDATE_NOTIFIER=1` suppresses the update check's
  unallowlisted call.
- **Trade-offs accepted** — Python holds the secret in memory with weak zeroization
  (broker is Phase 2); static tokens are repo-scoped but not time-bounded until Phase 2;
  Claude's own token stays readable in the cage.

#### 3. Commands reference

**File**: `docs/design.md` (the `## Commands` block, 419-488)
**Changes**: Add `credential add` / `list` / `remove` and the `allow --inject <name>` /
`allow --in-cage` flags. Match the existing terse one-line-per-command style.

#### 4. Config schema example

**File**: `docs/design.md` (the `## The configuration` YAML block, 77-105)
**Changes**: Add a `credentials:` block and show the per-rule `inject:` / `in_cage:`
axis, plus an `env:` phantom entry.

### Success Criteria:

#### Automated Verification:
- [ ] `make test` green (docs-only; asserts no regression)
- [ ] `make lint` green

#### Manual Verification:
- [ ] `docs/design.md` no longer claims the project does no credential injection
- [ ] The new section reflects the *shipped* design, not the discussion's aspirations —
      cross-check each claim against `internal/proxy/enforcer/enforcer.py`
- [ ] The most-specific-rule trap is documented with a worked YAML example
- [ ] No duplication of the 472 JSON body already at `docs/design.md:323-334`

---

## Phase 2: Go integration test — real GitHub end-to-end

### Overview

The only test in the project that drives a real upstream through a real proxy with a real
resolved secret. It covers acceptance criteria 1, 2, and 3 in one file, and exercises the
Go `Secrets` closure → `SpawnWithSecret` → fd-3 path that the Python suite only mirrors.

### Changes Required:

#### 1. New integration test

**File**: `internal/proxy/inject_github_integration_test.go` (new)
**Changes**: `//go:build integration`, package `proxy_test`, following
`internal/proxy/lifecycle_integration_test.go` exactly.

Gates (skip, never fail):

```go
if _, err := exec.LookPath("mitmdump"); err != nil {
	t.Skip("mitmdump not installed; skipping real-GitHub injection test")
}
ref := os.Getenv("AC_TEST_GITHUB_TOKEN_REF")
if ref == "" {
	t.Skip("set AC_TEST_GITHUB_TOKEN_REF to a readable op:// / keychain:// / env:// " +
		"reference for a fine-grained PAT to exercise this test")
}
// The Manager-spawned proxy uses the default confdir, so trust ~/.mitmproxy's CA.
caPath := filepath.Join(home, ".mitmproxy", "mitmproxy-ca-cert.pem")
if _, err := os.Stat(caPath); err != nil {
	t.Skip("no mitmproxy CA at ~/.mitmproxy; run `agent-creance setup` first")
}
```

A `startInjectProxy(t, secrets func(context.Context) ([]byte, error)) int` helper that,
per subtest, gets its own `t.TempDir()` + `t.Setenv("XDG_CACHE_HOME", …)` so each gets a
distinct project state dir and therefore a fresh **spawn** (the reuse path never
resolves secrets):

```go
lay, _ := state.New(sysdep.OSPathResolver{}).Resolve(t.TempDir())
enforcerPy, _ := proxy.NewExtractor(sysdep.OSFileSystem{}, sysdep.OSPathResolver{}).Extract()
// policy.json: credentials{github} + allow[api.github.com /graphql POST inject github]
mgr := proxy.NewManager(sysdep.OSFileSystem{}, sysdep.OSFlock{}, sysdep.OSProcessManager{},
	sysdep.OSPortAllocator{}, sysdep.OSSleeper{}, os.Stderr)
att, err := mgr.Attach(ctx, proxy.StartConfig{
	Layout: lay, EnforcerPy: enforcerPy, PolicyHash: "gh", SelfPID: os.Getpid(),
	Secrets: secrets,
})
t.Cleanup(func() { _ = mgr.Detach(lay, os.Getpid()) })
require.Eventually(t, func() bool { return sysdep.OSPortAllocator{}.Probe(att.Port) },
	10*time.Second, 100*time.Millisecond, "proxy did not come up")
```

The policy is the compiled shape the addon reads (mirror
`internal/proxy/enforcer/test_integration.py:549-558`; prefer marshalling
`policy.Compiled` if its exported fields line up, so the test cannot drift from the Go
types):

```json
{
  "version": 1,
  "credentials": {
    "github": {"source": "<ref>", "header": "Authorization", "template": "Bearer {token}"}
  },
  "allow": [
    {"host": "api.github.com", "paths": ["/graphql"], "methods": ["POST"],
     "mode": "intercept", "inject": "github"}
  ],
  "deny_always": []
}
```

An `httpsThroughProxy(t, port, caPath, authHeader string) (*http.Response, []byte)`
helper posting the GraphQL probe, **always** setting a phantom `Authorization` so every
subtest also proves overwrite:

```go
body := `{"query":"query { repository(owner:\"tobyS\", name:\"agent-creance\") { name } }"}`
req.Header.Set("Authorization", "Bearer ghp_phantom_the_proxy_overwrites_this")
tr := &http.Transport{
	Proxy:           http.ProxyURL(&url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(port)}),
	TLSClientConfig: &tls.Config{RootCAs: poolFrom(caPath)},
}
```

Three subtests, one proxy each:

1. **`real token → 200, overwrite proven`.** `Secrets` resolves the live ref via the
   production resolver, exactly as `internal/cli/cli.go:220-224` wires it:
   ```go
   r := sysdep.OSSecretResolver{Commander: sysdep.ExecCommander{}, Keychain: sysdep.OSKeychain{}, Paths: sysdep.OSPathResolver{}}
   tok, err := r.Resolve(ctx, ref)
   if errors.Is(err, sysdep.ErrSecretToolMissing) { t.Skip("`op` is not installed") }
   if errors.Is(err, sysdep.ErrKeychainLocked)    { t.Skip("secret store is locked") }
   return json.Marshal(map[string]string{"github": string(tok)})
   ```
   Assert `200`, and `data.repository.name == "agent-creance"`. The client sent a phantom
   and GitHub still answered — that *is* the overwrite proof against a real upstream
   (AC 1 + AC 2). Never log the token.

2. **`unresolvable credential → 472`.** `Secrets` returns `[]byte("{}")`. Assert status
   `472`, header `X-Cage-Reason: injection-unavailable`, header `X-Cage-Injected: github`,
   and body `.error == "agent_cage_injection_unavailable"` (AC 3, first half).

3. **`invalid token → upstream 401 + annotation`.** `Secrets` returns
   `{"github":"ghp_invalid_token_for_testing"}`. Assert status `401` (the upstream owns
   it) and header `X-Cage-Injected: github` (AC 3, second half).

Secret hygiene: assert `require.NotEmpty(t, tok, "expected a non-empty resolved secret")`
with a `// never print it` comment, per
`internal/sysdep/secretresolver_integration_test.go:45`.

### Success Criteria:

#### Automated Verification:
- [ ] `go vet -tags=integration ./...` passes (the tagged file compiles)
- [ ] `make test` green — the tagged test is excluded and nothing regresses
- [ ] `make lint` green
- [ ] `go test -tags=integration ./internal/proxy/ -run TestInjectGitHub` **skips**
      cleanly when `AC_TEST_GITHUB_TOKEN_REF` is unset

#### Manual Verification:
- [ ] (Phase 5, out-of-cage) all three subtests pass against real GitHub
- [ ] The resolved token never appears in test output, even on failure

---

## Phase 3: Python integration test — concurrent per-project scoping

### Overview

Acceptance criterion 4 ("concurrent project cages each authenticate with their own
scoped token — validated") has no test anywhere. The design claims it "falls out for
free" from the per-project proxy; this proves it. It needs a real `mitmdump` but **not**
real GitHub, so it belongs in the existing Python harness.

### Changes Required:

#### 1. Concurrency test

**File**: `internal/proxy/enforcer/test_integration.py`
**Changes**: Append to the `# --- credential injection end-to-end (AC-0068c) ---`
section. Reuse `_INJECT_POLICY` (its host is `127.0.0.1`, and `canonical_host` strips the
port, so one policy matches both origins), `running_proxy_with_secret` (:501-546) and
`_echo_origin` (:461-498) unchanged.

```python
def test_concurrent_proxies_hold_distinct_secrets_e2e():
    """Two per-project proxies resolve and hold their own token — no shared state.

    The multi-project claim (discussion 2026-06-28, "four repos = four proxies = four
    scoped tokens") reduced to its mechanism: each proxy gets its own fd payload, so
    each origin must see only its own proxy's token (AC-0068e).
    """
    with _echo_origin() as origin_a, _echo_origin() as origin_b:
        with running_proxy_with_secret(_INJECT_POLICY, {"gh": "TOKEN_PROJECT_A"}) as pa, \
             running_proxy_with_secret(_INJECT_POLICY, {"gh": "TOKEN_PROJECT_B"}) as pb:
            _, code_a, _, body_a = _curl(pa, f"http://127.0.0.1:{origin_a}/echo", use_mitm_ca=False)
            _, code_b, _, body_b = _curl(pb, f"http://127.0.0.1:{origin_b}/echo", use_mitm_ca=False)

    assert code_a == "200" and code_b == "200"
    assert json.loads(body_a)["authorization"] == "Bearer TOKEN_PROJECT_A"
    assert json.loads(body_b)["authorization"] == "Bearer TOKEN_PROJECT_B"
    # No cross-talk: neither proxy's secret map leaked into the other.
    assert "TOKEN_PROJECT_B" not in body_a
    assert "TOKEN_PROJECT_A" not in body_b
```

Add a second, sharper assertion that the secret is bound to the **proxy**, not the
origin: route origin B's URL through proxy A and confirm it receives
`Bearer TOKEN_PROJECT_A`. That distinguishes "each proxy holds its own token" from "each
origin happens to get the right one".

### Success Criteria:

#### Automated Verification:
- [ ] `make test-enforcer` green (the new test is `-m integration`, so excluded here)
- [ ] `make test` green

#### Manual Verification:
- [ ] (Phase 5, out-of-cage) `make test-enforcer-integration` runs the new test and it
      passes, with the two proxies genuinely concurrent (both alive at once)
- [ ] The existing three injection tests at `:561-598` still pass unchanged

---

## Phase 4: Dogfooding config — bind GitHub to the injected credential

### Overview

The deliverable that closes GH-1 for this repo.

> **Correction (2026-07-10, during implementation).** The plan assumed this was the last
> *in-cage* step. It cannot be: the cage renders
> `(deny file-write* (literal "<project>/.agent-creance.yaml"))`
> (`internal/profile/profile.go`, `RenderConfigReadOnlyFragment`), so a caged agent may
> not rewrite its own egress policy — exactly the property AC-0059/AC-0060 exist to
> preserve. Attempting the edit in-cage fails with `EPERM`. **This phase therefore runs
> at the start of the out-of-cage batch (Phase 5), not before it.** That is also the
> better sequencing: it keeps the cage breakout to a single window.

Once applied, the recompile hot-reloads the new inject rules into any *running* proxy,
which holds no `github` secret, so `api.github.com` answers 472 until that proxy
respawns. Harmless here (this repo's `origin` is SSH, so `git` does not traverse the
proxy), but it is why the config edit and the cage restart belong together.

### Changes Required:

#### 1. Credentials, phantom, and update-notifier suppression

**File**: `.agent-creance.yaml`
**Changes**: Add two new top-level blocks (both are known schema keys —
`internal/config/config.go:34-35`, `:152-153`).

```yaml
env:
  # gh refuses to send a request when "logged out", so prime it with a phantom the
  # proxy overwrites at egress. gh performs no token format check (go-gh's
  # tokenForHost requires only a non-empty value), so this is deliberately
  # self-describing rather than a realistic-looking token.
  GH_TOKEN: "ghp_phantom_the_proxy_overwrites_this_at_egress"
  # The update notifier calls an api.github.com path we do not allowlist (470 noise).
  GH_NO_UPDATE_NOTIFIER: "1"

credentials:
  github:
    source: op://Personal/fjv2nwlg4tdjpzfafo5ditavxa/token
    template: "Bearer {token}"
```

#### 2. Bind every GitHub API rule

**File**: `.agent-creance.yaml`
**Changes**: Add `inject: github` to each rule for a host `gh` sends its token to, and
add the new `/graphql` rule. Leave paths and methods untouched — the path allowlist stays
as an independent layer.

- line 22 — `api.github.com`, `paths: ["/repos/tobyS/agent-creance/"]` → `+ inject: github`
- line 89 — `api.github.com`, `paths: ["/repos/tobyS/agent-creance"]`, `methods: [GET, POST, PATCH, PUT, DELETE]` → `+ inject: github`
- line 92 — `api.github.com`, `paths: ["/user", "/rate_limit"]`, `methods: [GET]` → `+ inject: github` (this is what keeps `gh auth status` working)
- line 95 — `uploads.github.com`, `paths: ["/repos/tobyS/agent-creance"]`, `methods: [POST]` → `+ inject: github` (a `*.github.com` host `go-gh` will attach the token to)
- **new** —
  ```yaml
  - host: api.github.com
    paths: ["/graphql"]
    methods: [POST]
    inject: github
    reason: "GH-1: GraphQL is one endpoint; the repo-scoped token is the boundary (AC-0068e)"
  ```

Leave `github.com`, `raw.githubusercontent.com`, `codeload.github.com`, and
`objects.githubusercontent.com` **unbound**: they serve this public repo unauthenticated,
and `git` here is SSH. Add a comment saying so, since an unbound `*.github.com` rule now
looks like the trap Phase 1 documents.

### Success Criteria:

#### Automated Verification:
- [ ] `agent-creance policy refresh` (or any recompile) succeeds — `validateInjectRefs`
      (`internal/policy/compile/compile.go:402-418`) resolves `github`
- [ ] `agent-creance policy show` lists the `/graphql` rule with its `inject` annotation
- [ ] `agent-creance credential list` shows `github` with shape `Bearer {token}` and no value
- [ ] `make test` green (the repo's own config is not a test fixture)
- [ ] `make lint` green

#### Manual Verification:
- [ ] Every `api.github.com` allow rule carries `inject: github` — grep the file and
      count; a single unbound one silently breaks `gh` under the phantom
- [ ] The committed `source:` is a *reference*, never a value
- [ ] Understood and accepted: the running cage's `api.github.com` now returns 472 until
      it is restarted

---

## Phase 5: Out-of-cage validation batch

### Overview

Everything that cannot run inside the cage, batched into one breakout as
`CLAUDE.md` requires. It now **also carries Phase 4's config edit** (see that phase's
correction note), which is the only thing written here; the rest executes, observes, and
records results into the plan's status file and the ticket.

**Ask the user to close the cage before starting, and tell them when it can come back up.**

### Steps:

#### 0. Apply Phase 4's config edit

With the cage down, `.agent-creance.yaml` is writable. Apply the `env:`, `credentials:`,
and per-rule `inject: github` changes exactly as Phase 4 specifies, then verify with
`agent-creance policy show` and `agent-creance credential list` before going further.

#### 1. Verify the PAT (prerequisite)

`op read op://Personal/fjv2nwlg4tdjpzfafo5ditavxa/token` must return a **fine-grained**
PAT (`github_pat_…`), not a classic one (`ghp_…`). If it is classic, or if its
permissions are wrong, create a fine-grained PAT first:

- Repository access: **only** `tobyS/agent-creance`
- Metadata: Read (mandatory, auto-added)
- Issues: Read and write
- Contents: Read *(required by `gh issue create` — cli/cli#12798)*

Store it at that op reference. A broad classic PAT would make the whole test pass while
proving nothing about scoping.

#### 2. Automated suites

```
AC_TEST_GITHUB_TOKEN_REF='op://Personal/fjv2nwlg4tdjpzfafo5ditavxa/token' make test-integration
```

This runs `go test -race -tags=integration ./...` (Phase 2's three subtests) then chains
`make test-enforcer-integration` (Phase 3's concurrency test).

#### 3. Real `gh` matrix through a real cage

`make build`, then `bin/agent-creance run` in this repo, and inside the cage:

| Check | Expectation |
|---|---|
| `gh auth status` | authenticated; cannot report scopes (fine-grained PAT) |
| `gh issue list -R tobyS/agent-creance` | succeeds over `/graphql` |
| `gh issue create -R tobyS/agent-creance --title "AC-0068e smoke" …` | succeeds (proves `Contents: Read`) |
| `gh issue comment <n> --body …` | succeeds |
| `gh issue edit <n> --add-label …` | succeeds |
| `gh issue close <n>` | succeeds |
| `agent-creance logs` | shows `POST api.github.com/graphql` allowed; **no** `Authorization` header logged |

Close and delete the smoke issue afterwards — it is a real write to a real repo.

#### 4. Adversarial checks

- **Overwrite**: in-cage, `curl -H 'Authorization: Bearer ghp_bogus' -X POST
  https://api.github.com/graphql -d '{"query":"{viewer{login}}"}'` → `200`. The agent's
  own header was clobbered.
- **472**: `op signout`, start a *fresh* cage (resolution is spawn-only), run `gh issue
  list` → the request fails with 472 and the unlock guidance. `op signin`, restart, works.
- **Broad-token containment**: with a broad classic PAT exported as `GH_TOKEN` inside the
  cage, `gh` still cannot exceed the injected fine-grained scope against `api.github.com`
  — it is overwritten. (Optional but the sharpest demonstration of the epic's thesis.)

#### 5. Concurrency, for real

Start a cage in this repo and a second cage in another project configured with a
different credential; confirm from each `agent-creance logs` that each authenticated with
its own token and neither saw the other's.

### Success Criteria:

#### Automated Verification:
- [ ] `make test-integration` green with `AC_TEST_GITHUB_TOKEN_REF` set
- [ ] `make test` green
- [ ] `make lint` green
- [ ] `make build` run so `bin/agent-creance` reflects the final commit

#### Manual Verification:
- [ ] Every row of the `gh` matrix passes in-cage
- [ ] The overwrite, 472, and upstream-401 checks behave as documented
- [ ] Two concurrent cages authenticate with distinct tokens
- [ ] `agent-creance logs` never contains a token or an `Authorization` header
- [ ] The smoke issue is closed/deleted

---

## Testing Strategy

### Unit Tests:
None. No production code changes; the engine's unit tests already cover template
rendering, matcher specificity, and config validation.

### Integration Tests:
- **Go** (`internal/proxy/inject_github_integration_test.go`, new): real `op` → real
  `mitmdump` → real `api.github.com/graphql`. Covers overwrite against a live upstream,
  472 on an unresolvable credential, and upstream 401 + `X-Cage-Injected`.
- **Python** (`internal/proxy/enforcer/test_integration.py`, extended): two concurrent
  proxies, two secrets, two local echo origins; no real GitHub.

### Manual Testing Steps:
See Phase 5, steps 3–5. The `gh` matrix is the acceptance evidence for the ticket's first
criterion and cannot be automated without a live token *and* a live cage.

## Performance Considerations

None. Injection adds one dict lookup and one header assignment per allowed request
(`internal/proxy/enforcer/enforcer.py:360-371`). Secret resolution happens once per proxy
spawn, deliberately off the per-request path (`op read` is 200–500ms and rate-limited).

## Migration Notes

Committing Phase 4 changes runtime behavior for anyone running this repo in a cage:

- A **cage restart is required.** Until the proxy respawns it holds no `github` secret and
  `api.github.com` answers 472. The 472 body already tells the human exactly what to do.
- A contributor **without** the 1Password item gets 472 on GitHub, not a compile failure —
  `resolveInjectionSecrets` warns and omits an unresolvable credential rather than failing
  the spawn (`internal/cli/inject.go:44-47`). Non-GitHub egress is unaffected.
- Anyone relying on the cage's `gh` reading a broad keychain token loses that path, by
  design: `GH_TOKEN` outranks the keyring.

## Follow-ups to file

- `agent-creance init`'s git-allowlist generator (AC-0055) still emits unbound
  `api.github.com` REST rules. Post-AC-0068e that is the documented trap. It should emit a
  `/graphql` rule and an `inject:` placeholder, or warn.
- A lint/`doctor` check for "intercepted host with some rules bound and others not" would
  catch the trap mechanically. The `in_cage` axis was introduced partly to enable exactly
  this kind of lint.

## References

- Original ticket: `thoughts/shared/tickets/AC-0068e-github-flagship.md`
- Epic: `thoughts/shared/tickets/AC-0068-credential-injection-phase1-epic.md`
- Research: `thoughts/shared/research/2026-07-10-AC-0068e-github-flagship.md`
- Foundational discussion: `thoughts/shared/discussions/2026-06-28-credential-injection.md`
- Injection engine: `internal/proxy/enforcer/enforcer.py:339-404`
- Matcher specificity: `internal/proxy/enforcer/policy.py:221-238`
- Spawn-only resolution: `internal/proxy/lifecycle.go:173-191`
- Similar integration test: `internal/proxy/lifecycle_integration_test.go`
- Similar e2e injection tests: `internal/proxy/enforcer/test_integration.py:561-598`
- Secret-ref env gating: `internal/sysdep/secretresolver_integration_test.go:31-45`
- Fine-grained PAT + GraphQL: https://github.blog/changelog/2023-04-27-graphql-improvements-for-fine-grained-pats-and-github-apps/
- `gh issue create` needs Contents:Read: https://github.com/cli/cli/issues/12798
