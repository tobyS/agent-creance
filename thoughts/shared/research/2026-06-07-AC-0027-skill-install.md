---
date: 2026-06-07
ticket: AC-0027
title: "Skill install (WP-5.2) — research"
status: complete
branch: main
commit: e36514c167f3e52b3a20c0c400dc555e047b2dbd
researcher: Claude (Opus 4.8)
---

# AC-0027: Skill install (WP-5.2) — Research

## Research question

How does agent-creance embed (`go:embed`) a Claude Code `SKILL.md` and install it
idempotently to `~/.claude/skills/agent-creance/SKILL.md` — teaching the agent the
three network-refusal response types and their `X-Cage-Reason`/`agent_cage_`
triggers — with a `--no-skill` opt-out, while never touching the project's
`CLAUDE.md`? What exactly must the SKILL.md content say, and what frontmatter
makes Claude Code auto-activate it on a cage refusal?

## Summary

- `SKILL.md` **does not yet exist** in the repo. AC-0027 authors it, embeds it, and
  adds an installer to `internal/setup` (the package that today does only the CA
  bootstrap from AC-0026/WP-5.1).
- The install **target path is already pinned** by `internal/setupcheck`:
  `~/.claude/skills/agent-creance/SKILL.md` (`internal/setupcheck/setupcheck.go:38`).
  The skill **directory name is therefore fixed at `agent-creance`** — that is what
  Claude Code uses as the skill identity, so the `name:` frontmatter should match it.
- The **wire format the skill teaches is frozen** by AC-0017 (Done) in
  `internal/proxy/enforcer/responses.py` and its golden bodies. The SKILL.md prose
  must align with those exact strings (header name, reason values, `agent_cage_`
  error enums, and the reworded `how_to_proceed` copy).
- The **install idiom to mirror** is `internal/proxy/extract.go` — `go:embed` +
  `MkdirAll` + idempotent atomic write (`writeIfChanged`: read existing, compare,
  write-tmp-then-rename). `internal/setup` itself does not yet write files; it only
  `Stat`s for the CA. We add the file-writing capability to it.
- The **seams needed** (`sysdep.FileSystem` + `sysdep.PathResolver`) are already on
  the `setup.Installer` and on the `cli.App` composition root, so no new sysdep
  interface is required.
- **Activation** is best-effort relevance matching on the `description` field. We
  front-load the literal `X-Cage-Reason` and `agent_cage_` markers in the first
  sentence so Claude Code loads the skill when it sees a refusal in tool output.
- AC-0027 is **library-only**. The `--no-skill` flag and `setup` command wiring are
  AC-0028/WP-5.3. Within AC-0027 the opt-out is expressed as "the caller chooses not
  to call the installer" — i.e. the installer method exists; nothing wires a flag yet.

## Detailed findings

### 1. The three response types the SKILL.md must teach (frozen by AC-0017)

`docs/design.md:249-284` ("Network refusal handling") defines three types. The third
the ticket alludes to is **Allowed**; the two refusals are **soft-deny** and
**hard-deny**. The *authoritative, byte-exact* source is the AC-0017 enforcer
implementation, not the design doc (the design doc's hard-deny example URL is
truncated):

- Header + values — `internal/proxy/enforcer/responses.py:19-26`:
  - `X-Cage-Reason: soft-deny` / `X-Cage-Reason: hard-deny`
  - error enums `agent_cage_not_allowlisted` (soft) / `agent_cage_hard_deny` (hard)
- Both refusals are **HTTP 403** + `Content-Type: application/json`
  (`responses.py:42,78,95`).
- Soft-deny body fields/order: `error, url, host, path, method, how_to_proceed,
  allow_command_suggestion` — golden:
  `internal/proxy/enforcer/testdata/soft_deny_body.json.golden`.
  `allow_command_suggestion` = `agent-creance allow '{host}{path}'`
  (`responses.py:73`).
- Hard-deny body fields/order: `error, url, reason, how_to_proceed` (no host/path/
  method, no allow suggestion) — golden:
  `internal/proxy/enforcer/testdata/hard_deny_body.json.golden`.
