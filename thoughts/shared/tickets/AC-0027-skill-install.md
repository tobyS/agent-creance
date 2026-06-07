# AC-0027: Skill install (WP-5.2)

**Status:** Done
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

- [x] `SKILL.md` is embedded (`go:embed`) and explains all three response types + the `X-Cage-Reason`/`agent_cage_` triggers.
- [x] Installed to `~/.claude/skills/agent-creance/SKILL.md`; install is idempotent.
- [~] `--no-skill` skips installation. *(Library opt-out = the caller not invoking `InstallSkill()`; the `--no-skill` command flag is wired in AC-0028, per Out of Scope below.)*
- [x] The project's `CLAUDE.md` is never read or written.

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

- [x] Skill activation mechanics — what description makes Claude load it on seeing an `agent_cage_` error? **Answer:** Claude Code preloads only each skill's `name`+`description` and matches activation on the `description` alone. So the shipped `description` front-loads the literal trigger strings (`X-Cage-Reason`, `agent_cage_`, plus the two enum names) in its first sentence — third-person, well under the 1024-char cap so it survives skill-listing truncation. `name: agent-creance` matches the fixed skill directory. Activation is best-effort relevance (a hook would be deterministic, but that's out of scope).

## References

- `docs/design.md` — "Network refusal handling" (skill), "Commands" (setup).
- Spec WP-5.2.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.

### 2026-06-07
Implemented (library-only, WP-5.2). `internal/setup` embeds `SKILL.md` (`go:embed`)
and `Installer.InstallSkill()` writes it idempotently to
`~/.claude/skills/agent-creance/SKILL.md` (atomic write-tmp-then-rename, dirs 0o755,
file 0o644), mirroring `internal/proxy/extract.go`. The relative path is the shared
`setupcheck.SkillFileRel` constant so the writer and run's precondition checker can't
drift. SKILL.md is a concise reference covering allowed/soft-deny/hard-deny, aligned
byte-for-byte with the frozen AC-0017 wire wording. Hermetic tests cover write-to-
path, idempotency, drift-rewrite, error propagation, the CLAUDE.md guard, and the
content markers. `make test`, `make lint`, and the integration build tag are green.
The `--no-skill` flag + `setup` command wiring remain AC-0028.

Research: `thoughts/shared/research/2026-06-07-AC-0027-skill-install.md`
Plan: `thoughts/shared/plans/2026-06-07-AC-0027-skill-install.md`
