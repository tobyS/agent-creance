---
date: 2026-07-10T09:15:05+02:00
git_commit: cb8ff9c096c9441afcab18a510a65c4cb7806f5e
branch: main
repository: agent-creance
topic: "AC-0068e: GitHub flagship — open /graphql token-scoped, end-to-end validation, docs"
tags: [research, codebase, credential-injection, proxy, enforcer, github, gh, integration-tests, docs]
status: complete
last_updated: 2026-07-10
---

# Research: AC-0068e — GitHub flagship (open `/graphql` token-scoped, end-to-end validation, docs)

**Date**: 2026-07-10T09:15:05+02:00
**Git Commit**: cb8ff9c096c9441afcab18a510a65c4cb7806f5e
**Branch**: main
**Repository**: agent-creance

## Research Question

Close GH-1: with credential injection shipped (AC-0068a–d), allowlist
`api.github.com/graphql` bound to a repo-scoped injected token, validate the whole
Phase-1 substrate end-to-end against real GitHub, prove concurrent per-project cages
each hold their own scoped token, and write the `docs/design.md` credential-injection
section.

## Summary

**Nothing remains to be built in the injection engine.** AC-0068a–d are all `Status:
Done` and the substrate — resolve host-side → deliver over inherited fd 3 → overwrite
the auth header → fail closed with 472 → annotate upstream 401/403 with
`X-Cage-Injected` — exists, is unit-tested, and already has Python end-to-end coverage
against a local echo origin. AC-0068e is exactly what its title says: **open `/graphql`,
validate against a real upstream, and write docs.**

Research turned up four findings that materially shape the implementation, one of them
a trap that would silently defeat the feature:

1. **Injection is bound per *rule*, not per *host*, and the matcher picks the
   most-specific rule.** `_apply_injection` returns early unless the *matched* rule
   carries `inject` (`internal/proxy/enforcer/enforcer.py:358-359`), and `_best_match`
   selects the most-specific matching allow rule, not the first
   (`internal/proxy/enforcer/policy.py:221-238`). This repo's own
   `.agent-creance.yaml` already has three path-scoped `api.github.com` allow rules
   (lines 22, 89, 92). Adding a host-wide `api.github.com` rule with `inject: github`
   would therefore be **shadowed** by those more-specific path rules and never fire.

2. **Every `api.github.com` rule `gh` traverses must carry `inject`, or `gh` breaks.**
   `gh` sends the same `Authorization` header through one shared transport for both
   REST and GraphQL. Once `GH_TOKEN=<phantom>` primes the cage, a request matching a
   *non*-inject rule forwards the **phantom** upstream and earns a genuine 401. So this
   is not "open `/graphql` and leave REST alone" — the existing REST rules must be
   bound to the credential too. This inverts the ticket's framing of "whether existing
   REST allow rules can be tightened".

3. **Secrets resolve on the proxy *spawn* path only; the reuse path never
   re-resolves** (`internal/proxy/lifecycle.go:177-191`). Editing this repo's config to
   add an inject rule while a cage is running causes `api.github.com` to return **472
   until the proxy respawns** — the running proxy holds no secret for the new
   credential. This is an operational hazard for the dogfooding config specifically.

4. **Multi-project concurrent scoping is designed but has no test anywhere.** The
   discussion says it "falls out for free" (per-project proxy, per-proxy fd payload),
   and no plan or status file schedules a test for it. The ticket's acceptance criterion
   demands it be "validated", so this is net-new test work.

Two documentation facts: `docs/design.md:533` currently asserts **"v0.1 is OAuth-only
(Claude Pro/Max), and does no credential injection"** — a statement the shipped code
contradicts and that this ticket must rewrite. The 472 refusal-taxonomy entry already
landed in AC-0068c at `docs/design.md:323-334`, so the new section must cross-reference
rather than duplicate it.

## Detailed Findings

### The injection engine (AC-0068c) — shipped, complete