- The exact `how_to_proceed` strings (`responses.py:31-40`):
  - **soft:** "Not on the project allowlist. Ignore this resource if you can find the
    needed information elsewhere or can work reliably without it. If you think the
    information is important and would contribute significantly to your success,
    prompt the user and ask them to add the resource to the allowlist."
  - **hard:** "Permanently blocked. Do NOT ask the user to allow it. Do NOT retry.
    Find an alternative source."

**Load-bearing nuance:** AC-0017's "Done" note records that the soft-deny copy was
*deliberately reworded away from "route around" framing*
(`AC-0017-enforcer-decision-engine.md:82-84`; comment at `responses.py:28-30`). The
SKILL.md must use the current "ignore if available elsewhere / escalate only if
important" framing — **not** any "route around silently" phrasing.

Intended agent behavior per `docs/design.md`:
- **Allowed** (`:253`): proceed as usual.
- **Soft-deny** (`:266-269`): ignore and proceed if the info is available elsewhere
  or the agent can work reliably without it; escalate to the user (ask to allowlist)
  only when the info is important and would contribute significantly to success.
- **Hard-deny** (`:280-282`): never escalate, treat as final, find an alternative
  source or tell the user no authoritative source was found.

### 2. Activation contract

`docs/design.md:284`:

> The skill explains all three response types to Claude. It activates automatically
> when Claude sees the `X-Cage-Reason` header or the `agent_cage_` JSON error
> prefix — it's installed once by `agent-creance setup` into
> `~/.claude/skills/agent-creance/SKILL.md`. We don't touch the project's `CLAUDE.md`.

Claude Code skill mechanics (web research, sources below):
- A skill lives at `~/.claude/skills/<dir>/SKILL.md`; `SKILL.md` is the required
  entrypoint. **The directory name is the skill identity** — here fixed at
  `agent-creance`.
- At startup only `name` + `description` are preloaded; the body loads on invocation.
  **The `description` is the only thing Claude matches on** to decide activation.
- Best practice: third person, lead with the use case, include the **literal trigger
  strings**. Keep `description` ≤ 1024 chars (strictest cross-runtime cap) and
  front-load the markers (`X-Cage-Reason`, `agent_cage_`) because Claude Code may
  truncate long descriptions when many skills are present.
- `name` must be kebab-case, ≤ 64 chars, no XML, and must **not** contain the
  reserved words `anthropic`/`claude`. `agent-creance` satisfies all of these.
- Do **not** set `disable-model-invocation` (would block auto-activation) or `paths:`
  (gates on file globs, irrelevant to an error-string trigger).
- Caveat worth a one-line note to users: if `~/.claude/skills/` did not exist at
  session start, Claude Code needs a restart to pick up a newly created skills dir.
  Activation is best-effort relevance, not a guarantee (a hook would be deterministic
  — but that is out of scope here).

Proposed frontmatter:

```yaml
---
name: agent-creance
description: Explains how to react to agent-creance network egress refusals. Use when an HTTP request returns 403 with an "X-Cage-Reason" header or a JSON body whose "error" starts with "agent_cage_" (agent_cage_not_allowlisted = soft-deny, agent_cage_hard_deny = hard-deny). Describes the three response types (allowed, soft-deny, hard-deny) and the right action for each.
---
```

### 3. The install idiom to mirror (`internal/proxy/extract.go`)

`internal/setup` does not currently write files (it only `Stat`s for the CA —
`setup.go:84-101`). The repo's one embed+install precedent is
`internal/proxy/extract.go`:

- Embed: `//go:embed enforcer/...` → `var enforcerFS embed.FS` (`extract.go:30-31`).
  Embed paths are **slash-separated** regardless of OS — read with `path.Join`, not
  `filepath.Join` (`extract.go:34-36,77`). For a single file, either
  `//go:embed SKILL.md` → `var skillMD string` (simplest) or `embed.FS` (repo
  precedent). A single-file `string` embed is cleanest here.
