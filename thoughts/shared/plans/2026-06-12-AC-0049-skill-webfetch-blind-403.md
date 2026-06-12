# AC-0049: Skill trigger for body-blind WebFetch 403s — Implementation Plan

## Overview

Extend the shipped `agent-creance` skill so a caged agent recognizes a bare
WebFetch 403 (body and headers discarded by the client) as a probable cage
refusal and curls the URL for the structured reason instead of mirror-hunting.
Text-only change to the embedded `internal/setup/SKILL.md` plus its
content-pinning tests and one design.md sentence.

## Current State Analysis

From `thoughts/shared/research/2026-06-12-AC-0049-skill-webfetch-blind-403.md`:

- `internal/setup/SKILL.md` triggers only on wire markers (`X-Cage-Reason`
  header, `agent_cage_` JSON prefix) that Claude Code's WebFetch never shows
  the model (it reports only "HTTP 403 Forbidden. The response body was not
  retrieved." — proven by experiment 2026-06-12).
- Body sections: §1 Allowed, §2 Soft-deny, §3 Hard-deny, §4 Authentication
  failure (AC-0045).
- Content is pinned by exactly two tests in `internal/setup/skill_test.go`:
  `TestSkillContentMentionsTriggers` (substrings anywhere, lines 96-112) and
  `TestSkillAuthTriggersInFrontmatter` (substrings in frontmatter, lines
  117-131). Everything else is presence-only.
- Installed copy refreshes only on `agent-creance setup` (byte-compare drift
  rewrite, `internal/setup/skill.go:52-73`); `run` checks presence only.
- `docs/design.md:308` describes the activation contract and should gain the
  new trigger clause.

## Desired End State

- The skill frontmatter triggers on: a fetch tool (e.g. WebFetch) failing with
  a bare `403` / "response body was not retrieved" while working inside the
  cage.
- A new body section explains body-blind clients and the reaction: don't
  conclude site-blocking/auth, don't try mirrors, `curl` the URL inside the
  cage (proxy env + CA already set) to see the JSON refusal, then apply the
  soft-/hard-deny sections.
- Tests pin the new trigger phrases; design.md:308 mentions the new
  activation case; `bin/agent-creance` is rebuilt.

## Key Decisions

- **Placement:** new section becomes **§4 "Body-blind clients"** (directly
  after the hard-deny section it refers back to); the auth section is
  renumbered to §5. Keeps wire-response material contiguous.
- **AC-0047 forward-compatibility:** the literal `403` appears in exactly two
  new places (frontmatter clause + §4 text) — the spots AC-0047 will later
  swap to 470/471. No other code enumeration.
- **Trigger wording** uses the exact phrase WebFetch emits — "response body
  was not retrieved" — plus the tool name "WebFetch", so description matching
  works on what the model actually sees.
- **No connection-error creep:** the trigger is an HTTP 403 status from a
  fetch tool, NOT connection-refused/timeout — keeps design.md:444 (stranded
  agent case deliberately uncovered) true.

## What We're NOT Doing

- Status codes 470/471, launch-time `--append-system-prompt` briefing (AC-0047).
- Baseline allowlist additions (AC-0048).
- Any change to the enforcer wire responses, goldens, or setupcheck logic.
- Making `run` refresh a stale installed skill (setup-only refresh stays).

## Phase 1: Skill text, tests, design.md sync

### Changes Required:

#### 1. `internal/setup/SKILL.md`

**Frontmatter** — extend the single-line `description:` by inserting a third
trigger clause between the egress clause and the auth clause, and widen the
"Covers …" sentence:

> …(agent_cage_not_allowlisted = soft-deny, agent_cage_hard_deny = hard-deny),
> OR when a fetch tool such as WebFetch fails with a bare 403 — "response body
> was not retrieved", no headers or body visible — while running inside the
> cage, OR when Claude Code inside the cage shows a login/onboarding prompt …
> Covers the three egress response types — allowed, soft-deny, hard-deny —
> plus the body-blind fetch case and the authentication-failure case, and the
> right action for each.

**Body** — insert after §3 (renumber current §4 → §5):

```markdown
## 4. Body-blind clients — WebFetch hides the refusal

Some fetch tools (notably Claude Code's WebFetch) discard the body and headers
of non-2xx responses: you see only "The server returned HTTP 403 Forbidden.
The response body was not retrieved." Inside the cage, such a bare 403 is
almost always one of the two refusals above — NOT the website blocking you,
and NOT an authentication problem.

**What to do:** Do NOT conclude the site blocks direct fetches or requires
auth. Do NOT try mirrors or alternative URLs for the same content. Fetch the
same URL with curl in the shell instead — the cage already exports the proxy
environment and CA, so a plain `curl '<url>'` shows the structured JSON
refusal — then follow section 2 (soft-deny) or section 3 (hard-deny).
```

#### 2. `internal/setup/skill_test.go`

- `TestSkillContentMentionsTriggers`: append pinned substrings
  `"WebFetch"`, `"response body was not retrieved"`,
  `"Do NOT try mirrors"`.
- New `TestSkillWebFetchTriggerInFrontmatter`, mirroring
  `TestSkillAuthTriggersInFrontmatter` (same frontmatter extraction), asserting
  the frontmatter contains `"WebFetch"` and `"response body was not retrieved"`
  — the trigger must live in what agents match on, not only the body.

#### 3. `docs/design.md` (line ~308)

Extend the activation sentence so it also names the body-blind case, e.g.:
"…when Claude sees the `X-Cage-Reason` header or the `agent_cage_` JSON error
prefix, or — for body-blind clients like Claude Code's WebFetch, which surface
only the status line — a bare 403 from a fetch attempt inside the cage."
(Read the surrounding paragraph first and match its voice.)

### Success Criteria:

#### Automated Verification:

- [x] `make test` passes (including the extended/new skill tests)
- [x] `make lint` passes
- [x] `go build ./...` passes
- [x] `make build` run at the end (CLAUDE.md: `bin/agent-creance` must embed
      the final commit)

#### Manual Verification:

- [ ] User re-runs `agent-creance setup` so the installed
      `~/.claude/skills/agent-creance/SKILL.md` picks up the drift rewrite
- [ ] (Deferred to next caged session) a bare WebFetch 403 in a cage leads the
      agent to curl the URL / report the cage denial instead of mirror-hunting

## Testing Strategy

Unit only — the two content-pinning tests plus the new frontmatter test; no
golden files, no testscripts, no integration tests are affected (research:
"edit blast radius"). Run the full `make test` regardless.

## Migration Notes

Installed skill copies refresh on the next `agent-creance setup` (automatic
drift rewrite); no other migration. AC-0047 will later swap the two literal
`403` mentions introduced here to 470/471.

## References

- Ticket: `thoughts/shared/tickets/AC-0049-skill-webfetch-blind-403.md`
- Research: `thoughts/shared/research/2026-06-12-AC-0049-skill-webfetch-blind-403.md`
- Structural precedent: AC-0045 (auth-failure section + frontmatter-trigger test)
- Companion: AC-0047 (`thoughts/shared/tickets/AC-0047-webfetch-visible-refusals.md`)
