# Implementation Status: AC-0047 — Refusal visibility (470/471 + cage briefing)

## Phase 1: Status codes 470/471 on the wire
- **Status**: ✅ Complete
- **Started**: 2026-06-12 18:05
- **Completed**: 2026-06-12 18:35

### Steps Performed
1. `responses.py`: `_STATUS_FORBIDDEN` → `STATUS_SOFT_DENY = 470` /
   `STATUS_HARD_DENY = 471`; module + function docstrings now carry the
   rationale (body-blind clients, RFC 9110, ALB squat avoidance).
2. `enforcer.py`: docstring/comment 403 mentions updated (no code change — the
   http_connect hard-deny inherits 471 via `responses.hard_deny`).
3. Python tests: renames + asserts (`test_soft_deny_returns_470`,
   `test_hard_deny_returns_471_with_reason`, CONNECT-refusal → 471,
   `test_soft_deny_470` / `test_hard_deny_471` in test_integration.py, audit
   asserts 470/471, test_audit fixture, conftest comment).
4. Verify battery: matrix.go Expected tokens + Descs → `470:soft-deny` /
   `471:hard-deny`; fake-agent.sh `$code` checks/emits; battery_test fixtures;
   integration-test + cage-verification.md wording.
5. Go audit fixtures → 470/471; `format_lines.golden` regenerated via
   `make golden` (diff: exactly the two refusal lines).
6. `logs_summary.txtar` fixture + assertion → 470.
7. design.md: refusal section now says 470/471 + new rationale paragraph after
   the hard-deny guidance; skill paragraph updated.
8. SKILL.md: frontmatter + §2/§3/§4 swapped to 470/471 (the AC-0049-designated
   spots); skill_test.go markers extended with "470"/"471".

### Issues Encountered
- `make test-integration`: battery's kc-read/kc-write vectors FAIL — verified
  identical failure on unmodified HEAD (git stash → run → pop), so it's a
  pre-existing environmental keychain issue on this host, not a regression.
  All three proxy vectors PASS with the new codes against the REAL cage.

### Verification
- ✅ `make test-enforcer` (92 passed)
- ✅ `make test-enforcer-integration` (10 passed, live mitmdump: 470/471 on the wire)
- ✅ `make test` (full Go suite, race)
- ✅ `make lint`
- ✅ `make golden` (reviewed: only format_lines.golden, two lines)
- ⚠️ `make test-integration`: proxy vectors green; kc vectors pre-existing env failure (see above)

### Commit
- `e62eb15` feat(AC-0047): refusal status codes 470 (soft-deny) / 471 (hard-deny)

---

## Phase 2: Launch-time cage briefing
- **Status**: ✅ Complete
- **Started**: 2026-06-12 18:40
- **Completed**: 2026-06-12 18:50

### Steps Performed
1. New embedded `internal/cage/briefing.md`: names 470/471 and their semantics,
   WebFetch body-blindness, curl-for-details + skill pointer, no mirror-hunting,
   and the subagent-relay instruction (checkpoint decision).
2. `internal/cage/cage.go`: `//go:embed briefing.md`; `Build` appends
   `--append-system-prompt <briefing>` after the agent command iff
   `filepath.Base(agent.command[0]) == "claude"` (checkpoint decision).
3. Tests: `TestBuildCageBriefing` (claude / path-qualified claude /
   non-claude untouched); `TestRunHappyPath` asserts the flag+text on the
   recorded safehouse argv; `invocation.golden.json` regenerated (diff: the two
   appended args).
4. design.md: paragraph after the skill section documenting the briefing, the
   claude-basename rule, and the subagent limitation + relay.

### Issues Encountered
- None.

### Verification
- ✅ `make test` (full suite, race)
- ✅ `make lint`
- ✅ `make golden` (reviewed: only invocation.golden.json, two args)
- ✅ `make build` after the final commit

### Commit
- (filled after commit) feat(AC-0047): launch-time cage briefing for claude invocations
