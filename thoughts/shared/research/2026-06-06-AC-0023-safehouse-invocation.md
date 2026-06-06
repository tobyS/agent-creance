---
date: 2026-06-06
ticket: AC-0023
title: "Safehouse invocation (WP-4.2) — argv + env construction"
status: complete
tags: [research, cage, safehouse, proxy, credentials, WP-4.2]
related:
  - thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md
  - thoughts/shared/research/2026-06-04-s4-proxy-env.md
  - thoughts/shared/research/2026-06-05-s5-append-profile.md
  - thoughts/shared/research/2026-06-04-s1-ca-trust.md
git_commit: fe26455f0dfed54b7ca4bb8a066e782bb847a3ae
---

# Research: AC-0023 — Safehouse invocation (WP-4.2)

## Research question

How should `internal/cage` construct the `safehouse` argv + environment for a
compiled config: the mount-dir / capability flags, the live proxy env vars, the
four CA-bundle vars, the redirected ephemeral `CLAUDE_CONFIG_DIR` (seeded with a
sanitized settings file), and the two `--append-profile` fragments — as a pure,
golden-testable function of `(config, port, paths)`?

## Summary

Everything upstream of `internal/cage` already exists and is stable; the package
itself does **not** exist yet. The job is a thin, mostly-pure composition layer:

- **Inputs** are three already-built, independent producers joined only by
  `state.Layout`: the typed `*config.Config` (the `safehouse:`/`agent:`/`env:`
  blocks), the per-project `state.Layout` (path accessors), and the live proxy
  **port** (returned by `proxy.Manager.Attach`). Plus the mitmproxy CA path.
- The exact `safehouse` 0.10.1 CLI contract is now confirmed from `safehouse
  --help` (see "Safehouse CLI contract" below) — this closes every flag-spelling
  ambiguity the design doc left open.
- The env is delivered via `--env-pass NAMES` (a named-in-WP-4.2 mechanism):
  agent-creance sets the computed vars in the `safehouse` child process env and
  lists their names in `--env-pass`, so safehouse forwards them into the cage on
  top of its sanitized defaults. This keeps the builder a pure function returning
  `(args []string, env []string)`, both golden-testable.
