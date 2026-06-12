# AC-0043: setup scaffolds the global Claude-defaults egress baseline

## Overview

A fresh cage soft-denies `api.anthropic.com` because the design's assumed
"global allowlist baseline" is never materialized. `setup` gains a third step
that writes `~/.config/agent-creance.yaml` with the Claude Code egress
baseline when (and only when) the file does not exist.

## Current State Analysis

- `runSetup` (`internal/cli/setup.go:39-82`): two steps (CA trust, skill
  install), flags `--no-ca-install`/`--no-skill` as parameters, `✓ …` output to
  `app.Stdout`, abort on first error. The `--no-skill` branch early-returns
  (`setup.go:73-76`) — must be restructured so a third step still runs.
- `init` reuses `runSetup(ctx, app, false, false)` as its onboarding gate
  (`internal/cli/init.go:134`) — the new step will run there too (desired).
- No command writes the global config; readers treat it as optional
  (`internal/config/load.go:57`, compiler `compile.go:220`).
- Full host list, modes, and idioms pinned in the research:
  `thoughts/shared/research/2026-06-12-AC-0043-global-claude-baseline.md`.

## Desired End State

`agent-creance setup` on a machine without `~/.config/agent-creance.yaml`
writes the baseline below, prints `✓ Wrote …`; with an existing file it prints
"already exists — left untouched" and changes nothing; `--no-global-config`
skips the step. A subsequent `run` lets Claude Code reach its API.

### Key Discoveries (from research)

- Path must come from `config.NewLoader(app.FS, app.Paths).GlobalPath()`
  (`load.go:77-83`) — same resolution the compiler uses.
- Write-if-absent idiom: three-way Stat switch (`init.go:63-73`) +
  `MkdirAll(dir, 0o755)` (`mutate.go:103`) + `writeFileAtomic(...,
  configFilePerm 0o644)` (`init.go:265-275`). Never the self-healing
  content-compare idiom — the file is user-editable.
- Template precedent: `const` string in the cli layer (`configTemplate`,
  `init.go:309-328`), pinned by a `config.Parse` round-trip test
  (`init_test.go:71-79`).
- Validation: passthrough rules must omit `paths`/`methods`
  (`validate.go:35-40`); strict YAML decoding (unknown keys error).
- Setup unit tests assert via `strings.Contains`; new output must avoid the
  substrings "authorization dialog" and "login keychain" (negative assertions
  at `setup_test.go:96-98,176-178,228-230`). `setup_help.txtar` asserts only
  existing flags' presence — a new flag is additive-safe.
- Official Claude Code domain list (code.claude.com/docs/en/network-config,
  verified 2026-06-12) → baseline content decided in research: passthrough for
  `api.anthropic.com`/`claude.ai`/`platform.claude.com` (API + OAuth traffic
  carries tokens; design.md:254-270), intercept for `downloads.claude.ai` and
  `raw.githubusercontent.com`, telemetry hosts commented out,
  `storage.googleapis.com` (too broad, legacy) and
  `bridge.claudeusercontent.com` (Chrome-only) excluded.

## What We're NOT Doing

- No compiler-built-in implicit baseline (rejected option 2).
- No merging into existing global configs; no `--force` overwrite.
- No `setupcheck`/`doctor` involvement — the file stays optional for `run`.
- No project-level (`init`) allow-rule scaffolding.

## Implementation Approach

