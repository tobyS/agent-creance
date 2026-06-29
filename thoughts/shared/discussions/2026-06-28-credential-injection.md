---
date: 2026-06-28
topic: "Credential injection for the cage"
status: complete
---

# Technical Discussion: Credential Injection

## Challenge

An agent inside the cage needs to use authenticated services — first and foremost
`gh`, which the tce workflow uses for GitHub Issues (read/create tickets, label,
comment, close). But most `gh` operations go over GraphQL (`POST
api.github.com/graphql`), and the cage blocks it (470) because the allowlist is
host+path based and GraphQL is a single endpoint whose target lives in the request
body. The naive fix — allow `/graphql` — is a security regression: today's REST
per-path allowlist boxes even a broad keychain token to one repo; opening `/graphql`
with that broad token in reach hands the agent the whole account.

The real problem surfaced underneath: **GraphQL access cannot be scoped at the URL
layer — it has to be scoped at the credential layer.** And the cage's `gh` currently
uses a copy of the user's broad classic PAT, reachable from the cage's keychain
access. So the question became: how do we let the agent use authenticated services,
scoped to what it should reach, without exposing broad long-lived credentials to a
possibly prompt-injected agent — across multiple concurrent project cages?

The intended outcome: the agent uses `gh` (and, in principle, other authenticated
tools) properly, without opening the whole of GitHub (or any other account) to it.

### Context that shaped the conclusion

- **The keychain is readable cage-wide, by mechanism, not by intent.** The Seatbelt
  grant (`internal/profile/profile.go:151`, `testdata/keychain.golden`) is
  `(allow mach-lookup (global-name "com.apple.SecurityServer"))` plus a file-write to
  `login.keychain-db`. `mach-lookup` reaches `securityd`, which gates *all* keychain
  items; Seatbelt cannot scope it to one item. The grant is commented "Claude-only"
  but the mechanism is keychain-wide. Claude Code's own OAuth token *must* be readable
  in the cage (the agent uses it, and Anthropic hosts are passthrough), so this grant
  cannot be removed while auth is OAuth-in-cage. The egress allowlist — not the
  keychain ACL — is what bounds the damage (`docs/design.md:532`).
- **The proxy is host-side and per-project.** mitmproxy runs as a normal host daemon
  (`docs/design.md:516`), refcounted per project (keyed by state-dir hash). Four
  concurrent cages on four repos already run four proxies with four `policy.json`s.
  This is what makes per-project credential scoping feasible without shared state.
- **The enforcer only ever sees host+path+method.** `enforcer.py` builds
  `policy.Request(host, path, method)` — never the body. A GraphQL-aware body filter
  would be both bypassable (node IDs, `search`, `viewer`, aliases) and foreign to this
  enforcer. Ruled out.
- **This was always planned.** `docs/design.md:518` already names "proxy-side secret
  injection for non-Claude services (`op://` references resolved host-side, tokens
  added as `Authorization` at egress so the agent never sees them)" as a v0.2 feature.

## Prior Art

Surveyed how other agent sandboxes with egress control handle this. Three patterns,
ranked by how well they keep the secret from a compromised agent:

1. **Proxy-side header injection (strongest).** Secret lives in the host-side proxy;
   agent sends an unauthenticated or phantom-token request; proxy swaps in the real
   credential per-destination before forwarding. Used by **nono**
   (`always-further/nono` — Seatbelt/Landlock kernel sandbox + localhost-only egress
   proxy, almost a mirror of agent-creance; the user's "noop"), **Cloudflare Outbound
   Workers for Sandboxes** (2026, explicitly for "coding agents"), **Envoy's
   `credential_injector` filter**, **Ory Oathkeeper**, and Anthropic's *managed* git
   proxy. The agent never holds the token.
2. **Runtime secret-reference injection** (`op://` → env var, e.g. Dagger
   container-use): keeps secrets out of git/model-context, but the resolved value lands
   in the in-cage process env — no protection against a compromised agent.
3. **Scoped env-var token** (GitHub Codespaces/Actions `GITHUB_TOKEN`): agent holds the
   raw token, but it's repo-scoped, short-lived, and log-scrubbed.

Borrowed lessons that became design decisions:

- **nono** resolves once at startup, holds the secret in zeroizable memory, never
  writes it to disk, and uses a per-session phantom token plus header-stripping so the
  agent can neither override nor exfiltrate the injected credential.
- **Envoy** gives the design vocabulary: `overwrite` (clobber a client-supplied auth
  header) and fail-closed (`allow_request_without_credential=false`), and exposes only
  counters — never the secret value — to logs.
- **GitHub Actions `GITHUB_TOKEN`** is the model for short-lived minted tokens: the
  app private key stays host-side, a per-job repo-scoped installation token (≤1h) is
  injected and dies with the job — the Phase-2 target.
- **IMDSv2 SSRF lesson:** a local credential *endpoint* reachable through a forwarding
  proxy is the classic confused-deputy hole AWS hardened against. So **do not** build
  an in-cage token endpoint; header-injection-at-proxy avoids the whole class.
- **Claude Code's own SOCKS5 null-byte allowlist bypass** (`host\x00.google.com`,
  patched 2026-04): match hosts on normalized bytes, reject embedded nulls. Applies to
  the existing matcher, not just injection.