**Delivery.** Go resolves each injected credential once at proxy spawn
(`internal/cli/inject.go:26-54`, `resolveInjectionSecrets`), marshals
`map[string]string{credName: token}` to JSON, and `SpawnWithSecret` writes it into a
pipe whose read end becomes the child's fd 3
(`internal/sysdep/processmanager.go:87-117`; `const SecretFD = 3` at
`internal/sysdep/processmanager.go:59-63`). The fd *number* rides an addon option
(`--set creance_secret_fd=3`, `internal/proxy/lifecycle.go:495-507`); the secret itself
never touches argv, env, or disk. The addon reads it once at startup
(`internal/proxy/enforcer/enforcer.py:138-143`, `_read_secrets` at `:215-245`) and logs
only the count.

**Per-request injection.** `_apply_injection`
(`internal/proxy/enforcer/enforcer.py:339-373`) runs on `DECISION_ALLOW`. Its guards, in
order:

```python
if result.matched is None or result.matched.list != "allow":
    return
rule = self._ruleset.allow[result.matched.index]
if rule.in_cage:
    return
if not rule.inject:
    return
```
(`internal/proxy/enforcer/enforcer.py:353-359`)

Then it looks up `cred = self._ruleset.credentials.get(rule.inject)` and `token =
self._secrets.get(rule.inject)`, and **overwrites**:

```python
flow.request.headers[cred.header] = value
```
(`internal/proxy/enforcer/enforcer.py:371`) — a direct assignment, replacing any
client-supplied value of that header name, including the phantom.

**Fail-closed.** If either the credential or its token is missing, the request is never
forwarded; the addon synthesizes HTTP 472 with `X-Cage-Reason: injection-unavailable`
and `X-Cage-Injected: <name>` (`internal/proxy/enforcer/enforcer.py:362-366` →
`internal/proxy/enforcer/responses.py:146-170`; golden body at
`internal/proxy/enforcer/testdata/injection_unavailable_body.json.golden`).

**Upstream rejection.** A genuine 401/403 on an injected request keeps its upstream
status and gains `X-Cage-Injected: <name>` in the `responseheaders` hook
(`internal/proxy/enforcer/enforcer.py:375-404`), so the agent blames the injected
credential rather than its phantom.

### The matcher: most-specific wins, and injection rides the matched rule

`decide` finds the best-matching allow rule and returns its index
(`internal/proxy/enforcer/policy.py:180-218`). `_best_match` is explicit that
specificity, not order, decides:

```python
def _best_match(rules, req, eligible):
    """Index of the most-specific rule that matches req and passes ``eligible``.

    ``eligible`` of None means all rules are eligible. Returns (index, found). On an
    exact specificity tie the earlier index is kept (strict more-specific only).
    """
```
(`internal/proxy/enforcer/policy.py:221-226`)

`_apply_injection` then reads `inject` off **that** rule
(`internal/proxy/enforcer/enforcer.py:355-359`). Compiled `Rule.Inject`/`Rule.InCage`
are annotations the matcher itself ignores (`internal/policy/policy.go:79-94`) — they
only steer the addon's post-decision auth step.

**Consequence.** Injection cannot be declared once for a host if narrower rules for that
host exist. A `- host: api.github.com` + `inject: github` rule with no `paths:` is
strictly less specific than `- host: api.github.com, paths: ["/repos/tobyS/agent-creance"]`,
so any request under `/repos/...` matches the latter and skips injection entirely.

### This repo's own `.agent-creance.yaml` (the dogfooding config)

Three `api.github.com` allow rules exist today, none with `inject`, and there is **no**
`credentials:` block and **no** `env:` block:

- `.agent-creance.yaml:22` — `paths: ["/repos/tobyS/agent-creance/"]`, no `methods:`,
  `reason: "project git remote (origin)"` (written by `agent-creance init`, AC-0055).
- `.agent-creance.yaml:89` — `paths: ["/repos/tobyS/agent-creance"]`,
  `methods: [GET, POST, PATCH, PUT, DELETE]`.
