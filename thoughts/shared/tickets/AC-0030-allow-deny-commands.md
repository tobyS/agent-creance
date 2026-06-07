# AC-0030: `allow` / `deny` commands (WP-6.1)

**Status:** Done
**Estimated Complexity:** Medium
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-6.1 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** AC-0013 (compile), AC-0020 (overlay purge lifecycle)
**Spike gate:** none

## Problem Statement

When the agent hits a soft-deny, the operator needs a quick way to allow a URL — permanently (project or global config) or just for this session (`--once`) — and to add permanent denies. Mutations must recompile and hot-reload so the change is live without a restart.

## Desired Outcome

`agent-creance allow URL` appends a soft-allow to the project YAML; `--global` targets `~/.config`; `--once` writes to the session-overlay (purged on last-agent-exit per AC-0020); `agent-creance deny URL [--reason]` appends a `deny_always` rule. All trigger a recompile + hot-reload.

## User Stories / Use Cases

- As an operator, I want `agent-creance allow <url>` to unblock the agent immediately so that I don't restart the session.
- As an operator, I want `--once` to not pollute my committed config so that experiments stay ephemeral.

## Acceptance Criteria

- [x] `allow URL` appends a soft-allow to `.agent-creance.yaml`; `--global` to `~/.config/agent-creance.yaml`; `--once` to the session-overlay file (not the YAML).
- [x] `deny URL` appends a `deny_always` rule; `--reason` sets its reason.
- [x] Each mutation recompiles `policy.json`; the running proxy hot-reloads (mtime touch).
- [x] `--once` rules survive only the session (purged on last-agent-exit by AC-0020) — verified end to end.

## Verification & Test Steps

1. `go build ./...` → compiles.
2. Hermetic CLI tests (`testscript`):
   - `allow host/path` → the rule appears in `.agent-creance.yaml` and in a subsequent `policy show`.
   - `allow --global …` → lands in the global file, not the project file.
   - `allow --once …` → lands in the session-overlay (assert it is NOT in `.agent-creance.yaml`) and shows in `policy show`.
   - `deny … --reason "x"` → `deny_always` rule with reason present; `policy explain` reports hard-deny.
3. Recompile/reload: assert `policy.json` mtime advances after a mutation.
4. `--once` lifecycle: simulate last-agent-exit teardown (AC-0020) and assert the overlay is purged and the rule no longer appears.
5. `go test -race ./internal/cli/...` → green; `make lint` → clean.

## Out of Scope

- The overlay purge mechanism itself (AC-0020) — this writes overlay entries; AC-0020 deletes the file.
- Interactive escalation UX inside the agent (skill territory).

## Dependencies & Sequencing

Phase 6. Depends on the compiler and the overlay lifecycle.

## Questions for Research/Planning

- [ ] YAML append that preserves comments/formatting — `yaml.v3` node editing vs naive append.
- [ ] URL → rule parsing (host/path/method extraction) shared with `policy explain`?

## References

- `docs/design.md` — "Commands" (allow/deny), "Session-scoped allows".
- Spec WP-6.1.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.

### 2026-06-07 — Implemented (Done)
`allow`/`deny` commands shipped. Research and plan:
`thoughts/shared/research/2026-06-07-AC-0030-allow-deny-commands.md`,
`thoughts/shared/plans/2026-06-07-AC-0030-allow-deny-commands.md`.

Resolved questions:
- **YAML append (comment preservation):** added `config.AppendRule` — parse only to
  locate the insertion point via `yaml.Node` positions, splice the rendered rule in as
  text (rest of the file untouched), synthesizing missing network/egress/<list>
  structure. Bounded by a duplicate no-op and a validation gate (re-parse + rule-set
  diff) so a splice bug can never write a wrong policy. Chosen over node re-encode
  (drops comments) and naive append (brittle).
- **URL→rule:** bare host = whole host; host+path scopes to that prefix; no `--method`
  (all methods). Shares a `splitURL` helper with `policy explain`.
- **Forge expansion** for `allow <repo-url>`: deferred (single rule per invocation).
- **deny** is project-only with `--reason`; `--once`/`--global` are allow-only.

Recompile-then-reload reuses the existing compiler: the mutation changes the input
hash, forcing a `policy.json` rewrite (the rename advances mtime), and the enforcer's
1s mtime poll reloads it. `--once` purge is AC-0020's; tied to the new writer by a
hermetic end-to-end test.
