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
- Agent reading host files outside `./` and `~/.claude` — SSH keys, AWS creds, 1Password state, browser cookies are all denied.
- Agent making arbitrary outbound connections — the only network destination available is the mitmproxy.
- Agent reaching host services *not* on the whitelist — even on localhost. Apple's Seatbelt is precise about `(remote tcp "localhost:N")`, so `127.0.0.1:8123` (proxy) and `127.0.0.1:3306` (whitelisted MySQL) work while `127.0.0.1:8080` (some unrelated host service) does not.
- Anything spawned by the agent: Seatbelt's sandbox profile is inherited by child processes. When Claude runs `npm install`, `php artisan tinker`, or any other subcommand, those processes get the same filesystem and network restrictions. The cage isn't "the Claude process" — it's "every process descended from the wrapper's invocation."

**Prevented (proxy-enforced):**
- Egress to non-allowlisted hosts/paths/methods.
- Credential exfiltration for non-Claude services: tokens injected by the proxy at egress, never held in the agent's environment.
- DNS tunneling to unknown nameservers — DNS goes through the proxy's resolver only.

**Not prevented (the honest limits):**
- Damage to project files themselves: `rm -rf .`, destructive SQL, `git reset --hard`. Git, backups, and Claude's `/rewind` are the defense here, not the cage.
- Resource exhaustion: forkbombs, filling `/tmp`, memory exhaustion. macOS Seatbelt doesn't impose cgroup-style limits.
- Background daemons that survive the session: a `nohup something &` inherits the sandbox profile but stays running after the agent exits. `agent-creance doctor` finds and cleans them.
- Supply-chain attacks via allowed toolchains: a malicious `npm install <package>` runs with the same allowlist the legitimate install gets.
- Sandbox escapes: `sandbox-exec` is not a VM. Determined adversarial code execution is out of scope.
- Claude's own OAuth tokens in `~/.claude` — they must be mounted because Claude Code refreshes them, so a prompt injection that reads the file can exfiltrate. The egress allowlist is the only mitigation: tokens can only leave through the proxy, which only forwards to `api.anthropic.com`.

For the "agent goes full YOLO and walks away" use case, this is good enough on macOS — the practical damage is confined to the project files plus whatever the whitelist explicitly permits. For genuinely untrusted code execution where the agent is adversarial, you'd want a VM, which this isn't trying to be.

## The configuration

`.agent-creance.yaml` lives at the project root, gets committed alongside the code:

```yaml
# .agent-creance.yaml
agent:
  command: ["claude", "--dangerously-skip-permissions"]
  workdir: .

safehouse:
  # Forwarded to safehouse as --add-dirs* / --enable= flags
  add_dirs_rw: [.]
  add_dirs_ro: [~/.claude, ~/.config/git]
  enable: [shell-init]

# Optionally include other config files. The global config in
# ~/.config/agent-creance.yaml is included implicitly if it exists;
# additional includes let users structure their config however
# they want (a separate deny.yaml, a team-shared.yaml, etc).
include:
  - .agent-creance/team-shared.yaml

network:
  # Host services reachable from inside the cage.
  # These open both a Seatbelt network-allow and a host_services
  # entry; the cage does NOT block other host services bound to 0.0.0.0
  # — the user must bind those to 127.0.0.1 themselves.
  host_services:
    - mysql:3306
    - redis:6379
    - mailpit:1025

  egress:
    # Built-in generators that produce allow rules from project
    # manifests. See "Allowlist generators" below.
    generators:
      - package_json
      - composer_json

    # Soft-allow rules. URLs not matched here get a soft-deny (the agent
    # may escalate to the user if it really needs them).
    allow:
      - host: api.github.com
        paths: ["/repos/schlitt/this-project/"]
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
  # Just-in-time secret injection; resolved by the host CLI before
  # the proxy starts, never enters config or the agent's environment
  ANTHROPIC_API_KEY: "op://Personal/Anthropic/api-key"
```

