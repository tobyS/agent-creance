# AC-0051: First-run DX — import allowlist & ports during init/setup

**Status:** Done
**Estimated Complexity:** Large
**Created:** 2026-06-14
**Updated:** 2026-06-15

## Problem Statement

Setting up agent-creance for the first time on a project is more manual than it
needs to be. After `setup` and `init`, the engineer is handed a config with
mostly commented-out stubs and must hand-author the egress allowlist and local
port list before the cage will let their agent and dev environment work. Yet a
lot of the information needed already exists on the machine:

- The project's and the user's Claude Code config (`.claude/settings.json`,
  `.claude/settings.local.json`, `~/.claude/settings*.json`, `.mcp.json`)
  already declares allowed web domains (`WebFetch(domain:…)`) and MCP servers.
- The project itself reveals likely dev-server ports (npm scripts,
  docker-compose, Procfile, .env).
- The running agent instance can reason about which documentation hosts and
  ports a given stack needs, if asked the right way.

Today none of this is used: `init` only scans `package.json`/`composer.json` to
pre-fill the generators list. The engineer reconstructs the rest by hand, which
is slow and error-prone, and a too-narrow allowlist surfaces later as cage
denials mid-session.

## Desired Outcome

When this is complete, a first-run engineer can get from zero to a working,
reasonably complete config with a few confirmations instead of hand-authoring:

- `init` offers to seed the project config from the project's own Claude Code
  settings and from static project signals, shows the result, and only writes
  after the engineer confirms.
- `setup` seeds the global baseline (`~/.config/agent-creance.yaml`) from the
  user's global Claude Code settings when it first creates that file.
- For everything that can't be inferred statically, `init` hands the engineer a
  ready-to-use prompt for their agent that produces a schema-conforming config
  fragment, which the engineer inspects and merges via a new `import` command.
- Every automated import is conservative (read-only where possible) and never
  written without an explicit human review step. Non-interactive runs skip all
  of the new behavior and behave exactly as today.

## User Stories / Use Cases

- As an engineer adopting agent-creance on an existing project, I want my
  already-configured Claude Code web domains and MCP servers pulled into the
  cage config automatically, so that I don't re-type an allowlist I already
  maintain elsewhere.
- As an engineer, I want my dev-server ports detected from the project, so that
  my local environment works inside the cage without me hunting down port
  numbers.
- As an engineer setting up the machine for the first time, I want my global
  Claude Code domains and MCP servers seeded into the global agent-creance
  baseline during `setup`, so that they apply across all my projects without
  per-project repetition.
- As an engineer with a non-trivial stack, I want a prompt I can hand to my
  agent that emits a correct config fragment (extra doc hosts, ports), which I
  can review and merge, so that I capture project knowledge the tool can't infer.
- As a security-conscious engineer, I want to review the final config before it
  is written, so that nothing I didn't approve ends up in my allowlist.

## Acceptance Criteria

### `init` — project-scoped imports (interactive only)

