# AC-0068d: CLI — credential management and --inject binding

**Status:** In Progress
**Estimated Complexity:** Medium
**Created:** 2026-06-29
**Updated:** 2026-07-05

> Sub-ticket of **AC-0068** (Credential injection, Phase 1). **Depends on AC-0068b**
> (config model), and on AC-0068a for `add --source`. Read the epic and research
> doc for context.

## Problem Statement

The config model from AC-0068b needs an authoring surface. Users must be able to
register a credential (name → source reference + shape) and bind it to a host/path
without hand-editing config, reusing the existing recompile + hot-reload path so
changes take effect on running cages.

## Desired Outcome

The config-mutation command family (today `allow`/`mutate`/`edit` →
`AppendRule`) gains:

- A **`credential` group**: `credential add --source op://… [--header … | --bearer]`,
  `credential list`, `credential rm <name>`.
- A **binding** on `allow`: `allow <host/path> --inject <name>` (and the means to
  mark a host `in-cage`).
- Reuse of the existing **recompile + hot-reload** path (AC-0053) so edits apply
  without restarting the cage.
- **Long/Example `--help`** on every new command, matching the AC-0064
  help-as-doc-surface style.

## User Stories / Use Cases

- As a user setting up a project, I want `creance credential add --source
  op://Private/GitHub/token --bearer` then `creance allow api.github.com/graphql
  --inject github` so that the agent can use `gh` over GraphQL without me editing
  config files.
- As a user, I want `credential list` to show configured credentials by name/source/
  shape (never the value) so that I can audit what the cage injects.

## Acceptance Criteria

- [ ] `credential add` / `list` / `rm` create, show, and remove entries in the
      `credentials:` block (AC-0068b); `list` never prints a resolved secret value.
- [ ] `allow <host/path> --inject <name>` writes an inject binding; binding to an
      undefined credential is rejected with a clear error.
- [ ] A way to mark a host `in-cage` exists and is documented.
- [ ] Mutations go through the existing recompile + hot-reload path; a running cage
      picks up the change (covered by a testscript `.txtar`).
- [ ] Every new command has Long + Example help (AC-0064 style).
- [ ] `make test` green; CLI behavior covered by `.txtar` scripts with stubbed
      tools (no real `op`/`security`/network).

## Out of Scope

- The injection mechanism itself (AC-0068c).
- Opening `/graphql` / GitHub validation (AC-0068e).
- Minted-token or broker config (Phase 2, AC-0069).

## Open Questions

None blocking.

## Questions for Research/Planning

- [ ] Whether `credential add` validates the source by attempting a resolve at
      add-time (fail-fast) or defers to spawn (the discussion favors resolve at
      spawn; add-time validation could be opt-in).
- [ ] Flag surface for shape selection (`--bearer` vs `--header NAME` vs
      `--token`/bare/`--basic`) and the username sentinel for Basic.

## References

- Epic: AC-0068. Research: `thoughts/shared/discussions/2026-06-28-credential-injection.md`
- Code: `internal/cli/allow.go`, `mutate.go`, `edit.go` (config mutation),
  `internal/config/config.go`.
- Related: AC-0053 (config hot-reload), AC-0064 (help-as-doc-surface), AC-0067
  (config editing commands/TUI), AC-0066 (CLI ergonomics bundle).

## Implementation Plan

(Filled when planned.)

## Notes & Updates

### 2026-06-29
Created as the CLI sub-ticket of AC-0068. Can proceed in parallel with AC-0068c once
AC-0068b lands.