- Two side effects are required and must be split out from the pure builder to
  satisfy AC criterion 4 (pure argv+env): **(a)** seeding the ephemeral
  `CLAUDE_CONFIG_DIR` with a minimal sanitized `settings.json`, and **(b)**
  writing the launch-time proxy-port `.sb` fragment to a file so it can be passed
  as the second `--append-profile` (nothing writes it today — `internal/profile`
  only *renders* the string, and its doc comment explicitly calls this "the
  ordering contract for AC-0023").

The established project patterns (pure argv builder behind a `const Binary`,
`sysdep` seams injected at construction, `state.New(paths)` built internally,
golden tests with a `-update` flag, deterministic sorting before render) map
cleanly onto this work.

## Detailed findings

### The ticket

`thoughts/shared/tickets/AC-0023-safehouse-invocation.md`. Depends on AC-0014
(the `.sb`, now `internal/profile`) and AC-0022 (creds, `internal/cred`). Gated by
spikes **S4** (proxy env) and **S5** (append-profile), both resolved PASS with no
reshape. Out of scope: process-group/signal forwarding (AC-0024) and `run`
orchestration (AC-0025). Acceptance criteria (ticket lines 26-29):

1. `--add-dirs`/`--add-dirs-ro`/`--enable` from `safehouse:` config; `--append-profile` from the `.sb`.
2. Inject `HTTPS_PROXY` + the four CA vars (`NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE`, `GIT_SSL_CAINFO`).
3. Redirect `CLAUDE_CONFIG_DIR` to `~/.cache/agent-creance/projects/<hash>/claude/`, seeded with minimal sanitized settings; real `~/.claude` RO/absent.
4. The argv+env is a **pure function of (config, port, paths)** — golden-testable without launching anything.

### Safehouse CLI contract (confirmed from `safehouse --help`, v0.10.1)

The installed binary is `/opt/homebrew/bin/safehouse`, self-identifying as **Agent
Safehouse 0.10.1**. Verified flags:

| Need | Flag | Format |
|------|------|--------|
| RW mount dirs | `--add-dirs PATHS` | **colon**-separated paths (single flag) |
| RO mount dirs | `--add-dirs-ro PATHS` | **colon**-separated paths (single flag) |
| Capabilities | `--enable=FEATURES` | **comma**-separated feature names |
| Env passthrough | `--env-pass NAMES` | **comma**-separated var *names*; reads values from host env; repeatable; deduped |
| Append profile | `--append-profile PATH` | a file PATH; **repeatable**, appended in argument order |
| Workdir | `--workdir DIR` | RW workdir; empty string disables auto-workdir grants |
| Command | `safehouse [opts] [--] <command> [args...]` | wrapped command after `--` |

Supported `--enable` values include `shell-init`, `keychain`, `docker`, `ssh`,
`process-control`, etc. (`shell-init` = shell startup file reads; the config
example uses `[shell-init]`).

**Env-injection mechanism.** Default mode runs the wrapped command with
"sanitized defaults". `--env-pass NAMES` passes named vars "through from host env
on top of sanitized defaults" (incompatible with `--env`). So the clean path:
agent-creance sets `HTTPS_PROXY=…`, the CA vars, `CLAUDE_CONFIG_DIR=…` (and any
`config.env`) in the `safehouse` child's environment and passes
`--env-pass HTTPS_PROXY,SSL_CERT_FILE,…`. The builder therefore returns the *set
of additional env vars* and lists their keys in `--env-pass`; the run
orchestration (AC-0025) merges them onto the inherited env when it execs. There is
also a brittle `safehouse -- VAR=123 cmd` prefix form ("one-off child env vars"),
but `--env-pass` is the documented, WP-4.2-named mechanism and keeps values out of
argv.

**Do NOT pass `--trust-workdir-config`** (default disabled): it would load
`<workdir>/.safehouse`, which a prompt-injected agent could plant.

### Inputs the builder consumes

**Config** — `internal/config/config.go`:
- `config.Config` (config.go:29-35): `Agent`, `Safehouse`, `Include`, `Network`, `Env map[string]string`.
- `config.Safehouse` (config.go:45-49): `AddDirsRW []string`, `AddDirsRO []string`, `Enable []string` (yaml `add_dirs_rw`/`add_dirs_ro`/`enable`).
- `config.Agent` (config.go:38-41): `Command []string`, `Workdir string`.
- Paths in config are kept **verbatim**; the doc comment (config.go:43-44) explicitly states `~` expansion is "the safehouse compiler's job, a later phase" → that is AC-0023.

**State layout** — `internal/state/state.go`:
- `state.New(paths sysdep.PathResolver) *Resolver` (state.go:64); `(*Resolver).Resolve(dir) (Layout, error)` (state.go:83).
- `Layout` accessors: `NetworkSB()` → `<root>/network.sb` (state.go:179, the `--append-profile` cached fragment); `ClaudeConfigDir()` → `<root>/claude` (state.go:195, the redirect target); `PolicyJSON()`, `ProxyLock()`, `EgressJSONL()`, `SessionOverlay()`. Root = `<cache>/agent-creance/projects/<hash>` where `<hash>` is 16 hex chars of `sha256(canonical project path)`; cache honours `XDG_CACHE_HOME` else `$HOME/.cache` (state.go:102-114, 164-173).
- **No accessor exists yet for the launch-time proxy-port fragment file** — a new `Layout.ProxyProfileSB()` (e.g. `<root>/proxy.sb`) is the consistent place to add it.

**Proxy port** — `internal/proxy/lifecycle.go`:
- `(*Manager).Attach(ctx, StartConfig) (Attachment, error)` (lifecycle.go:96) returns `Attachment.Port int` (lifecycle.go:80) — "the live proxy port the caller must point the cage at". The proxy binds `127.0.0.1` (lifecycle.go:230-239). There is **no** URL formatter — the cage builds `http://127.0.0.1:<port>`.

**Proxy fragment renderer** — `internal/profile/profile.go`:
- `RenderProxyFragment(port int) (string, error)` (profile.go:74) renders the live-port allow line; it omits its own `(deny network*)` and "relies on network.sb being appended before it (the ordering contract for AC-0023)". **Nothing writes this to disk yet** (grep: only the renderer + its tests). AC-0023 writes it and passes both fragment paths to `--append-profile` in order: `network.sb` then the proxy fragment.

**CA path** — there is **no Go constant** for it anywhere. The path is
`~/.mitmproxy/mitmproxy-ca-cert.pem` (docs/design.md:426, s1/s4 research,
AC-0001:40). Reachable via `sysdep.PathResolver.UserHomeDir()`. All four CA vars
get this **same** value (S4).

**Credentials** — `internal/cred/cred.go`: `Detect(...)` is a pre-flight **gate
only**; it returns `Result{Status}` and injects **nothing** into the cage env (the
in-cage agent uses the Keychain directly via the `.sb` ACL). So AC-0023 does *not*
consume cred output for env construction — credential refusal is a `run`/`doctor`
precondition (AC-0025), not a cage concern.

### The env set (S4, authoritative — `2026-06-04-s4-proxy-env.md:181-195`)

S4 explicitly gates "AC-0023 (WP-4.2 env injection)" and prescribes (P = live
port, host `127.0.0.1`):

- Proxy routing (set BOTH upper- and lower-case): `HTTPS_PROXY`/`https_proxy`, `HTTP_PROXY`/`http_proxy` = `http://127.0.0.1:P`; `NO_PROXY`/`no_proxy` = `localhost,127.0.0.1,::1`. All proxy vars use `http://` even for HTTPS.
- CA trust (all = the mitmproxy CA PEM path): `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE`, `NODE_EXTRA_CA_CERTS`, `GIT_SSL_CAINFO`.
- `go` is intentionally **not** given any CA var (macOS go trusts only the keychain CA); no `GOPROXY` tweak. `ALL_PROXY` deliberately omitted. Lowercase variants are "cheap insurance," not strictly required.

The ticket text names only `HTTPS_PROXY` + the four CA vars; S4 is broader (adds
`HTTP_PROXY`/`NO_PROXY` + lowercase). **Open question 1** below.

### CLAUDE_CONFIG_DIR redirect + sanitized seed (design.md:432-435)

`CLAUDE_CONFIG_DIR` → `~/.cache/agent-creance/projects/<hash>/claude/`
(= `Layout.ClaudeConfigDir()`), mounted RW, "seeded with a minimal sanitized
settings file"; the real `~/.claude` is "read-only or not at all". This closes the
config-persistence vector (no planted hook/MCP/skill survives). The credential is
**not** seeded here — it stays in the Keychain. The **exact contents** of the
sanitized seed are not specified anywhere (design says only "minimal sanitized
settings file" that omits hooks/MCP/skills) → **Open question 2** below.

Since the credential lives in the Keychain (reached via mach-lookup ACL in the
`.sb`, not via a mount), the real `~/.claude` does **not** need to be mounted at
all — simply never adding it to `--add-dirs*` satisfies "absent" and the AC's
negative test (never mount real `~/.claude` RW) trivially.

### Path expansion (AC-0023's job)

`add_dirs_rw: [.]` and `add_dirs_ro: [~/.config/git]` show the two cases the cage
must expand: `~` → `UserHomeDir()`, and relative/`.` → absolute against the
canonical project dir (`Layout.Canonical`). `agent.workdir: .` → `--workdir`
likewise. Config keeps these verbatim by design (config.go:43-44).

### sysdep seam + composition root

- `internal/sysdep/pathresolver.go:20-33`: `Abs`, `EvalSymlinks`, `UserHomeDir()`, `Getenv(key)` — the home/env seam. Fake: `sysdeptest/pathresolver.go` (`HomeDir`, `Env`).
- `internal/sysdep/filesystem.go:19-39`: `ReadFile`/`WriteFile`/`Stat`/`MkdirAll`/`Remove`/`Rename` — for seeding + writing the proxy fragment. Fake: `sysdeptest/filesystem.go` (in-memory).
- `cli.App` (cli.go:20-36) holds `Commander`, `FS`, `Paths`, `Clock`, `HTTP`, `Keychain`. **No** `ProcessGroup`/`ProcessManager` field yet (those land with AC-0024/0025). A `cage` constructor should follow the established pattern: take the `sysdep` seams, build `state.New(paths)` internally (cf. `proxy/extract.go:51-59`, `policy/compile/compile.go:99-118`).

### Patterns to copy

- **Pure argv builder behind a `const`**: `internal/proxy/lifecycle.go:230-239` (`mitmArgs(port, cfg) []string`, `const proxyBin`). Tested without executing in `lifecycle_internal_test.go:9-71` via `assertContainsPair`.
- **Golden text test with `-update`**: `internal/profile/profile_test.go:14-52` (+ `testdata/*.golden`). JSON-marshal variant + reusable helper: `internal/generator/generator_test.go:21-78`.
- **Deterministic ordering**: `internal/generator/manifest.go:74-100` (`sortedUnique`); order-preserving dedup `internal/profile/profile.go:83-94`.

## Code references

- `internal/config/config.go:29-49` — `Config`, `Safehouse`, `Agent` structs.
- `internal/state/state.go:69-77,176-199` — `Layout` + path accessors.
- `internal/proxy/lifecycle.go:77-87,96,230-239` — `Attachment.Port`, `Attach`, `mitmArgs` pattern.
- `internal/profile/profile.go:54,74` — `RenderNetworkSB`, `RenderProxyFragment` (no writer yet).
- `internal/cred/cred.go:103-117` — `Detect` (gate only, no env injection).
- `internal/sysdep/{pathresolver,filesystem}.go` — the seams cage needs.
- `internal/cli/cli.go:20-36,67-85` — `App` + `Main()` wiring.
- `internal/proxy/extract.go:51-59` — constructor pattern (`state.New(paths)` internally).
- `docs/design.md:82-90,424-441` — `safehouse:` schema + credential story.

## Open questions for planning

1. **Env var breadth.** Inject the full S4 confirmed set (`HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` + lowercase variants + 4 CA vars) — the authoritative spike result — or only the `HTTPS_PROXY` + 4 CA vars literally listed in the ticket? *(Recommend: full S4 set; S4 gates this ticket.)*
2. **Sanitized `settings.json` seed contents.** Write an empty `{}` (most minimal/secure — contains nothing executable by construction), or a small fixed baseline object? *(Recommend: minimal `{}`.)*
3. **Scope of the built argv.** Should the cage also (a) inject the user's `config.env` map via the same `--env-pass` path, (b) map `agent.workdir` → `--workdir`, and (c) append `config.agent.command` after `--` so the integration test can run `echo ok`? *(Recommend: yes to all — they are pure functions of config and the integration test in this ticket requires a wrapped command.)*

## Risks / notes

- **Version skew:** `internal/buildinfo/buildinfo.go:35` records tested-against
  `agent-safehouse: 1.4.2`, but the installed binary is **0.10.1** (the version
  S5 verified). The flags above are from 0.10.1. Reconcile the constant (or the
  installed tool) before the integration test, and bump per the buildinfo
  convention when re-tested.
- The full isolation matrix is verified by AC-0033 (M3 gate); this ticket's
  integration test is the minimal "egress blocked except via proxy" smoke.
