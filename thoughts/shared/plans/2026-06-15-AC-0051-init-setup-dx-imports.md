---
date: 2026-06-15
ticket: AC-0051
title: "Plan — First-run DX: import allowlist & ports during init/setup"
status: ready
branch: main
research: thoughts/shared/research/2026-06-14-AC-0051-init-setup-dx-imports.md
---

# Implementation Plan: AC-0051 — First-run DX imports

## Overview

Make first run do the tedious allowlist/port wiring for the engineer. `init`
gains three optional, TTY-gated import steps (web domains, MCP servers, static
dev ports) plus a review-before-write confirm and an end-of-run agent prompt;
`setup` seeds the global baseline from the user's global Claude Code config when
it first creates that file; and a new `agent-creance import <file>` command
merges an agent-generated YAML fragment into the project config after review.

All imports are conservative and reviewed; non-interactive runs behave exactly
as today.

## Decisions (from the planning checkpoint)

- **Remote MCP servers** (remote `https` `url`) → allow rule with
  `mode: passthrough` (no methods — passthrough rules cannot carry methods).
- **Web domains** (`WebFetch(domain:…)` ∪ `sandbox.network.allowedDomains`) →
  allow rule `mode: intercept`, `methods: [GET]`. A bare `*` domain is **skipped**
  (it would allow all egress).
- **Static port detection** reads docker-compose.yml, package.json scripts,
  Procfile, and `.env` (broadest).
- Local **stdio** MCP servers are ignored (no host/port). An MCP `url` whose host
  is `localhost`/`127.0.0.1`/`::1` is a **port** (`host_services`), not egress.

## Current state

- `runInit` (`internal/cli/init.go:54-91`) scans manifests, renders a static
  template (`configTemplate` `:310-329`, no `host_services:` block), writes once.
  The only interactive gate is the host-setup `confirm` (`:145-157`).
- `runSetup` → `scaffoldGlobalConfig` (`internal/cli/setup.go:104-126`) writes a
  static `globalConfigTemplate` (`:137-178`) only when the global file is absent;
  it never overwrites.
- Config writes are comment-preserving only via `config.AppendRule`
  (`internal/config/edit.go:52-81`), which handles **allow/deny_always rules
  only** — no `host_services`/`generators` splice exists. There is no
  Config→YAML serializer.
- `config.Parse` (`internal/config/config.go:144-178`) strict-validates a full or
  partial document. `matchHost` (`internal/policy/glob.go:10-24`) already matches
  `*`/`*.suffix`. Files are read through `app.FS` (`sysdep.FileSystem`); JSON is
  parsed leniently (`internal/generator/manifest.go:18-54`).
- Nothing reads `.claude/settings*.json`, `.mcp.json`, or `~/.claude.json`.

## Desired end state

- `agent-creance init` in a project with Claude Code config offers, each behind
  its own y/N (auto-skipped without a TTY, and only offered when the source
  yields candidates): import web domains, import MCP servers, detect dev ports —
  then shows the resulting `.agent-creance.yaml` and asks to confirm before
  writing, and finally offers to print an agent prompt.
- `agent-creance setup`, when creating a fresh `~/.config/agent-creance.yaml`,
  seeds it with the user's global web domains + MCP servers and prints a summary.
- `agent-creance import <file>` strict-validates a YAML fragment, shows the merged
  result, and on confirmation merges it into `.agent-creance.yaml`
  (comment-preserving), then recompiles a running policy.
- Non-interactive `init`/`setup` and config-less projects behave exactly as today.

## What we're NOT doing

- Importing Claude Code `deny` rules, tool permissions, or hooks.
- Re-syncing settings over time / a project "sync" mode beyond `import`.
- A built-in stack→docs-host catalog (delegated to the agent prompt).
- Auto-running the agent or auto-applying its output without review.
- Seeding an *existing* global baseline in `setup` (fresh-file only, matching the
  current never-overwrite contract).

## Phase 1 — Claude Code config readers (`internal/claudeimport`)

