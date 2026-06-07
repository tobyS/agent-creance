---
date: 2026-06-07
ticket: AC-0029
title: "Research: `init` command (WP-5.4)"
status: complete
branch: main
commit: 0acfe820764afacb35f3d0d1e0e1e4e8aabb29e9
---

# Research: `init` command (WP-5.4)

## Research Question

How should `agent-creance init` be implemented so that it writes a valid
`.agent-creance.yaml` template into the project, pre-populates the `generators:`
list based on detected `package.json` / `composer.json`, and refuses to clobber
an existing config without explicit consent — all consistent with the project's
established command, sysdep-seam, and testscript conventions?

## Summary

`init` is a small, self-contained CLI command with no new external dependencies.
It mirrors the newest command, `setup` (AC-0028), almost exactly:

- A thin `newInitCmd(app *App) *cobra.Command` in `internal/cli/init.go`, with a
  `--force` boolean flag, delegating to a testable `runInit(ctx, app, dir, force)`.
- Registered in `internal/cli/cli.go` via `root.AddCommand(newInitCmd(app))`.
- All filesystem work goes through the injected `app.FS` (`sysdep.FileSystem`):
  `Stat` to detect the manifests and the pre-existing config, `WriteFile`/`Rename`
  to write the template atomically (the temp-file + rename idiom from
  `internal/setup/skill.go`).
- The generator names are the existing constants
  `generator.GeneratorPackageJSON = "package_json"` and
  `generator.GeneratorComposerJSON = "composer_json"`.
- Tested with a hermetic `.txtar` under `internal/cli/testdata/script/`, plus a
  Go unit test in `internal/cli/` driving `runInit` against the sysdeptest fakes.

The single open design decision is the **template content** (the ticket's only
"Question for Research/Planning"). The design doc's example config is a teaching
artifact full of project-specific content (`tobyS/this-project` allow rules,
`w3schools.com` denies, a personal `GIT_AUTHOR_NAME`, an `include:` of a
non-existent file). It should NOT be emitted verbatim. The template should be a
minimal, commented, *valid* config with the detected `generators:` filled in.
See "Open Questions / Decisions" for the recommendation.

## Detailed Findings

### What the design specifies for `init`

The entire design-level spec is two lines in `docs/design.md` "Commands"
(`docs/design.md:359-360`):

```
agent-creance init   # writes .agent-creance.yaml template in the project; detects
                     #   existing manifests (package.json, composer.json) and
                     #   pre-populates the generators: list accordingly
```

The technical spec WP-5.4
(`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md:323-326`):

> **WP-5.4 — `init` command** (`internal/cli`). Write the `.agent-creance.yaml`
> template; detect `package.json`/`composer.json` and pre-populate `generators:`.
> *Done when:* `testscript` shows template emission + manifest-driven generator
> prepopulation.

A load-bearing constraint from `docs/design.md:422`: **`init` must NOT write any
`.gitignore` block** — all runtime/compiled/session state lives out-of-tree, so
there is nothing to gitignore. The only in-tree file is the config itself.

### Detection semantics

Detection is presence-based, not content-based. Per `docs/design.md:359-360` and
the generators section (`docs/design.md:154-209`): if `./package.json` exists →
add `package_json`; if `./composer.json` exists → add `composer_json`. `init`
does **not** parse the manifests (that's what the generators do at policy-compile
time, out of scope here — ticket "Out of Scope"). A `Stat` is sufficient.

### The command pattern to mirror (`setup`, AC-0028)

`internal/cli/setup.go:17-70` is the template:

- `newSetupCmd(app *App)` declares flags as local `bool` vars bound with
  `cmd.Flags().BoolVar`, and `RunE` closes over `app` + the vars, delegating to
  `runSetup(cmd.Context(), app, noSkill, noCAInstall)`.
- `runSetup` takes the flags **as parameters** (not globals) — the comment at
  `setup.go:34-37` states this is precisely what lets unit tests exercise every
  flag combination against the sysdep fakes. `init` should follow suit:
  `runInit(ctx, app, dir, force)`.
- Output via `fmt.Fprintln(app.Stdout, ...)`, never bare `fmt.Println`.
- Errors returned up to cobra; `Main` (`cli.go:103-107`) prints `"error: "+...`
  and returns exit 1. Root has `SilenceUsage`/`SilenceErrors` set
  (`cli.go:65-66`) so a runtime error doesn't dump usage.

The `dir` parameter convention: production commands pass `"."` and take the dir
as a param so tests can point it elsewhere — see `runRun(cmd.Context(), app, ".")`
at `run.go:40,48` and `run_test.go:62` (`paths.Cwd = runProj`). `init` should do
the same: `runInit(cmd.Context(), app, ".", force)`.

### Registration

`internal/cli/cli.go:72-78` lists every subcommand. Add:
```go
root.AddCommand(newInitCmd(app))
```
Because the testscript harness maps `agent-creance` → `cli.Main` (`script_test.go:26-30`),
the new command is automatically available in `.txtar` scripts once registered.

