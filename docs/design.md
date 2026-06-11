---
created: 2026-05-29
updated: 2026-06-01
tags:
  - AI-involved
  - Claude/Opus/4-7
  - Claude/Opus/4-8
  - project/agent-creance
---

# Design

A single command (`agent-creance`) that starts Claude Code (or any other coding agent) inside an isolated environment within a source tree, configured by one YAML file. v0.1 is macOS-only — it composes [agent-safehouse](https://agent-safehouse.dev/) (which already does filesystem and process isolation well) with mitmproxy (which does TLS-terminating egress filtering well), and adds the orchestration glue plus a network-policy compiler so the two pieces feel like one tool.

## Open spikes (resolve before build)

These are load-bearing assumptions that haven't been validated against the real tools yet. Each one can invalidate part of the architecture, so they get spiked **before** implementation starts, not discovered during it.

- **S1 — CA trust / certificate pinning.** The whole egress story depends on the agent and its toolchain accepting mitmproxy's CA for TLS interception. If Claude Code pins `api.anthropic.com` (or `npm`/`pip`/`git` pin their registries), MITM fails and that path breaks. Verify a caged request to `api.anthropic.com`, the npm/PyPI registries, and `github.com` all validate against the injected CA. This spike *picks the default enforcement mode for `api.anthropic.com`* (see "Per-host enforcement modes"): if interception works, ship it as a host-wide `intercept` rule so the channel stays audited; if Claude pins, ship it as `mode: passthrough`. Either outcome is a one-line change to the global baseline, not an architectural one — the mode framework absorbs the result. **Resolved (2026-06-04, AC-0001): interception works — `intercept`.** A real Claude Code inference call (`POST /v1/messages`) and the npm/PyPI/GitHub baseline hosts all validate TLS-terminated through the mitmproxy CA; nothing pinned. `api.anthropic.com` ships host-wide `intercept` (channel stays audited), and the `passthrough` path stays an optional feature rather than load-bearing. Findings: `thoughts/shared/research/2026-06-04-s1-ca-trust.md`.
- **S2 — Keychain access from inside the sandbox + concurrent refresh.** On macOS the OAuth token lives in the login Keychain, not a file. Confirm a `sandbox-exec`-confined process can read and refresh the specific Anthropic Keychain item without an interactive auth prompt or denial, and that two concurrent caged sessions sharing the one item don't break each other's refresh-token rotation. Pin down the exact service/item name the Seatbelt profile must allow. **Resolved (2026-06-04, AC-0002): works — two allowances.** The Anthropic OAuth credential is the login-Keychain generic-password item `Claude Code-credentials` (account = login short name); a `sandbox-exec`-confined process reads *and* refreshes it non-interactively given `(allow mach-lookup (global-name "com.apple.SecurityServer"))` plus a file-write scoped to `~/Library/Keychains/login.keychain-db*`. The item's ACL is not bound to Claude's signed identity, and concurrent caged writers are serialized by `securityd` (the single-shared-item assumption holds). The one non-interactive failure mode is a *locked* login keychain (blocking GUI prompt) — `doctor` must detect it. Findings: `thoughts/shared/research/2026-06-04-s2-keychain.md`.
- **S3 — Seatbelt port-level localhost filtering across address families.** Confirm `(remote tcp "127.0.0.1:N")` allow rules genuinely refuse a non-allowlisted localhost port over *both* IPv4 and IPv6, with proxy and services pinned to one family. The "even on localhost" guarantee rests entirely on this. **Resolved (2026-06-04, AC-0003): holds — port-enforced and family-agnostic.** A non-allowlisted localhost port is refused (EPERM) over *both* IPv4 and IPv6 — but via `(remote tcp "localhost:N")`, because the literal-IP rule form this doc assumed does **not** compile on macOS 26.5 ("host must be `*` or localhost"). `localhost` inherently spans v4+v6 and the port is the discriminator, so family-pinning is neither possible nor needed; the compiler emits per-port `localhost:N` rules and must never emit `*:N`. (Corrections to the literal-IP wording below are owned by the gated build tickets.) Findings: `thoughts/shared/research/2026-06-04-s3-localhost-ports.md`.
- **S4 — Proxy-env-var coverage.** Confirm the tools the agent actually uses (Claude Code, `npm`, `pip`, `git` over HTTPS, `go`) all honor `HTTPS_PROXY` and route through mitmproxy; tools that ignore it hard-fail under the deny-all baseline. Note up front: `git` over SSH (`git@github.com:`) cannot traverse an HTTP proxy and is unsupported in v0.1 — HTTPS remotes only. **Resolved (2026-06-04, AC-0004): all five route via `HTTPS_PROXY`.** Claude Code/`npm`/`pip`/`git`-over-HTTPS honor the proxy and the belt-and-suspenders CA env vars (`NODE_EXTRA_CA_CERTS`/`SSL_CERT_FILE`/`REQUESTS_CA_BUNDLE`/`GIT_SSL_CAINFO`); `go` honors `HTTPS_PROXY` but on macOS ignores those CA env vars and trusts the mitmproxy CA only via the keychain — so `setup --no-ca-install` breaks `go`, and its allowlist needs both `proxy.golang.org` and `sum.golang.org`. `git`-over-SSH stays unsupported. Findings: `thoughts/shared/research/2026-06-04-s4-proxy-env.md`.
- **S5 — Appended-profile network narrowing.** The entire network model assumes an `--append-profile` fragment can *narrow* Safehouse's "network: open by default" base stance: the fragment denies `network*` and then re-allows only the proxy and host-service ports. This rests on three stacked assumptions about Seatbelt (SBPL) and Safehouse's append behavior — that the fragment is concatenated *after* Safehouse's base `(allow network*)`, that Seatbelt's last-match-wins precedence lets the fragment's `(deny network*)` override that base allow, and that the fragment's subsequent specific `(allow …)` rules re-open only the intended ports. S3 tests the *precision* of those allow rules but assumes the deny baseline is already in effect; this spike validates that the deny baseline takes effect at all through `--append-profile`. If it doesn't compose this way, the network model needs a different composition strategy (e.g. a full generated profile rather than an append). **Resolved (2026-06-05, AC-0005): narrowing holds — `--append-profile` kept.** A fragment of `(deny network*)` + per-port `(allow network* (remote tcp "localhost:N"))` fed to `safehouse --append-profile` lands *after* the base `(allow network*)` and narrows it: arbitrary egress is EPERM-refused (and DNS denied) while the proxy and a host-service port stay reachable, with a real request flowing end-to-end through the proxy (HTTP 200). All three stacked assumptions confirmed against Agent Safehouse 0.10.1 — and Safehouse's own base already relies on deny-after-allow (its docker/ssh-socket `(deny network-outbound …)` blocks sit after the base allow). The literal-IP rule still doesn't compile (use `localhost:N`, per S3). No fully-generated profile needed — AC-0014/AC-0023 unchanged. Findings: `thoughts/shared/research/2026-06-05-s5-append-profile.md`.

## Goals

- **One command** to start a caged Claude session in any project directory.
- **One config file** (`.agent-creance.yaml`) per project, version-controllable. Optional global defaults at `~/.config/agent-creance.yaml`. Users can split into multiple files via `include:`.
- **TLS-terminating egress** with host + path + method allowlist — the thing Safehouse and `sandbox-runtime` don't ship by default.
- **Strict network isolation** via a generated Seatbelt profile: deny everything, allow only the proxy and explicitly whitelisted host services.
- **Auto-generated allowlists** for project dependencies — list `package_json` or `composer_json` under `generators:` and the corresponding library homepages and repository URLs are added to the allowlist automatically.
- **Multi-agent friendly**: multiple `agent-creance` invocations in the same project share one mitmproxy via a refcounted lock file.
- **Agent-friendly refusals**: three response types (allow / soft-deny / hard-deny) with structured JSON bodies the agent can read and act on, plus a Claude Code skill that teaches the agent how to interpret them.
- **No competition with Safehouse**: agent-creance uses Safehouse's documented extension points (`--append-profile`, `--env-pass`, `--add-dirs`) and never modifies it.

Explicit non-goals: not a hypervisor, not cross-platform (Linux is out of scope for v0.1), not a fork of Safehouse, not a Safehouse competitor. And — important honest one — **not a tool that can hide host services from the cage** if the user binds those services to `0.0.0.0`. The cage either allows them (via the network whitelist) or denies them. Selective hiding is the user's job, via `127.0.0.1`-binding.

## Architecture

![[agent-creance-architecture.svg]]

The CLI is the only thing the user invokes. It reads `.agent-creance.yaml`, compiles two artifacts (a Seatbelt `.sb` profile and a mitmproxy policy JSON), manages the mitmproxy lifecycle for the project, then invokes `safehouse` with the compiled profile and the right env vars pointing at the proxy.

Inside the cage, the agent runs as a normal `sandbox-exec`-confined process — Safehouse handles filesystem and process boundaries (its core competence), while the generated network profile narrows Safehouse's "network: open by default" stance to a strict deny-all-except-proxy-and-whitelisted-host-services.

The agent's only egress path is the mitmproxy on localhost. Mitmproxy terminates TLS using its own CA (installed once in the user's login keychain), matches each request against the compiled policy, and either forwards it or returns a 403 with a structured JSON body explaining the decision. Every request lands in a JSONL audit log.

