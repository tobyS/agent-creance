# AC-0054: `include` command — add config include-list entries

**Status:** Open
**Estimated Complexity:** Small
**Created:** 2026-06-18
**Updated:** 2026-06-18

## Problem Statement

The config supports a top-level `include: []` list (AC-0008) that merges other
config fragments into the effective config, and there are `allow`/`deny` commands
to append egress rules to `network.egress.allow` / `deny_always`. But there is no
command to add an `include:` entry — users must hand-edit the YAML to compose a
config from shared fragments or a baseline. That is the one config-editing
operation with no CLI affordance, despite being how layered configs are meant to
be built.

## Desired Outcome

A command (e.g. `agent-creance include PATH`) appends an entry to the config's
`include:` list, preserving the file's existing comments and formatting (as
`config.AppendRule` does for rules), and recompiles the policy so a running cage
picks it up — mirroring `allow`/`deny`. The operation is idempotent (an entry
already present is a no-op with a message), and an include that cannot be
resolved or parsed produces a clear error rather than a silently broken config.

## User Stories / Use Cases

- As a developer composing a project config from a shared baseline fragment, I
  want to add an include with one command instead of hand-editing YAML, so the
  workflow matches `allow`/`deny`.
- As a developer, I want the new include to take effect immediately (policy
  recompiled), so a running cage reflects it without a manual rebuild.
- As a developer who points an include at a wrong or missing path, I want a clear
  error naming the path, so I'm not left with a config that fails to compile later
  with a confusing message.

## Acceptance Criteria

- [ ] `agent-creance include PATH` appends `PATH` to the project config's
      `include:` list, preserving existing comments and formatting, and recompiles
      the policy.
- [ ] Re-adding an entry already present is a no-op with an "already included"
      message (idempotent), matching the allow/deny behavior.
- [ ] On success, a confirmation line reports the change and that the policy
      recompiled.
- [ ] If the added include cannot be resolved or parsed, the command surfaces a
      clear error naming the path; the user is left able to recover (not silently
      broken).
- [ ] Target selection parallels `allow`/`deny` at least for the project config
      (whether `--global` / `--once` also apply is resolved in planning).
- [ ] Unit-tested via the `sysdep` fakes (no real filesystem or external tools);
      CLI behavior covered by a hermetic testscript.
- [ ] `make test`, `make lint` pass; `make build` at the end.

## Out of Scope

- Commands to remove or list include entries — add-only for now (possible
  follow-up).
- Hot-reloading hand-edits to the config (separate ticket, AC-0053). This command
  recompiles explicitly on its own, like allow/deny.
- Remote/URL includes if the loader does not support them — defer to whatever
  AC-0008's include resolution actually accepts.

## Open Questions

None blocking.

## Questions for Research/Planning

- [ ] What forms may an `include:` entry take — relative path, absolute path, a
      named/shipped baseline, a URL? (whatever the loader resolves) — this
      determines validation.
- [ ] Which targets to support: project config only, or also `--global` / `--once`
      like allow/deny? Does an include in the ephemeral session overlay make sense?
- [ ] Validate-before-write (pre-check the path resolves) vs. the
      write-then-recompile-and-report pattern allow/deny use; whether to roll back
      the write on a failed recompile.
- [ ] Is there a `config.AppendRule` analogue for the include list, or does a
      sibling (`AppendInclude`) need to be added that preserves comments?
- [ ] Dedup/normalization of include paths (trailing slash, `./` prefix,
      equivalent relative paths) so idempotency is reliable.

## References

- `internal/config/config.go` — `Include []string` (the list this appends to).
- `internal/cli/mutate.go` — `config.AppendRule`, `recompile()`, `mutationTarget`
  (the comment-preserving append + recompile machinery to mirror).
- `internal/cli/allow.go`, `internal/cli/deny.go` — the thin-wrapper command
  pattern to follow.
- Related: AC-0008 (include merge), AC-0030 (allow/deny commands).

## Implementation Plan

[Leave empty — filled when the plan is created.]

## Notes & Updates

### 2026-06-18

- Created alongside AC-0053 from one request, split out as the thin CLI half: a
  comment-preserving append to the `include:` list plus a recompile, mirroring
  allow/deny.
- Complexity Small: the append + recompile + idempotency machinery already exists
  for rules; this reuses it for the include list. The only real unknowns are
  include-path validation and which targets to support.