The global file `~/.config/agent-creance.yaml` has the same schema; it holds your "always-allowed" baseline (Claude's API, npm/PyPI/cache.nixos.org registries, GitHub orgs you trust, etc.) and your personal hard-deny list (sources you never want the agent to consult). Per-project configs only declare deltas. `include:` resolution is recursive with cycle detection and a depth limit; later files override earlier ones for scalar fields, while `allow:` and `deny_always:` lists union additively.

Default behavior for unmatched URLs is implicit: anything not in `allow:` and not in `deny_always:` produces a soft-deny. There is no separate "default" knob — the categories are exhaustive by design.

## Allowlist generators

Manually allowlisting every library a project depends on is tedious and bit-rotty. v0.1 ships two generators that read the project's dependency manifests and emit allow rules for the libraries' official homepages and source repositories:

- **`package_json`** — reads `./package.json`, walks the direct dependencies (`dependencies` + `devDependencies`, no transitives), and looks each one up on the npm registry (`registry.npmjs.org`).
- **`composer_json`** — reads `./composer.json`, walks `require` + `require-dev` direct dependencies, and looks each one up on Packagist (`packagist.org`).

For each direct dependency, the generator emits:

- An allow rule for the package's `homepage` host (any path), if the registry reports one.
- An allow rule for the package's `repository` host scoped to `/<org>/<repo>/` (covers web view + `.git` operations), if the registry reports one.

If a package has no homepage in its registry metadata, no homepage rule is emitted — the user can `agent-creance allow ...` it manually if it matters. Same for missing repositories.

**No options in v0.1.** A generator's behavior is hardcoded; the user lists generators by name and gets the defaults above. Per-generator configuration (custom paths, transitive-deps mode, narrower path scoping) is a future extension if a concrete use case justifies it.

**Trust model.** The generator trusts registry-reported `homepage` and `repository` fields verbatim, without validation. The reasoning: if you've decided to depend on a package, you're already going to execute its code (and its `postinstall`/scripts) — at that point, trusting the maintainer's stated homepage URL is a strictly smaller leap of faith. A malicious maintainer can already do far worse than getting a domain allowlisted. The honest consequence: malicious packages can declare arbitrary `homepage:` values to get arbitrary hosts allowlisted. Users with stricter threat models should pin/audit dependencies and avoid this generator, or use `deny_always:` to shadow specific hosts they don't want allowed regardless of generator output.

**Manifest as source of truth.** The generator reads the manifest (`package.json` / `composer.json`), not the lockfile. The registry lookup fetches the latest version's metadata matching whatever version range is declared. This is good-enough for v0.1: package homepage/repository URLs rarely change between versions. Lockfile-based generation is a future refinement if version-drift becomes a problem in practice.

**Caching.** Two layers:

- **Per-package metadata cache** at `~/.cache/agent-creance/registries/<registry>/<package>.json`, refreshed lazily (default: 30 days). Survives across projects.
- **Generator output cache** keyed on the manifest's hash; if `package.json` hasn't changed, the previous run's rules are reused without re-fetching anything.

First `agent-creance run` on a new project with 50 npm direct deps: ~10 second one-time cost. Steady state: zero.

**Visibility.** Generated rules are part of the compiled policy but flagged with their source. `agent-creance policy show` dumps the full resolved policy with each rule annotated:

```
[explicit]                         allow github.com /repos/schlitt/this-project/
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
  "how_to_proceed": "Not on the project allowlist. If this is genuinely important...",
  "allow_command_suggestion": "agent-creance allow 'docs.somelib.io/v2/auth/'"
}
```

The agent's instructions (via the shipped skill, see below) say: route around silently if you have any alternative, escalate to the user only if all three of (no alternative exists, source is authoritative, information is genuinely needed) are true.

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

`agent-creance` hashes the inputs (the project YAML + all transitively-included files + the implicit global + the manifests referenced by enabled generators) on every invocation. When the hash matches the cached one, it skips regeneration entirely — same-config runs are near-instant. When inputs changed, it runs the configured generators and produces two artifacts under `.agent-creance/cache/`:

- **`network.sb`** — a Seatbelt profile that gets passed to Safehouse via `--append-profile`. Contains the `deny network*` baseline plus the narrow allow rules derived from `network.host_services` and the proxy port.
- **`policy.json`** — the mitmproxy enforcer addon reads this on every request. Compact representation of the unioned `allow` and `deny_always` rules (explicit + generator output + included files). Mitmproxy's addon polls the file's mtime and reloads on change, so `agent-creance allow ...` from another terminal propagates within milliseconds.

The mitmproxy addon itself (`enforcer.py`) is shipped embedded in the agent-creance binary and extracted to `.agent-creance/cache/enforcer.py` on first run — it's a constant, not a per-project file. Only the policy JSON it reads is per-project.

## Prerequisites and version handling

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

agent-creance init                  # writes .agent-creance.yaml template in the project; detects
                                    #   existing manifests (package.json, composer.json) and
                                    #   pre-populates the generators: list accordingly
agent-creance run                   # starts the cage and the agent; if setup hasn't been run
                                    #   yet (no trusted CA, no skill), prints a clear pointer
                                    #   to `agent-creance setup` and exits non-zero rather than
                                    #   failing with a stack trace

agent-creance allow URL             # append a soft-allow rule to .agent-creance.yaml
agent-creance allow --once URL      # session-scoped allow; removed on agent-creance clean
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

## Multi-agent lifecycle

![[agent-creance-lifecycle.svg]]

Two agents in the same project share one mitmproxy. The proxy's lifecycle is governed by a lock file (`.agent-creance/cache/proxy.lock`) tracking the proxy's PID, port, current policy hash, and the PIDs of attached agents.

**Project identity.** The "project" a lock file belongs to is identified by the canonical absolute path of the project directory (`realpath` resolution). This means symlinked aliases resolve to the same project (correct: it's the same physical directory) and renamed/moved projects are seen as different projects (correct: any running proxy under the old path is irrelevant, the new path gets a fresh proxy after a `clean` of the orphan). `agent-creance doctor` warns when the project lives on a filesystem with unreliable `flock` semantics (notably iCloud Drive and SMB shares).

Every `agent-creance run` invocation:

1. **Acquires `flock`** on the lock file (atomicity for read-modify-write).
2. **Validates state** — prunes dead agent PIDs (via `kill -0`), verifies the proxy is alive, checks whether the on-disk policy hash matches what the running proxy was started with. If the proxy is dead but agents are listed, it's been a crash and we start fresh. If the hash differs, we touch `policy.json` so mitmproxy hot-reloads.
3. **Starts or attaches.** Starts mitmproxy if none is running for this project; either way, adds its own PID to the agents array and releases the lock.
4. **Exec safehouse** with the right flags and env vars (proxy URL, CA path, project-specific append-profile).

On any exit (clean, SIGINT, crash), the wrapper's trap handler reacquires the flock, removes its PID, and kills the proxy if and only if the agents array is now empty. The last agent out is, by definition, responsible for cleanup — but no single agent needs to know whether *it* started the proxy. The lock file is the single source of truth.

**Process group handling.** The wrapper starts its child (Safehouse, which execs the agent, which spawns its own children) in a new process group via `setsid` (or equivalent `Setpgid: true` on the Go side). Signals delivered to the wrapper (`SIGINT`, `SIGTERM`) are forwarded to the whole group via `kill(-pgid, signal)`. This ensures that Ctrl-C reliably tears down the agent and everything it spawned (`npm run test`, `php artisan ...`, etc.) — not just the wrapper. Without this, orphan processes inside the sandbox can survive a Ctrl-C and keep churning until they exit on their own. The wrapper waits for the whole group to exit before doing the lock-file decrement, so cleanup ordering is deterministic.

For two projects with different policies running concurrently, each gets its own mitmproxy on its own port. Port allocation hashes the canonical project path to `8000 + hash mod 999`, with collision-stepping if that port is in use, persisted in the lock so subsequent runs of the same project are deterministic.

## Audit log

mitmproxy writes a JSONL file at `.agent-creance/egress.jsonl`, mode `0600`, with sensitive headers (`Authorization`, `Cookie`, `X-Api-Key`, etc.) filtered before logging. Each entry records timestamp, method, URL, decision (allow / soft-deny / hard-deny), matching rule, and response status.

**Rotation.** When a write would push the file past 500 MB, the existing `.1` backup (if any) is deleted, the current file is renamed to `egress.jsonl.1`, and the next write begins a fresh current file. This caps disk use at roughly 1 GB per project (current + backup), and never silently drops entries.

**Tooling.** `agent-creance logs --follow` is rotation-aware (implemented natively in Go via `fsnotify`, not `tail -f`). `agent-creance logs --summary` reads `.1` and the current file in order, treating them as one logical stream.

**Gitignore.** `agent-creance init` writes a `.gitignore` block including `.agent-creance/egress.jsonl*` (catches both files) and `.agent-creance/cache/`.

## The proxy and the credential story

Mitmproxy is a normal host daemon — installed via Homebrew, runs as your user, no container needed. The first-ever run generates a CA in `~/.mitmproxy/`, which `agent-creance setup` installs into the login keychain (one `sudo` prompt, one time). After that:

- The agent trusts the CA via the system trust store, plus belt-and-suspenders env vars: `NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE`, `GIT_SSL_CAINFO`.
- The proxy holds API keys for non-Claude services (resolved on the host via 1Password's `op` CLI before the proxy starts) and injects them as `Authorization` headers at egress. The agent never sees them; a prompt injection that exfiltrates environment variables leaks nothing useful for those services.
- Claude's OAuth credentials live in `~/.claude` because Safehouse mounts that path read-write — the agent needs to refresh tokens. A prompt injection reading that file *can* exfiltrate Claude tokens; the egress allowlist is the only mitigation (tokens can only leave through the proxy, which only forwards to `api.anthropic.com`).

**Post-install CA verification.** `security add-trusted-cert` on macOS returns exit code 0 even when the user cancels the auth dialog or the trust policy is set wrong — failure mode where the cert exists in the keychain but isn't actually trusted by clients. After running the install, `agent-creance setup` does a live verification: spawn a short-lived mitmproxy on a random localhost port, make a `curl` request to `https://example.com` through that proxy, and verify the cert chain validates against the system trust store. If verification fails, the user gets an explicit error pointing at the likely cause (cancelled prompt, missing trust setting) instead of a confusing "everything looks fine" followed by mysterious TLS errors during the first `agent-creance run`. The same verification runs as part of `agent-creance doctor` on every invocation, so a keychain change that revokes the CA gets caught immediately.

## Tech stack

**Go.** Single static binary, single-step Homebrew install, no runtime dependency on user machines. Standard library covers everything: YAML via `gopkg.in/yaml.v3`, signal handling via `os/signal`, file locking via `golang.org/x/sys/unix.Flock`, subprocess management via `os/exec`, log-file watching via `fsnotify`. CLI structure via `cobra` (the same library powering `kubectl`, `gh`, `helm` — familiar to the contributor pool).

The mitmproxy enforcer (`enforcer.py`) is the one piece of Python in the stack. It's embedded in the Go binary via `embed`, extracted to `.agent-creance/cache/enforcer.py` on first run. Users never install or version it themselves — they just see "mitmproxy is running."

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

- **v0.1:** core orchestration — Safehouse invocation, mitmproxy lifecycle with refcounting, network `.sb` and `policy.json` compilation, two built-in generators (`package_json`, `composer_json`), three-response-type enforcement, skill install, `agent-creance allow`/`deny`/`policy` commands, JSONL audit log with rotation, CA bootstrap with post-install verification, prerequisite check (refuse-and-suggest) with tested-version warnings, `doctor` command, process-group signal forwarding.
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