## What the cage prevents — and what it doesn't

**Prevented (kernel-enforced by Seatbelt):**
- Agent reading host files outside `./` and its redirected Claude config dir — SSH keys, AWS creds, 1Password state, browser cookies, and the *real* `~/.claude` are all denied. (The one deliberate exception is read of the single mitmproxy CA PEM via the `ca.sb` fragment — see "State directory" / AC-0034 — needed so env-var-CA clients trust the proxy; the CA private key stays unreadable.)
- Agent making arbitrary outbound connections — the only network destination available is the mitmproxy.
- Agent reaching host services *not* on the whitelist — even on localhost. Apple's Seatbelt is precise about `(remote tcp "localhost:N")`, so `127.0.0.1:8123` (proxy) and `127.0.0.1:3306` (whitelisted MySQL) work while `127.0.0.1:8080` (some unrelated host service) does not. **Caveat — address family (validated by spike S3/AC-0003):** the rule names a host *token*, not a literal address — only `localhost` and `*` compile (`(remote tcp "127.0.0.1:N")` and `[::1]:N` are rejected with "host must be `*` or localhost"). `localhost` spans **both** `127.0.0.1` (IPv4) and `::1` (IPv6), and the **port** is the discriminator, so the port-level guarantee is *family-agnostic*: a non-allowlisted localhost port is refused (EPERM) over v4 and v6 alike — no IPv4-pinning is needed (and none is possible). The compiler emits `(remote tcp "localhost:N")` per allowlisted port and **never `*:N`** (which would permit external egress). One nuance: `localhost` matches *every* address assigned to this machine (loopback plus interface IPs like `192.168.0.65`), not loopback-only — an intra-machine widening, never external; scoping a service strictly to loopback is the app's job (bind it to `127.0.0.1`). A shipped self-test (`internal/profile` integration test, the S3 probes) confirms a non-allowlisted localhost port is genuinely refused over both v4 and v6.
- Anything spawned by the agent: Seatbelt's sandbox profile is inherited by child processes. When Claude runs `npm install`, `php artisan tinker`, or any other subcommand, those processes get the same filesystem and network restrictions. The cage isn't "the Claude process" — it's "every process descended from the wrapper's invocation."

**Prevented (proxy-enforced):**
- Egress to non-allowlisted hosts/paths/methods.
- *(v0.2)* Credential exfiltration for non-Claude services: once proxy-side secret injection lands, tokens for those services are injected by the proxy at egress and never held in the agent's environment. v0.1 is OAuth-only and does not inject secrets (see "The proxy and the credential story").
- DNS tunneling to unknown nameservers — DNS goes through the proxy's resolver only.

**Not prevented (the honest limits):**
- Damage to project files themselves: `rm -rf .`, destructive SQL, `git reset --hard`. Git, backups, and Claude's `/rewind` are the defense here, not the cage.
- Resource exhaustion: forkbombs, filling `/tmp`, memory exhaustion. macOS Seatbelt doesn't impose cgroup-style limits.
- Background daemons that survive the session: a `nohup something &` inherits the sandbox profile but stays running after the agent exits. `agent-creance doctor` finds and cleans them.
- Supply-chain attacks via allowed toolchains: a malicious `npm install <package>` runs with the same allowlist the legitimate install gets.
- Exfiltration via whitelisted host services: a dev database or cache you expose (`mysql`, `redis`, …) is reached by raw TCP that bypasses the proxy, and several such services can themselves open outbound connections (Redis `REPLICAOF`/`MIGRATE`, MySQL `FEDERATED`/`LOAD DATA`). A prompt-injected agent could use a whitelisted service as a confused deputy to reach the network around the egress filter. This is inherent to giving the agent your dev data store — hiding it would make debugging useless — so agent-creance accepts and documents it rather than pretending the cage covers it.
- Sandbox escapes: `sandbox-exec` is not a VM. Determined adversarial code execution is out of scope.
- Claude's own OAuth token — the agent has to use it, so it's reachable by the agent (on macOS via a Seatbelt-granted ACL to the one login-Keychain item, not a file in the config dir — see "The proxy and the credential story"; the real `~/.claude` is never writable). A prompt injection can therefore still exfiltrate the token, but only through an allowlisted destination, since direct egress is blocked. Note this is *every* allowlisted host that accepts an agent-controlled body (e.g. a `POST` to the GitHub API), not just `api.anthropic.com`: the allowlist *narrows* the exfil surface to your allowed POST targets, it does not eliminate it. A `mode: passthrough` host is the *least* observable target of all — uninspectable by design — so the audit log cannot even record what was sent there; this is the deliberate trade for not intercepting it. (What the ephemeral-config-dir approach *does* fully close is config-persistence — the agent cannot plant a hook, MCP server, or skill that fires on your next un-caged Claude run.)

