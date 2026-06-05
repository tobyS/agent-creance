# AC-0007: Config schema & loader (WP-1.2)

**Status:** Done
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

- [x] Structs cover: `agent` (command, workdir), `safehouse` (add_dirs_rw/ro, enable), `network.host_services`, `network.egress.{generators,allow,deny_always}` (each rule: host, paths, methods, mode, reason), `env`, `include`.
- [x] The design's example `.agent-creance.yaml` round-trips (parse → struct → no loss of meaningful fields). *(Strict decoding is itself the no-silent-loss guarantee; `testdata/example.yaml` parses with no error + field assertions.)*
- [x] `mode` defaults to `intercept`; `mode: passthrough` with `paths` or `methods` is a validation error.
- [x] Invalid configs produce stable, human-readable errors (golden-tested in `internal/config/testdata/*.golden`).

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

- [x] Strict mode (error on unknown keys) vs lenient — which does `yaml.v3` give us and which do we want?
  **Resolved: strict (fail-closed).** Plain `yaml.Unmarshal` is lenient (silently drops unknown keys); strict requires `yaml.Decoder.KnownFields(true)`. We chose strict — the user story ("a typo produces a precise error") and the security posture (a silently-ignored `deny_always` typo would be a hole) both demand it. Cost accepted: a config using a field a newer agent-creance adds will error on an older binary. Implementation note: `KnownFields` is defeated by any custom `UnmarshalYAML` (go-yaml #642), so we decode via a plain-struct mirror and apply defaults/parse `host_services` in a separate pass.
- [x] Should `host_services` accept bare `name:port` and normalize the address to `127.0.0.1` here or in AC-0014?
  **Resolved: parse + validate here; address-forcing in AC-0014.** `host_services` is parsed into typed `[]HostService{Label, Port}` with port-range validation (1–65535) in this package (the "typed config so downstream never guesses shapes" story). The `127.0.0.1` address-forcing stays in the Seatbelt compiler (AC-0014), since that is where the exact socket address matters.

## References

- `docs/design.md` — "The configuration", "Per-host enforcement modes".
- Spec WP-1.2.

## Implementation Plan

Research: `thoughts/shared/research/2026-06-05-AC-0007-config-schema-loader.md`.
Plan: `thoughts/shared/plans/2026-06-05-AC-0007-config-schema-loader.md`.

`internal/config` — pure parse + validate from bytes (no filesystem; include
resolution is AC-0008):

- `config.go` — typed schema structs + `Parse([]byte) (*Config, error)`: strict
  `KnownFields` decode via a plain-struct mirror (`rawConfig`), then `applyDefaults`
  (rule `mode` → `intercept`; `host_services` `label:port` → typed `{Label, Port}`),
  then `validate`. Rule `Paths`/`Methods` are `*[]string` to distinguish omitted from
  empty.
- `validate.go` — `validate()` (passthrough⊕paths/methods, unknown mode, missing
  host; uniform across allow + deny_always) and `parseHostService`.
- `errors.go` — `ValidationError` (accumulates issues; stable, type-name-free render)
  and `reformat()` for yaml decode errors.
- Tests: `config_test.go` (round-trip/empty/defaults), `validate_test.go`
  (`TestValidate` table + `TestGoldenErrors` + accumulation), `testdata/*.yaml` +
  `*.golden`.

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.

### 2026-06-05
Implemented `internal/config` (research → plan → 2 implementation phases). Both open
questions resolved (strict fail-closed parsing; typed `host_services` validated here,
address-forcing deferred to AC-0014). All acceptance criteria met; `go build`,
`go test -race ./...`, `make lint` green. **Note:** the repo-wide `make golden`
target (`go test ./... -update`) has a pre-existing cross-package `-update` flag
defect (packages without a golden test — `cli`/`state`/`sysdep` — reject the flag);
this package's goldens were regenerated with the scoped `go test ./internal/config
-update`. Flagged for separate follow-up.
