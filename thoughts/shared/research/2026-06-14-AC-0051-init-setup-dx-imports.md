---
date: 2026-06-14
ticket: AC-0051
title: "Research — First-run DX: import allowlist & ports during init/setup"
status: complete
branch: main
commit: 20d78af9641169b7989d94aa9e5f28cc0d555f20
repo: git@github.com:tobyS/agent-creance.git
---

# Research: AC-0051 — First-run DX, import allowlist & ports during init/setup

## Research question

How do we seed the egress allowlist and local ports during first run — `init`
importing `WebFetch(domain:…)` domains + MCP servers from the project's Claude
Code settings plus static dev-port detection, `setup` seeding the global baseline
from the user's global Claude Code settings, and a new `agent-creance import
<file>` command that merges an agent-generated YAML fragment — given how the
config schema, parsing, merge, and CLI mutation already work?

## Summary of findings

1. **The interactive seam already exists and is the model to follow.** `confirm`
   (`internal/cli/init.go:145-157`) prints to `app.Stdout`, reads one line from
   `app.Stdin`, defaults to No on empty/EOF. The decision of *whether* to prompt
   is `app.Terminal.IsInteractive()` (`internal/sysdep/terminal.go:22-50`). Every
   new yes/no gate uses exactly this pattern; non-interactive auto-skips. This is
   tested with `*App` + fakes (`FakeTerminal.Interactive`, `strings.Reader`
   stdin), **not** testscript (testscript always runs non-tty and can't stub the
   absolute-path keychain — see `init.txtar:6-12`).

2. **Our policy host matcher already supports Claude Code's WebFetch glob
   semantics 1:1.** `matchHost` (`internal/policy/glob.go:10-24`) implements `*`
   (any host) and `*.suffix` (subdomains, apex excluded) — identical to the
   documented `WebFetch(domain:…)` matching. So `WebFetch(domain:*.example.com)`
   maps directly to a `Rule{Host: "*.example.com"}`. The **only** special case:
   a bare `WebFetch(domain:*)` would become `Host: "*"` = allow-all egress, which
   defeats the cage — it must be skipped (with a warning), never imported.

3. **There is no Config→YAML serializer.** All comment-preserving writes go
   through `config.AppendRule` (`internal/config/edit.go:52-81`), a positional
   text-splice — and it **only handles `allow`/`deny_always` rules**. There is no
   equivalent splice for `host_services` (ports) or `generators`. This is the
   central implementation constraint (see "Key design tension").

4. **`init` writes a fresh file; `setup` and `import` touch existing files.** This
   split determines strategy: `init` can render imported entries into the
   generated template text directly (no splice needed), gate the result behind a
   review-before-write confirm, then `writeFileAtomic`. `import` must merge into
   an existing, comment-rich `.agent-creance.yaml` — that needs the splice path
   (and a new host_services splice). `setup`'s `scaffoldGlobalConfig` currently
   early-returns if the global file exists and **never overwrites** — global
   import needs new logic to also append to an existing baseline.

5. **Claude Code config lives in JSON across several files** read leniently
   (decode only the fields we need, ignore unknowns — the established pattern in
   `internal/generator/manifest.go:18-54`). Permissions (`permissions.allow` with
   `WebFetch(domain:…)`) are in `settings.json` / `settings.local.json`; MCP
   servers are in `.mcp.json` (project) and `~/.claude.json` (user top-level +
   per-project nested) — **not** in settings.json.

6. **Two important corrections to the ticket's assumptions** (see "Corrections"):
   (a) local *stdio* MCP servers have no network host and no port — nothing to
   import; only MCP servers with a `url` matter, and a `url` pointing at
   localhost is a *port* (`host_services`) while a remote `url` is an *egress
   allow*. (b) Remote MCP needs POST + streaming (and `sse` needs GET+POST), so
   GET-only is wrong for MCP — confirming the ticket's "POST-capable" note, but
   the precise posture (intercept vs passthrough) is an open decision.

## Detailed findings

### A. The `init` flow and where new steps slot in

`runInit` (`internal/cli/init.go:54-91`) in order:

1. **Host-setup gate first** (`init.go:55-59` → `ensureHostSetup` `:100-139`),
   before any write — onboarding is all-or-nothing (doc comment `:50-53`).
   `--no-setup` skips it.
2. **Clobber guard** (`:64-73`): refuse if `.agent-creance.yaml` exists unless
   `--force`.
3. **Generator scan + render + atomic write** (`:75-79`): `scanGenerators` →
   `renderConfigTemplate(gens)` → `writeFileAtomic(... configFilePerm=0o644)`.
4. **Success messages** (`:81-89`), branching on `--no-setup`.

New optional import/detect steps slot **between step 1 and step 3**; the
review-before-write confirm goes immediately before `writeFileAtomic` at
`init.go:77`. Imported entries must be folded into the rendered `content` string
before the write. Because `init` builds the file fresh, the cleanest approach is
to **extend the template render** to accept imported allow rules + host_services
(rather than splice), keeping one write.

`configTemplate` (`init.go:310-329`) is a static `fmt.Sprintf` string with one
`%s` (the generators block via `generatorsBlock` `:291-305`). It currently has
**no `host_services:` block** and carries commented `# allow:` / `# deny_always:`
stubs. `renderConfigTemplate` (`:282-284`) would grow parameters for the imported
allow rules and detected ports.