### The filesystem seam

`internal/sysdep/filesystem.go:19-39` — `sysdep.FileSystem`:
`ReadFile`, `WriteFile(name, data, perm)`, `Stat`, `MkdirAll`, `Remove`, `Rename`.
- `Stat` returns an error satisfying `errors.Is(err, fs.ErrNotExist)` when absent
  — this is the detection seam for the manifests and the existing-config check.
- Real impl `OSFileSystem` (`filesystem.go:46-72`); fake `FakeFileSystem`
  (`internal/sysdep/sysdeptest/filesystem.go`) is an in-memory map with
  per-op error knobs (`Files`, `Dirs`, `StatErrs`, `WriteErrs`, …) and returns
  `fs.ErrNotExist` for unknown paths — so unit tests pre-load `package.json`
  / `composer.json` and assert the written `.agent-creance.yaml`.

The atomic-write idiom (`internal/setup/skill.go:52-73`): write to `dest+".tmp"`
then `Rename(tmp, dest)`, best-effort `Remove(tmp)` on rename failure. `init`'s
write is a fresh-file create (not an idempotent rewrite), so the read-and-compare
step is unnecessary, but the temp+rename for crash safety is the house style.

Path note: paths are built by joining the `dir` param. The filename constant
already exists: `const configFile = ".agent-creance.yaml"` at `run.go:23` (in
package `cli`), so `init` can reuse it: `filepath.Join(dir, configFile)`,
`filepath.Join(dir, "package.json")`, `filepath.Join(dir, "composer.json")`.

### The config schema (so the template is valid)

`internal/config/config.go` models one document; it is pure (no FS). Public
typed structs at `config.go:29-89`; the YAML mapping lives in the raw mirror
(`rawConfig` etc., `config.go:94-122`) with `yaml:"..."` tags. Top-level keys:
`agent` (`command`, `workdir`), `safehouse` (`add_dirs_rw`, `add_dirs_ro`,
`enable`), `include`, `network` (`host_services`, `egress` →
`generators`/`allow`/`deny_always`), `env`. The `generators` field is
`Egress.Generators []string` (`config.go:69`), tag `yaml:"generators"`
(`config.go:118-122`).

`config.Parse(data)` (`config.go:128-157`) strict-decodes with
`KnownFields(true)`; an **empty document is valid** (`io.EOF` → empty `Config`,
`config.go:133-136`). So the template can be minimal. The whole-file parse-check
for the test is `config.NewLoader(app.FS, app.Paths).Load(filepath.Join(dir, configFile))`
(`load.go:39-48`; used in production at `run.go:98`).

Validation gotchas that constrain the template (`internal/config/validate.go`):
the template must parse AND validate. The safest validity guarantee is to reuse
the proven-good shape — `internal/config/testdata/example.yaml` is annotated
"this real example must load without error" (AC-0007 verification step 5). The
template should be a trimmed subset of that known-good file.

### Generator name constants

`internal/generator/manifest.go:11-14`:
```go
const (
    GeneratorPackageJSON  = "package_json"
    GeneratorComposerJSON = "composer_json"
)
```
`init` should reference these constants rather than hardcoding strings, so the
template and the generator engine can never drift. (Importing `internal/generator`
from `internal/cli` is fine — no cycle; `cli` already imports many internal pkgs.)

### Testing approach

Per `CLAUDE.md` + profile, two layers:

1. **Hermetic testscript** `.txtar` under `internal/cli/testdata/script/`
   (model: `setup_help.txtar`). Each scenario gets an isolated temp `$WORK`
   cwd. A script can `cp`/create `package.json` / `composer.json`, run
   `agent-creance init`, then assert the written file exists and `grep` its
   contents for the right generators; and assert a second `init` fails / `--force`
   succeeds. Set `env PATH=$CREANCE_BIN` for a hermetic PATH.
   - For the "template parses" criterion, the cleanest in-script assertion is a
     follow-up command that loads the config without error. `agent-creance run`
     can't be used hermetically (needs real tools); `policy show` reads/compiles
     the config — see `policy_show.txtar` / `policy_no_config.txtar`. A `policy`
     subcommand over the freshly-written config is a candidate "does it parse"
     probe, but verify it doesn't require network/tools in the no-rule template.
     The unit test (below) is the more reliable parse assertion.
2. **Go unit test** `internal/cli/init_test.go` (model: `setup_test.go`,
   `run_test.go`) driving `runInit(ctx, app, dir, force)` against
   `sysdeptest.FakeFileSystem` + `FakePathResolver`: pre-load manifests, run,
   then `config.Parse`/`Loader.Load` the written bytes and assert generators +
   no-clobber + `--force`.

### Existing testscript inventory

