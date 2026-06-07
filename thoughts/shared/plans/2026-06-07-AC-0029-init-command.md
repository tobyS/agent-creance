---
date: 2026-06-07
ticket: AC-0029
title: "Implementation Plan: `init` command (WP-5.4)"
status: ready
branch: main
research: thoughts/shared/research/2026-06-07-AC-0029-init-command.md
---

# Implementation Plan: `init` command (WP-5.4)

## Overview

Add `agent-creance init`: a small CLI command that writes a `.agent-creance.yaml`
template into the project and pre-populates the `network.egress.generators:` list
from detected `package.json` / `composer.json`. It refuses to overwrite an
existing config unless `--force` is given. No new external dependencies, no new
`sysdep` seam — it reuses `app.FS` (`sysdep.FileSystem`) and `app.Paths`.

This mirrors the `setup` command (AC-0028) structure exactly: a thin
`newInitCmd(app)` cobra command delegating to a testable `runInit(ctx, app, dir, force)`.

## Decisions (from the question checkpoint)

1. **Template content:** minimal + commented stubs — `agent` (command/workdir),
   `safehouse.add_dirs_rw: [.]`, the detected `generators:`, commented-out
   `allow:`/`deny_always:` stubs, and a header pointing at `docs/design.md` and
   the `allow`/`deny` commands.
2. **No-manifest `generators:`:** emit a commented placeholder listing both
   available names (`package_json`, `composer_json`) so the user can uncomment.
3. **Overwrite:** refuse by default with an actionable exit-1 message; `--force`
   overwrites. Flag spelled `--force`.

## Current State

- `internal/cli/` holds the cobra tree (`cli.go`), with commands `version`,
  `doctor`, `policy`, `logs`, `run`, `setup`. Each takes `*App`.
- `const configFile = ".agent-creance.yaml"` already exists at `run.go:23`.
- Filesystem access goes through `app.FS` (`sysdep.FileSystem`): `Stat` (absent →
  `fs.ErrNotExist`), `WriteFile`, `Rename`, `Remove`.
- Generator name constants live in `internal/generator/manifest.go:11-14`
  (`GeneratorPackageJSON`, `GeneratorComposerJSON`).
- Config schema (`internal/config`) parses + validates a `.agent-creance.yaml`;
  an empty document is valid; `config.NewLoader(fs, paths).Load(path)` loads a
  whole file through the FS seam.
- No `init` command, no config template exists yet.

## Desired End State

- `agent-creance init` in an empty dir writes a valid, commented
  `.agent-creance.yaml` with a commented generators placeholder.
- With `package.json` present → `generators:` contains `package_json`; with
  `composer.json` present → `composer_json`; with both → both (sorted, stable).
- Running `init` when the config already exists prints
  `error: .agent-creance.yaml already exists (use --force to overwrite)` and
  exits 1, leaving the file untouched. `--force` overwrites.
- The written template parses + validates via `config.Parse` / `Loader.Load`.
- `make test` (race), `make lint`, `go build ./...` all green.

## What We're NOT Doing

- Not running generators / hitting registries (out of scope — `init` only lists).
- Not writing a `.gitignore` block (design.md:422 — nothing to gitignore).
- Not creating a new `internal/` sub-package — the command is small enough to
  live in `internal/cli/init.go`.
- Not adding a new `sysdep` interface.
- Not touching the global `~/.config/agent-creance.yaml`.

---

## Phase 1 — Command + template rendering, wired into the tree

### Changes

**New file `internal/cli/init.go`:**

1. `newInitCmd(app *App) *cobra.Command`
   - `Use: "init"`, `Short:` one-liner, `Args: cobra.NoArgs`.
   - `var force bool`; `cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing .agent-creance.yaml")`.
   - `RunE: func(cmd, _) error { return runInit(cmd.Context(), app, ".", force) }`.

2. `runInit(ctx context.Context, app *App, dir string, force bool) error`
   - `dest := filepath.Join(dir, configFile)` (reuse the `run.go` const).
   - Refuse-if-exists unless `--force`: `app.FS.Stat(dest)`; if `err == nil` and
     `!force`, return an error
     `fmt.Errorf(".agent-creance.yaml already exists (use --force to overwrite)")`.
     If `errors.Is(err, fs.ErrNotExist)` → proceed. Any other Stat error → wrap+return.
   - Detect manifests: `gens := detectGenerators(app.FS, dir)` — `Stat` of
     `filepath.Join(dir, "package.json")` and `composer.json`; append the
     matching `generator.GeneratorPackageJSON` / `GeneratorComposerJSON` constant
     (package first, composer second — deterministic order).
   - Render: `content := renderConfigTemplate(gens)`.
   - Write atomically via temp + rename (the `internal/setup/skill.go:52-73`
     idiom): `WriteFile(dest+".tmp", []byte(content), 0o644)` → `Rename(tmp, dest)`;
     best-effort `Remove(tmp)` on rename failure. (No read-and-compare; this is a
     fresh create / forced overwrite, not an idempotent rewrite.)
   - Success output (✓-style, matching `setup`):
     - `✓ Wrote .agent-creance.yaml` + a generators note: if `gens` non-empty,
       `(generators: package_json, composer_json)`; else
       `(no manifests detected — generators left commented)`.
     - A next-step hint line: `Next: run \`agent-creance setup\`, then \`agent-creance run\`.`
   - Return `nil`.