Each of the following is offered as its own independent yes/no confirmation, all
defaulting to skip and **automatically skipped when stdin is not an interactive
terminal** (preserving today's non-interactive behavior):

- [ ] **Import WebFetch domains:** when `.claude/settings.json` and/or
      `.claude/settings.local.json` exist in the project, `init` offers to import
      their `permissions.allow` entries of the form `WebFetch(domain:HOST)` as
      `network.egress.allow` rules that are **GET-only, intercept mode**.
- [ ] **Import MCP servers:** when project `.claude/settings*.json` and/or
      `.mcp.json` declare MCP servers, `init` offers to import them — remote
      (HTTP/SSE) servers become `network.egress.allow` rules permitting the
      methods MCP requires (i.e. **not** GET-only; POST-capable); local (stdio)
      servers with a known port surface as `network.host_services` entries.
- [ ] **Detect dev ports statically:** `init` offers to add `host_services`
      entries inferred from static project signals (e.g. `package.json` scripts,
      `docker-compose.yml` published ports, `Procfile`, `.env`).
- [ ] **Review gate:** when at least one optional step contributed content,
      `init` displays the resulting `.agent-creance.yaml` and requires an
      explicit confirmation before writing it. If no optional step contributed
      content, `init` writes without an extra confirmation (unchanged from
      today).
- [ ] Imported/detected entries are merged with the generated template using the
      existing union/dedupe semantics; duplicates are not added twice.
- [ ] Missing or unreadable settings files are not an error — the corresponding
      step is silently unavailable/skipped.

### `init` — agent prompt for what can't be inferred

- [ ] After the config is written, `init` offers (yes/no) to print a prompt the
      engineer can hand to their running agent.
- [ ] The prompt instructs the agent to produce a config fragment covering (a)
      local ports needed for the dev environment and (b) additional documentation
      hosts for the project's stack (e.g. databases, frameworks), and to **write
      it to a file**.
- [ ] The prompt specifies the exact YAML shape so the output conforms to the
      agent-creance schema (`network.host_services`, `network.egress.allow`).
- [ ] The prompt tells the engineer to inspect the generated file carefully and
      then run `agent-creance import <file>` to merge it.

### `agent-creance import <file>` — new command

- [ ] Reads a YAML config fragment from the given file and strict-validates it
      against the config schema (unknown keys are a hard error, matching existing
      strict parsing).
- [ ] Displays the resulting merged config and requires explicit confirmation
      before writing.
- [ ] On confirmation, merges the fragment into the existing project
      `.agent-creance.yaml` using the established union/dedupe merge semantics.
- [ ] On validation failure, reports actionable errors and does not modify the
      config.
- [ ] Usable standalone, independent of `init`.

### `setup` — global baseline seeding (machine-scoped)

- [ ] When `setup` creates the global `~/.config/agent-creance.yaml` (and only
      when it is creating it — never overwriting an existing one), it also imports
      from the user's global Claude Code config (`~/.claude/settings.json`,
      `~/.claude/settings.local.json`, and global MCP config):
  - [ ] global `WebFetch(domain:…)` entries as **GET-only intercept** allow
        rules in the baseline;
  - [ ] global MCP servers as allow rules (remote, POST-capable) / host_services
        (local) in the baseline.
- [ ] This seeding respects the existing `--no-global-config` skip behavior.

### General

- [ ] All new automated imports default to conservative/read-only postures where
      the source semantics allow (WebFetch domains and agent-suggested doc hosts
      are GET-only; MCP allow rules permit only what MCP needs).
- [ ] Behavior is fully backward compatible for non-interactive invocations and
      for projects/users with no Claude Code settings present.

## Out of Scope

- A built-in catalog mapping detected stacks (Postgres, Redis, etc.) to
  documentation hosts — that knowledge is delegated to the agent prompt here.
- Automatically *running* the agent or auto-applying agent-generated config
  without human review.
- Watching `.claude/settings*.json` for changes and re-syncing over time; this
  ticket is about first-run/init/setup seeding only.
- A re-run/re-import "sync" mode for already-initialized projects beyond what the
  standalone `import` command provides.
- Importing anything other than web domains, MCP servers, and ports from Claude
  Code settings (e.g. permissions for tools, hooks).
- Generator-produced allow rules from `package.json`/`composer.json` (already
  covered by the existing generators mechanism).

## Open Questions

_None — business/product questions resolved during ticket creation._

## Questions for Research/Planning

- [ ] Exact shape/parsing of Claude Code `.claude/settings*.json` and `.mcp.json`
      — where `WebFetch(domain:…)` permissions and MCP server definitions live,
      and how to robustly distinguish remote (HTTP/SSE) from local (stdio) MCP
      servers and extract any port.
- [ ] Which global Claude Code files to read for the `setup` path and how global
      MCP servers are declared (`~/.claude/settings*.json` vs a global
      `.mcp.json`/`~/.claude.json`).
- [ ] What methods a remote MCP allow rule must permit (POST at minimum; whether
      GET/streaming are needed for SSE), and how to express that with the `Rule`
      `methods`/`mode` fields.
- [ ] Reliable, low-false-positive static port detection heuristics (npm
      scripts, `docker-compose.yml`, `Procfile`, `.env`) and how to label the
      resulting `host_services` entries.
- [ ] How to render the "resulting config" / review output (full file vs. diff
      against the base template) consistently across `init` and `import`.
- [ ] Where the `import` merge logic should live so it reuses the existing
      config merge (union/dedupe) implementation rather than duplicating it.
- [ ] Whether a non-interactive escape hatch flag (mirroring `--no-setup`) is
      warranted in addition to the automatic TTY-based skip.