For the "agent goes full YOLO and walks away" use case, this is good enough on macOS — the practical damage is confined to the project files plus whatever the whitelist explicitly permits. For genuinely untrusted code execution where the agent is adversarial, you'd want a VM, which this isn't trying to be.

## The configuration

`.agent-creance.yaml` lives at the project root, gets committed alongside the code:

```yaml
# .agent-creance.yaml
agent:
  command: ["claude", "--dangerously-skip-permissions"]
  workdir: .

safehouse:
  # Forwarded to safehouse as --add-dirs* / --enable= flags.
  # Note: ~/.claude is intentionally NOT listed here. agent-creance
  # gives the cage a redirected, ephemeral Claude config dir via
  # CLAUDE_CONFIG_DIR so the real ~/.claude is never writable by the
  # agent (see "The proxy and the credential story").
  add_dirs_rw: [.]
  add_dirs_ro: [~/.config/git]
  enable: [shell-init]

# Optionally include other config files. The global config in
# ~/.config/agent-creance.yaml is included implicitly if it exists;
# additional includes let users structure their config however
# they want (a separate deny.yaml, a team-shared.yaml, etc).
include:
  - .agent-creance/team-shared.yaml

network:
  # Host services reachable from inside the cage. The `<label>:<port>`
  # form's label is cosmetic — the Seatbelt rule keys on the PORT, via
  # the `localhost` host token (the only loopback token that compiles; a
  # literal 127.0.0.1/::1 is rejected). `localhost` covers BOTH IPv4 and
  # IPv6, so caged tooling may address these services as `localhost` or
  # `127.0.0.1` — either works, and only the listed ports are reachable
  # (see the "address family" caveat under "What the cage prevents").
  # The cage does NOT block other host services bound to 0.0.0.0 — the
  # user must bind those to 127.0.0.1 themselves.
  host_services:
    - mysql:3306
    - redis:6379
    - mailpit:1025

  egress:
    # Built-in generators that produce allow rules from project
    # manifests. A bare name reads the root manifest; the object form
    # (type + path) points a generator at a specific manifest, so a
    # monorepo lists the same type once per package. See "Allowlist
    # generators" below.
    generators:
      - package_json
      - composer_json
      # - type: package_json
      #   path: apps/web/package.json

    # Soft-allow rules. URLs not matched here get a soft-deny (the agent
    # may escalate to the user if it really needs them).
    allow:
      - host: api.github.com
        paths: ["/repos/tobyS/this-project/"]
        methods: [GET, POST]

    # Hard denies. The agent gets a different response type for these
    # (see "Network refusal handling" below) and is instructed to never
    # ask the user to allow them. Use this for SEO sources, content
    # farms, untrustworthy authors, secrets paths, etc.
    deny_always:
      - host: w3schools.com
        reason: "Known low-quality source. Use MDN or official docs instead."
      - host: "*.medium.com"
        paths: ["/@*"]
        reason: "User-published Medium posts are unreliable for technical content."
      - host: "*"
        paths: ["**/.env", "**/.git/config"]
        reason: "Secrets path."

env:
  GIT_AUTHOR_NAME: "Toby (caged)"
  # v0.1 sets plain env vars for the agent only. Just-in-time secret
  # injection (op:// references resolved host-side and injected by the
  # proxy at egress, never entering the agent's environment) is a v0.2
  # feature; v0.1 is OAuth-only and does not inject credentials.
```

