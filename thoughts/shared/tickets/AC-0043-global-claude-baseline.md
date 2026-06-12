# AC-0043: setup scaffolds the global Claude-defaults egress baseline

**Status:** Done
**Estimated Complexity:** Small
**Created:** 2026-06-12
**Updated:** 2026-06-12

## Problem Statement

A freshly set-up cage cannot run Claude Code: `api.anthropic.com` is not in any
allowlist layer, so the enforcer soft-denies the agent's own API traffic with a
403 (`agent_cage_not_allowlisted`), which Claude Code surfaces as
`Failed to connect to api.anthropic.com: ERR_BAD_REQUEST`. The design assumes a
"global allowlist baseline" containing `api.anthropic.com` with
`mode: passthrough` (docs/design.md:254-270; the OAuth-refresh passage at :457
even states the token endpoint "is on the global allowlist baseline") — but
nothing ever materializes that baseline: `setup` installs only the CA and the
skill, `init` writes only commented allow-stubs, and the global
`~/.config/agent-creance.yaml` is optional and never created.

Observed live (2026-06-11) in a real monorepo, immediately after AC-0042 made
the agent launch: the TUI starts, then fails its connectivity probe with the
cage's 403 rendered as ERR_BAD_REQUEST.

## Desired Outcome

After `agent-creance setup` on a fresh machine, the global
`~/.config/agent-creance.yaml` exists and contains the Claude-defaults egress
baseline, so `run` produces a cage in which Claude Code connects to its API out
of the box. An existing global config is never modified.

## User Stories / Use Cases

- As a new user running `setup` then `run`, I want Claude Code to reach
  `api.anthropic.com` out of the box so that the cage is usable without
  reverse-engineering the allowlist from soft-deny logs.
- As an existing user with a hand-maintained global config, I want `setup` to
  leave my file untouched so that my own rules are never overwritten.
- As a security-conscious user, I want the scaffolded baseline to be a visible,
  editable file (not invisible built-in magic) so that I can audit and trim it.

## Acceptance Criteria

- [x] `setup` on a machine without `~/.config/agent-creance.yaml` writes the
  file containing at least `api.anthropic.com` with `mode: passthrough`
  (per docs/design.md:254-270) plus the other hosts Claude Code requires
  (claude.ai, platform.claude.com passthrough; downloads.claude.ai,
  raw.githubusercontent.com intercept; telemetry hosts commented out —
  per the official network-config docs).
- [x] `setup` with an existing global config leaves the file byte-identical
  and says so (or stays appropriately quiet).
- [x] An opt-out flag (`--no-global-config`, consistent with the existing
  `--no-skill` / `--no-ca-install` style) skips the scaffolding.
- [x] The scaffolded file passes the project's own config validation
  (config.Parse runs validation; pinned by TestSetupScaffoldsGlobalConfig).
- [x] `setup` output announces what was written, following the existing
  step-output style ("✓ …").
- [x] Existing tests continue to pass (`make test`, `make lint`).

## Out of Scope

- A built-in implicit baseline in the policy compiler (option 2 from the
  diagnosis — rejected in favor of the visible scaffolded file).
- Migrating/merging the baseline into existing global configs.
- `doctor` checks for baseline presence/staleness.
- Scaffolding project-level (`init`) allow rules.

## Open Questions

None — this is a well-understood quickfix; the host list is a research task.

## Questions for Research/Planning

- [ ] Which hosts does Claude Code require (official Anthropic network/domain
  documentation), and which of them should be `passthrough` vs normal
  intercept allows per the design's mode guidance?
- [ ] Which host serves the OAuth token refresh (design.md:457 says it must be
  on the baseline)?
- [ ] How is `setup` structured (steps, flags, output, tests) — where does a
  new "write global config if absent" step fit, and how are its testscript/
  unit tests shaped?
- [ ] Which config-loader path resolves the global file (`GlobalPath`) so the
  scaffold writes to exactly the path the compiler reads?
- [ ] How do existing tests fake the FS/paths for setup, and does any
  testscript assert setup's exact output (would need updating)?

## References

- Quickfix initiated via `/quickfix` command
- Diagnosis: AC-0042 follow-up session (2026-06-11/12) — enforcer analysis
  confirmed no 400 path exists; the 403 soft-deny is rendered as
  ERR_BAD_REQUEST by Claude Code's axios probe
- `docs/design.md:254-270` — passthrough mode; api.anthropic.com canonical
- `docs/design.md:457` — OAuth refresh assumed on the global baseline
- `internal/cli/init.go:309-328` — init template (commented stubs only)
- `internal/config/load.go:55-61` — optional global config loading

## Implementation Plan

[Leave empty — will be filled when plan is created]

## Notes & Updates

### 2026-06-12 (implementation)
- Implemented as a third `runSetup` step (`scaffoldGlobalConfig` in
  `internal/cli/setup.go`) with the `--no-global-config` opt-out; the
  `--no-skill` early return was restructured so the baseline step always
  runs. `init`'s bootstrap (which reuses `runSetup`) scaffolds it too.
- Baseline content from the official Claude Code network docs
  (code.claude.com/docs/en/network-config, verified 2026-06-12):
  passthrough for token-carrying hosts (api.anthropic.com, claude.ai,
  platform.claude.com), intercept for downloads.claude.ai and
  raw.githubusercontent.com, telemetry hosts commented out;
  storage.googleapis.com (legacy, too broad) and
  bridge.claudeusercontent.com (Chrome-only) excluded.
- End-to-end connectivity (run → Claude Code connects) awaits the user's
  live run; everything else is covered by unit tests.

### 2026-06-12
- Quickfix ticket auto-created from `/quickfix` command
- User chose option 1 (setup scaffolds the visible global file) over a
  compiler-built-in baseline
