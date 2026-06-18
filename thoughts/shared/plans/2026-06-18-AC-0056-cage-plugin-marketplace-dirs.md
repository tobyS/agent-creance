---
date: 2026-06-18
ticket: AC-0056
research: thoughts/shared/research/2026-06-18-AC-0056-cage-plugin-marketplace-dirs.md
branch: main
status: in-progress
---

# Implementation Plan: Grant local Claude plugin marketplace directories into the cage (AC-0056)

## Overview

Caged Claude Code reads each registered marketplace's catalog at startup from
`<source.path>/.claude-plugin/marketplace.json`. For a **local** marketplace
(`source.source == "directory"` or `"file"`), `source.path` is an arbitrary
on-disk location outside the cage's mounts (project dir + `~/.claude`), so Seatbelt
`EPERM`-denies the read and the marketplace + its plugins fail to load.

We will detect local-marketplace source directories from
`~/.claude/plugins/known_marketplaces.json` **at each `run` launch** and grant the
ones outside the cage **read-only**, printing a one-line notice. This is
self-maintaining as marketplaces change and is naturally per-user (marketplaces
are a per-user registry).

## Current state

- `cage.Build` (`internal/cage/cage.go:98-113`) finalizes the safehouse mount
  flags. RO mounts come verbatim from `config.Safehouse.AddDirsRO`; `--add-dirs-ro`
  is emitted only when that list is non-empty. `Build` is a **pure** function
  (golden-tested) — detection (I/O) must happen before it.
- `runRun` (`internal/cli/run.go:50,156-179`) loads config, resolves the project
  `Layout` (canonical path), then `Resolve` → `Prepare` → `Build` → `Run`.
- No code derives a safehouse mount from detection today; `internal/claudeimport`
  only yields egress rules + ports.
- Marketplaces live in `~/.claude/plugins/known_marketplaces.json` keyed by name;
  local ones are `source.source` `"directory"`/`"file"` with the path in
  `source.path` (== `installLocation`). Git sources and the plugin cache live under
  `~/.claude` (already mounted).

## Desired end state

`agent-creance run` grants every local (`directory`/`file`) marketplace source dir
that is outside the cage as a read-only safehouse mount, so caged Claude Code loads
those marketplaces without `EPERM`. Missing/unreadable registrations degrade to a
clear non-fatal warning; git sources and already-mounted paths are skipped; the
grant is announced in one line.

## What we are NOT doing

- No static/global-config persistence of these dirs (rejected at checkpoint in
  favor of dynamic).
- No read-write mounting; no support for git/remote marketplaces (already covered
  by the `~/.claude` mount).
- No handling of `.claude/settings.json` `extraKnownMarketplaces` beyond what the
  resolved `known_marketplaces.json` already contains (possible later follow-up).
- No change to Claude Code's own error wording.

---

## Phase 1 — `internal/pluginmkt`: detect local marketplace source dirs

### Changes

New package `internal/pluginmkt`:

- `internal/pluginmkt/pluginmkt.go`:
  ```go
  // Detect reads ~/.claude/plugins/known_marketplaces.json and returns the
  // absolute, canonicalized source directories of locally-sourced
  // ("directory"/"file") marketplaces, for read-only mounting into the cage.
  // Git/remote sources are skipped (they live under ~/.claude, already mounted).
  // A missing registry file is normal: no dirs, no warning. An unreadable or
  // malformed file, an unresolvable home dir, or a local source dir that does
  // not exist each yield one warning and are skipped. Returned dirs are
  // deduplicated and sorted.
  func Detect(fsys sysdep.FileSystem, paths sysdep.PathResolver) (dirs []string, warns []string)
  ```
  Implementation notes:
  - Resolve home via `paths.UserHomeDir()`; on error return `nil, []string{...}`
    (mirror `claudeimport.Global`).
  - Read `filepath.Join(home, ".claude", "plugins", "known_marketplaces.json")`
    with the lenient reader idiom (`claudeimport.readJSON`, `:279-292`): absent →
    no warning; unreadable/parse-error → one warning.
  - JSON shape:
    ```go
    type entry struct {
        Source struct {
            Source string `json:"source"`
            Path   string `json:"path"`
        } `json:"source"`
        InstallLocation string `json:"installLocation"`
    }
    // file is map[string]entry
    ```
  - For each entry whose `Source.Source` is `"directory"` or `"file"`: take
    `Source.Path` (fallback `InstallLocation`); skip if empty. Canonicalize with
    `paths.Abs` then best-effort `paths.EvalSymlinks`. Confirm it exists and is a
    directory via `fsys.Stat`; if Stat fails or it is not a dir, append a warning
    naming the marketplace + path and skip.
  - Dedup via a `map[string]bool` seen-set on the canonical string; `sort.Strings`
    the result.

### Tests — `internal/pluginmkt/pluginmkt_test.go`

Mirror `claudeimport_test.go` conventions (`FakeFileSystem.Files` by absolute
path, `FakePathResolver.HomeDir`, `Dirs` for directory existence, `Errs`/parse for
warnings):

