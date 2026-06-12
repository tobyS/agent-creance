---
date: 2026-06-12
ticket: AC-0047
topic: "Make egress refusals visible to body-blind HTTP clients (WebFetch)"
status: complete
git_commit: fb00fb6e2931faaafbe8f0792a193172d7174534
branch: main
repository: git@github.com:tobyS/agent-creance.git
---

# Research: AC-0047 — refusal visibility (launch briefing + status codes 470/471)

**Ticket:** `thoughts/shared/tickets/AC-0047-webfetch-visible-refusals.md`
**Related:** AC-0049 (skill update for body-blind 403s, Done — its `403` mentions
are designated swap points for this ticket), AC-0048 (baseline hosts, separate).

## Part A — launch-time cage briefing (`--append-system-prompt`)

### Where the agent argv is assembled

- The agent command is **pure config data**: `agent.command` (`internal/config/config.go:37-41`),
  seeded by init's template as `["claude", "--dangerously-skip-permissions"]`
  (`internal/cli/init.go:314`). No hardcoded `claude` in the launch path; no CLI
  passthrough (`run` is `cobra.NoArgs`, `internal/cli/run.go:40`). Project
  `agent.command` replaces the global one (`internal/config/merge.go:23`).
- `cage.Build` (`internal/cage/cage.go:77-136`) is a pure function producing
  `Invocation{Path, Args, Env}`: safehouse flags → five `--append-profile`
  fragments → `--env-pass` → `--` → `in.Config.Agent.Command...` (cage.go:124-129).
  The injection point for a briefing flag is immediately after the agent command
  at cage.go:129. Env and argv are built in the same function (`buildEnv`,
  cage.go:254-279), whose precedence pattern (computed values overwrite user
  config) is the precedent for "creance-controlled args".