- Install (`Extract`, `extract.go:68-86`): `MkdirAll(root, 0o755)` then per-file
  `writeIfChanged`.
- Idempotent atomic write (`writeIfChanged`, `extract.go:93-114`): `ReadFile(dest)`;
  if `bytes.Equal(got, want)` return nil (up to date); if `fs.ErrNotExist` fall
  through; else surface. Write `dest+".tmp"` with perm `0o644`, then `Rename(tmp,
  dest)`, best-effort `Remove(tmp)` on failure.
- Perms: dirs `0o755`, files `0o644` (`extract.go:40-41`).

`sysdep.FileSystem` methods available (`internal/sysdep/filesystem.go:19-39`):
`ReadFile`, `WriteFile(name,data,perm)`, `Stat`, `MkdirAll(name,perm)`, `Remove`,
`Rename`. Existence is `Stat` + `errors.Is(err, fs.ErrNotExist)` (no `Exists`
method). Home dir is `sysdep.PathResolver.UserHomeDir()`
(`internal/sysdep/pathresolver.go:29`).

### 4. Where the installer lives + DI

- `setup.Installer` (`internal/setup/setup.go:58-79`) already holds `fs
  sysdep.FileSystem` and `paths sysdep.PathResolver` (among others), injected via
  `NewInstaller(...)`. We add a method (e.g. `InstallSkill() error`) to it — no
  constructor change, no new sysdep interface.