- `.agent-creance.yaml:92` — `paths: ["/user", "/rate_limit"]`, `methods: [GET]`.

Plus `.agent-creance.yaml:95` — `uploads.github.com`, `paths: ["/repos/tobyS/agent-creance"]`,
`methods: [POST]`.

Rules 22 and 89 are near-duplicates differing only in a trailing slash and the presence
of `methods:`. `git remote get-url origin` is `git@github.com:tobyS/agent-creance.git`
(SSH), so git push/pull does not traverse the proxy; only `gh` and WebFetch do.

There is no `~/.config/agent-creance.yaml` on this machine, so no global `credentials:`
block is inherited.

### `gh` behavior: GraphQL-first, one shared transport, no token format check

Web research (`gh` 2.96.0 is what is on PATH here) found:

- **The whole issue workflow is GraphQL.** `gh issue list`, `view`, `create`, `comment`,
  `close`, and `edit --add-label` all issue `POST https://api.github.com/graphql`. So do
  `gh pr create`, `gh pr view`, `gh repo view`. `gh` resolves the base repo from git
  remotes plus GraphQL, so it does **not** call `GET /repos/{owner}/{repo}` for these.
- **`gh auth status`** is the exception: it makes an authenticated request to
  `api.github.com` and reads the `X-OAuth-Scopes` response header. Fine-grained PATs do
  not emit that header, so `gh auth status` works but cannot enumerate scopes
  (cli/cli#6520, cli/cli#9403).
- **`gh` emits `Authorization: token <TOKEN>` (classic scheme) for both REST and
  GraphQL via one shared transport** — recorded in
  `thoughts/shared/research/2026-07-02-AC-0068c-proxy-injection-engine.md:266-267`.
  Because the proxy overwrites the whole header, the phantom's scheme is cosmetic and
  the credential's own `Bearer {token}` template governs the upstream value.
- **`gh` performs no local token format validation.** `go-gh`'s `tokenForHost` returns
  the raw env value subject only to a non-empty/non-whitespace check; `GH_TOKEN`
  outranks `GITHUB_TOKEN` and bypasses the keyring; `gh` makes no startup auth call
  (`thoughts/shared/research/2026-07-02-AC-0068c-proxy-injection-engine.md:252-266`).
  So any non-empty phantom primes `gh`.
- **`go-gh` only attaches the auth header when the request host matches the configured
  GitHub host** (`isSameDomain`) — flagged as an AC-0068e topology constraint at
  `thoughts/shared/research/2026-07-02-AC-0068c-proxy-injection-engine.md:268-270`. All
  `gh` egress must reach canonical GitHub hostnames through the proxy.
- **`gh`'s update notifier** independently fetches the latest `cli/cli` release from
  `api.github.com`, which is not allowlisted here and would soft-deny (470) as noise;
  `GH_NO_UPDATE_NOTIFIER=1` suppresses it.

### The phantom + non-inject-rule interaction (the second trap)

Combining the three preceding sections: once `env: {GH_TOKEN: <phantom>}` primes the
cage, `gh` attaches the phantom to **every** `api.github.com` request. A request whose
most-specific matched rule lacks `inject` is allowed through with the phantom
untouched (`internal/proxy/enforcer/enforcer.py:358-359` returns before the overwrite)
and GitHub answers 401.

Today the agent's `gh` works because it reads a **real** broad token from the cage's
keychain. The moment the phantom takes precedence (it does — `GH_TOKEN` outranks the
keyring), every non-inject `api.github.com` rule becomes a 401. Therefore the rules at
`.agent-creance.yaml:22`, `:89`, and `:92` must each be bound with `inject: github`, or
be removed/consolidated, at the same time `/graphql` is opened. This is the concrete
answer to the ticket's open question about REST rules — they cannot merely be
"tightened"; they must be bound.

### Fine-grained PAT permissions

- Fine-grained PATs support GraphQL since the
  [2023-04-27 changelog](https://github.blog/changelog/2023-04-27-graphql-improvements-for-fine-grained-pats-and-github-apps/),
  with the constraint that the token's resource owner must match the repo owner.
- **Metadata: Read** is mandatory and auto-added to any fine-grained PAT with repo
  permissions.
- **Issues: Read and write** covers list, view, create, comment, close, and add/remove
  labels *on issues*. (Labels on *pull requests* are governed by the Pull requests
  permission instead — cli/cli#9166.)
- **Contents: Read** is required specifically because `gh issue create`'s GraphQL query
  selects `defaultBranchRef` ([cli/cli#12798](https://github.com/cli/cli/issues/12798),
  filed 2026-02-27, no fix landed at report time). The plain REST equivalent does not
  need it; this is a `gh`-specific over-ask.

**Recommended minimum for the tce workflow on one repo:** repository access limited to
`tobyS/agent-creance`, with Metadata: Read (automatic) + Issues: Read and write +
Contents: Read.

### Spawn-only resolution — the dogfooding hazard

`Attach` invokes the `Secrets` closure only on the spawn branch, never on proxy reuse,
deliberately, to avoid re-prompting Touch ID (`internal/proxy/lifecycle.go:174-191`).
An individual unresolvable credential is warned and omitted rather than failing the
spawn (`internal/cli/inject.go:44-47`), producing a 472 at request time instead
(`thoughts/shared/plans/2026-07-03-AC-0068c-proxy-injection-engine.md:55-57, 204-206`).

Meanwhile a config edit recompiles `policy.json` and the enforcer hot-reloads it within
~1s (`internal/cli/mutate.go:224-234`). So the addon will pick up the new
`inject: github` rule **without** having a `github` secret in `self._secrets`, and
`_apply_injection` will take the `cred is None or token is None` branch → 472 on every
`api.github.com` request until the proxy respawns.

Practical effect for this ticket: committing the dogfooding config change while a cage
is running severs that session's GitHub access. The config change and the cage restart
must be sequenced together.

### Multi-project concurrent scoping — designed, untested

The design is explicit that this falls out of the architecture:

> "Multi-project: falls out for free — each per-project proxy resolves and holds its own
> project's credential; four repos = four proxies = four scoped tokens, no shared state."
> (`thoughts/shared/discussions/2026-06-28-credential-injection.md:189-192`)

The mechanism it rests on is real: the proxy is refcounted per project (keyed by
state-dir hash) and each spawn gets its own fd payload. But **no document schedules a
test**, and no test exists. The ticket's acceptance criterion — "Concurrent project
cages each authenticate with their own scoped token (no shared state) — validated" —
is therefore net-new work.

Notably this does **not** need real GitHub: two proxies, two distinct secrets, two
local echo origins, asserting each origin sees only its own token, proves the isolation
claim. It does need a real `mitmdump`, so it belongs behind an integration gate.

### Test patterns to follow

**Go, `//go:build integration`.** Fifteen `*_integration_test.go` files exist. Shape
(`internal/proxy/lifecycle_integration_test.go:1-29`): build tag, blank line, external
`_test` package, doc comment naming the gate and the `make` command, then
`exec.LookPath` skips:

```go
//go:build integration

func TestLifecycleStartAttachTeardownRealProxy(t *testing.T) {
	if _, err := exec.LookPath("mitmdump"); err != nil {
		t.Skip("mitmdump not installed; skipping real-proxy integration test")
	}
```

**Env-var opt-in for a live secret** — the closest existing precedent
(`internal/sysdep/secretresolver_integration_test.go:31-45`):

```go
ref := os.Getenv("AC_TEST_OP_REF")
if ref == "" {
	t.Skip("set AC_TEST_OP_REF to a readable op:// reference to exercise this test")
}
...
require.NotEmpty(t, secret, "expected a non-empty resolved secret") // never print it
```

The `// never print it` / assert-presence-only convention is repeated at
`internal/sysdep/keychain_integration_test.go:36-37`. A destructive-op gate uses
`os.Getenv("CREANCE_LIVE_CA_INSTALL") != "1"`
(`internal/setup/setup_integration_test.go:75-79`).

**Python, `pytest.mark.integration`.** `internal/proxy/enforcer/test_integration.py`
(module marker line 33) stands up a real `mitmdump` and drives it with `curl`. The
existing end-to-end injection coverage lives here, at lines 458-598, using
`running_proxy_with_secret` (lines 501-546 — mirrors the Go fd-3 handoff via
`os.pipe()` + `pass_fds`) and `_echo_origin` (lines 461-498 — a local
`BaseHTTPRequestHandler` returning the received `Authorization` header as JSON, plus
`/reject401` and `/reject403` paths). The three tests there assert exactly the
overwrite, 472, and `X-Cage-Injected`-on-401 behaviors, against a **local** origin:

```python
assert json.loads(body)["authorization"] == "Bearer REALTOKEN"   # :571
assert code == "472"; assert "x-cage-injected: gh" in headers.lower()   # :583-586
assert code == "401"; assert "x-cage-injected: gh" in headers.lower()   # :595-598
```

**There is no Go integration test for injection**, and no test of any kind against real
GitHub. `make test-integration` runs `go test -race -tags=integration ./...` then chains
`make test-enforcer-integration` (`Makefile:58-62`, `:82-84`).

### Docs: what must change

`docs/design.md` outline places `## The proxy and the credential story` at line 529,
between `## Audit log` (519) and `## Tech stack` (551). Its second paragraph is now
false:

> **v0.1 is OAuth-only (Claude Pro/Max), and does no credential injection.** Proxy-side
> secret injection for non-Claude services (`op://` references resolved host-side, tokens
> added as `Authorization` at egress so the agent never sees them) is a v0.2 feature.
> (`docs/design.md:533`)

The remaining paragraphs (534-549) are all about the Claude OAuth token and remain
accurate. The 472 refusal entry already exists at `docs/design.md:323-334` (landed by
AC-0068c), and the AC-0068c plan is explicit about the split:

> "docs/design.md: add the 472 entry to the refusal taxonomy (288-327) now — it is the
> refusal section, distinct from the credential-injection section AC-0068e owns."
> (`thoughts/shared/plans/2026-07-03-AC-0068c-proxy-injection-engine.md:42-44`)

The `## Commands` reference block (419-488) does not yet list the `credential` group or
the `allow --inject` / `--in-cage` flags. The `## The configuration` YAML schema example
(77-105) does not show `credentials:` or the per-rule auth axis.

### Docs: the `--help` acceptance criterion is ambiguous

The ticket asks that "`--help` on the new commands references it (AC-0064 style)".
Research found that **no cobra command's user-facing `Long:` or `Example:` string
references `docs/design.md`**. The established convention is the opposite: design-doc
pointers live in Go `//` comments above the command constructors
(`internal/cli/allow.go:13`, `internal/cli/init.go:27`, `internal/cli/setup.go:24`,
`internal/cli/run.go:84,104`). The single place a doc path reaches a user is the
generated config header (`internal/cli/init.go:477`).

Meanwhile AC-0068d already gave the new commands full help surfaces: `credential` group
`Long:` (`internal/cli/credential.go:30-34`), `credential add` `Long:`/`Example:`
(`:58-72`), `list` (`:155-160`), `remove` (`:206-214`), and `allow`'s `--inject` /
`--in-cage` flags with a worked GraphQL example (`internal/cli/allow.go:42-43`,
`:57-60`). These are locked by testscript: `internal/cli/testdata/script/credential.txtar:11-19`
and `allow_inject.txtar:10-14`. The "help is a doc surface" pattern itself is asserted
in `internal/cli/testdata/script/more_help.txtar:1-4`.

So "AC-0064 style" most plausibly means *"help is a doc surface: Long + worked
Examples"* — already satisfied — rather than *"help contains a literal `docs/design.md`
path"*, which would be a new deviation from the codebase's convention. This needs a
decision.

## Impact Analysis

### Existing usages found

- `.agent-creance.yaml:22,89,92` — three `api.github.com` allow rules, path-scoped, no
  auth axis. Consumed by the running proxy for this repo's own cage.
- `.agent-creance.yaml:95` — `uploads.github.com` POST rule (release-asset uploads);
  `gh` would send the phantom here too if it were ever used.
- `internal/proxy/enforcer/test_integration.py:549-558` — `_INJECT_POLICY` fixture,
  a single host-wide `127.0.0.1` inject rule. Extending it must not regress the
  existing three assertions at `:561-598`.
- `internal/cli/testdata/script/allow_inject.txtar:26-27` — asserts `allow
  api.github.com/graphql --inject github` prints `allowed api.github.com/graphql`.
- `internal/config/setauth_test.go:23`, `internal/cli/allow_inject_test.go:27` — already
  exercise the exact rule shape this ticket needs to write.

### Current contract

- **Input** to `_apply_injection`: the *matched* allow rule plus the resolved
  `{name: token}` map read from fd 3.
- **Output**: the rendered header value assigned onto `flow.request.headers[cred.header]`,
  or a synthesized 472.
- **Assumptions held by consumers**: (a) exactly one rule matches, and it is the
  most-specific one; (b) the secret map is populated at spawn and never refreshed;
  (c) a rule without `inject` leaves auth completely untouched.

Assumption (c) is what makes the phantom dangerous on unbound rules, and assumption (b)
is what makes a mid-session config edit produce 472s.

### Adaptation requirements

- `.agent-creance.yaml` — needs a `credentials:` block, an `env:` block with the phantom,
  and an auth axis on **every** `api.github.com` rule (or consolidation into one
  host-wide inject rule after deleting the path-scoped ones). Sequenced with a cage
  restart.
- `docs/design.md:533` — the "does no credential injection" sentence must be rewritten,
  and a new subsection added; `docs/design.md:419-488` (Commands) and `:77-105` (schema)
  want the `credential` group and auth axis.
- `internal/proxy/enforcer/test_integration.py` — a multi-proxy concurrency test would
  extend `running_proxy_with_secret` to two simultaneous instances.

### Backward compatibility options for the `api.github.com` rules

- **Option A — bind each existing rule.** Add `inject: github` to the rules at lines 22,
  89, 92 and add a new `/graphql` POST rule. *Pros*: keeps the path allowlist as an
  independent defense layer on top of token scope; no rule deletion; `allow <host/path>
  --inject <name>` upserts in place via `SetRuleAuth` (`internal/config/setauth.go:28-69`).
  *Cons*: four rules to keep in sync; a future un-bound `api.github.com` rule silently
  reintroduces the phantom-401 trap.
- **Option B — consolidate to one host-wide inject rule.** Delete the three path-scoped
  rules, add `- host: api.github.com` + `inject: github`. *Pros*: exactly the epic's
  thesis (the credential, not the URL, is the boundary); impossible to shadow. *Cons*:
  discards path-level defense-in-depth; widens the allowlist to the entire GitHub API
  surface, bounded only by the PAT's scope; `/user` becomes reachable.
- **Option C — host-wide inject rule plus `deny_always` for sensitive paths.** *Pros*:
  simple positive rule, explicit negative list. *Cons*: denylists are the wrong default
  here and the project's design consistently prefers allowlists.

## Code References

- `internal/proxy/enforcer/enforcer.py:339-373` — `_apply_injection`; the `if not
  rule.inject: return` guard at `:358-359` and the overwrite at `:371`.
- `internal/proxy/enforcer/enforcer.py:375-404` — `responseheaders`; `X-Cage-Injected`
  on upstream 401/403.
- `internal/proxy/enforcer/policy.py:180-218` — `decide`.
- `internal/proxy/enforcer/policy.py:221-238` — `_best_match`; most-specific wins, ties
  keep the earlier index.
- `internal/proxy/enforcer/responses.py:146-170` — `injection_unavailable`, status 472.
- `internal/proxy/lifecycle.go:174-191` — `Attach`; `Secrets` closure runs on the spawn
  branch only.
- `internal/proxy/lifecycle.go:495-507` — `mitmArgs`; `--set creance_secret_fd=3`.
- `internal/sysdep/processmanager.go:59-63,87-117` — `SecretFD = 3`, `SpawnWithSecret`.
- `internal/sysdep/secretresolver.go:30-38,59-63` — `SecretResolver` interface, the
  `op://` / `keychain://` / `env://` schemes.
- `internal/cli/inject.go:26-54` — `resolveInjectionSecrets`; unresolvable credentials
  are warned and omitted, not fatal.
- `internal/cli/mutate.go:103-139,224-234` — `applyAndRecompile`; the ~1s hot-reload.
- `internal/config/setauth.go:28-69` — `SetRuleAuth` upsert-in-place.
- `internal/policy/policy.go:79-94` — `Inject`/`InCage` are annotations `Decide` ignores.
- `internal/cage/cage.go:311-336` — `buildEnv`; the config `env:` block reaches the cage.
- `internal/cli/credential.go:30-34,58-72,155-160,206-214` — the credential group's help.
- `internal/cli/allow.go:42-43,57-60` — `--inject` / `--in-cage` flags and the worked
  `api.github.com/graphql` example.
- `internal/proxy/enforcer/test_integration.py:461-498` — `_echo_origin` local upstream.
- `internal/proxy/enforcer/test_integration.py:501-546` — `running_proxy_with_secret`.
- `internal/proxy/enforcer/test_integration.py:561-598` — the three existing e2e
  injection assertions.
- `internal/sysdep/secretresolver_integration_test.go:31-45` — the `AC_TEST_OP_REF`
  env-var opt-in pattern.
- `internal/setup/setup_integration_test.go:75-79` — the `CREANCE_LIVE_CA_INSTALL=1`
  destructive gate.
- `Makefile:58-62,82-84` — `test-integration`, `test-enforcer-integration`.
- `docs/design.md:323-334` — the 472 refusal entry (already landed).
- `docs/design.md:529-549` — `## The proxy and the credential story`; `:533` is the
  false "no credential injection" claim.
- `.agent-creance.yaml:22,89,92,95` — the GitHub allow rules to bind.

## Architecture Documentation

- **Two orthogonal axes.** Transport (`mode: intercept|passthrough`) × auth
  (`inject: <name>` XOR `in_cage: true`), the auth axis meaningful only on intercepted
  hosts. `inject` on a passthrough rule is rejected at validation
  (`internal/config/validate.go:65-67`) because a raw tunnel is never TLS-terminated.
  `api.github.com` must therefore be **intercept** (it is, by default —
  `internal/config/config.go:251-257`).
- **Credentials are references, never values, in the compiled artifact.**
  `policy.Credential` carries `source`/`header`/`template`/`username`
  (`internal/policy/policy.go:112-117`); the golden `policy.json` shows
  `"source": "op://Private/GitHub PAT/token"` with no value
  (`internal/policy/compile/testdata/policy.golden:4-10`). The secret exists only in
  proxy memory.
- **Value template.** `Bearer {token}` is the gold-plated shape; `token {token}`, bare
  `{token}`, `Basic base64({user}:{token})`, and custom headers ride the same renderer
  (`internal/config/template.go:34-53`, ported to `internal/proxy/enforcer/inject.py:31-58`
  and held byte-identical).
- **Fail-closed everywhere.** Empty secret map on read error
  (`internal/proxy/enforcer/enforcer.py:240`), 472 on unresolved token (`:362`),
  hard-deny on any request-hook exception (`:329-337`).
- **Compile-time reference check.** `validateInjectRefs`
  (`internal/policy/compile/compile.go:402-418`) fails the compile if a rule injects an
  undefined credential — so a config typo cannot reach the proxy.
- **Testing seams.** `credential add` only syntax-checks `--source`
  (`internal/cli/credential.go:133-135`); resolution happens at spawn. `credential list`
  prints a `SHAPE` column (the template), never a value (`:185-189`). `credential
  remove` refuses while a rule still injects the credential (`:228-233`).

## Historical Context (from thoughts/)

- `thoughts/shared/discussions/2026-06-28-credential-injection.md:20-22` — "GraphQL
  access cannot be scoped at the URL layer — it has to be scoped at the credential
  layer." The thesis of the whole epic.
- `…:203-205` — "Once a repo-scoped token is the boundary, `/graphql` can be allowlisted
  safely — solving the original GH-1 problem without opening the account."
- `…:106-111` — overwrite semantics are what neutralize the cage-wide keychain read.
- `…:160-164` — phantom priming via the config `env:` block; "SDK-format checks mean the
  phantom may need a plausible shape" (does not bite `gh`).
- `…:189-192` — the multi-project claim ("falls out for free").
- `…:314` — accepted trade-off: "a leak is repo-scoped but not time-bounded until Phase 2."
- `thoughts/shared/research/2026-07-02-AC-0068c-proxy-injection-engine.md:53` — "live
  `gh` priming is AC-0068e (out-of-cage)."
- `…:244-246` — "Real `gh` + GitHub GraphQL is **AC-0068e**, not here. Batch these when
  the cage is down."
- `…:252-266` — the `GH_TOKEN` phantom decision, confirmed against `go-gh` source.
- `…:268-270` — the `isSameDomain` topology constraint.
- `thoughts/shared/plans/2026-07-03-AC-0068c-proxy-injection-engine.md:118-120` — "No
  opening of `/graphql`, no real GitHub end-to-end, no `docs/design.md`
  *credential-injection section* — AC-0068e."
- `thoughts/shared/plans/2026-07-05-AC-0068d-cli-credential-management.md:522-523` —
  "the end-to-end `gh`/GraphQL validation is AC-0068e, behind the integration tag."

## Related Research

- `thoughts/shared/research/2026-06-29-AC-0068a-secretresolver-seam.md`
- `thoughts/shared/research/2026-07-02-AC-0068b-config-injection-model.md`
- `thoughts/shared/research/2026-07-02-AC-0068c-proxy-injection-engine.md`
- `thoughts/shared/research/2026-07-05-AC-0068d-cli-credential-management.md`
- `thoughts/shared/research/2026-06-12-AC-0045-incage-credential-access.md`
- `thoughts/shared/research/2026-06-25-AC-0062-doctor-credential-preconditions.md`

## Open Questions

1. **How should the `api.github.com` rules be bound?** Options A/B/C above. The
   shadowing behavior means this cannot be left implicit. (Recommendation: A — bind each
   existing rule, keeping the path allowlist as an independent layer.)
2. **Where does the real fine-grained PAT live** — `op://…`, `keychain://…`, or
   `env://…`? Determines the `credentials:` `source:` value committed to
   `.agent-creance.yaml` and the env-var name the integration test opts in on.
3. **Should the dogfooding config change be committed at all**, given it breaks a
   running cage's GitHub access until respawn, and given `source:` embeds a reference to
   the user's personal secret store in a repo intended for other users?
4. **What does "`--help` … references it (AC-0064 style)" require** — the existing
   Long/Example doc surfaces (already shipped), or a literal `docs/design.md` pointer in
   help text (a new deviation from the codebase's convention)?
5. **Is the end-to-end GitHub test a Go `//go:build integration` test or a Python
   `pytest -m integration` test?** The ticket says "behind the `integration` build tag"
   (Go phrasing), but all existing e2e injection coverage is Python. A Go test would
   additionally exercise `resolveInjectionSecrets` → `SpawnWithSecret`, which the Python
   tests only mirror.
6. **Does `gh auth status` need to keep working in-cage?** With a fine-grained PAT it
   cannot report scopes. If it must work, its `api.github.com` probe needs an inject-bound
   rule too.
