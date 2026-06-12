# Implementation Status: AC-0043 — global Claude-defaults egress baseline

## Phase 1: Scaffold step + baseline template
- **Status**: ✅ Complete
- **Started**: 2026-06-12
- **Completed**: 2026-06-12

### Steps Performed
1. `internal/cli/setup.go`: new `--no-global-config` flag; `runSetup` gained
   the parameter; the `--no-skill` early return restructured to an if/else so
   the new third step always runs; `scaffoldGlobalConfig` (GlobalPath →
   three-way Stat switch → MkdirAll + writeFileAtomic, never overwriting) and
   the `globalConfigTemplate` const (passthrough for api.anthropic.com /
   claude.ai / platform.claude.com; intercept for downloads.claude.ai /
   raw.githubusercontent.com; telemetry hosts commented out).
2. `internal/cli/init.go:134`: bootstrap call updated (scaffolds the baseline
   during init onboarding too).
3. `internal/cli/setup_test.go`: existing call sites updated; four new tests —
   fresh scaffold (parse + passthrough-mode assertions, success line),
   exists-untouched (byte-identical), `--no-global-config` skip,
   `--no-skill` still scaffolds.
4. `docs/design.md`: setup command block lists the third step + new flag; the
   OAuth-refresh passage now notes the baseline is materialized by setup.

### Issues Encountered
- None. (`config.Parse` runs validation internally — config.go:173 — so the
  parse round-trip test covers the passthrough constraints as planned.)

### Verification
- ✅ `make test` (full hermetic suite, race)
- ✅ `make lint`
- ✅ `go build ./...` (via vet/test)
- ⚠️ End-to-end (run → Claude Code connects): user's live run pending;
  `bin/agent-creance` rebuilt for it.

### Commit
- `46f1b96` feat(AC-0043): setup scaffolds the global Claude egress baseline
