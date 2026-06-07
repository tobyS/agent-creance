# AC-0029: `init` command (WP-5.4)

**Status:** Done
**Estimated Complexity:** Small
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-5.4 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** AC-0007 (WP-1.2, schema)
**Spike gate:** none

## Problem Statement

Starting a project should be one command that writes a sensible `.agent-creance.yaml` and detects existing manifests so the generators list is pre-populated — rather than making the user hand-author the config.

## Desired Outcome

`agent-creance init` writes a `.agent-creance.yaml` template into the project and pre-populates `generators:` based on detected `package.json`/`composer.json`.

## User Stories / Use Cases

- As an operator starting out, I want `init` to scaffold a working config so that I can `run` quickly.

## Acceptance Criteria

- [x] `init` writes a valid `.agent-creance.yaml` template (parses cleanly via AC-0007).
- [x] If `package.json` exists, `package_json` is added to `generators:`; if `composer.json` exists, `composer_json` is added.
- [x] Existing `.agent-creance.yaml` is not clobbered without explicit consent (refuse or require a flag).

## Verification & Test Steps

1. `go build ./...` → compiles.
2. Hermetic CLI test (`testscript`):
   - empty dir → `init` writes a template; assert the file parses (pipe through a config-load assertion or a follow-up `policy show` that doesn't error).
   - dir with `package.json` only → generated config contains `package_json` and not `composer_json`.
   - dir with both manifests → both generators present.
   - pre-existing config → `init` refuses (or honors the overwrite flag) — assert no silent clobber.
3. `go test -race ./internal/cli/...` → green.
4. `make lint` → clean.

## Out of Scope

- Running generators (Phase 2) — `init` only lists them.

## Dependencies & Sequencing

Phase 5. Depends on AC-0007 for a valid template shape.

## Questions for Research/Planning

- [x] Template contents/comments — mirror the design's example config? Decided: emit a
  minimal, commented, *valid* config (agent + safehouse + the detected generators block)
  with commented allow/deny stubs as inline guidance — NOT the design's project-specific
  teaching example. No-manifest → commented generators placeholder. No .gitignore block.

## References

- `docs/design.md` — "Commands" (init).
- Spec WP-5.4.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.

### 2026-06-07
Implemented (WP-5.4). `internal/cli/init.go` adds `agent-creance init` over the App
seams (mirrors `setup`): `--force` to overwrite, atomic temp+rename write, presence-
based generator detection (`package.json`→`package_json`, `composer.json`→
`composer_json`). Tests: golden render (none/package_only/both), parse+validate of
every variant, `runInit` unit tests against the sysdep fakes, and a hermetic
`init.txtar`. `make test` + `make lint` + `make golden` green.