New pure package (reads via `sysdep.FileSystem`, returns `config` types).

**Produce** a result struct: `WebRules []config.Rule` (intercept, `[GET]`),
`MCPRules []config.Rule` (passthrough), `Ports []config.HostService`. Each rule
carries a provenance `Reason` (e.g. `imported from .claude/settings.local.json`,
`imported from MCP server "stripe"`).

**Two entry points:**
- `Project(fs, paths, projectDir)` reads `.claude/settings.json`,
  `.claude/settings.local.json`, `.mcp.json`, and the per-project block of
  `~/.claude.json` (`projects["<abs projectDir>"].mcpServers`).
- `Global(fs, paths)` reads `~/.claude/settings.json`,
  `~/.claude/settings.local.json`, and top-level `mcpServers` of `~/.claude.json`.

**Parsing rules:**
- Web domains: union `permissions.allow` entries matching
  `WebFetch(domain:<g>)` with `sandbox.network.allowedDomains`, across all read
  files; trim, lowercase host; skip bare `*`; map `<g>` directly to `Rule.Host`
  (globs already supported). Dedupe by host. → intercept `[GET]`.
- MCP: parse `mcpServers` maps. Stdio (`command`, or `type:stdio`, or no `url`)
  → ignore. Remote (`url` with `type` http/streamable-http/sse/ws or inferred):
  expand `${VAR}`/`${VAR:-default}` in `url`; if a required var is unresolved,
  skip the server. Extract host: `localhost`/`127.0.0.1`/`::1` → `HostService`
  (label = server name, port from url, default 80/443 if absent → only add if a
  port is present); else → passthrough `Rule{Host: host}`. Dedupe MCP servers by
  name (project files), highest-precedence wins; dedupe resulting rules by host.
- Missing/unreadable files are not errors (skip that source). Malformed JSON
  in a present file → return a wrapped error (surface to caller, which warns and
  skips that source rather than aborting init).

**Tests:** table-driven over `FakeFileSystem` fixtures: WebFetch glob variants
(`example.com`, `*.example.com`, `*` skipped), sandbox domains union+dedupe,
remote vs stdio vs localhost MCP, `~/.claude.json` nested project block, env-var
expansion, absent files, malformed JSON.

**Success criteria:**
- [ ] `make test` green; `go build ./...`; `make lint` clean.
- [ ] Unit tests cover every parsing rule above, incl. bare-`*` skip and
      stdio-ignored.

## Phase 2 — Static dev-port detection (`internal/portscan`)

New pure package: `Detect(fs, projectDir) ([]config.HostService, []Warning)`.

**Sources (broadest):**
- `docker-compose.yml`/`.yaml`: published host ports from `services.*.ports`
  (`"HOST:CONTAINER"`, long-form `published:`); label = service name.
- `package.json` scripts: framework defaults (vite 5173, next 3000, react-scripts
  3000, vue-cli 8080, astro 4321, nuxt 3000, svelte/kit 5173) keyed on the tool
  named in a `dev`/`start` script; label = tool name. Conservative pattern table,
  documented inline.
- `Procfile`: `web:`/process entries with a `$PORT`/`-p PORT`/`--port PORT`;
  label = process name.
- `.env`/`.env.local`: `PORT=`/`*_PORT=` numeric values; label = var name.

Dedupe by port (port is the meaningful key; first/most-reliable source wins,
docker-compose > package.json > Procfile > .env). Validate 1-65535; ignore the
rest. Never read `.env` secrets beyond `*PORT*` keys.

**Tests:** table-driven fixtures per source + dedupe + invalid-port rejection.

**Success criteria:**
- [ ] `make test` green; `go build ./...`; `make lint` clean.
- [ ] Each source and the dedupe/precedence covered; no false port from a
      non-port `.env` value.

## Phase 3 — Shared YAML renderers + `host_services` splice (`internal/config`)

Init/setup need to render entries into a template; `import` needs to splice into
an existing comment-rich file.