- [ ] CLI/testscript test strategy for the new interactive gates and the
      `import` command, consistent with the project's hermetic `.txtar` approach.

## References

- `internal/cli/init.go` — current `init` flow, config template, generator scan.
- `internal/cli/setup.go` — `setup` flow and `globalConfigTemplate`
  (`~/.config/agent-creance.yaml` baseline, AC-0048 docs hosts).
- `internal/config/config.go` — config schema (`Rule` with
  `host`/`paths`/`methods`/`mode`/`reason`; `HostService`), strict parsing.
- `internal/config/merge.go` — union/dedupe merge semantics to reuse for
  `import`.
- `internal/cli/allow.go`, `internal/cli/deny.go`, `internal/cli/mutate.go` —
  existing project/`--global` rule mutation patterns.
- `docs/design.md:160-233` — allowlist generators, incl. the noted future
  "documentation generator that prompts the agent to expand the allowlist"
  (`design.md:230`), which this ticket formalizes.
- AC-0048 — Anthropic docs hosts added to the egress baseline (GET-only
  intercept precedent followed here for WebFetch imports).

## Implementation Plan

- Research: `thoughts/shared/research/2026-06-14-AC-0051-init-setup-dx-imports.md`
- Plan: `thoughts/shared/plans/2026-06-15-AC-0051-init-setup-dx-imports.md`

## Notes & Updates

### 2026-06-14

Key decisions made during ticket creation:

- "Include-listed websites" map to Claude Code `permissions.allow:
  WebFetch(domain:…)` entries; imported as GET-only intercept allow rules,
  mirroring how WebFetch itself behaves and the AC-0048 docs-host posture.
- Global imports (WebFetch domains + MCP servers) belong to `setup`, not `init`,
  because `setup` owns the machine-wide `~/.config/agent-creance.yaml` baseline;
  mutating it from a project-scoped `init` would be a layering smell.
- MCP remote-server allow rules cannot be GET-only (JSON-RPC/SSE need POST), so
  the GET-only posture applies to WebFetch-domain imports and agent-suggested
  doc hosts only.
- The agent prompt is offered at the *end* of `init` (not mid-flow). It tells the
  agent to write a file; the engineer inspects it and merges it with a new,
  reusable `agent-creance import <file>` command rather than pasting into a
  running prompt.
- Each optional `init` step is an independent yes/no gate; all are auto-skipped
  when not attached to an interactive terminal. The pre-write review gate only
  appears when an optional step actually contributed content, keeping a plain
  `init` as quiet as it is today.
- Added scope beyond the original two ideas: MCP server detection (project +
  global) and static dev-port detection, plus a mandatory review/confirm step
  before writing — all confirmed in discussion.

### 2026-06-15 — Implemented (Done)

Shipped across six commits (b66258f → 4994f89):

- `internal/claudeimport` — reads `.claude/settings*.json`, `.mcp.json`,
  `~/.claude.json` (project + global scopes) → web rules (GET intercept), MCP
  rules (passthrough), localhost MCP ports.
- `internal/portscan` — docker-compose / package.json scripts / Procfile / .env
  → `host_services` (deduped by port, source precedence).
- `internal/config` — `AppendHostService` comment-preserving splice +
  exported `RenderRule`/`RenderHostService`.
- `agent-creance import FILE` — strict-validate, review, splice-merge, `--yes`.
- `init` — three independent TTY-gated import steps + review-before-write +
  end-of-run agent prompt; template assembled from pieces (no-import output
  byte-identical to before).
- `setup` — seeds a fresh global baseline from global Claude config; existing
  file still left untouched. Docs updated (design.md, README).

Planning-checkpoint decisions: remote MCP → `passthrough`; static port
detection broadest (compose + scripts + Procfile + .env); also import
`sandbox.network.allowedDomains` alongside `WebFetch(domain:…)`.

Research-driven corrections to the original ACs: local *stdio* MCP servers have
no port/egress (only a localhost MCP `url` is a port); WebFetch globs map 1:1 to
`Rule.Host` (our `matchHost` already supports `*`/`*.suffix`), with a bare `*`
skipped. No non-interactive escape-hatch flag was needed — the automatic TTY
skip suffices; `import` uses `--yes` for non-interactive apply.
