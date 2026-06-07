# AC-0027: Skill install (WP-5.2)

**Status:** In Progress
**Estimated Complexity:** Small
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-5.2 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** none (content references AC-0017's response types)
**Spike gate:** none

## Problem Statement

The agent needs to understand the three network-refusal response types. agent-creance ships a Claude Code skill that teaches this, installed once into the user's skills dir — without touching the project's `CLAUDE.md`.

## Desired Outcome

`internal/setup` embeds `SKILL.md` and installs it to `~/.claude/skills/agent-creance/SKILL.md`, with a `--no-skill` opt-out, never modifying project `CLAUDE.md`.

## User Stories / Use Cases

- As an operator, I want the agent to know how to react to soft/hard-denies so that it routes around or escalates correctly without me coaching it.

## Acceptance Criteria

- [ ] `SKILL.md` is embedded (`go:embed`) and explains all three response types + the `X-Cage-Reason`/`agent_cage_` triggers.
- [ ] Installed to `~/.claude/skills/agent-creance/SKILL.md`; install is idempotent.
- [ ] `--no-skill` skips installation.
- [ ] The project's `CLAUDE.md` is never read or written.

## Verification & Test Steps

1. `go build ./...` → compiles (embed resolves).
2. `go test -race ./internal/setup/...`: with a fake FS, assert install writes the file to the expected path; re-install is idempotent; `--no-skill` writes nothing.
3. Guard: a test asserts no write targets any `CLAUDE.md` path.
4. Content check: assert the embedded SKILL mentions `X-Cage-Reason`, `soft-deny`, `hard-deny` (so the agent's activation triggers exist).
5. `make lint` → clean.

## Out of Scope

- The `setup` command wiring (AC-0028).
- Authoring deep skill prose beyond covering the three response types.

## Dependencies & Sequencing

Phase 5. Independent; wired by AC-0028.

## Questions for Research/Planning

- [ ] Skill activation mechanics — what description makes Claude load it on seeing an `agent_cage_` error?

## References

- `docs/design.md` — "Network refusal handling" (skill), "Commands" (setup).
- Spec WP-5.2.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.
