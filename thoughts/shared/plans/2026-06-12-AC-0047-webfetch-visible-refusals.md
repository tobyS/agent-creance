# AC-0047: Refusal visibility for body-blind clients — Implementation Plan

## Overview

Make cage egress refusals recognizable to agents whose HTTP clients hide
response bodies (Claude Code WebFetch): distinct refusal status codes —
**470 soft-deny / 471 hard-deny** instead of 403 — plus a launch-time
`--append-system-prompt` cage briefing injected into claude invocations.

## Current State Analysis

From `thoughts/shared/research/2026-06-12-AC-0047-webfetch-visible-refusals.md`:

- The refusal status lives in exactly one constant
  (`internal/proxy/enforcer/responses.py:42`, `_STATUS_FORBIDDEN = 403`) used by
  `soft_deny()` and `hard_deny()`; the `http_connect` passthrough hard-deny
  reuses `responses.hard_deny` (`enforcer.py:168-169`) so it inherits any change.
- The literal 403 is pinned across: Python tests (test_responses, test_enforcer,
  test_audit, test_integration — names and asserts), the verify battery
  (`matrix.go` Expected tokens + `fake-agent.sh` `$code` checks +
  `battery_test.go` fixtures + `cage-verification.md`), Go audit test fixtures +
  `format_lines.golden`, `logs_summary.txtar`, design.md (5 lines), and
  SKILL.md (4 lines, marked as swap points by AC-0049).
- The agent argv is assembled in the pure function `cage.Build`
  (`internal/cage/cage.go:77-136`), ending with `in.Config.Agent.Command...`
  (user config; init template seeds `["claude", "--dangerously-skip-permissions"]`).
  Coverage: `TestBuildGolden` (`internal/cage/testdata/invocation.golden.json`)
  and `TestRunHappyPath` (`internal/cli/run_test.go:131-146`, `argsContain`).
- `--append-system-prompt` works in interactive mode (since Claude Code
  v1.0.51), is per-invocation (fine — argv is rebuilt every `run`), and is NOT
  inherited by subagents.

## Desired End State

- Intercepted soft-denies answer HTTP **470**; hard-denies (request-hook and
  CONNECT-phase) answer HTTP **471**; `X-Cage-Reason` header and JSON bodies
  byte-identical to today. No refusal path returns 403.
- `agent-creance run` appends `--append-system-prompt <briefing>` to the agent
  command **iff** `filepath.Base(agent.command[0]) == "claude"`. The briefing
  names 470/471, explains WebFetch's body-blindness, says curl for the
  structured refusal / consult the skill / no mirror-hunting, and instructs the
  main agent to relay the cage context into subagent task prompts.
- Skill, design.md, and cage-verification.md reflect the new codes; the
  subagent limitation is documented in design.md.

## Decisions (from the question checkpoint, 2026-06-12)

1. **Injection scope:** only when the command basename is `claude` (wrapper
   scripts and non-Claude commands untouched).
2. **Subagent gap:** accepted; mitigated by a briefing sentence instructing
   Claude to pass the cage context to subagents in their task prompts.