Flags: `--force`, `--no-setup` (`newInitCmd` `:28-43`). `dir` is hardcoded `"."`
in production but a parameter for tests.

### B. The `setup` flow and global baseline seeding

`runSetup` (`internal/cli/setup.go:47-98`): construct Installer → CA step → skill
step → **global config baseline** (`:90-97`), the last step and the place for a
global WebFetch/MCP import. `--no-global-config` skips it.

`scaffoldGlobalConfig` (`setup.go:104-126`) resolves
`~/.config/agent-creance.yaml` via `config.NewLoader(...).GlobalPath()`
(`load.go:77-83`), then **Stat-then-decide: if the file exists it prints "left
untouched" and returns — it never overwrites** (`:109-117`). It writes the static
`globalConfigTemplate` (`:137-178`) — the AC-0043/AC-0048 baseline
(`api.anthropic.com`/`claude.ai`/`platform.claude.com` passthrough;
`code.claude.com` `paths:[/docs/]` GET; `docs.anthropic.com`/`docs.claude.com`
GET; telemetry commented out), pinned by `TestSetupScaffoldsGlobalConfig`.

Implication: global import on a **fresh** machine = render imported entries into
the template before the write at `:121`. Global import into an **existing**
baseline = new behavior (today the function early-returns); would need the splice
path (`config.AppendRule` for rules + a new host_services splice) against the
existing file, or it stays scoped to fresh-file only (a scope decision for the
plan).

### C. Config schema, strict parsing, merge (reuse map)

Schema (`internal/config/config.go:29-101`): `Rule{Host, Paths *[]string,
Methods *[]string, Mode, Reason}` (`:89-95`); `mode ∈ {intercept (default),
passthrough}` (`:98-101`, defaulted by `defaultRuleModes` `:206-212`, validated
`validate.go:28-43`). `Paths`/`Methods` are pointers so "omitted" differs from
"empty". `HostService{Label, Port}` (`:60-63`) parsed from `"label:port"` by
`parseHostService` (`validate.go:97-114`): split on **last** colon, non-empty
label, port 1-65535.

**`Parse` (`config.go:144-178`) is reusable verbatim to strict-validate an
imported fragment** — pure, `KnownFields(true)` strict (unknown key → hard
error), empty doc = empty Config. Caveat: it validates a *full document shape*
(top-level keys like `network:`), so a fragment must be a valid partial
`.agent-creance.yaml`, not a bare rule list. It does not expand `include:`.

`merge(base, over)` (`internal/config/merge.go:20-41`) is pure: scalars override,
lists union+dedupe (`Allow`/`DenyAlways` via `dedupeRules` O(n²)
`reflect.DeepEqual` `:173-191`; `HostServices` via comparable-struct map-set
`:131-145`; `Generators` `:151-165`), env key-merges. Layering
(`load.go:48-71`): global merged first as base, project over it.

**Two distinct dedupe notions exist** and the plan must pick: `merge`/
`dedupeRules` use full-value DeepEqual (mode/reason matter); the CLI splice path
`containsRule`/`ruleIdentity` (`edit.go:271-292`) uses host+paths+methods
identity, **ignoring mode/reason** ("re-allowing a host is a no-op regardless of
an added reason"). For imports the splice identity is the better fit (avoids
duplicate hosts that differ only by an added provenance reason).

### D. The CLI mutation precedent (`allow`/`deny`)

`mutateAndRecompile` (`internal/cli/mutate.go:85-116`): read bytes (nil if
absent) → `config.AppendRule(data, list, rule)` → if changed, `MkdirAll` +
`writeFileAtomic` → `recompile` so a running proxy hot-reloads. `mutationTarget`
(`:62-79`): `--once` overlay / `--global` (`Loader.GlobalPath()`) / project file.
`config.AppendRule` (`edit.go:52-81`) parses, dedup-checks, locates insert point
via a `yaml.Node` tree (`planInsert` `:86-129`), **splices rendered text**
(`renderRuleItem` `:177-193`), and re-parses + diffs (`validateAppend`
`:232-257`) so a splice bug can't reach disk. **It only targets `allow`/
`deny_always` — no host_services/generators splice exists.**