3. `detectGenerators(fsys sysdep.FileSystem, dir string) []string` — pure-ish
   helper; returns the ordered list. (Kept separate so it's unit-testable.)

4. `renderConfigTemplate(generators []string) string` — builds the YAML string.
   Implemented as a base template constant with one `%s` substitution for the
   generators block, plus a `generatorsBlock(generators []string) string` helper:
   - Non-empty: `    generators:\n      - package_json\n      - composer_json\n`.
   - Empty: commented placeholder
     `    # generators:\n    #   - package_json\n    #   - composer_json\n`.

**Template content** (the base constant — indentation is significant YAML):

```yaml
# .agent-creance.yaml — agent-creance project config.
# Full schema and guidance: docs/design.md. Manage egress rules interactively
# with `agent-creance allow` / `agent-creance deny`.
agent:
  command: ["claude", "--dangerously-skip-permissions"]
  workdir: .

safehouse:
  add_dirs_rw: [.]

network:
  egress:
<GENERATORS BLOCK>
    # allow:
    #   - host: api.github.com
    #     paths: ["/repos/you/your-project/"]
    #     methods: [GET, POST]
    # deny_always:
    #   - host: w3schools.com
    #     reason: "Known low-quality source. Use MDN or official docs instead."
```

**Edit `internal/cli/cli.go`:** add `root.AddCommand(newInitCmd(app))` in
`newRootCmd` (alongside the other `AddCommand` calls, ~`cli.go:72-77`).

### Validity guard (important)

The minimal/no-manifest template leaves `network.egress` with only comments
(null egress) — confirm this parses AND validates via `config.Parse`. If a null
`egress` (or a `network` with no rules) fails validation, adjust the template to
keep `egress:` a non-null mapping (e.g. always emit a real `generators:` key, or
an empty `allow: []`). This is verified by the Phase 2 parse test; resolve here
before moving on.

### Success Criteria

#### Automated
- [x] `go build ./...` compiles.
- [x] `make test` (= `go test -race ./...`) green.
- [x] `make lint` (`go vet` + `golangci-lint`) clean.

#### Manual
- [x] `make run ARGS="init"` in an empty temp dir writes a sensible commented
      template; re-running without `--force` refuses; `--force` overwrites.

---

## Phase 2 — Tests

Follows the project's three test layers (CLAUDE.md / profile).

### Changes

**1. Golden render tests — `internal/cli/init_test.go` (+ `testdata/`)**

The rendered template is a generated artifact → golden-file test with `-update`.
Table over three cases, asserting `renderConfigTemplate` output against goldens:
- `none` → `internal/cli/testdata/init/none.golden`
- `package_only` → `.../package_only.golden`
- `both` → `.../both.golden`

Generate with `make golden` and review the diff.

**2. Parse assertion (in `init_test.go`)**

For each of the three rendered templates, assert it parses + validates:
`_, err := config.Parse([]byte(renderConfigTemplate(gens)))` → `require.NoError`.
This is the reliable "template is valid" check (acceptance criterion 1) and
guards the null-egress concern from Phase 1.

**3. `runInit` unit test (in `init_test.go`, model: `setup_test.go` / `run_test.go`)**

Drive `runInit(ctx, app, dir, force)` against `sysdeptest.FakeFileSystem` +
`FakePathResolver` with redirected `app.Stdout`:
- empty dir → file written; assert it `config.Parse`s; generators commented.
- `package.json` only preloaded → written config contains `package_json`, not
  `composer_json`.
- both manifests → both generators present.
- pre-existing `.agent-creance.yaml` + `force=false` → returns error, original
  bytes unchanged (assert no clobber).
- pre-existing + `force=true` → overwritten with the new template.
- assert success stdout mentions the generators / next-step hint.

**4. Hermetic testscript — `internal/cli/testdata/script/init.txtar`**
(model: `setup_help.txtar`)

- `env PATH=$CREANCE_BIN`.
- empty dir: `agent-creance init`; assert `exists .agent-creance.yaml` and
  `grep` for the commented generators placeholder.
- with a `package.json` (write it inline in the `.txtar`): fresh dir, `init`,
  `grep 'package_json'` in the file, assert no `composer_json`.
- both manifests: `grep` both.
- re-run without `--force`: `! agent-creance init`; `stderr 'already exists'`;
  assert the file is unchanged.
- `agent-creance init --force`: succeeds (`stdout '✓'`).
- extra positional arg rejected: `! agent-creance init bogus`; `stderr 'unknown command'`.
- optionally also add `init --help` assertions advertising `--force`.

### Success Criteria

#### Automated
- [ ] `make test` green (new `init.txtar` + `init_test.go` pass under race).
- [ ] `make golden` produces the three goldens; committed and reviewed.
- [ ] `make lint` clean; `go build ./...` compiles.

#### Manual
- [ ] Golden diff reviewed — the three templates read well and are valid YAML.

---

## Testing Strategy

- **Pure render logic** → golden-file tests (generated artifact convention) +
  inline parse assertions.
- **Command behavior with the FS seam** → `runInit` unit test against sysdeptest
  fakes (the only hermetic path, since `cli.Main` wires real OS seams).
- **End-to-end CLI** → hermetic `.txtar` with a minimal `$CREANCE_BIN` PATH.
- External tools are never invoked (none are needed — `init` is pure FS).

## Performance Considerations

None — three `Stat`s and one file write.

## References

- Research: `thoughts/shared/research/2026-06-07-AC-0029-init-command.md`
- Ticket: `thoughts/shared/tickets/AC-0029-init-command.md`
- Pattern: `internal/cli/setup.go`, `internal/cli/cli.go:61-79`
- Write idiom: `internal/setup/skill.go:52-73`
- Schema/parse: `internal/config/config.go:128-157`, `internal/config/load.go:39-48`
- Generator constants: `internal/generator/manifest.go:11-14`
- Filename const: `internal/cli/run.go:23`
- Design: `docs/design.md:359-360` (init), `:422` (no gitignore), `:72-152` (schema)