3. Briefing text ships as an embedded markdown sibling in `internal/cage`
   (mirrors `internal/setup/skill.go`'s `//go:embed SKILL.md` pattern).

## What We're NOT Doing

- UA-scoped refusal rendering (parked in the ticket's out-of-scope).
- Skill *trigger-logic* redesign — only the literal status swaps + trigger
  wording for the new codes (AC-0049 prepared the spots).
- Baseline allowlist hosts (AC-0048).
- Any JSON body / header change; any audit log migration (status is recorded
  pass-through; old 403 entries stay readable).
- Non-Claude agent support.

## Phase 1: Status codes 470/471 on the wire

### Overview

Swap the single shared constant for two, then update every pin from the
research's exhaustive change list.

### Changes Required:

1. **`internal/proxy/enforcer/responses.py`** — replace `_STATUS_FORBIDDEN`
   with `STATUS_SOFT_DENY = 470` and `STATUS_HARD_DENY = 471` (exported-style
   names so tests can import them); use in `soft_deny()` / `hard_deny()`;
   update module/function docstrings ("three 403 bodies" → describe 470/471 and
   the rationale: status line is the only signal body-blind clients surface).
2. **`internal/proxy/enforcer/enforcer.py`** — comments/docstrings only
   (:7-8, :29, :162, :190, :224): 403 → 470/471 as appropriate.
3. **Python tests** (`make test-enforcer`):
   - `test_responses.py:49` → 470, `:56` → 471, docstring.
   - `test_enforcer.py`: rename `test_soft_deny_returns_403` →
     `..._returns_470` (assert 470), `test_hard_deny_returns_403_with_reason` →
     `..._returns_471_...` (assert 471), CONNECT-refusal test asserts 471
     (:124-128), audit asserts :233 → 470, :246 → 471, comment :226.
   - `test_audit.py:54` fixture → 470.
   - `test_integration.py`: rename `test_soft_deny_403`/`test_hard_deny_403` →
     `..._470`/`..._471`, asserts :194-209 → 470/471, hot-reload pre-state
     :261 → 470, audit asserts :288 → 470 / :300 → 471, docstring.
   - `conftest.py:4` comment.
4. **Verify battery** (tokens + probe in lock-step):
   - `internal/verify/matrix.go`: Expected `470:soft-deny` (:90, :100),
     `471:hard-deny` (:95); Desc strings (:92, :97).
   - `internal/verify/testdata/fake-agent.sh`: `$code` checks/emits
     :95-96 → 470, :105-106 → 471, :116-117 → 470, comment :113.
   - `internal/verify/battery_test.go:15`, `:27` fixture tokens → 470.
   - `internal/verify/verification_integration_test.go:49` comment.
   - `docs/cage-verification.md:95` checklist row.
5. **Go audit fixtures** — document the new contract: `entry_test.go:32/:39`,
   `format_test.go:13-14` (soft 470 / hard 471), `summary_test.go:16` → 471,
   `:19` → 470; regenerate `internal/audit/testdata/format_lines.golden` via
   `make golden` (review diff).
6. **`internal/cli/testdata/script/logs_summary.txtar:16/:32`** → 470.
7. **`docs/design.md`** :46, :267, :279, :295, :308 → 470/471, plus one
   rationale sentence at the refusal-handling section (status line is the only
   WebFetch-visible signal; 470/471 chosen clear of AWS ALB's 460/463/464;
   unknown 4xx = generic 400-class per RFC 9110, no retry semantics).
8. **`internal/setup/SKILL.md`** — swap the AC-0049-marked spots: frontmatter
   (:3, both occurrences → "returns 470 (soft-deny) or 471 (hard-deny)" / "bare
   470 or 471"), §2 → `470`, §3 → `471`, §4 (WebFetch quote becomes e.g. "HTTP
   470" and "bare 470/471"). Extend `TestSkillContentMentionsTriggers`
   (`internal/setup/skill_test.go`) with markers `"470"` and `"471"`.

### Success Criteria:

#### Automated Verification:
- [x] `make test-enforcer` passes (pytest, renamed tests assert 470/471)
- [x] `make test` passes (audit fixtures, verify battery unit, cli testscripts, skill tests)
- [x] `make lint` passes
- [x] `make golden` produces only the expected `format_lines.golden` diff (reviewed)
- [x] `make test-integration`: real-proxy battery vectors observe 470/471 (PASS);
      enforcer integration suite green (10 passed, live mitmdump). The battery's
      kc-read/kc-write vectors fail identically on unmodified HEAD (verified via
      stash) — pre-existing environmental keychain issue, unrelated to this change.

#### Manual Verification:
- [ ] (Deferred to live session) `agent-creance logs` shows 470/471 entries

---

## Phase 2: Launch-time cage briefing

### Overview

Embed the briefing text in `internal/cage` and inject
`--append-system-prompt <text>` into claude invocations in `cage.Build`.

### Changes Required:

1. **`internal/cage/briefing.md`** (new, embedded) — briefing text, drafted:

   > You are running inside an agent-creance cage: outbound HTTP is filtered
   > through an allowlist proxy. Refused requests return HTTP 470 (soft-deny:
   > not allowlisted; if the resource matters, ask the user to allowlist it)
   > or HTTP 471 (hard-deny: permanently blocked; never retry, never ask).
   > Fetch tools that hide response bodies (e.g. WebFetch) surface only the
   > bare status code — a 470/471 is the cage, not the website blocking you
   > and not an auth problem; do not hunt mirrors. To see the structured JSON
   > refusal, fetch the same URL with curl in the shell (proxy env and CA are
   > already set), and follow the agent-creance skill. When you delegate to
   > subagents, include this cage notice in their task prompts — subagents do
   > not inherit it automatically.

   (Exact wording may be tuned during implementation; must keep: 470/471,
   WebFetch blindness, curl, no mirrors, subagent relay.)
2. **`internal/cage/cage.go`** — `//go:embed briefing.md` into `var briefingMD
   string`; in `Build`, after appending `in.Config.Agent.Command...`
   (cage.go:129): if `filepath.Base(in.Config.Agent.Command[0]) == "claude"`,
   append `"--append-system-prompt", strings.TrimSpace(briefingMD)`. Comment
   states the claude-only detection decision and the subagent caveat.
3. **Tests:**
   - `internal/cage/cage_test.go`: table cases — claude command gets the flag
     +text; non-claude command (e.g. `["my-agent"]`) does not; path-qualified
     `/usr/local/bin/claude` does (basename match). Golden
     `testdata/invocation.golden.json` regenerated (review: two appended args).
   - `internal/cli/run_test.go` `TestRunHappyPath`: `argsContain` asserts
     `--append-system-prompt` is present with the briefing text.
4. **`docs/design.md`** — short paragraph in the run/launch (or refusal
   handling) section: the briefing, the claude-basename rule, and the
   documented subagent limitation (+ relay instruction).

### Success Criteria:

#### Automated Verification:
- [x] `make test` passes (cage unit + golden, run happy path)
- [x] `make lint` passes
- [x] `make golden` diff reviewed (invocation golden gains the two args)
- [x] `make build` run at the end (bin/agent-creance reflects final commit)

#### Manual Verification:
- [ ] Live caged session: agent states it knows it's caged (e.g. reacts to a
      470 without mirror-hunting); `ps` shows the flag on the claude process

---

## Testing Strategy

- Phase 1 is contract-heavy: pytest suite (`make test-enforcer`) is the
  authoritative gate for the wire change; the verify battery integration test
  (`make test-integration`) exercises the REAL mitmproxy + enforcer end to end
  — run it in Phase 1 since the probe script's `$code` checks changed.
- Phase 2 is pure Go: unit + golden. No new sysdep seams (argv assembly is a
  pure function).
- Full `make test` after each phase; `make build` at the very end (CLAUDE.md).

## Migration Notes

- Wire contract change is documented in design.md; old audit logs with 403
  remain readable (status pass-through). User scripts keying on 403 from the
  proxy must move to 470/471 — called out in the design.md rationale.
- The installed skill refreshes on the next `agent-creance setup` (drift
  rewrite) — remind the user at the end.

## References

- Ticket: `thoughts/shared/tickets/AC-0047-webfetch-visible-refusals.md`
- Research: `thoughts/shared/research/2026-06-12-AC-0047-webfetch-visible-refusals.md`
- AC-0049 (Done): skill swap points; `internal/setup/SKILL.md`
- Wire renderer: `internal/proxy/enforcer/responses.py`
- Argv assembly: `internal/cage/cage.go:77-136`