## The Agreed Shape (Version A: general mechanism, GitHub-validated)

Build the full substrate generally; validate and ship Bearer + GitHub as the single
flagship; let other shapes ride along via a value-template, present but not
gold-plated.

### Two orthogonal axes

The existing transport axis gains a second, orthogonal auth-handling axis (meaningful
only on intercepted hosts):

```
transport:                  intercept | passthrough
auth (intercept only):      inject(<credential>) | in-cage | (none / default)
```

- **`inject`** — the proxy supplies the credential from a host-side source, with
  **overwrite** (replace any client-supplied auth header) and **fail-closed** (deny if
  the secret won't resolve). Overwrite is what makes injection robust against the
  shared keychain: even if a prompt-injected agent reads a broad token and sets the
  header itself, the proxy clobbers it with the scoped one for that host, so the cage
  cannot exceed the injected scope *against that host*.
- **`in-cage`** — the proxy guarantees it will never add, strip, or modify any auth
  header; the agent authenticates with credentials it holds in the cage, and the proxy
  only enforces host/path/method. This is the honest, self-documenting marker for
  cases header injection structurally cannot serve: **SigV4** (AWS/IONOS S3 — a
  signature computed client-side breaks if a header is rewritten), **`op`/1Password**
  (service-account token, not a plain header), and conceptually anything SDK-minted. It
  also lets tooling lint ("credentialed host with neither `inject` nor `in-cage`") and
  marks in the threat model that a real credential intentionally lives in the cage for
  that host.
- **default** — today's behavior (proxy doesn't touch auth).

`api.anthropic.com` stays **passthrough** (so Claude Code's OAuth token is never
inspected); a host cannot be both passthrough and intercept-inject, so any Anthropic
*API key* on that host is necessarily `in-cage` (see Exclusions).

### Injection shapes (one value-template, all shapes)

Model an injection rule as `(host/path) -> header name + value template`, so all
shapes are one mechanism:

- `Bearer {token}` — the gold-plated, validated v1 path (GitHub, and most APIs)
- `token {token}` and bare `{token}` — `gh` API accepts `token`; crates.io / classic
  Linear use the bare token
- `Basic base64({user}:{token})` with a parameterized username sentinel —
  git-over-HTTPS (`x-access-token`), PyPI (`__token__`), GitLab git (`oauth2`), Jira
  (email)
- arbitrary **custom header name** — Anthropic `x-api-key`, Qdrant `api-key`, GitLab
  `PRIVATE-TOKEN`, Lettermint `x-lettermint-token`

Bearer is validated end-to-end in v1; custom-header and Basic are present via the
template but not gold-plated until a real consumer appears.

### Secret resolution and delivery — host-side, in memory, never plaintext-on-disk

- **Resolve host-side, before the sandbox applies**, via a new `internal/sysdep`
  `SecretResolver` seam (interface + `OS*` impl + `sysdeptest` fake, mirroring
  `keychain.go`) with backends `op://`, `keychain://`, `env://`. The long-lived secret
  stays in 1Password/Keychain; only the resolved value lives transiently in proxy
  memory.
