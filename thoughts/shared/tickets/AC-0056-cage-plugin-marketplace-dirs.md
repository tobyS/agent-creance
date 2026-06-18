# AC-0056: Grant local Claude plugin marketplace directories into the cage (read-only)

**Status:** Open
**Estimated Complexity:** Medium
**Created:** 2026-06-18
**Updated:** 2026-06-18

## Problem Statement

Claude Code running inside the agent-creance cage fails to load a locally-sourced
plugin marketplace. Observed warning:

```
⚠ Warning: Failed to load marketplace 'toby-plugins': Failed to load marketplace
"toby-plugins" from source (directory): Failed to parse marketplace file at
/Users/toby/code/work/toby-plugins/.claude-plugin/marketplace.json: EPERM:
operation not permitted, open '/Users/toby/code/work/toby-plugins/.claude-plugin/marketplace.json'.
Showing available plugins.
```

`EPERM` (not `EACCES`) is the Seatbelt sandbox-denial signature: the marketplace
is registered as a **local directory source** at `~/code/work/toby-plugins`, which
is outside the two filesystem regions the cage mounts (the caged project directory
and `~/.claude`). The cage therefore denies the read, and the marketplace — plus
every plugin it provides — fails to load inside the cage. A caged agent silently
loses its locally-developed plugins.

This is the filesystem analogue of the config import already shipped in AC-0051:
`internal/claudeimport` reads the user's Claude config to import MCP servers and
WebFetch domains into the egress allowlist, but nothing grants the cage read
access to local plugin/marketplace **source directories** the same config points at.

## Desired Outcome

agent-creance auto-detects local/directory-source plugin marketplaces (and the
plugin source directories they resolve to) from the Claude config and grants them
into the cage **read-only**, so a caged Claude Code loads those plugins with no
manual configuration — mirroring how `claudeimport` already pulls MCP servers and
domains. Git/remote marketplaces, which install under `~/.claude/plugins` (already
mounted), need no extra grant. When a referenced directory can't be resolved, the
user gets a clear, actionable warning rather than a silent loss or a hard failure.

## User Stories / Use Cases

- As a developer who maintains a local plugin marketplace and runs Claude Code in
  the cage, I want my plugins to load without hand-editing safehouse mounts, so
  the cage doesn't silently strip my tooling.
- As a security-conscious operator, I want the cage to grant only the specific
  plugin directories my Claude config actually references, read-only, so the
  filesystem widening is minimal and predictable.
- As a developer hitting the EPERM warning, I want agent-creance to tell me which
  directory it couldn't grant and what to do, so a misconfigured marketplace is
  diagnosable instead of a cryptic sandbox error.

## Acceptance Criteria

- [ ] agent-creance detects local **directory-source** plugin marketplaces from
      the Claude config and grants their source directories into the cage
      read-only, so the marketplace's `marketplace.json` and the plugins it
      provides load inside the cage (the EPERM warning no longer occurs for a
      validly-configured local marketplace).
- [ ] Git/remote marketplace sources (installed under `~/.claude/plugins`, already
      mounted) are not granted again — only paths outside the cage are added.
- [ ] A detected directory that is already inside the cage (under the project or
      `~/.claude`) is not duplicated as a mount.
- [ ] A referenced marketplace/plugin directory that is missing or cannot be
      resolved produces a clear warning naming the path and the fix, not a silent
      drop or a hard error that aborts the launch.
- [ ] Granted directories are mounted read-only (the caged agent cannot modify the
      plugin source).
- [ ] Detection and mount-list construction are unit-tested against the `sysdep`
      fakes (no real filesystem or external tools); behavior covered consistent
      with the `claudeimport` tests.
- [ ] `make test`, `make lint` pass; `make build` at the end.

## Out of Scope

- Read-write / plugin-development mounting of these directories — chosen read-only.
- Importing plugin-provided MCP servers or egress domains — that is the
  `claudeimport` egress path and a separate concern.
- Agents other than Claude Code, and any change to Claude Code's own error wording.
- Marketplaces sourced from git/remote URLs (already handled via the `~/.claude`
  mount).

## Open Questions

None blocking — root cause (cage Seatbelt denial of an out-of-cage local
marketplace dir), approach (auto-detect and grant), and access (read-only) were
all settled during authoring.

## Questions for Research/Planning

- [ ] Where and how Claude Code records local marketplace/plugin registrations
      (the file path and JSON shape to read), and how a "directory" source is
      distinguished from a git/remote source.
- [ ] Whether a local-directory marketplace's installed plugins live in the source
      directory itself (dev mode) or are copied elsewhere — i.e. is granting the
      marketplace source dir sufficient, or are additional plugin dirs involved?
- [ ] Integration point: write the detected dirs as **visible
      `safehouse.add_dirs_ro` entries at `import`/`init` time** (consistent with
      AC-0051, recommended for reviewability) vs. computing them dynamically at
      each cage launch. Trade-off is visibility/auditability vs. always-current.
- [ ] Path normalization/dedup against the already-mounted project dir and
      `~/.claude` so a path is never granted twice.
- [ ] Security note to validate in planning: auto-granting read to directories
      named in the Claude config widens the cage's filesystem surface; confirm the
      grant stays read-only and limited to detected marketplace/plugin sources
      (the config is the user's own, so the trust boundary is acceptable, but it
      should be explicit).

## References

- Screenshot (2026-06-18): EPERM loading
  `/Users/toby/code/work/toby-plugins/.claude-plugin/marketplace.json` in the cage.
- `internal/claudeimport/claudeimport.go` — the Claude-config reader to mirror
  (imports MCP servers + WebFetch domains; AC-0051).
- `internal/cli/import.go` (≈ line 147) — where an imported fragment's
  `Safehouse.AddDirsRW/RO` are assembled.
- `internal/config/config.go` — `Safehouse.AddDirsRO` (the read-only mount list).
- Related: AC-0051 (init/import from Claude config), AC-0045 (in-cage `~/.claude`
  read-write), AC-0035 (in-cage Claude config dir writable), AC-0023
  (safehouse invocation / mounts).

## Implementation Plan

[Leave empty — filled when the plan is created.]

## Notes & Updates

### 2026-06-18

- Created from a screenshot of caged Claude Code EPERM-failing to load the local
  `toby-plugins` marketplace.
- Decisions: (a) root cause confirmed as the cage's Seatbelt filesystem isolation
  denying an out-of-cage local-directory marketplace; (b) **auto-detect and grant**
  the local marketplace/plugin source dirs (not manual config); (c) grant them
  **read-only**.
- Complexity Medium: the detection mirrors `claudeimport` and the mount wiring
  reuses `safehouse.add_dirs_ro`, but it needs a new reader for Claude's plugin
  registration format, dedup against existing mounts, and a clear failure path.