- Path resolution pattern to copy (`setup.go:141-147` / `setupcheck.go:111-115`):
  `home, err := i.paths.UserHomeDir()` then
  `filepath.Join(home, ".claude", "skills", "agent-creance", "SKILL.md")`. Prefer a
  package-level `var skillFileRel = filepath.Join(".claude","skills","agent-creance","SKILL.md")`
  to match `setupcheck`. (Optional: export/share a single constant so the writer and
  the checker can't drift — see Open Questions.)
- The `cli.App` composition root already carries `FS` and `Paths`
  (`internal/cli/cli.go:28-30`, wired at `:79-94`), so AC-0028 can build the
  installer from `app` with no new plumbing.

### 5. Testing approach (hermetic, mirrors `internal/setup` + `internal/proxy`)

- Build the `Installer` from `sysdeptest` fakes with
  `paths.HomeDir = "/home/toby"` (pattern: `setup_test.go:21-47`). Expected path:
  `filepath.Join("/home/toby", ".claude","skills","agent-creance","SKILL.md")`.
- `FakeFileSystem` (`internal/sysdep/sysdeptest/filesystem.go`): seed via
  `Files[path] = []byte(...)`, inspect via `Files`, `Dirs`, `Perms`; inject errors via
  `WriteErrs`/`StatErrs`/`MkdirErrs`/`RenameErrs`. Assert content + perm
  (`filesystem_test.go:11-23`) and dir creation (`extract_test.go:67-78`).
- Tests required by the ticket (`AC-0027-skill-install.md:32-36`):
  1. `go build ./...` resolves the embed.
  2. Install writes the file to the expected path (assert `Files[skillPath]` ==
     embedded bytes, `Perms` == `0o644`, dir created `0o755`).
  3. Idempotency: pre-seed `Files[skillPath]` with the embedded bytes; re-install
     performs no rewrite/rename (use a wrapper over the fake that fails `Rename`/
     `WriteFile` to prove the no-op path, à la `appearingFS` in `setup_test.go:52-67`;
     `writeIfChanged` returns before writing when content matches).
  4. **CLAUDE.md guard:** after install, assert no key in `Files`/`Dirs`/`Perms`
     contains the substring `CLAUDE.md`.
  5. **Content check:** read the embedded string directly (no fake) and assert it
     contains `X-Cage-Reason`, `soft-deny`, `hard-deny` (and ideally
     `agent_cage_not_allowlisted`, `agent_cage_hard_deny`).
- No integration test is required for AC-0027 (pure FS write through the seam); the
  CA bootstrap's integration test (`setup_integration_test.go`) is unrelated.

### 6. "Never touch CLAUDE.md" — requirement and rationale

Required by ticket `:13,:17,:28` and verification `:34`; spec WP-5.2
(`2026-06-04-v0.1-technical-specification.md:317-319`); design `:284`. Rationale
(`docs/design.md:432-434`): `~/.claude` contents (`settings.json`, hooks, MCP
servers, `skills/`) are *executable config*. The caged agent is pointed at a
throwaway `CLAUDE_CONFIG_DIR`, closing the config-persistence vector. The skill is
host-side, user-scoped config installed once by `setup` — deliberately separate from
the per-project `CLAUDE.md` (version-controlled project input the agent could
otherwise edit). The guard test enforces that the installer's writes never target a
`CLAUDE.md` path.

## Code references

- `internal/setup/setup.go:58-79` — `Installer` struct + `NewInstaller` (add
  `InstallSkill` here).
- `internal/setup/setup.go:141-147` — `~`-relative path resolution idiom.
- `internal/proxy/extract.go:30-31,40-41,68-114` — embed + `MkdirAll` +
  idempotent atomic `writeIfChanged` (the pattern to mirror).
- `internal/sysdep/filesystem.go:19-39` — `FileSystem` interface.
- `internal/sysdep/pathresolver.go:29` — `UserHomeDir()`.
- `internal/setupcheck/setupcheck.go:38,111-115` — the pinned skill path
  (`~/.claude/skills/agent-creance/SKILL.md`) the writer must match.
- `internal/proxy/enforcer/responses.py:19-40` — frozen wire format the SKILL.md
  teaches.
- `internal/proxy/enforcer/testdata/{soft,hard}_deny_body.json.golden` — byte-exact
  bodies.
- `internal/setup/setup_test.go:21-67`, `internal/proxy/extract_test.go:67-78`,
  `internal/sysdep/sysdeptest/filesystem.go` — test harness patterns.
- `internal/cli/cli.go:28-30,79-94` — `App` seams (for AC-0028 wiring).
- `docs/design.md:249-284,350-364,432-434` — refusals, commands, executable-config.

## Open questions

1. **Skill directory vs `name` frontmatter.** The path is fixed to
   `.../skills/agent-creance/SKILL.md`, so the directory is `agent-creance`. Set
   `name: agent-creance` to match (recommended), or omit `name` (Claude Code defaults
   to the directory name)? Recommendation: set it explicitly for portability.
2. **Share the relative-path constant or duplicate it?** `setupcheck` defines its own
   `skillFileRel`. Should AC-0027 (a) export a single shared constant both packages
   use (avoids drift between the checker and the writer), or (b) duplicate the
   literal in `internal/setup` to keep packages decoupled? Recommendation: share one
   exported constant (e.g. in `setupcheck` or a small shared location) so the write
   target and the precondition check can never diverge.
3. **`InstallSkill` signature.** A plain `func (i *Installer) InstallSkill() error`
   (opt-out handled by the AC-0028 caller not calling it) vs. threading a `noSkill
   bool` now. Recommendation: plain no-arg method; `--no-skill` is AC-0028's concern.
4. **Depth of SKILL.md prose.** Ticket out-of-scope says "no deep prose beyond
   covering the three response types." Confirm a concise body (frontmatter + the
   three types + intended action for each + the exact JSON field names) is the target,
   not an exhaustive playbook.

## Related

- Ticket: `thoughts/shared/tickets/AC-0027-skill-install.md`
- Depends-on context: `thoughts/shared/tickets/AC-0017-enforcer-decision-engine.md`
  (Done — frozen wire format)
- Sibling: AC-0028/WP-5.3 (the `setup` command that wires CA + skill, `--no-skill`)
- Spec: `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md:317-322`

### Web sources (Claude Code skill format & activation)

- https://code.claude.com/docs/en/skills — Claude Code skills (frontmatter, layout,
  activation, truncation, live reload).
- https://platform.claude.com/docs/en/agents-and-tools/agent-skills/best-practices —
  description best practices, `name`/`description` validation limits.
- https://docs.claude.com/en/docs/agents-and-tools/agent-skills/overview — Agent
  Skills overview.
