---
date: 2026-06-12
ticket: AC-0049
topic: "Skill recognizes body-blind WebFetch 403s as cage refusals"
status: complete
git_commit: 496cd4e280accbdd4c1197751cc1e964dd62f9da
branch: main
repository: git@github.com:tobyS/agent-creance.git
---

# Research: AC-0049 — skill trigger for body-blind WebFetch 403s

**Ticket:** `thoughts/shared/tickets/AC-0049-skill-webfetch-blind-403.md`
**Companion evidence:** AC-0047 (`thoughts/shared/tickets/AC-0047-webfetch-visible-refusals.md`)

## Problem context (from the live session, 2026-06-12)

Claude Code's WebFetch reports non-2xx responses to the model as only a status
line — verified by fetching a byte-identical soft-deny mock (HTTPS, 403,
`X-Cage-Reason: soft-deny`, full JSON body) through WebFetch, which returned:

> The server returned HTTP 403 Forbidden. The response body was not retrieved.
> If this URL requires authentication, use an authenticated tool…

So the skill's current trigger markers (header + `agent_cage_` body prefix)
never reach the model on the WebFetch path. The skill must additionally trigger
on the *situation* (bare 403 from a fetch tool while caged) rather than only on
the *markers*.

## Current skill content

`internal/setup/SKILL.md` — frontmatter `description:` (single line, SKILL.md:3)
lists the egress triggers (403 + `X-Cage-Reason` / `agent_cage_*`) and the
AC-0045 auth-failure triggers. Body has four sections: Allowed (§1), Soft-deny
(§2), Hard-deny (§3), Authentication failure (§4). §§2–3 describe the JSON
fields and the correct reaction; nothing covers a refusal whose body the client
cannot see.

## Edit blast radius (everything that pins SKILL.md content)

Only two tests assert content, both in `internal/setup/skill_test.go` against
the embedded `skillMD` (`//go:embed SKILL.md`, `internal/setup/skill.go:22-23`):

- `TestSkillContentMentionsTriggers` (skill_test.go:96-112) — requires these
  substrings anywhere in the file: `X-Cage-Reason`, `soft-deny`, `hard-deny`,
  `agent_cage_not_allowlisted`, `agent_cage_hard_deny`,
  `Failed to start OAuth callback server`, `log in`, `on the host`,
  `restart the caged session`. All survive an additive edit; the new trigger
  should be appended to this list.
- `TestSkillAuthTriggersInFrontmatter` (skill_test.go:117-131) — extracts the
  `---`-delimited frontmatter and requires it to contain
  `Failed to start OAuth callback server` and `login/onboarding prompt`. The
  pattern to follow for asserting the new WebFetch trigger lands in the
  frontmatter (what agents match on), not just the body.

Everything else is presence/path-only and content-agnostic:
`internal/setupcheck/setupcheck.go:100-125` (`Verify` does `Stat` only),
`internal/cli/{run,init,setup}_test.go` (seed/assert file existence),
`internal/cli/testdata/script/setup_help.txtar:8` (`--no-skill` flag in help).
No golden files read SKILL.md. The `X-Cage-Reason`/`agent_cage_*` goldens under
`internal/proxy/enforcer/testdata/` pin the *wire* contract (unchanged here).

## Install / staleness mechanics

- `Installer.InstallSkill()` (`internal/setup/skill.go:35-45`) writes to
  `~/.claude/skills/agent-creance/SKILL.md` (path constant
  `setupcheck.SkillFileRel`, `internal/setupcheck/setupcheck.go:39`) via
  `writeSkillIfChanged` (skill.go:52-73): byte-compare against embedded content,
  atomic tmp+rename on drift.
- Consequence: after this change + `make build`, the installed copy refreshes on
  the next `agent-creance setup` (drift rewrite, pinned by
  `TestInstallSkillRewritesOnDrift`, skill_test.go:53-64). `agent-creance run`'s
  precondition is presence-only and will NOT refresh — the final summary must
  tell the user to re-run `setup`.

## docs/design.md sync points

- design.md:308 describes the activation contract ("activates automatically when
  Claude sees the `X-Cage-Reason` header or the `agent_cage_` JSON error
  prefix"). It already lags AC-0045 (no auth trigger mentioned). Adding the
  WebFetch-blind trigger widens that gap; a one-sentence amendment there keeps
  the design doc honest (optional but cheap).
- design.md:293/:306 paraphrase the soft/hard-deny guidance — unchanged by this
  edit (the new section defers to §§2–3 rather than restating them).
- design.md:444 (stranded-agent raw connection errors are deliberately not a
  skill trigger) — must stay true: the new trigger is specifically an HTTP 403
  status from a fetch tool, not connection-level failures. Wording must not
  accidentally cover connection-refused.

## Forward compatibility with AC-0047

AC-0047 will change refusal codes to 470 (soft) / 471 (hard). Keep every
status-code mention in the new text as the literal `403` in exactly the spots
where today's contract says 403 (frontmatter trigger + new body section), so the
later change is a mechanical number swap. Do not enumerate codes anywhere else.

## Implementation shape (for the plan)

1. `internal/setup/SKILL.md`:
   - Frontmatter description: add the third trigger clause — a fetch tool
     (e.g. Claude Code WebFetch) failing with a bare `403` and "response body
     was not retrieved" / no body or headers visible, while running inside an
     agent-creance cage.
   - New body section (between §3 and §4, becoming §4; auth becomes §5 — or
     appended after auth; placement is a plan decision): "Body-blind clients:
     WebFetch hides the refusal" — explains the situation, forbids the wrong
     conclusions (site blocks fetches / needs auth / try mirrors), and gives the
     reaction: `curl <url>` inside the cage (proxy env + mitmproxy CA are
     already exported) to see the structured JSON refusal, then apply §2/§3.
2. `internal/setup/skill_test.go`:
   - Extend `TestSkillContentMentionsTriggers` with the new pinned substrings.
   - Add a frontmatter assertion (mirroring `TestSkillAuthTriggersInFrontmatter`)
     for the WebFetch trigger phrase.
3. `docs/design.md:308` — extend the activation sentence (optional, recommended).
4. `make test`, `make lint`, `make build` (CLAUDE.md: binary must reflect final
   commit); remind user to re-run `agent-creance setup` to refresh the installed
   copy.

## Impact analysis

Additive text-only change to an embedded asset plus its tests. No runtime code
paths change; no wire contract changes; no golden files affected. The only
behavioral consumer is the Claude Code skill loader reading the installed copy.