- **Export shared renderers** reused everywhere: `RenderRuleYAML(r Rule) string`
  (refactor `renderRuleItem` `edit.go:177-193`) and `RenderHostServiceYAML(hs
  HostService) string` (emits `- label:port`). Keep output identical to current
  splice output (golden-stable).
- **`AppendHostService(src []byte, hs HostService) (out []byte, changed bool,
  err error)`** mirroring `AppendRule`: `Parse` first (refuse unparseable),
  dedupe by **port** against existing `host_services` (no-op if present), locate
  `network.host_services` via the node tree (synthesize `network:`/
  `host_services:` if absent — extend `planInsert`), splice rendered text,
  re-`Parse` + diff to assert exactly this host_service was added
  (`validateAppend`-style gate).

**Tests:** golden/unit mirroring `edit_test.go`: append into existing
host_services, synthesize missing structure, dedupe-by-port no-op, validation
gate. Confirm `RenderRuleYAML` golden unchanged (no regression to allow/deny).

**Success criteria:**
- [ ] `make test` green; `go build ./...`; `make lint` clean.
- [ ] Existing `AppendRule`/edit golden tests still pass unchanged.
- [ ] `AppendHostService` preserves surrounding comments (asserted in test).

## Phase 4 — `agent-creance import <file>` command (`internal/cli/import.go`)

`newImportCmd(app)` with `Args: cobra.ExactArgs(1)`, flag `--yes`/`-y`, registered
in `cli.go`. Testable body `runImport(ctx, app, dir, file, yes)`:

1. Read `file` via `app.FS`; `config.Parse` to strict-validate (reject unknown
   keys / bad shape with the actionable `ValidationError`; do nothing on error).
2. Read existing `.agent-creance.yaml` (nil if absent). Fold the fragment in via
   the splice path: `AppendRule(AllowList)` per allow rule, `AppendRule(DenyList)`
   per deny rule, `AppendHostService` per host_service. Skip generators/agent/etc.
   in a fragment with a clear "import only handles allow/deny_always/host_services"
   message if present (or ignore — decide: ignore silently, document).
