# AC-0007: Config schema & loader (WP-1.2)

**Status:** Open
**Estimated Complexity:** Medium
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-1.2 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** none
**Spike gate:** none

## Problem Statement

`.agent-creance.yaml` is the single source of truth a user authors. Before anything can be compiled, the tool needs Go types for the full schema, a loader, and structural validation that fails with actionable messages instead of cryptic YAML errors — including the new per-rule `mode` field and its constraints.

## Desired Outcome

An `internal/config` package that parses the full schema into typed structs, validates structure, and rejects invalid configs (notably `mode: passthrough` carrying `paths`/`methods`) with clear, golden-tested error messages.

## User Stories / Use Cases

- As an operator, I want a typo in my config to produce a precise error so that I can fix it without reading source.
- As a developer of the compiler, I want a validated typed config so that downstream code never guesses at shapes.

## Acceptance Criteria

- [ ] Structs cover: `agent` (command, workdir), `safehouse` (add_dirs_rw/ro, enable), `network.host_services`, `network.egress.{generators,allow,deny_always}` (each rule: host, paths, methods, mode, reason), `env`, `include`.
- [ ] The design's example `.agent-creance.yaml` round-trips (parse → struct → no loss of meaningful fields).
- [ ] `mode` defaults to `intercept`; `mode: passthrough` with `paths` or `methods` is a validation error.
- [ ] Invalid configs produce stable, human-readable errors (golden-tested).

## Verification & Test Steps

1. `go build ./...` → compiles.
2. `go test -race ./internal/config/...` → pass. Must include: a fixture equal to the design's example config parses without error; a passthrough-with-paths fixture returns the expected validation error; unknown-top-level-key handling per chosen policy.
3. Golden error messages: `go test ./internal/config -run TestValidate` green; `make golden` produces no unexpected diff (review with `git diff`).
4. `make lint` → clean.
5. Parse the real example: add a testdata fixture copied from `docs/design.md`'s config block; assert it loads.

## Out of Scope

- Include resolution and merge (AC-0008).
- Generator execution, policy compilation (Phase 2).

## Dependencies & Sequencing

Phase 1. Pairs with AC-0008. Foundation for AC-0013.

## Questions for Research/Planning

- [ ] Strict mode (error on unknown keys) vs lenient — which does `yaml.v3` give us and which do we want?
- [ ] Should `host_services` accept bare `name:port` and normalize the address to `127.0.0.1` here or in AC-0014?

## References

- `docs/design.md` — "The configuration", "Per-host enforcement modes".
- Spec WP-1.2.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.