- **Where the briefing text should live:** embedded markdown sibling file in
  `internal/cage` (following `internal/setup/skill.go:22-23`'s `//go:embed
  SKILL.md` pattern) — next to the argv assembly, automatically covered by the
  existing golden test.

### Existing tests to extend

- `TestRunHappyPath` asserts the recorded exec via `sysdeptest.FakeProcessGroup`
  (`internal/cli/run_test.go:131-146`, helper `argsContain` at :280-287).
- `TestBuildGolden` pins the entire `Invocation` in
  `internal/cage/testdata/invocation.golden.json` (cage_test.go:48-64; the
  golden currently ends `"--", "claude", "--dangerously-skip-permissions"`).
- No testscript launches the cage (`run_missing_prereq.txtar` header explains
  why: real-Keychain dependency); the unit + golden pair is where the new flag
  gets asserted.

### Claude Code flag semantics (web research, confirmed against official docs 2026-06-12)

- `--append-system-prompt` **works in interactive (TUI) mode** since v1.0.51
  (official CHANGELOG; current CLI reference: all four system-prompt flags
  "work in both interactive and non-interactive modes").
- **Not inherited by subagents.** Subagents "receive only this [their own]
  system prompt …, not the full Claude Code system prompt" (sub-agents docs) —
  so appended text reaches the main thread only. Forks are the exception.
  **Consequence: the briefing does NOT directly fix the subagent case that
  triggered this ticket** — for subagents, the 470/471 status codes (Part B)
  are the only always-visible channel, plus whatever the main agent passes into
  subagent task prompts. Document this limitation (the ticket already allows
  "main-agent-only is acceptable; document").
- Per-invocation only: no settings.json/env equivalent exists; a `--resume`
  invocation must pass the flag again — automatic here, since agent-creance
  composes the argv on every `run`.
- Length/escaping: OS argv limits only (E2BIG for huge texts);
  `--append-system-prompt-file` exists as escape hatch. Irrelevant for a short
  briefing passed as a single argv element via `exec` (no shell involved).

### Design wrinkle: `agent.command` is arbitrary

The config can hold any argv (custom wrapper scripts, in principle non-Claude
agents). Blindly appending `--append-system-prompt <text>` would pass an
unknown flag to non-claude commands. Options:

1. Append only when `filepath.Base(command[0])` is `claude` (conservative;
   v0.1 is Claude-only anyway, and wrappers keep working).
2. Always append (simplest; breaks wrapper/non-claude commands).
3. Config opt-out flag (more surface; YAGNI for v0.1).

→ Decision for the planning checkpoint.

## Part B — status codes 470 (soft) / 471 (hard)

### Single source, two callers

- The wire status exists in exactly one constant: `_STATUS_FORBIDDEN = 403`
  (`internal/proxy/enforcer/responses.py:42`), used by `soft_deny()` (:77) and
  `hard_deny()` (:94). The `http_connect`-phase passthrough hard-deny reuses
  `responses.hard_deny` (`enforcer.py:168-169`), so it inherits the new code
  automatically. The split requires replacing the shared constant with two
  (e.g. `_STATUS_SOFT_DENY = 470`, `_STATUS_HARD_DENY = 471`).
- `policy.py` has zero wire/status concerns. `enforcer.py` mentions 403 only in
  comments/docstrings (:7-8, :29, :162, :190, :224).

### Complete change list for the literal 403 (verified exhaustive, thoughts/ excluded)

Python tests (run via `make test-enforcer` = pytest in `.venv-enforcer`,
**not** part of `make test`; integration variant via `make test-integration`):
- `test_responses.py:1` (docstring), `:49`, `:56` (status asserts)
- `test_enforcer.py:89/93`, `:103/107` (test names contain 403 — rename),
  `:124-128` (http_connect refusal → will assert 471), `:226` (comment),
  `:233`, `:246` (audit status asserts)
- `test_audit.py:54` (fixture arg)
- `test_integration.py:8-9` (docstring), `:194/200`, `:205/209` (test names +
  asserts), `:261` (hot-reload pre-state), `:288`, `:300` (audit asserts)
- `conftest.py:4` (comment)
- Golden bodies contain **no status literal** — no body golden regeneration
  needed for the status change.

Verify battery (tokens must change in lock-step with the probe script, which
runs against the REAL proxy in the integration test):
- `internal/verify/matrix.go:90/92` (`403:soft-deny` + Desc), `:95/97`
  (`403:hard-deny` + Desc), `:100` (`403:soft-deny`)
- `internal/verify/testdata/fake-agent.sh:95-96`, `:105-106`, `:113` (comment),
  `:116-117` (`$code` checks + emitted tokens)
- `internal/verify/battery_test.go:15`, `:27` (fixture tokens)
- `internal/verify/verification_integration_test.go:49` (comment)
- `docs/cage-verification.md:95` (manual checklist row)

Go-side audit (status is pass-through, `internal/audit/entry.go:53`; fixtures
document the old contract):
- `internal/audit/entry_test.go:32`, `:39`; `format_test.go:13-14`;
  `summary_test.go:16`, `:19`
- `internal/audit/testdata/format_lines.golden:2-3` (`-> 403`; regenerate via
  `make golden`)

CLI testscript:
- `internal/cli/testdata/script/logs_summary.txtar:16`, `:32`

Docs:
- `docs/design.md:46`, `:267`, `:279`, `:295`, `:308`

Skill (AC-0049 marked these as the swap points):
- `internal/setup/SKILL.md:3` (×2 in the description line), `:20`, `:33`,
  `:42-43` — plus `internal/setup/skill_test.go` markers if wording changes
  (current markers don't pin "403", so only SKILL.md text changes).

`internal/policy/render`, `internal/proxy/*.go`, README, Makefile: zero hits.
`__pycache__/*.pyc`: auto-regenerated, ignore.

### Audit log compatibility

The audit `status` field records whatever the response carried (pass-through);
`agent-creance logs` formatting (`internal/audit/format.go:16`) prints it
verbatim. Old logs with 403 entries remain readable — no migration.

## Impact analysis

- **Wire contract change** (status line only; header + JSON body byte-identical
  per ticket AC): consumers are the skill (AC-0049 text), the verify battery,
  and any user scripts keying on 403 — design.md is the documented contract and
  changes with it. RFC 9110: clients treat unknown 4xx as generic 400-class;
  no retry semantics attach to 470/471.
- **Launch path change** is additive argv injection in one pure function with
  golden + unit coverage; no new sysdep interfaces needed.
- The enforcer pytest suite is the verification gate for Part B
  (`make test-enforcer`); `make test` covers Go-side fixtures/goldens; the
  full battery (`make test-integration`) exercises the real proxy tokens.

## Open questions (for the planning checkpoint)

1. Briefing injection scope: claude-detection vs always-append (Part A wrinkle).
2. Subagent limitation of the briefing: accept + document (recommended; 470/471
   remains the subagent-visible signal), or extend scope?
3. Briefing text: short inline constant vs embedded .md file (embed recommended,
   mirrors SKILL.md pattern); exact wording delegated to implementation per the
   ticket's acceptance criteria (must name 470/471, WebFetch blindness, curl).