- **Never write the resolved secret to disk.** Per-request resolution is too slow
  (`op read` is 200–500ms and rate-limited) and an encrypted file is theater unless the
  key is in the Keychain — at which point store the secret in the Keychain directly and
  drop the file. macOS has no `tmpfs`/`memfd`, which argues against materializing it as
  a file at all.
- **v1 delivery: resolve in Go at proxy spawn, hand to the `mitmdump` addon over an
  inherited file descriptor / stdin** (`exec.Cmd.ExtraFiles`) — never argv (visible in
  `ps`), never env (same-uid processes can read it via `KERN_PROCARGS2`/`ps -E`), never
  disk. The addon holds it in memory for the proxy's lifetime.
- **Phantom-token priming.** Many clients won't send a request without a credential
  (`gh` refuses if "logged out"). So the cage gets a non-secret placeholder
  (`GH_TOKEN=<phantom>` via the config `env:` block) and the proxy overwrites it.
  SDK-format checks (e.g. `sk-…` prefixes) mean the phantom may need a plausible shape.

### Failure transparency — a new status code 472

Requirement: when a request fails because of injection/overwrite, the agent must get
enough to inform the user. Verified that WebFetch discards **both** body and headers
and surfaces only the status line (`SKILL.md:39-43`, `docs/design.md:323`: "a distinct
code per refusal type is the one marker every client shows"). Therefore:

- **Add `472 = injection-unavailable`** (fail-closed when the secret won't resolve). It
  is a genuine third category — allowlisted, transient, **recoverable by the human, not
  the agent** — that neither 470 (agent-recoverable via `allow`) nor 471 (permanent)
  conveys. Reusing either mis-signals to body-blind clients: 470 would suggest a
  useless `allow`; 471 would imply a permanent block and the agent would never tell the
  user to unlock 1Password. Pick 472 (clear of 470/471 and ALB's 460/463/464); update
  the skill and the launch briefing (`internal/cage/briefing.md`).
- For an injected credential **rejected upstream** (real 401/403), annotate the
  response (e.g. `X-Cage-Injected: <name>`) rather than inventing a status — the
  upstream owns that status — so the agent blames the injected credential, not its
  phantom.

### CLI, multi-project, docs

- **CLI:** extend the config-mutation commands (`allow`/`mutate`/`edit` → `AppendRule`)
  with a `credential` group (`add --source op://…`, `list`, `rm`) and binding
  (`allow <host/path> --inject <name>`), reusing the recompile + hot-reload path.
- **Multi-project:** falls out for free — each per-project proxy resolves and holds its
  own project's credential; four repos = four proxies = four scoped tokens, no shared
  state. With overwrite semantics this is the clean answer to the four-concurrent-cages
  problem and neutralizes the shared keychain.
- **Docs:** a `docs/design.md` section, Long/Example `--help` on the new commands
  (matching the AC-0064 help-as-doc-surface work), and a SKILL.md update for 472.

## Why this shape (reasons)

1. **Proxy-side header injection is the only model robust against the cage's keychain
   read.** The keychain is readable cage-wide by mechanism; keeping the secret
   host-side and overwriting at egress means a compromised agent cannot use even a
   broad token it reads against the injected host. It is also the pattern the closest
   analog (nono) and the current state of the art (Cloudflare/Envoy) converge on.
2. **The credential, not the URL, must be the boundary for GraphQL.** Once a
   repo-scoped token is the boundary, `/graphql` can be allowlisted safely — solving
   the original GH-1 problem without opening the account.
3. **In-memory, no-disk delivery matches the threat model and macOS reality.** Disk
   plaintext is avoidable; encrypted-at-rest is secret-zero theater; the inherited-fd
   channel keeps the secret off disk, argv, and env, reachable only by the two
   host-side processes.
4. **A new 472 is required by the project's own design principle.** Body-blind clients
   see only the status code; the desired agent action for "credential unavailable"
   differs from both existing codes, so it needs its own.
5. **Version A is general for little marginal cost.** The substrate (resolve, in-memory,
   overwrite, fail-closed, 472, phantom priming, CLI, docs) is identical whether we
   support one shape or four; the value-template generality and the `credentials:`
   indirection are the cheap parts, so building general buys the next service as pure
   config.
6. **The `in-cage` axis turns gaps into declared modes.** SigV4, `op`, and SSH cannot
   be injected; `in-cage` makes that an explicit, documented, lint-able choice rather
   than an unhandled case.

## Scope, grounded in real usage

Audited two real projects for what the **agent itself** invokes through the cage (a
first audit mistakenly measured the *application's* runtime dependencies — those run
outside the cage and are irrelevant unless the agent runs the app/integration tests
in-cage with real calls; both projects mock those). Corrected result, consistent
across both:

- **The only credential the default dev loop needs is the GitHub token for `gh`
  (Bearer, `api.github.com`).** This is the GH-1 driver and the v1 flagship.
- **Conditional deploy tooling** (Coolify Bearer, Hetzner Terraform Bearer) — only if
  deploys are delegated to the agent. A future expander, not v1.
- **`op`** — used by the agent, but service-account/desktop auth, not header-injectable
  → `in-cage` (`*.1password.com`).
- Everything else is public (npm/docs), SSH (git push — not proxyable), or mocked
  app-runtime.

So v1's validated need is a single service. Version A still builds the general
substrate, because it is nearly free and because the audits surfaced two concrete
"someday" consumers: **deploy-delegation** (Bearer CLIs) and **go-live / record-cassette
app-runtime** (Anthropic `x-api-key`, Qdrant `api-key`, Google OAuth).

## Phasing

- **Phase 1 (v1):** static-reference injection (`op://`/keychain/env → resolve →
  inject) with overwrite + fail-closed; Bearer gold-plated; GitHub the flagship
  (`/graphql` opened, token-scoped); custom-header/Basic via the template; `in-cage`
  mode; 472; phantom priming; CLI + docs; inherited-fd delivery.
- **Phase 2 (deferred):** minted short-lived tokens — GitHub App installation tokens
  (host-side JWT-sign with the app key, ≤1h repo-scoped, refresh loop) and OAuth2
  refresh-grant minting (Google Drive). This is where rotation and the hot-reload
  delivery channel pay off.

## What we excluded (and why)

- **A new HTTP code per refusal beyond 472** — rejected; 470/471/472 cover
  agent-recoverable / permanent / human-recoverable. Held the line to avoid code
  proliferation.
- **GraphQL-aware body-inspecting firewall** — bypassable (node IDs, `search`,
  `viewer`, aliases) and foreign to an enforcer that only sees host+path+method.
- **`GH_TOKEN` as a boundary** — it is only a default; the broad keychain token stays
  readable underneath it. Used only as the phantom-priming channel, not as security.
- **Plaintext resolved-secret file on disk** — avoidable; the user objected; in-memory
  delivery is strictly better.
- **Encrypted-at-rest secret file** — theater unless the key is in the Keychain, in
  which case store the secret in the Keychain and drop the file (secret-zero).
- **Per-request `op read`** — 200–500ms latency and rate limits; resolve once at
  startup instead.
- **In-cage local token endpoint (IMDS-style)** — reintroduces a credential into the
  cage and recreates the SSRF/confused-deputy hole IMDSv2 was hardened against.
- **Unix-socket broker for v1** — deferred to a hardening follow-up; the inherited-fd
  channel is simpler and sufficient for static tokens. (The broker remains the target
  for Phase-2 rotation and to avoid Python's weak memory-zeroization, since Go can hold
  the secret in `mlock`-able, zeroizable memory.)
- **Proxy-side token minting in v1** (GitHub App / OAuth refresh) — deferred to Phase 2;
  no near-term agent-tooling need in the audits.
- **Private package-registry recipes (npm/PyPI/etc.)** — both projects use public
  registries; generic Bearer/Basic covers them if needed, no special-casing.
- **AWS/GCP/Azure cloud APIs and cloud container registries (ECR/Artifact Registry)** —
  SigV4 signing / SDK-minted OAuth; injecting a header breaks the signature. Declared
  `in-cage`, not injectable.
- **Docker Hub / ghcr.io image auth** — needs a two-host Bearer token-exchange dance
  (401 challenge → token realm → retry), its own code path; defer past v1.
- **SSH-based git/SFTP** — not HTTP-proxyable (already unsupported, `docs/design.md:22`);
  out of scope for an HTTP-header feature.
- **App-runtime service calls when the app runs outside the cage** — never traverse the
  proxy; only relevant if the agent runs the app/tests in-cage with real outbound
  calls (currently mocked in both projects).

## Conclusion

Adopt **proxy-side credential injection** as the v0.2 direction, in the **Version A**
shape: a general host-side substrate — `SecretResolver` seam, two-axis
`intercept|passthrough` × `inject|in-cage` model, value-template covering
Bearer/`token`/bare/Basic/custom-header, in-memory inherited-fd delivery, overwrite +
fail-closed, 472 + response annotation, phantom priming, per-project scoping, CLI +
docs — with **Bearer + GitHub** as the single validated flagship that closes GH-1
(allow `/graphql`, token-scoped, multi-project-safe). Phase 2 adds minted short-lived
tokens (GitHub App / OAuth refresh) and, as hardening, a host-side unix-socket broker.

### Trade-offs Accepted

- **Generality now for a single current consumer.** Building the full substrate when
  only GitHub needs it; justified because the substrate is identical regardless of
  shape count and two concrete future consumers exist.
- **Python holds the secret in memory in v1** (weak zeroization) rather than Go via a
  broker — accepted for simplicity; far better than disk or the cage; broker is the
  Phase-2 hardening.
- **Claude's own token and any `in-cage` credential remain readable in the cage** —
  unavoidable while the agent uses them; the egress allowlist bounds exfiltration to
  allowlisted POST targets. Injection removes *non-Claude* secrets from the agent's
  reach, not Claude's own.
- **Static tokens in v1 are longer-lived than minted ones** — a leak is repo-scoped but
  not time-bounded until Phase 2.

## Open Decisions

- **Delivery channel evolution:** v1 uses inherited-fd (recorded as chosen); the
  unix-socket broker is the Phase-2 hardening for rotation and Go-side memory hygiene.
  Confirm at Phase-2 planning.

## References

- Codebase: `internal/proxy/enforcer/enforcer.py` (request hook, intercept/passthrough),
  `policy.py` (matcher, `Rule`), `internal/proxy/lifecycle.go` (`mitmArgs`, spawn),
  `internal/config/config.go` (`Egress`, `Rule`), `internal/policy/` (compile/render),
  `internal/sysdep/keychain.go` + `sysdeptest/` (seam pattern), `internal/cred/`
  (detection, status-as-data), `internal/cli/allow.go`/`mutate.go`/`edit.go`
  (config mutation), `internal/profile/profile.go:151` + `testdata/keychain.golden`
  (keychain grant), `internal/setup/SKILL.md`, `internal/cage/briefing.md`,
  `docs/design.md` (286-327 refusals, 183-194 forge generator, 514-534 credential
  story).
- nono (`always-further/nono`): https://nono.sh/docs/cli/features/credential-injection.md ,
  https://nono.sh/blog/blog-credential-injection
- Cloudflare Sandboxes credential injection: https://blog.cloudflare.com/sandbox-auth/
- Envoy credential injector filter: https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/credential_injector_filter
- GitHub Actions `GITHUB_TOKEN` / runner auth: https://docs.github.com/en/actions/concepts/security/github_token , https://github.com/actions/runner/blob/main/docs/design/auth.md
- GitHub App installation tokens: https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/authenticating-as-a-github-app-installation
- Fine-grained PAT GraphQL support (since 2023, GA 2025): https://github.blog/changelog/2023-04-27-graphql-improvements-for-fine-grained-pats-and-github-apps/
- `gh issue create` needs Contents:Read (defaultBranchRef): https://github.com/cli/cli/issues/12798
- IMDSv2 SSRF defense-in-depth: https://aws.amazon.com/blogs/security/defense-in-depth-open-firewalls-reverse-proxies-ssrf-vulnerabilities-ec2-instance-metadata-service/
- Claude Code SOCKS5 null-byte bypass: https://www.securityweek.com/anthropic-silently-patches-claude-code-sandbox-bypass/
- 1Password secret references / service-account rate limits: https://www.1password.dev/cli/secret-references , https://developer.1password.com/docs/service-accounts/rate-limits/
- macOS env exposure (`KERN_PROCARGS2`): https://getargv.narzt.cam/
- RFC 8693 OAuth token exchange: https://datatracker.ietf.org/doc/html/rfc8693