3. **Review:** print the resulting file content (or "no changes — nothing to
   import" and return). If interactive, `confirm("Write these changes?")`; if
   non-interactive, require `--yes` else abort with a hint.
4. `writeFileAtomic` + `recompile(ctx, app, dir)` (reuse `mutate.go`), so a
   running proxy hot-reloads.

**Tests:** unit (`*App`+fakes): valid fragment merged (allow + ports), strict
rejection of unknown key, idempotent re-import (dedupe no-op), decline aborts,
`--yes` non-interactive path, comment preservation. Testscript `import.txtar`:
file-fixture + non-interactive `--yes` happy path and a strict-reject case.

**Success criteria:**
- [ ] `make test` green; `go build ./...`; `make lint` clean.
- [ ] `import` rejects an invalid fragment without touching the config.
- [ ] Re-importing the same fragment is a no-op.

## Phase 5 — `init` integration (`internal/cli/init.go`)

1. **Extend the template render**: `renderConfigTemplate(gens, webRules,
   mcpRules, ports)` emits the imported allow rules (under `allow:`) and a
   `host_services:` block, using the Phase 3 renderers; empty inputs keep today's
   commented stubs. Update `init.txtar`/golden as needed.
2. **Optional gates** between the host-setup gate and the write, each only
   offered when `app.Terminal.IsInteractive()` AND its source yields candidates:
   - `claudeimport.Project(...)` → if web domains: `confirm("Import N allowed
     web domains from .claude settings?")`; if MCP: a separate `confirm`.
   - `portscan.Detect(...)` → if ports: `confirm("Add N detected dev ports?")`.
   Accumulate accepted entries (dedupe across sources).
3. **Review-before-write**: if any optional step contributed entries, print the
   rendered config and `confirm("Write this configuration?")`; decline aborts
   without writing. If nothing was contributed, write directly (unchanged).
4. **Agent prompt**: after a successful write, if interactive,
   `confirm("Print a prompt to have your agent suggest more config?")` → print a
   constant prompt (`msgAgentConfigPrompt`) that instructs the agent to write a
   YAML fragment (`host_services` + `network.egress.allow` for local ports and
   stack documentation hosts) conforming to the schema, telling the user to
   inspect it and run `agent-creance import <file>`.

**Tests:** unit (`*App` + `FakeTerminal{Interactive:true}` + `strings.Reader`
stdin + `FakeFileSystem` seeded with `.claude/*.json` fixtures): each gate
accept/decline, review accept/decline, agent-prompt accept/decline, dedupe vs
template, non-interactive = today's behavior. Keep the non-interactive scaffold
testscript hermetic.

**Success criteria:**
- [ ] `make test` green; `go build ./...`; `make lint` clean.
- [ ] Non-interactive `init` output/behavior unchanged (existing tests pass).
- [ ] Manual: `init` in a temp project with fake `.claude/settings.json` +
      `docker-compose.yml` offers the gates, review shows the merged file,
      declining the review writes nothing.

## Phase 6 — `setup` global seeding + docs (`internal/cli/setup.go`, docs)

1. In `scaffoldGlobalConfig`, on the **fresh-file** branch only, call
   `claudeimport.Global(...)` and render its web rules + MCP rules + ports into
   the baseline before `writeFileAtomic` (reuse Phase 3 renderers; respect
   `--no-global-config`). Existing-file branch unchanged (left untouched). Print
   a one-line summary of what was seeded.
2. Update `TestSetupScaffoldsGlobalConfig` / golden for the seeded baseline (with
   a fixture global `~/.claude/settings.json`); keep the no-Claude-config case
   producing today's baseline.
3. **Docs**: `docs/design.md` — document the init import gates, the review step,
   the agent prompt, the `import` command, and the setup global seeding; update
   the Commands section and replace the L230 "future documentation generator"
   note with a pointer to the shipped agent-prompt flow. `README.md` — add the
   `import` command and the first-run import behavior. Bump nothing in
   `buildinfo` (no external tool version change).

**Success criteria:**
- [ ] `make test` green; `go build ./...`; `make lint` clean.
- [ ] `setup` on a fresh machine with global Claude config seeds the baseline;
      with none, produces today's baseline (golden).
- [ ] `make build` so `bin/agent-creance` reflects the final commit.

## Testing strategy

- Pure packages (`claudeimport`, `portscan`) and config renderers/splice →
  table-driven + golden, no OS calls.
- CLI interactive paths → `*App` + `sysdep` fakes (`FakeTerminal`,
  `strings.Reader` stdin, `FakeFileSystem`), per the established `init_test.go`
  pattern.
- Non-interactive scaffold/`import --yes` paths → hermetic testscript `.txtar`.
- No external tools (`agent-safehouse`/`mitmproxy`/`security`) in unit tests.

## Automated verification (run each phase + at the end)

- [ ] `make test` (race, hermetic) — green.
- [ ] `go build ./...` — compiles.
- [ ] `make lint` — clean.
- [ ] `make golden` then review diff where golden/template changed.
- [ ] `make build` at the end (binary reflects final commit).

## Manual verification (end to end)

- [ ] `init` in a scratch project with `.claude/settings.json` (a WebFetch
      domain + a sandbox domain), `.mcp.json` (one remote, one stdio, one
      localhost), `docker-compose.yml`: the three gates appear, MCP remote →
      passthrough, stdio ignored, localhost → port, review shows the merged file,
      decline writes nothing, accept writes it.
- [ ] End-of-init agent prompt prints and references `agent-creance import`.
- [ ] `agent-creance import frag.yaml` merges, preserves comments, is idempotent,
      and rejects an unknown-key fragment.
- [ ] `setup` with a fresh global path + a fake `~/.claude/settings.json` seeds
      the baseline; rerun leaves it untouched.
- [ ] Non-interactive `init` (piped stdin) writes today's scaffold with no
      prompts.
```