### E. Reading Claude Code config (web research, docs.claude.com, 2026)

- **Settings files** (`code.claude.com/docs/en/settings`, `/permissions`):
  project `.claude/settings.json`, local `.claude/settings.local.json`, user
  `~/.claude/settings.json`, managed (macOS
  `/Library/Application Support/ClaudeCode/managed-settings.json`). Permissions
  under top-level `permissions` → `allow`/`deny`/`ask` arrays. Permission rule
  arrays **merge (union) across scopes**; `deny` beats `allow` across any scope.
- **WebFetch rule syntax**: `WebFetch(domain:<host-pattern>)`, matches
  **hostname only**, case-insensitive, trailing dot stripped, `*` and leading
  `*.` wildcards (same semantics as our `matchHost`). `WebFetch` (no parens) =
  all. **`WebSearch` has no `domain:` specifier** — contributes no domains.
- A separate `sandbox.network.allowedDomains` / `deniedDomains` block also lists
  network domains (used by the Bash sandbox); a fuller "allowed domains" picture
  is `permissions WebFetch` ∪ `sandbox.network.allowedDomains`. **Out of the
  ticket's stated scope** (WebFetch only) — flagged as an open question.
- **MCP config** (`code.claude.com/docs/en/mcp`): project `.mcp.json`
  (`{mcpServers:{…}}`); user/local `~/.claude.json` — user-scope top-level
  `mcpServers` **and** local-scope nested under
  `projects["<abs project path>"].mcpServers`. Entry schema: `type`
  (`stdio`|`http`/`streamable-http`|`sse`|`ws`), `command`/`args`/`env` (stdio),
  `url`/`headers` (remote). `type` may be absent on old stdio entries (presence
  of `command` ⇒ stdio, `url` ⇒ remote). `${VAR}`/`${VAR:-default}` expansion
  applies in `url` — must expand before extracting a host. MCP dedupe is **by
  name, highest-precedence wins, no field merge** (Local > Project > User).
- **Transport / methods**: remote `http` = JSON-RPC over **POST**, response may
  upgrade to `text/event-stream` (GET-only breaks it). `sse` = GET stream + POST
  channel. `ws` = `wss://` upgrade. Allowlist the **host** of `url`. Remote MCP
  may also hit OAuth discovery on a distinct auth host and `api.anthropic.com`
  for the WebFetch preflight (already in baseline).

### F. Reading files + JSON, the testability seam

Read through `app.FS` (`sysdep.FileSystem`, `filesystem.go:19-44`:
`ReadFile`/`Stat`/`WriteFile`/`Rename`/`MkdirAll`), paths via `app.Paths`
(home dir, like `load.go:77-83`). No new sysdep interface needed — the existing
`FileSystem` seam covers reading `.claude/*.json`, `.mcp.json`, `~/.claude.json`.
JSON parsing follows `internal/generator/manifest.go:45-54` (lenient
`json.Unmarshal` into a struct with only the needed fields). Fakes:
`FakeFileSystem.Files` (in-memory map), `FakeTerminal.Interactive`, `strings.
Reader` stdin. The `App` composition root is `cli.go:20-66`; register a new
command via `root.AddCommand(newImportCmd(app))` near `cli.go:96`; command bodies
take explicit params (`run*`) for testability (pattern: `allow.go:37`,
`deny.go:32`); `import <file>` uses `Args: cobra.ExactArgs(1)`.

## Key design tension: how to write imported entries

There are two write strategies and the plan must choose per surface:

- **Render-into-template (no splice)** — works only when building a *fresh* file:
  `init` always, `setup` on a fresh machine. Extend the template renderers to
  emit imported allow rules + `host_services`. Simple, one atomic write, no new
  edit primitive.
- **Comment-preserving splice (into existing file)** — required for `import`
  (existing `.agent-creance.yaml`) and for `setup` if we want to seed an
  *existing* baseline. `config.AppendRule` covers rules; **a new
  `AppendHostService`-style splice is needed for `host_services`** (no precedent
  exists). Reusing `merge()` + `yaml.Marshal` is the comment-discarding
  alternative the codebase deliberately avoids (`edit.go:3-19`).

Recommended direction (for the plan): `init` and fresh-machine `setup` use
render-into-template; `import` uses the splice path and gets the new
host_services splice primitive; decide whether `setup`-into-existing-baseline is
in scope (see open questions).

## Corrections to the ticket

