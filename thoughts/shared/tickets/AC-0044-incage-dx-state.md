# AC-0044: Provide host Claude Code DX state to the caged agent

**Status:** Rejected
**Estimated Complexity:** Large
**Maturity:** Skeleton — to be detailed in a dedicated session
**Created:** 2026-06-12
**Updated:** 2026-06-14

> **Skeleton ticket.** Captures what we observed on 2026-06-12; the desired
> outcome, scope, and acceptance criteria need a dedicated discussion
> (`/tce:discuss` or a full `/tce:create_ticket` pass) before research starts.
>
> **Superseded for v0.1 (2026-06-12).** The AC-0045 decision mounts the real
> `~/.claude` (and `~/.claude.json`) read-write into the cage, so the
> developer's global `CLAUDE.md`, settings, and hooks all reach the caged
> session for free — this ticket's v0.1 pain is resolved as a side effect. It
> does so by exactly the thing this ticket flagged as out of scope (a blunt RW
> mount), trading away config isolation deliberately. The careful per-class
> sharing model sketched below is no longer a v0.1 concern; it folds into the
> config-cage revisit, **AC-0046**.

## Problem Statement

The cage points Claude Code at an empty per-project `CLAUDE_CONFIG_DIR`
(`~/.cache/agent-creance/projects/<hash>/claude`, seeded with `{}` only —
`internal/cage/cage.go:187-199`, AC-0035) and never exposes the real
`~/.claude`. This is a deliberate security property: Claude config is
*executable* (settings can define hooks/commands), so copying it into the cage
would import attack surface.

But the consequence, observed live today: a developer who uses Claude Code all
day starts a caged session and gets a **fresh install** — re-onboarding, no
global `CLAUDE.md` memory, no hooks configuration, no settings, none of the
accumulated developer-experience state. That experience is bad enough to scare
users away from the cage immediately. The DX state is vital, not cosmetic.

## Desired Outcome (sketch — to be refined)

A caged session feels like *the developer's own* Claude Code — their global
instructions, relevant settings, and familiar behavior — while preserving the
cage's core property that the host's real config can neither be exfiltrated
wholesale nor mutated from inside, and that executable config entering the
cage is a deliberate, visible choice rather than an accident.

## What we observed (2026-06-12)

- First caged `run` in a long-used project performed Claude Code's first-time
  onboarding (theme, defaults) despite daily host usage.
- Global `CLAUDE.md`, hooks config, and settings from `~/.claude` were absent
  in-cage.
- Per-project ephemeral state does persist across launches (onboarding repeats
  per project, not per launch) — by design (AC-0035).
- Related but separate: auth/login state is handled in AC-0045 (credential
  access); this ticket is about the *developer-experience* state.

## Open Questions (for the upcoming discussion)

- Which classes of host state matter most? (global `CLAUDE.md`/memory,
  `settings.json`, hooks, MCP server config, keybindings, themes,
  plugins/skills, command history?) Which are safe, which are exactly the
  attack surface the cage exists to exclude (hooks = arbitrary command
  execution)?
- Sharing model per class: copy-once at launch, sanitized projection
  (non-executable subset), read-only mount, or live share? What does
  "read-only" even protect when the agent could act on poisoned content?
- Direction of trust: protecting the host from the caged agent (writes back to
  `~/.claude`) vs. protecting the cage session from host config that the
  threat model considers untrusted input.
- Should the user choose per class (config knobs in `.agent-creance.yaml` /
  global config), with which defaults?
- Does suppressing re-onboarding (seeding the onboarding-complete flag) stand
  alone as a cheap first slice?
- How does this interact with AC-0045's minimal auth-state seeding (if any)?

## Out of Scope (preliminary)

- Credential/keychain access and login state — AC-0045.
- Anything that silently mounts the real `~/.claude` read-write.

## Questions for Research/Planning

[To be filled after the detailed discussion.]

## References

- Live observation: 2026-06-12 session (first caged run after AC-0042/0043
  fixes; re-onboarding + missing global CLAUDE.md/hooks noted by the user)
- `internal/cage/cage.go:171-199` — ephemeral CLAUDE_CONFIG_DIR seeding (`{}`)
- `thoughts/shared/tickets/AC-0035-incage-claude-config-dir-writable.md` — the
  deliberate per-project persistence design
- `docs/design.md:465` — "Executable config is redirected and ephemeral"
- AC-0045 — in-cage credential access (sibling ticket)

## Implementation Plan

[Leave empty — skeleton.]

## Notes & Updates

### 2026-06-12
- Skeleton created at the user's request after live testing surfaced the
  fresh-install experience; full requirements discussion deferred to a
  dedicated session. Key user statement: global CLAUDE.md, hooks config, etc.
  are vital for developer experience — not providing them "would scare users
  away immediately".

### 2026-06-14
- **Rejected, superseded by AC-0046.** The v0.1 pain (re-onboarding, missing
  global CLAUDE.md/hooks) is already resolved as a side effect of the AC-0045
  RW mount of `~/.claude`. The remaining substance — the careful per-class
  sharing model and the open questions about which config classes are safe vs.
  attack surface — folds into the config-cage revisit (AC-0046). Closed here to
  avoid duplicate tracking.