`internal/cli/testdata/script/`: `version`, `doctor_missing`, `doctor_healthy`,
`policy_show`, `policy_explain`, `policy_no_config`, `policy_refresh`, `logs`,
`logs_summary`, `run_missing_prereq`, `setup_help`. New: `init_*.txtar`.

## Code References

- `internal/cli/setup.go:17-70` — command pattern to mirror (flags as params,
  thin `RunE` → testable body).
- `internal/cli/cli.go:61-79` — `newRootCmd`; add `newInitCmd(app)` at :72-78.
- `internal/cli/cli.go:85-109` — `Main()` real wiring; error→exit-1 contract.
- `internal/cli/run.go:23` — `const configFile = ".agent-creance.yaml"` (reuse).
- `internal/cli/run.go:40,48` — `dir` param convention (`"."` in prod).
- `internal/sysdep/filesystem.go:19-39` — `FileSystem` seam (`Stat`/`WriteFile`/`Rename`).
- `internal/sysdep/sysdeptest/filesystem.go` — `FakeFileSystem` for unit tests.
- `internal/setup/skill.go:52-73` — atomic temp-file + rename write idiom.
- `internal/config/config.go:29-122` — schema structs + YAML tags; `generators` at :69/:118-122.
- `internal/config/config.go:128-157` — `Parse` (empty doc valid; strict fields).
- `internal/config/load.go:39-48` — `NewLoader(...).Load(path)` whole-file parse check.
- `internal/config/testdata/example.yaml` — known-good config to trim into a template.
- `internal/generator/manifest.go:11-14` — `GeneratorPackageJSON`/`GeneratorComposerJSON`.
- `internal/cli/testdata/script/setup_help.txtar` — `.txtar` model.
- `internal/cli/script_test.go:26-53` — testscript harness, `$CREANCE_BIN`.
- `docs/design.md:359-360` — init spec; `docs/design.md:422` — no gitignore block.
- `docs/design.md:72-152` — canonical example config (teaching artifact, not the template).

## Architecture Insights

- **Composition root discipline:** every side effect goes through an `App` seam;
  `init` introduces no new seam — `FileSystem` + `PathResolver` already cover it.
  No `internal/sysdep` change needed.
- **No new sub-package required.** `setup` factored logic into `internal/setup`
  because it has real complexity (CA bootstrap, keychain, TLS probe). `init` is
  small enough to live entirely in `internal/cli/init.go` (a template constant +
  Stat/Write). Adding an `internal/scaffold` package would be over-engineering;
  prefer the in-package body unless planning surfaces shared logic.
- **Template as an embedded/inline constant:** the template is a fixed string
  with one variable region (the `generators:` block). Options: (a) build the
  YAML in Go with the generator list interpolated; (b) `//go:embed` a base
  template and splice generators. (a) is simpler given the single variable —
  recommend a small builder that assembles the string with 0–2 generator lines.
- **Golden-file fit:** the generated template is a "generated artifact," which by
  convention (`CLAUDE.md`) is golden-file tested. A golden test of the rendered
  template (no-manifest / package-only / both) under `internal/cli/testdata/`
  would complement the `.txtar` behavior test and catch accidental drift.

## Open Questions / Decisions (for the checkpoint)

1. **Template content (the ticket's explicit question).** Recommendation: emit a
   *minimal, commented, valid* config — NOT the design's full teaching example.
   Concretely: `agent:` with a sensible default `command`/`workdir`, a brief
   `safehouse.add_dirs_rw: [.]`, the detected `network.egress.generators:` list
   (or an empty/commented block if no manifests), and comments pointing at
   `docs/design.md` and `agent-creance allow`/`deny`. Exclude project-specific
   allow/deny rules, the `include:` of a non-existent file, and personal `env`.
   Need confirmation on: the default `agent.command` (e.g.
   `["claude", "--dangerously-skip-permissions"]` per the example) and whether
   to include commented-out `allow:`/`deny_always:` stubs as guidance.

2. **No-manifest behavior for `generators:`.** When neither manifest exists, emit
   an empty `generators: []` (valid) vs. a commented placeholder vs. omit the key.
   Recommendation: a commented placeholder under `egress:` showing the two
   available names, so the user can uncomment — keeps the file self-documenting.

3. **Flag name + refuse semantics.** Recommendation: refuse by default when
   `.agent-creance.yaml` exists (exit 1 with an actionable message), `--force`
   to overwrite. Confirm the flag spelling (`--force` vs `--overwrite`).

4. **Success output.** Confirm desired stdout (e.g. `✓ Wrote .agent-creance.yaml
   (generators: package_json, composer_json)` + a "next: agent-creance setup"
   hint), matching the `setup` command's ✓-prefixed style.

## Related Research

- `thoughts/shared/research/2026-06-07-AC-0028-setup-command.md` — the immediately
  preceding command; this command reuses its structure.
- `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md` — WP-5.4
  and the Phase 5 onboarding context.