One phase, all in the cli layer (pure filesystem work over `app.FS`, like
`init`'s scaffold — no new sysdep seam, no `internal/setup` change).

## Phase 1: Scaffold step + baseline template

### Changes Required:

#### 1. `internal/cli/setup.go`

- New flag `--no-global-config` ("skip writing the global config baseline"),
  threaded as a new `runSetup` parameter.
- Restructure the skill step to `if noSkill { print skip } else { install +
  ✓ }` (no early return), then append step 3:

```go
// 3. Global config baseline: scaffold ~/.config/agent-creance.yaml so a
//    fresh cage lets Claude Code reach its own API (AC-0043). Never touches
//    an existing file — it is the user's to edit; `allow --global` appends
//    to it.
if noGlobalConfig {
    fmt.Fprintln(app.Stdout, "Skipping global config baseline (--no-global-config).")
    return nil
}
return scaffoldGlobalConfig(app)
```

- `scaffoldGlobalConfig(app *App) error`: `GlobalPath()` → three-way Stat
  switch (exists → `"Global config %s already exists — left untouched."`,
  nil; not-exist → `MkdirAll` + `writeFileAtomic` + `"✓ Wrote %s (Claude Code
  egress baseline)."`; other → wrapped error).
- `const globalConfigTemplate` (exact content):

```yaml
# Global agent-creance configuration. Merged beneath every project's
# .agent-creance.yaml; rules here apply to all cages on this machine.
# Scaffolded by `agent-creance setup` — edit freely, setup never overwrites
# an existing file.
network:
  egress:
    allow:
      # Claude Code essentials (https://code.claude.com/docs/en/network-config).
      # API and OAuth traffic carries tokens, so it is tunneled raw
      # (passthrough) — the proxy never sees those bytes.
      - host: api.anthropic.com
        mode: passthrough
      - host: claude.ai
        mode: passthrough
      - host: platform.claude.com
        mode: passthrough
      # Plugin and native-updater downloads.
      - host: downloads.claude.ai
      # Release-notes feed and plugin marketplace metadata.
      - host: raw.githubusercontent.com
      # Optional telemetry — Claude Code works without it (requests are
      # soft-denied and only metrics/error reporting degrade). Uncomment
      # to allow:
      # - host: statsig.anthropic.com
      # - host: statsig.com
      # - host: sentry.io
```

#### 2. `internal/cli/init.go:134`

Update the bootstrap call: `runSetup(ctx, app, false, false, false)` — init's
onboarding scaffolds the baseline too.

#### 3. Tests (`internal/cli/setup_test.go`)

- `TestSetupScaffoldsGlobalConfig`: fresh fixture → file present in fake FS at
  `<HomeDir>/.config/agent-creance.yaml`; content passes `config.Parse` **and
  the config validation** (whichever exported function enforces
  `validate.go` — confirm at implementation; if `Parse` doesn't validate,
  call the validation entry point the loader uses); a parsed rule for
  `api.anthropic.com` has `Mode == config.ModePassthrough`; stdout contains
  `✓ Wrote`.
- `TestSetupGlobalConfigExistsUntouched`: pre-seed custom bytes → byte-identical
  after run; stdout contains "left untouched".
- `TestSetupNoGlobalConfig`: flag set → file absent; stdout mentions the skip.
- `TestSetupNoSkillStillScaffoldsGlobalConfig`: `--no-skill` → baseline file
  still written (pins the restructured branch).
- Existing `runSetup` call sites in tests gain the new argument.

#### 4. `docs/design.md`

- Setup command block (~:377): mention the third step ("scaffolds the global
  Claude-defaults egress baseline if absent").
- The global-baseline mention (~:155) and the passage assuming the token
  endpoint is "on the global allowlist baseline" (~:457): note the baseline is
  materialized by `setup` since AC-0043.

#### 5. Ticket close + build

Tick acceptance criteria (live-run criterion stays with the user), status
Done, `make build` (end-of-ticket convention).

### Success Criteria:

#### Automated Verification:

- [ ] `make test` green (incl. the four new setup tests and init bootstrap
  tests with the changed signature)
- [ ] `go build ./...`
- [ ] `make lint` clean

#### Manual Verification:

- [ ] On the user's machine: `agent-creance setup` (or manually placing the
  baseline) followed by `run` lets Claude Code connect — no
  `ERR_BAD_REQUEST`; `agent-creance logs --summary` shows passthrough allows
  for `api.anthropic.com`.
- [ ] User's existing `~/.config/agent-creance.yaml` (the workaround file from
  2026-06-11, if created) is left untouched by re-running setup.

## Testing Strategy

Unit tests over the existing setup fixture (fake FS/paths/keychain) cover
fresh-write, exists-untouched, opt-out flag, and `--no-skill` interaction; the
template's validity is pinned by parse+validate round-trip (mirroring
`TestRenderConfigTemplateParses`). No testscript changes needed
(`setup_help.txtar` is flag-presence only; real steps aren't runnable in
txtar). End-to-end connectivity is the user's manual criterion.

## Performance Considerations

None — one Stat and at most one small file write at setup time.

## Migration Notes

Existing installs: next `setup` run creates the file only if absent; users who
already hand-wrote a global config (like the 2026-06-11 workaround) are
untouched. The appearing global file changes the policy input hash → first
`run` afterwards recompiles (expected).

## References

- Ticket: `thoughts/shared/tickets/AC-0043-global-claude-baseline.md`
- Research: `thoughts/shared/research/2026-06-12-AC-0043-global-claude-baseline.md`
- Template/test precedent: `internal/cli/init.go:309-328`, `init_test.go:50-79`
- Write idiom: `init.go:63-73,265-275`, `mutate.go:71,103-108`