1. **Local stdio MCP servers have no port and no egress.** They run as
   subprocesses over stdin/stdout. The ticket's "local (stdio) servers with a
   known port surface as host_services" doesn't apply — there's nothing to
   import. The correct decomposition by MCP `url`:
   - no `url` (stdio) → ignore;
   - `url` host = `localhost`/`127.0.0.1`/`::1` → a **port** → `host_services`;
   - remote `url` host → an **egress allow rule**.
2. **WebFetch globs map directly** (finding 2) — no lossy transformation needed
   except skipping bare `*`. The ticket's "GET-only intercept" posture is correct
   for WebFetch domains.
3. **GET-only is wrong for remote MCP** (finding/E) — confirms the ticket's
   "POST-capable" note; exact posture is an open question below.

## Code references

- `internal/cli/init.go:54-91` — `runInit` flow (insert points)
- `internal/cli/init.go:145-157` — `confirm` helper (the gate pattern)
- `internal/cli/init.go:266-276` — `writeFileAtomic` (0o644)
- `internal/cli/init.go:282-329` — template render + `configTemplate`
- `internal/cli/setup.go:47-126` — `runSetup` + `scaffoldGlobalConfig` (never overwrites)
- `internal/cli/setup.go:137-178` — `globalConfigTemplate` baseline
- `internal/cli/mutate.go:62-116` — `mutationTarget` + `mutateAndRecompile`
- `internal/config/config.go:29-178` — schema + strict `Parse`
- `internal/config/validate.go:97-114` — `parseHostService` ("label:port")
- `internal/config/merge.go:20-191` — merge + dedupe (DeepEqual identity)
- `internal/config/edit.go:30-292` — `AppendRule` splice + `ruleIdentity` (host+paths+methods)
- `internal/config/load.go:48-103` — layering + `GlobalPath`
- `internal/policy/glob.go:10-24` — `matchHost` (`*` / `*.suffix`, apex excluded)
- `internal/policy/match.go:15-42` — `Decide` (passthrough blind-spot)
- `internal/sysdep/terminal.go:22-50` — `Terminal.IsInteractive`
- `internal/sysdep/filesystem.go:19-44` — `FileSystem` seam
- `internal/generator/manifest.go:18-54` — lenient JSON parse pattern
- `internal/cli/cli.go:20-98` — `App` struct + command registration
- `internal/cli/init_test.go:101-449` — interactive prompt unit-test pattern
- `internal/cli/testdata/script/init.txtar` — non-interactive scaffold testscript

## Related prior work (thoughts/)

- AC-0043 (`setup` scaffolds global baseline) — ticket/research/plan
  `thoughts/shared/{tickets,research,plans}/…AC-0043-global-claude-baseline…` —
  direct predecessor for the global seeding surface.
- AC-0048 (docs hosts in baseline) — GET-only intercept precedent followed for
  WebFetch imports.
- AC-0029 (`init`), AC-0028 (`setup`), AC-0030 (`allow`/`deny`) — command +
  mutation precedents.
- AC-0008 (include & merge), AC-0007 (schema/loader) — merge semantics.
- AC-0012/AC-0036 (allowlist generators) — manifest-scan + JSON-parse precedent.
- `docs/design.md:160-231` — allowlist generators; **L230 the future
  "documentation generator that prompts the agent to expand the allowlist"** —
  the idea AC-0051's agent-prompt step formalizes.

## Open questions (for the checkpoint)

1. **Remote MCP rule posture** — intercept (GET+POST, traffic visible/audited) vs
   passthrough (opaque, bearer token never seen by the proxy). The baseline makes
   other token-carrying hosts passthrough; MCP tool traffic is exactly what you'd
   want to audit. Recommended: intercept + `methods:[GET, POST]`.
2. **Static port-detection scope for v1** — which sources, given false positives
   annoy: docker-compose published ports (most reliable) only, vs + package.json
   script defaults, vs + Procfile/.env. The agent-prompt step also covers ports,
   so static detection can stay conservative.
3. **Also import `sandbox.network.allowedDomains`?** — the ticket scoped to
   `WebFetch(domain:…)`, but Claude's Bash sandbox network allowlist is arguably
   a more direct "network domains" source. Include it (GET-only intercept) or
   keep strictly to WebFetch?

## Questions resolved during research (no longer open)

- WebFetch glob → `Rule.Host` mapping (finding 2); bare `*` skipped.
- Strict fragment validation = reuse `config.Parse` (finding C).
- File reading needs no new sysdep interface — reuse `app.FS` (finding F).
- Local stdio MCP → nothing to import; localhost `url` → port (correction 1).
- Comment preservation = splice for existing files, render for fresh (tension).