The global file `~/.config/agent-creance.yaml` has the same schema; it holds your "always-allowed" baseline (Claude's API, npm/PyPI/cache.nixos.org registries, GitHub orgs you trust, etc.) and your personal hard-deny list (sources you never want the agent to consult). Per-project configs only declare deltas. `include:` resolution is recursive with cycle detection and a depth limit; later files override earlier ones for scalar fields, while `allow:` and `deny_always:` lists union additively.

Default behavior for unmatched URLs is implicit: anything not in `allow:` and not in `deny_always:` produces a soft-deny. There is no separate "default" knob — the categories are exhaustive by design.

## Allowlist generators

Manually allowlisting every library a project depends on is tedious and bit-rotty. v0.1 ships two generators that read the project's dependency manifests and emit allow rules for the libraries' official homepages and source repositories:

- **`package_json`** — reads `package.json`, walks the direct dependencies (`dependencies` + `devDependencies`, no transitives), and looks each one up on the npm registry (the per-version endpoint `registry.npmjs.org/<pkg>/latest` — full packuments can exceed the HTTP body cap; vite's is ~39 MB).
- **`composer_json`** — reads `composer.json`, walks `require` + `require-dev` direct dependencies, and looks each one up on Packagist (`packagist.org`).

A bare entry (`- package_json`) reads the manifest at the project root (`./package.json`). The default path is owned by the generator itself, which also declares the installed-dependency directory it manages (`node_modules/` for `package_json`, `vendor/` for `composer_json`) — used by the init scan below.

For each direct dependency, the generator emits:

- An allow rule for the package's `homepage`, if the registry reports one. If the homepage is a bare host (e.g. `https://react.dev/`), the rule covers the whole host. If it carries a path (e.g. `https://someuser.github.io/coollib/`), the rule is scoped to that path prefix (`github.io/someuser/coollib/`) — this is what stops a package hosted on a shared, path-multiplexed domain (`github.io`, `*.readthedocs.io`, …) from allowlisting every other tenant's content. Subdomain-based shared hosting (`coollib.vercel.app`) is already correctly scoped, since each project gets its own host.
- An allow rule for the package's `repository` host scoped to `/<org>/<repo>/` (covers web view + `.git` operations), if the registry reports one.

If a package has no homepage in its registry metadata, no homepage rule is emitted — the user can `agent-creance allow ...` it manually if it matters. Same for missing repositories.

**Forge content hosts.** A repository on a forge isn't reachable from one host alone — viewing files, cloning, fetching raw content, and pulling release assets each hit a *different* host, and a `repository` rule scoped to `github.com/<org>/<repo>/` covers none of them. So when the repository host is a known forge, the generator emits companion allow rules for that forge's content hosts, scoped to the same `<org>/<repo>` wherever the host's URL layout permits. For a GitHub `repository`, alongside `github.com/<org>/<repo>/`:

- `raw.githubusercontent.com/<org>/<repo>/` — raw file fetches (READMEs, schemas, example configs, install scripts).
- `codeload.github.com/<org>/<repo>/` — tarball/zip downloads and `git` clone-over-HTTPS.
- `<org>.github.io/<repo>/` — project documentation pages.
- `objects.githubusercontent.com` — the release-asset CDN. Its URLs are hash-addressed, not org-scoped, so this rule is necessarily host-wide; it's emitted as a separate, lower-trust rule that a stricter threat model can drop.

GitLab and other forges get analogous companion-host tables (`gitlab.com` → `*.gitlab.io`, etc.) as they're added. The forge→content-host mapping is data, not per-generator code, so extending it is a table edit — and the same table is what `agent-creance allow <repo-url>` consults, so a manual repo allow expands to the same companion set.

**Monorepos — one generator per manifest.** A generator may be listed more than once for the same type, each scoped to a manifest path, so a monorepo gets dependency allow-rules for every one of its packages:

```yaml
generators:
  - type: package_json
    path: apps/web/package.json
  - type: package_json
    path: apps/api/package.json
  - type: composer_json
    path: services/billing/composer.json
```

The bare-string form is unchanged and equivalent to the object form with the root manifest path (`- package_json` ≡ `{type: package_json, path: package.json}`), so existing single-package configs keep working untouched. Identical `(type, path)` entries dedupe across the global/project/include layers, and the input-hash cache watches every referenced manifest, so editing any package's manifest triggers a recompile. When a generated rule comes from a sub-package manifest, its `policy show` annotation carries the path (`generated:package_json:apps/web/package.json:react`) to disambiguate which package produced it; a root-manifest rule keeps the bare `generated:package_json:react` form.

`agent-creance init` pre-populates these entries automatically — see "The `init` command".

**No further options in v0.1.** Beyond the manifest path, a generator's behavior is hardcoded; the user gets the defaults above. Other per-generator configuration (transitive-deps mode, narrower path scoping) is a future extension if a concrete use case justifies it.

**Trust model.** The generator trusts registry-reported `homepage` and `repository` fields verbatim, without validation. The reasoning: if you've decided to depend on a package, you're already going to execute its code (and its `postinstall`/scripts) — at that point, trusting the maintainer's stated homepage URL is a strictly smaller leap of faith. A malicious maintainer can already do far worse than getting a domain allowlisted. The honest consequence: malicious packages can declare arbitrary `homepage:` values to get arbitrary hosts allowlisted. Users with stricter threat models should pin/audit dependencies and avoid this generator, or use `deny_always:` to shadow specific hosts they don't want allowed regardless of generator output.

**Manifest as source of truth.** The generator reads the manifest (`package.json` / `composer.json`), not the lockfile. The registry lookup fetches the latest version's metadata matching whatever version range is declared. This is good-enough for v0.1: package homepage/repository URLs rarely change between versions. Lockfile-based generation is a future refinement if version-drift becomes a problem in practice.

**Caching.** Two layers:

- **Per-package metadata cache** at `~/.cache/agent-creance/registries/<registry>/<package>.json`, refreshed lazily (default: 30 days). Survives across projects.
- **Generator output cache** keyed on the manifest's hash; if `package.json` hasn't changed, the previous run's rules are reused without re-fetching anything.

First `agent-creance run` on a new project with 50 npm direct deps: ~10 second one-time cost. Steady state: zero.

**Visibility.** Generated rules are part of the compiled policy but flagged with their source. `agent-creance policy show` dumps the full resolved policy with each rule annotated:

```
[explicit]                         allow github.com /repos/tobyS/this-project/
[generated:package_json:react]     allow react.dev (any path)
[generated:package_json:react]     allow github.com /facebook/react/
[generated:composer_json:laravel/framework]  allow laravel.com (any path)
[global:claude-defaults]           allow api.anthropic.com /v1/
[explicit]                         deny w3schools.com  (reason: Known low-quality source)
```

`agent-creance policy explain URL` answers "why was this allowed/denied" for a specific URL by reporting the matching rule and its source.

**Future, post-v0.1.** Once the generator framework is in place, several refinements become natural extensions:

- Generators for other ecosystems: `pyproject_toml`, `cargo_toml`, `go_mod`, `gemfile`. Each is a small additional Go file.
- A documentation generator that, given a list of dependencies, prompts the agent (via the user's existing Claude session) to expand the allowlist with each library's commonly-used external documentation hosts beyond just the registry-stated homepage. Useful when a library's docs are spread across multiple hosts.
- Per-generator configuration: transitive-deps mode, narrower path scoping, custom manifest paths.

None of these are in v0.1. The schema accommodates them when they land.

## Per-host enforcement modes

Not every allowed host wants the same enforcement style. A rule carries an optional `mode:` (default `intercept`) that selects how the proxy handles it:

```yaml
network:
  egress:
    allow:
      # Mode A — filtered intercept (default). TLS-terminated, matched on
      # host + path + method, full audit (path/method/status).
      - host: api.github.com
        paths: ["/repos/tobyS/this-project/"]
        methods: [GET, POST]

      # Mode B — host-wide intercept. TLS-terminated, any path/method on
      # the host, still fully audited, still subject to path-based
      # deny_always (e.g. **/.env). Just omit paths/methods.
      - host: react.dev
        mode: intercept

      # Mode C — passthrough. The proxy tunnels raw bytes (CONNECT
      # passthrough) and never terminates TLS. Used for hosts that pin
      # their certificate, or hosts the user trusts enough to not inspect.
      - host: api.anthropic.com
        mode: passthrough
```

**Passthrough is host-granularity only, by construction** — without TLS termination the proxy sees only the CONNECT target. Its rules and limits:

- `paths`/`methods` on a passthrough rule are meaningless and are **rejected at compile time** (a clear error, not a silent ignore).
- The host **allow/deny decision still happens** at CONNECT time, so a `deny_always` on a passthrough host is still enforced (the tunnel is refused). But **path-based denies cannot apply** — e.g. `deny_always` with `paths: ["**/.env"]` will *not* be caught on a passthrough host. This is a real hole the user accepts per passthrough host, which is why passthrough is a deliberate, explicit choice rather than a default.
- **Audit degrades** for passthrough hosts to host + timestamp + byte counts — no path, method, or response status.
- **Soft-deny is unaffected.** Every non-passthrough host is intercepted, so the structured 403 path (below) is intact; a passthrough host is always an `allow` by definition and never produces a 403.

**Visibility.** Because each passthrough host is an audit blind spot, `agent-creance policy show` flags passthrough rules distinctly (the same way generated rules are annotated), so the blind spots are visible at a glance rather than buried.

`api.anthropic.com` is the canonical passthrough candidate: it's Claude's own backend, the OAuth token *belongs* there, and interception buys near-zero security value (there's no meaningful policy to enforce on Claude-talking-to-Anthropic) while carrying all of [S1](#open-spikes-resolve-before-build)'s certificate-pinning risk. Whether the shipped global baseline sets it to `intercept` (keep the audit) or `passthrough` (dodge pinning) is exactly what S1 decides.

## Network refusal handling

The proxy distinguishes three response types so the agent can react appropriately without being trained to ask about every block:

**Allowed.** Normal HTTP response from the upstream server. The URL matched an `allow` rule (or a rule emitted by a generator) and no `deny_always` rule shadowed it. Header: none. The agent proceeds as usual.

**Soft-deny** — *"not in the allowlist, but could be added if it matters."* HTTP 403 with `X-Cage-Reason: soft-deny` and a JSON body:

```json
{
  "error": "agent_cage_not_allowlisted",
  "url": "https://docs.somelib.io/v2/auth/",
  "host": "docs.somelib.io",
  "path": "/v2/auth/",
  "method": "GET",
  "how_to_proceed": "Not on the project allowlist. Ignore this resource if you can find the needed information elsewhere or can work reliably without it. If you think the information is important and would contribute significantly to your success, prompt the user and ask them to add the resource to the allowlist.",
  "allow_command_suggestion": "agent-creance allow 'docs.somelib.io/v2/auth/'"
}
```

The agent's instructions (via the shipped skill, see below) say: ignore the resource and proceed if the needed information is available elsewhere or the agent can work reliably without it; escalate to the user — asking them to allowlist it — only when the information is important and would contribute significantly to success.

**Hard-deny** — *"on the permanent block list, do not ask, find another way."* HTTP 403 with `X-Cage-Reason: hard-deny` and a JSON body including the `reason:` field from the matching `deny_always` rule:

```json
{
  "error": "agent_cage_hard_deny",
  "url": "https://w3schools.com/...",
  "reason": "Known low-quality source. Use MDN or official docs instead.",
  "how_to_proceed": "Permanently blocked. Do NOT ask the user to allow it. Do NOT retry. Find an alternative source."
}
```

The agent's instructions say: never escalate hard-denies, treat them as final, find an alternative source or tell the user no authoritative source could be found.

The skill explains all three response types to Claude. It activates automatically when Claude sees the `X-Cage-Reason` header or the `agent_cage_` JSON error prefix — it's installed once by `agent-creance setup` into `~/.claude/skills/agent-creance/SKILL.md`. We don't touch the project's `CLAUDE.md`.

## Config compilation

![[agent-creance-policy-flow.svg]]

`agent-creance` hashes the inputs (the project YAML + all transitively-included files + the implicit global + the manifests referenced by enabled generators + the session-overlay file holding any `allow --once` rules, see "Session-scoped allows") on every invocation. When the hash matches the cached one, it skips regeneration entirely — same-config runs are near-instant. When inputs changed, it runs the configured generators and produces the compiled artifacts in the project's **state directory** — `~/.cache/agent-creance/projects/<hash>/`, where `<hash>` derives from the canonical project path (the same identity scheme the lock file uses).

The state directory lives *outside* the source tree on purpose. The agent runs with the project mounted read-write, so anything security-critical kept inside `./` would be editable by the very process it constrains — and because mitmproxy hot-reloads its policy on mtime change, an in-tree `policy.json` could be rewritten by a prompt-injected agent to allow itself anything. Keeping the compiled policy, the enforcer, the lock, and the audit log out-of-tree means the agent cannot rewrite its own controls; none of these files are mounted into the cage at all. Only host-side processes read them — the CLI, mitmproxy, and safehouse-at-launch.

- **`network.sb`** — a Seatbelt profile *fragment* passed to Safehouse via `--append-profile` (it narrows Safehouse's base `allow network*`; validated by spike S5/AC-0005, so it carries no `(version 1)`/`(deny default)` header of its own). Contains the `deny network*` baseline plus the narrow `(remote tcp "localhost:<port>")` allow rules derived from `network.host_services`. **It is exempt from the input-hash cache — regenerated on every launch.** The `.sb` is a few lines of text, so regenerating it is free; the input-hash cache exists to skip the *expensive* part — the `policy.json` generator registry fetches — not this. The **live proxy port** is supplied as a *separate* launch-time fragment, appended after `network.sb` (the port is ephemeral — `:0`-allocated, see "Multi-agent lifecycle" — and read from the lock file at launch, so it cannot be baked into a config-hash-keyed artifact). Both fragments use the `localhost` host token, never a literal IP (which does not compile) and never `*:N`.
- **`ca.sb`** — a second Seatbelt fragment, also passed via `--append-profile` and regenerated per launch (AC-0034). It grants in-cage read of *exactly* the one mitmproxy CA PEM (`~/.mitmproxy/mitmproxy-ca-cert.pem`, symlink-resolved so the literal matches the kernel's firmlink path), plus metadata-only on its parent dir for `open()` traversal. Safehouse's `(deny default)` base otherwise denies `~/.mitmproxy`, so the four injected CA env vars (`NODE_EXTRA_CA_CERTS` / `SSL_CERT_FILE` / `REQUESTS_CA_BUNDLE` / `GIT_SSL_CAINFO`) would point at an unreadable file and env-var-CA clients (node, python) could not trust the proxy. The grant is the one deliberate filesystem widening agent-creance makes; the CA *private key* (`mitmproxy-ca.pem`) in the same dir stays unreadable (metadata-only, never read-data). The `env-ca-node` / `env-ca-python` cage-verification vectors guard it.
- **`policy.json`** — the mitmproxy enforcer addon reads this on every request. Compact representation of the unioned `allow` and `deny_always` rules (explicit + generator output + included files). Mitmproxy's addon polls the file's mtime and reloads on change, so `agent-creance allow ...` from another terminal propagates within milliseconds. Because the file lives out-of-tree and unmounted, only the host-side CLI can write it — the agent cannot.

The mitmproxy addon itself (`enforcer.py`) is shipped embedded in the agent-creance binary and extracted to the state directory on first run — it's a constant, not a per-project file. Only the policy JSON it reads is per-project.

The one thing that stays in the source tree is the human-authored config (`.agent-creance.yaml` and any `include:`d files) — it's version-controlled input, and the agent editing it changes nothing until the next `agent-creance run` recompiles, which the agent inside the cage cannot trigger. The live policy the proxy enforces is always the out-of-tree compiled artifact.

### Session-scoped allows

`agent-creance allow --once URL` adds a rule that lives only for the current project session, without touching the committed `.agent-creance.yaml`. It is written to a **session-overlay file in the project's state directory** (out-of-tree, like everything else security-critical), and the compiler unions it on top of the YAML inputs exactly as it unions an `include:`d file. Because the overlay is part of the compilation inputs, adding a `--once` rule recompiles `policy.json` and the running proxy hot-reloads it within milliseconds.

Its scope is the **project session, not a single invocation**: with the shared refcounted proxy, every agent attached to the project sees the rule (they share one `policy.json`). The overlay is **purged on last-agent-exit teardown** — when the trap handler removes the final PID and tears the proxy down, it deletes the overlay so the rule does not silently survive into the next session. (Explicit `agent-creance clean` also removes it, for the orphan-cleanup path where no live teardown ran.)

agent-creance requires two tools installed on the host: `agent-safehouse` and `mitmproxy`. It does **not** auto-install them — installing other people's software silently is overreach, especially for a security tool. When either is missing, agent-creance refuses to run and prints actionable install instructions:

```
agent-creance requires the following tools, which are not installed:

  - agent-safehouse: brew install eugene1g/safehouse/agent-safehouse
  - mitmproxy:       brew install mitmproxy

Install both and run `agent-creance setup`.
```

The prerequisite check runs on every command (it's cheap — `exec.LookPath` plus a `--version` invocation). When both are missing, both are listed; the user installs once rather than running, failing, installing one, running, failing again.

**Tested-against versions are compiled into the binary** as constants, bumped per agent-creance release. On `run`, `setup`, and `doctor`, agent-creance parses the installed versions and compares against these constants:

- **Exact match** — silent.
- **Patch-level mismatch** (e.g. tested 1.4.2, installed 1.4.5) — silent on `run` and `setup` (bugfixes don't usually change behavior); `doctor` always reports it.
- **Minor or major mismatch** — warned loudly on `run`, `setup`, and `doctor`. Single-line yellow warning per command:

  ```
  ⚠ agent-safehouse 2.0.1 installed, but agent-creance was tested against 1.4.2.
    Behavior may differ. Run `agent-creance doctor` for details.
  ```

- **Unparseable version** — treated as compatible, no warning. The cost of a false warning every time the upstream tool's version format shifts would be worse than the cost of a missed warning.

**agent-creance never blocks on a version mismatch.** Even a major-version skew only warns; the user might know exactly what they're doing. Only a *missing* tool is a hard failure. The threshold for that is "the binary isn't on `$PATH`" — anything that reports a version, parseable or not, is given the benefit of the doubt.

`agent-creance doctor` produces a full compatibility report — and unlike normal commands, it highlights **every** mismatch, including patch-level ones:

```
Version compatibility:
  agent-safehouse:  installed 1.4.5, tested against 1.4.2     ⚠ patch skew
  mitmproxy:        installed 12.0.1, tested against 12.0.1   ✓
  
  agent-creance was tested against specific versions of its dependencies.
  Other versions may work, but behavior is not guaranteed. If you hit
  unexpected issues, try pinning to the tested versions or open an
  issue with your version combination.
```

The patch-level info is silent on `run`/`setup` precisely because it's noise during normal work — but on `doctor` (which exists to tell you everything it noticed) it's surfaced explicitly. That way, when something weird happens, "what versions am I on" is one command away.

## Commands

```sh
agent-creance setup                 # one-time: install mitmproxy CA into keychain (with post-install
                                    #   verification curl-test), install skill into ~/.claude/skills/
agent-creance setup --no-skill      # opt out of the skill install
agent-creance setup --no-ca-install # use the CA via env vars only, don't trust system-wide

agent-creance init                  # writes .agent-creance.yaml template in the project; scans for
                                    #   manifests (package.json, composer.json) at the root and up to
                                    #   two directory levels deep — skipping node_modules/ & vendor/ —
                                    #   and pre-populates one generators: entry per detected manifest
agent-creance run                   # starts the cage and the agent; if setup hasn't been run
                                    #   yet (no trusted CA, no skill), prints a clear pointer
                                    #   to `agent-creance setup` and exits non-zero rather than
                                    #   failing with a stack trace

agent-creance allow URL             # append a soft-allow rule to .agent-creance.yaml
agent-creance allow --once URL      # project-session-scoped allow (see "Session-scoped allows")
agent-creance allow --global URL    # append to ~/.config/agent-creance.yaml instead
agent-creance deny URL              # append a deny_always rule (optionally with --reason)

agent-creance policy show           # dump the fully-resolved policy with rule sources
agent-creance policy explain URL    # show which rule (if any) matches a given URL
agent-creance policy refresh        # force re-fetch of generator registry metadata

agent-creance doctor                # check Safehouse install, CA trust (with live curl-verify),
                                    #   prerequisite versions (highlights every mismatch including
                                    #   patch-level), orphan proxies, exposed host services
agent-creance doctor --fix          # auto-fix what it can

agent-creance logs --follow         # tail the egress log live (rotation-aware)
agent-creance logs --summary        # what was allowed/blocked

agent-creance status                # show running cages across all projects
agent-creance clean                 # tear down this project's proxy and lock
```

`agent-creance run` blocks while the agent runs, traps signals to ensure cleanup, decrements the proxy's refcount on exit, and kills the proxy only when the last agent has exited.

`run` reports its startup progress on **stderr** (its stdout belongs to the agent session): each major step — policy compile, sandbox profile, proxy start — is announced and completed with a `✓` line carrying its duration, and the agent launch is announced last. When the policy compile misses its input-hash cache, an expectation message explains the wait (first run or changed config/manifest; metadata is fetched from packagist/npm and cached for future runs) and each manifest shows a live per-dependency lookup counter — rewritten in place when stderr is a terminal, degraded to ~25/50/75% milestone lines when piped (CI). A fully cached run shows the same steps completing in milliseconds. All of this happens strictly before the agent takes the terminal, and a failing step's announcement gives the `error:` line its phase context. (AC-0041)

## Multi-agent lifecycle

![[agent-creance-lifecycle.svg]]

Two agents in the same project share one mitmproxy. The proxy's lifecycle is governed by a lock file (`proxy.lock` in the project's state directory, `~/.cache/agent-creance/projects/<hash>/` — out-of-tree, so the caged agent cannot corrupt the refcount) tracking the proxy's PID, port, current policy hash, and the PIDs of attached agents.

**Project identity.** The "project" a lock file belongs to is identified by the canonical absolute path of the project directory (`realpath` resolution). This means symlinked aliases resolve to the same project (correct: it's the same physical directory) and renamed/moved projects are seen as different projects (correct: any running proxy under the old path is irrelevant, the new path gets a fresh proxy after a `clean` of the orphan). `agent-creance doctor` warns when the project lives on a filesystem with unreliable `flock` semantics (notably iCloud Drive and SMB shares).

Every `agent-creance run` invocation:

1. **Acquires `flock`** on the lock file (atomicity for read-modify-write).
2. **Validates state** — prunes dead agent PIDs (via `kill -0`), verifies the proxy is alive (a recycled PID is a real hazard, so liveness is *both* a `kill -0` on the recorded PID *and* a TCP probe of the recorded port — a bare PID match is not trusted), checks whether the on-disk policy hash matches what the running proxy was started with. If the proxy is dead but agents are listed, it's been a crash and we start fresh. If the hash differs, we touch `policy.json` so mitmproxy hot-reloads.
3. **Starts or attaches.** Starts mitmproxy if none is running for this project; either way, adds its own PID to the agents array and releases the lock.
4. **Exec safehouse** with the right flags and env vars (proxy URL, CA path, project-specific append-profile).

On any exit (clean, SIGINT, crash), the wrapper's trap handler reacquires the flock, removes its PID, and kills the proxy if and only if the agents array is now empty. The last agent out is, by definition, responsible for cleanup — but no single agent needs to know whether *it* started the proxy. The lock file is the single source of truth.

**Process group handling.** The wrapper starts its child (Safehouse, which execs the agent, which spawns its own children) in a new process group (`Setpgid: true`). When stdin is a terminal, that group is also made the terminal's **foreground** group at spawn (`SysProcAttr{Foreground, Ctty}` — AC-0042): a TUI agent in a *background* group would be stopped by `SIGTTIN`/`SIGTTOU` on its first tty access and the wrapper would wait forever. As the foreground group, the agent's subtree receives keyboard signals (Ctrl-C) directly from the kernel; signals sent to the wrapper itself (`kill`, context cancel) are still forwarded to the whole group via `kill(-pgid, signal)`. Either way, a Ctrl-C reliably tears down the agent and everything it spawned (`npm run test`, `php artisan ...`, etc.) — not just the wrapper. With a non-tty stdin (tests, CI) the foreground handover is skipped. After the agent exits, the wrapper reclaims the terminal (ignoring `SIGTTOU`) for its own teardown output, and waits for the whole group before doing the lock-file decrement, so cleanup ordering is deterministic.

For two projects with different policies running concurrently, each gets its own mitmproxy on its own port. Port allocation binds mitmproxy to `:0` and lets the OS assign a free ephemeral port, which is then recorded in the lock file; subsequent runs of the same project read the port back from the lock. There's no need to hash the path into a port range and step on collisions — the lock is already the single source of truth for the port, so the deterministic-hashing scheme was just complexity without payoff.

**Crash recovery and the port.** An already-running agent's Seatbelt profile is frozen at *its own* launch — it cannot be rewritten mid-flight. So if mitmproxy crashes while agents are attached and is restarted on a *different* port, those surviving agents are stranded: their `network.sb` only allows the old port, and there is no way to amend it without relaunching them. To avoid that, a restart **attempts to reclaim the recorded port** (best-effort `bind` to the same number); if it succeeds, the attached agents recover transparently. Reclaim cannot be guaranteed — ephemeral ports are exactly the range the OS hands to any process binding `:0`, so another process (often another project's proxy) may already hold it; `SO_REUSEADDR` does not help against a live holder.

**agent-creance never kills attached agents to recover** — that could disrupt important in-flight sessions. When the port could not be reclaimed *and* agents are still attached, the restarting `agent-creance run` emits a loud warning naming the affected session PIDs ("proxy restarted on port N (was M); attached agents <pids> will see egress failures and should be relaunched"), and `doctor`/`status` surface the same condition. **Honest limitation:** a stranded agent's next request hits a dead port and gets a raw connection-refused error — *not* a structured cage refusal, because there is no proxy there to return the `X-Cage-Reason` JSON. The shipped skill therefore won't engage for this case; to the agent it looks like a generic network outage.

## Audit log

mitmproxy writes a JSONL file at `egress.jsonl` in the project's state directory (`~/.cache/agent-creance/projects/<hash>/`), mode `0600`, with sensitive headers (`Authorization`, `Cookie`, `X-Api-Key`, etc.) filtered before logging. Each entry records timestamp, method, URL, decision (allow / soft-deny / hard-deny), matching rule, and response status. Keeping the log out-of-tree matters for integrity: the agent runs with `./` writable, so an in-tree audit log could be truncated or doctored by the very process it records. `agent-creance logs` is how you read it — it never needs to live in the repo.

**Rotation.** When a write would push the file past 500 MB, the existing `.1` backup (if any) is deleted, the current file is renamed to `egress.jsonl.1`, and the next write begins a fresh current file. This caps disk use at roughly 1 GB per project (current + backup), and never silently drops entries.

**Tooling.** `agent-creance logs --follow` is rotation-aware (implemented natively in Go via `fsnotify`, not `tail -f`). `agent-creance logs --summary` reads `.1` and the current file in order, treating them as one logical stream.

**Gitignore.** With all runtime, compiled, and session state living out-of-tree, there's nothing for agent-creance to add to `.gitignore` — the only in-tree files are the human-authored config you commit deliberately. (`agent-creance init` no longer writes a gitignore block; session-scoped `allow --once` rules are written to the state directory and purged on last-agent-exit teardown — see "Session-scoped allows" — so they never touch the repo either.)

## The proxy and the credential story

Mitmproxy is a normal host daemon — installed via Homebrew, runs as your user, no container needed. The first-ever run generates a CA in `~/.mitmproxy/`, which `agent-creance setup` installs into the login keychain (one `sudo` prompt, one time). After that the agent trusts the CA via the system trust store, plus belt-and-suspenders env vars: `NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE`, `GIT_SSL_CAINFO`.

**v0.1 is OAuth-only (Claude Pro/Max), and does no credential injection.** Proxy-side secret injection for non-Claude services (`op://` references resolved host-side, tokens added as `Authorization` at egress so the agent never sees them) is a v0.2 feature. In v0.1 the only credential in play is Claude Code's own OAuth token, handled as follows.

**Login happens on the host, never in the cage.** The interactive OAuth browser flow (`claude` login) is run normally on the host, outside the sandbox, before the first caged run — a prerequisite, like being logged into `gh`. The cage never opens a browser or handles the callback; it only needs the *resulting* credential plus the ability to *refresh* it, and refresh is a plain HTTPS call to the Anthropic token endpoint, which is on the global allowlist baseline and so traverses the proxy like any other allowed request.

**Executable config and the credential need opposite handling.** Mounting `~/.claude` read-write would expose far more than the token — `settings.json`, hooks, MCP server definitions, and `skills/` are all *executable config* that fires on the user's next, un-caged Claude run. The token, by contrast, is shared state that every session must read *and* write. So agent-creance splits them:

- **Executable config is redirected and ephemeral.** The caged agent is pointed at a throwaway config directory via `CLAUDE_CONFIG_DIR`, in the project's state directory (`~/.cache/agent-creance/projects/<hash>/claude/`), seeded with a minimal sanitized settings file and mounted read-write — via an explicit `--add-dirs` of exactly that `…/claude` dir (AC-0035), since it lives under `~/.cache`, which safehouse's base policy does not grant. The real `~/.claude` is mounted read-only or not at all. This **fully closes the config-persistence vector** — a prompt-injected agent cannot plant a hook, MCP server, or skill that survives into a later session.
- **The credential stays in the one shared store.** On macOS, Claude Code normally does *not* keep the OAuth token in a config file — typically there is no `~/.claude/.credentials.json` on the machine — it lives in the login **Keychain**, keyed by service name, independent of `CLAUDE_CONFIG_DIR` (the file-based exception is handled below). agent-creance deliberately does **not** clone it per project; every caged session uses the same Keychain item, exactly as un-caged Claude Code does. The generated Seatbelt profile grants the cage access to that one item (mach-lookup to `securityd` plus the item's ACL).

**File-based credential fallback (out of scope for v0.1).** The split above assumes the token is in the Keychain — the macOS default. Some setups instead keep a file-based `~/.claude/.credentials.json` (older versions, managed configs, CI-style machines). v0.1 does **not** support that case: with the real `~/.claude` mounted read-only, a file-based token can be neither refreshed nor rotated, so the credential model would fail closed. Rather than silently break, v0.1 should **detect** the situation (Keychain item absent but a credentials file present) at `run`/`doctor` and refuse with a clear message — "caged sessions require a Keychain-stored credential; run `claude` login on the host" — instead of a confusing mid-session TLS/auth failure. Supporting file-based credentials properly is deferred to a later version.

**Why not clone the credential per project (the concurrency reason).** OAuth refresh rotates: a successful refresh typically issues a new refresh token and invalidates the old one. If each project cloned the credential into its own config dir and refreshed on its own cycle, the first project to refresh would rotate the token out from under all the others, and a sync-back-on-exit step would be a last-writer-wins race that clobbers good tokens with stale ones. Keeping a single shared Keychain item makes the store itself the synchronization point — whoever refreshes writes the rotated token back to the one place everyone reads. Any residual race (two sessions refreshing in the same instant) is a narrow, pre-existing window identical to running two un-caged Claude sessions at once, not something agent-creance introduces. **Spike [S2](#open-spikes-resolve-before-build) validates both the sandboxed Keychain access and the concurrent-refresh behavior before we build on this.**

**What this does not close (honest).** The token is still *readable* by the agent — it has to be, because the agent uses it (the cage is granted the Keychain item). A prompt injection can therefore still exfiltrate it, but only through an allowlisted destination, since direct egress is blocked — and that means *every* allowlisted host accepting an agent-controlled body (e.g. a `POST` to the GitHub API), not just `api.anthropic.com`. The allowlist **narrows** the exfil surface to your allowed POST targets; it does not eliminate it. For OAuth-in-the-cage there's no way around this short of not giving the agent the token at all — v0.2's proxy-side injection is what removes tokens from the agent's reach for *non-Claude* services, but Claude's own token is fundamentally exposed because the agent is the thing using it.

**Post-install CA verification.** `security add-trusted-cert` on macOS returns exit code 0 even when the user cancels the auth dialog or the trust policy is set wrong — failure mode where the cert exists in the keychain but isn't actually trusted by clients. After running the install, `agent-creance setup` does a live verification: spawn a short-lived mitmproxy on a random localhost port, make a `curl` request to `https://example.com` through that proxy, and verify the cert chain validates against the system trust store. If verification fails, the user gets an explicit error pointing at the likely cause (cancelled prompt, missing trust setting) instead of a confusing "everything looks fine" followed by mysterious TLS errors during the first `agent-creance run`. The same verification runs as part of `agent-creance doctor` on every invocation, so a keychain change that revokes the CA gets caught immediately.

## Tech stack

**Go.** Single static binary, single-step Homebrew install, no runtime dependency on user machines. Standard library covers everything: YAML via `gopkg.in/yaml.v3`, signal handling via `os/signal`, file locking via `golang.org/x/sys/unix.Flock`, subprocess management via `os/exec`, log-file watching via `fsnotify`. CLI structure via `cobra` (the same library powering `kubectl`, `gh`, `helm` — familiar to the contributor pool).

The mitmproxy enforcer is the one piece of Python in the stack — a small addon of four modules (`enforcer.py` plus the `policy`/`audit`/`responses` siblings it imports). All four are embedded in the Go binary via `embed` and extracted together to a constant, cross-project location in the out-of-tree state directory (`~/.cache/agent-creance/enforcer/`) on first run, refreshed whenever the binary's embedded copy changes. It's a constant, not a per-project file (only the `policy.json` it reads is per-project). Users never install or version it themselves — they just see "mitmproxy is running."

The reason for Go over Bash (despite Safehouse's Bash precedent): agent-creance has refcounted shared resources, atomic lock-file updates, signal handling across child processes, and YAML/JSON manipulation. Each of these is doable in Bash and several of them are miserable. The maintainability cliff for Bash projects sits around 1500-2000 lines; agent-creance will live near that boundary. Go costs ~1-2 second build cycles for the guarantee of correctness on the concurrency-sensitive paths.

## Extension model for future contributions

The design is currently focused on one job: starting Claude inside Safehouse with the proxy in front. But future contributors may want to add more lifecycle-managed processes (a Redis sniffer, a request replay recorder, a stats collector that watches the JSONL log). The architecture leaves a clean extension point:

**Config-driven process registration.** Plugin authors drop YAML files in `~/.config/agent-creance/processes.d/` declaring a process to start alongside the cage, its env, how to know it's ready, how to stop it. agent-creance manages them with the same refcount/lifecycle logic as mitmproxy. No code needed from contributors — only configuration.

Example future plugin:

```yaml
# ~/.config/agent-creance/processes.d/redis-spy.yaml
name: redis-spy
command: ["redis-cli", "--latency-history"]
start_when: cage_starts
stop_when: cage_stops
env:
  REDIS_HOST: localhost
ready_check:
  port: 6379
```

This isn't on the v0.1 roadmap — but the schema includes a `plugins:` block from day one so the door is open. If the constraint ever shows up that someone wants Turing-complete plugin logic (a custom decision function for the proxy, a custom CA installer, etc.), the next step would be subprocess plugins on the Terraform/kubectl model: any executable named `agent-creance-FOO` on `$PATH`, invoked with structured JSON over stdio at known lifecycle events. Still language-agnostic; still doesn't require contributors to write Go.

## v0.1 → v1.0 roadmap

- **v0.1:** core orchestration — Safehouse invocation, mitmproxy lifecycle with refcounting, network `.sb` and `policy.json` compilation, two built-in generators (`package_json`, `composer_json`), per-host enforcement modes (`intercept`/`passthrough`), three-response-type enforcement, skill install, `agent-creance allow`/`deny`/`policy` commands, JSONL audit log with rotation, CA bootstrap with post-install verification, prerequisite check (refuse-and-suggest) with tested-version warnings, `doctor` command, process-group signal forwarding.
- **v0.2:** secret injection (1Password, env), DNS resolver in the proxy with blocklists (Hagezi, OISD), structured deny-decision log alongside the main audit log, additional ecosystem generators (`pyproject_toml`, `cargo_toml`, etc.) as contributions arrive.
- **v0.3:** Haiku-as-judge for ambiguous URLs (optional, requires API key; default off so the proxy needs no credentials to operate); agent-driven documentation-host expansion (prompt the user's running agent to enrich each library's allowlist beyond just the registry-stated homepage).
- **v0.4:** config-driven process plugins (the `~/.config/agent-creance/processes.d/` mechanism above); community policy bundles via `bundle:` references in the schema; per-generator options (transitive-deps, custom paths, etc.).
- **v0.5:** non-Claude agents (Codex, Gemini, Cursor CLI) — mostly just verifying their behavior under the cage and shipping starter policies.
- **v1.0:** stable config schema, signed releases, Homebrew tap, contribution guide for plugin authors.

## Prior art

- **[agent-safehouse](https://agent-safehouse.dev/)** — the foundation. macOS-native filesystem + process sandboxing via `sandbox-exec`, Apache-2.0, single Bash script. agent-creance's filesystem and process boundaries are entirely Safehouse's.
- **Anthropic [`sandbox-runtime`](https://github.com/anthropic-experimental/sandbox-runtime)** — Apache-2.0, same `sandbox-exec` primitive plus optional HTTP/SOCKS5 proxies. Recently added opt-in TLS termination. Conceptually adjacent but containerless and without the per-project YAML/refcount/multi-agent shape.
- **[mattolson/agent-sandbox](https://github.com/mattolson/agent-sandbox)** — Docker-based on macOS via Colima, mitmproxy sidecar + iptables + path-level allowlist + secret injection. Closest prior art for the proxy design. Linux-VM-via-Colima cost; macOS-only by virtue of Colima.
- **mitmproxy** — the egress proxy. Apache-2.0, Python addon API. agent-creance ships a small addon (`enforcer.py`) that reads `policy.json` and enforces host/path/method allowlists with hot reload.

License: Apache-2.0, same as Safehouse and the rest of the prior art.