- missing registry file → `nil, nil`.
- one `directory` source pointing at an existing fake dir → that dir, no warning.
- `file` source → detected.
- `github` (and `url`) source → skipped (not in result).
- mixed entries → only local dirs, sorted + deduped.
- malformed JSON → 1 warning, no dirs.
- unreadable file (`fsys.Errs[path]=fs.ErrPermission`) → 1 warning.
- `directory` source whose path does not exist (no `Dirs` entry) → 1 warning, skipped.
- `UserHomeDir` error (`paths.HomeErr`) → 1 warning, no dirs.

### Success criteria

#### Automated
- [ ] `go test ./internal/pluginmkt/...` passes.
- [ ] `make lint` clean.

#### Manual
- [ ] Detection returns exactly the local-directory marketplace(s) on this machine
      (`toby-plugins` → `/Users/toby/code/work/toby-plugins`).

---

## Phase 2 — Thread detected dirs into the cage as read-only mounts

### Changes

1. `internal/cage/cage.go`:
   - Add field `ExtraDirsRO []string` to `Inputs` (doc: "additional read-only
     mounts resolved at launch, e.g. local plugin-marketplace dirs; already
     absolute/canonical").
   - In `Build`, change RO assembly (`:111-113`) to combine config + extra:
     ```go
     ro := append(append([]string{}, sh.AddDirsRO...), in.ExtraDirsRO...)
     if len(ro) > 0 {
         args = append(args, "--add-dirs-ro", expandColonList(ro, in))
     }
     ```
     (`expandPath` leaves absolute paths `Clean`-ed, so canonical dirs pass through
     unchanged; config entries keep their `~`/relative expansion.)

2. `internal/cli/run.go`:
   - After `builder.Resolve` returns `in` (so `in.HomeDir` is set) and before
     `cage.Build`, compute and assign `in.ExtraDirsRO`:
     - call `pluginmkt.Detect(app.FS, app.Paths)`; print warnings to `app.Stderr`
       (reuse/`warnVersionSkew`-style helper).
     - filter out any detected dir equal to or inside an already-mounted root: the
       project canonical dir (`layout.Canonical`) and `filepath.Join(in.HomeDir,
       ".claude")` (best-effort `EvalSymlinks` on the latter). Helper
       `withinAny(path string, roots []string) bool` (equal, or `path` has prefix
       `root + string(os.PathSeparator)`).
     - if any remain, print one notice line to `app.Stderr`, e.g.
       `"granting read-only cage access to N local plugin marketplace dir(s): a, b"`,
       and set `in.ExtraDirsRO = remaining`.
   - Keep this in a small testable helper, e.g.
     `pluginMarketplaceDirs(app *App, homeDir, projectDir string) []string`.

### Tests

- `internal/cage/cage_test.go`: new focused test — set `Inputs.ExtraDirsRO` and
  assert both config RO dirs and extra dirs appear in `--add-dirs-ro` (via
  `argValue`). Existing `invocation.golden.json` stays unchanged (fixture sets no
  `ExtraDirsRO`).
- `internal/cli/run_test.go`: seed `home/.claude/plugins/known_marketplaces.json`
  with one `directory` source pointing at an existing fake dir outside the project,
  plus one pointing **inside** the project; run `runRun`; assert
  `argsContain(started[0].Args, "--add-dirs-ro", <outside dir>)` and that the
  inside-project dir is absent (filtered). Add a malformed-registry case asserting
  a stderr warning and a clean launch (no extra RO mount).

### Success criteria

#### Automated
- [ ] `make test` passes (incl. cage golden unchanged, new cage + run tests green).
- [ ] `make lint` clean.

#### Manual
- [ ] `agent-creance run` in this repo launches with `--add-dirs-ro` including
      `/Users/toby/code/work/toby-plugins`, and caged Claude Code no longer prints
      the `EPERM` marketplace-load warning for `toby-plugins`.

---

## Phase 3 — Docs + final verification

### Changes
- `docs/design.md`: add a short note in the filesystem/mounts discussion that
  `run` grants local plugin-marketplace source dirs (from
  `~/.claude/plugins/known_marketplaces.json`) read-only, and why (per-user,
  reloaded each launch).
- Update `thoughts/shared/tickets/AC-0056-*.md` checkboxes / status as work lands.

### Success criteria

#### Automated
- [ ] `make test` and `make lint` pass.
- [ ] `make build` succeeds (binary reflects the final commit, per CLAUDE.md).

#### Manual
- [ ] Re-run `agent-creance run`; confirm marketplace loads and plugins are
      available inside the cage.

---

## Testing strategy

- Pure detection logic → table-driven unit tests in `internal/pluginmkt` against
  the `sysdep` fakes (no real FS, no external tools).
- Mount wiring → cage argv assertion + golden (unchanged) and an end-to-end
  `runRun` test through the `ProcessGroup` fake (`argsContain`), all hermetic.
- No `//go:build integration` test needed; real safehouse is never invoked in unit
  tests.

## References

- Research: `thoughts/shared/research/2026-06-18-AC-0056-cage-plugin-marketplace-dirs.md`
- `internal/cage/cage.go:98-113,173-185,304-325`
- `internal/cli/run.go:50,156-179,186-193`
- `internal/claudeimport/claudeimport.go:279-292` (lenient reader to mirror)
- `internal/state/state.go:85-100` (canonicalization)
