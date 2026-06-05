---
date: 2026-06-05
ticket: AC-0016
work_package: WP-2.7
topic: "`policy refresh` command — force registry re-fetch + recompile"
status: complete
git_commit: bdece625d85927a703362a496ea41767cad3971f
branch: main
repository: agent-creance
---

# Research: AC-0016 `policy refresh` command (WP-2.7)

## Research question

How should `agent-creance policy refresh` be implemented so that it (a) invalidates
the per-package generator metadata cache for the project's generators and (b) triggers
a recompile, reports counts, and exits 0 — without requiring the cage to be running?

## Summary / key findings

1. **There are three caches between an `agent-creance` invocation and a fresh registry
   fetch, not one.** To make a re-fetch actually happen (the AC verification requires
   "fake registry called again"), `policy refresh` must invalidate **all three**:

   | # | Cache | Location | Key | TTL | Short-circuits at |
   |---|-------|----------|-----|-----|-------------------|
   | 1 | **Registry metadata** (the ticket's "per-package metadata cache") | `<cache>/agent-creance/registries/<registry>/<pkg>.json` | package name | **30 days** | `registry.Client.Lookup` (`registry.go:118`) |
   | 2 | **Generator output** | `<cache>/agent-creance/generators/<generator>/<manifest-hash>.json` | SHA-256(manifest bytes) | **none** | `generator.Generator.Generate` (`generator.go:62`) |
   | 3 | **Compiler input-hash gate** | `<cache>/agent-creance/projects/<hash>/policy.json` (`input_hash` field) | SHA-256(config layers + manifests) | n/a | `Compiler.Compile` (`compile.go:175-184`) |

   Clearing only cache #1 is **insufficient**: a recompile would hit cache #3
   (`Skipped:true`, generators never run) or, past that, cache #2 (cached rules
   returned, registry never consulted). The "fake registry called again" assertion
   only passes when all three are defeated. **This is the crux of the ticket** and the
   ticket's one-line "invalidate the per-package cache and recompile" understates it.

2. **Caches #1 and #2 are intentionally cross-project / shared** (`state.go:110-138`
   documents this explicitly: a package's homepage is the same for every project, so
   the cache survives across them). Therefore a faithful `refresh` must invalidate
   **only the packages this project's generators reference** — not blow away the whole
   `registries/` or `generators/` tree, which would penalise unrelated projects. This
   is the principled design and it also yields the per-package **counts** the AC asks
   for naturally.

3. **The `sysdep.FileSystem` seam has no `ReadDir` and no `RemoveAll`** — only
   single-file/empty-dir `Remove` (`filesystem.go:33`). You therefore **cannot
   enumerate** cache directories to bulk-delete; you must **compute the exact paths**
   to remove. This actually pushes the design in the right (per-package) direction:
   the package names come from re-parsing the project's manifests (logic that already
   exists in `generator/manifest.go`), and the cache paths are computable from those.

4. **No invalidation/delete API exists anywhere today.** All cache methods are
   unexported read/write pairs (`registry.readFreshCache`/`writeCache`,
   `generator.readCache`/`writeCache`). `refresh` is the first consumer that needs to
   *delete* cache entries, so it will add small exported invalidation methods on the
   packages that own the path computation — mirroring the existing seam patterns.

5. **"Does not require the cage to be running" is satisfied for free** — refresh only
   touches the cache tree and recompiles `policy.json`; it never talks to the proxy,
   lock file, or Safehouse. Same property as the sibling `policy show`/`explain`.

## The three caches, in detail

### Cache #1 — registry metadata (the headline target), 30-day TTL
- `internal/generator/registry/registry.go`
- TTL: `refreshInterval = 30 * 24 * time.Hour` (`registry.go:35`).
- Path: `cachePath` → `<registriesRoot>/<registry>/<pkg>.json` (`registry.go:142-147`),
  where `<registry>` is `src.name()` = `"npm"` or `"packagist"` (unexported).
- Record: `cacheEntry{FetchedAt time.Time; Metadata}` (`registry.go:110-113`); freshness
  read from the in-file `fetched_at` via the injected `sysdep.Clock`, not mtime.
- Read/write only; **no delete**. `validatePackage` (`registry.go:153`) guards path
  traversal and must be reused if we compute the path from a package name.

### Cache #2 — generator output, no TTL
- `internal/generator/cache.go`
- Path: `cachePath` → `<generatorsRoot>/<generator>/<manifest-hash>.json` (`cache.go:37-39`),
  hash = `cacheKey(manifest)` = hex SHA-256 of the manifest bytes (`cache.go:31-34`).
- Record: `cacheRecord{Rules []Rule}` (`cache.go:23-25`). Explicitly no TTL
  (`cache.go:28-30`) — "per-package freshness is the registry client's concern".
- Checked *first* inside `Generate` (`generator.go:62-68`): **a hit makes zero registry
  lookups**, so it must be cleared for refresh to reach cache #1.

### Cache #3 — compiler input-hash gate
- `internal/policy/compile/compile.go`
- `Compile` computes `inputHash` over config layers + manifest bytes (`compile.go:168`,
  `266-283`), then if an existing `policy.json` has a matching `Version` + `InputHash`
  it returns `Result{Skipped:true}` **before running any generator** (`compile.go:175-184`).
- A registry-TTL elapsing does **not** change `inputHash` (config + manifests are
  unchanged), so without defeating this gate the recompile is a no-op.
- Two ways to defeat it: (a) `Remove` the `policy.json` before recompiling, or
  (b) give the compiler a "force" path that skips the early return. See "Open
  design decisions".

## How the pieces fit (the recompile path)

`Compiler.Compile(ctx, dir)` (`compile.go:133`) is the orchestrator:
1. `state.Resolve(dir)` → `Layout` (Canonical dir, Root, `PolicyJSON()` path).
2. Loads global + project + session-overlay config layers.
3. `mergeGenerators(global, project)` (`compile.go:156`, `227-239`), validates each via
   `generator.Known`.
4. `readManifests(layout.Canonical, gens)` (`compile.go:243-260`) — reads
   `package.json` / `composer.json`; an absent manifest contributes nothing.
5. `inputHash(...)` → cache-gate check (#3).
6. `buildRuleSet` → `runGenerators` → `realGenerators.Run` → `generator.New(...).Generate(...)`.
7. Atomic write of `policy.json`.

`refresh` reuses steps 1–4 to know **which generators and which manifests** this project
has, which is exactly what's needed to enumerate the packages to invalidate.

`Result` (`compile.go:122-128`) already exposes `Skipped`, `AllowCount`, `DenyCount` —
useful for the post-recompile portion of the refresh report.

## Command wiring (mirror the siblings)

- `internal/cli/policy.go`: `newPolicyCmd` (`policy.go:20`) groups `show`+`explain`;
  add `newPolicyRefreshCmd(app)` to the `AddCommand` call (`policy.go:25`).
- Subcommand constructor pattern: `newXxxCmd(app *App) *cobra.Command`, `Args:
  cobra.NoArgs` (or a filter arg — see open decisions), `RunE` returns `error`, all
  output via `app.Stdout` (never `os.Stdout`), `cmd.Context()` threaded down.
- Dependencies come off the injected `App`: `app.FS`, `app.Paths`, `app.Clock`,
  `app.HTTP` (`cli.go:20-32`). The compiler is constructed per-call:
  `compile.New(app.FS, app.Paths, app.Clock, app.HTTP)` (see `resolvePolicy`,
  `policy.go:101-119`) — the exact idiom to follow.
- No Go change is needed to register a new testscript: `script_test.go` auto-runs every
  `*.txtar` under `testdata/script/`.

## Sysdep / testability constraints

- `FileSystem`: `ReadFile`, `WriteFile`, `Stat`, `MkdirAll`, `Remove`, `Rename`
  (`filesystem.go:18-38`). **No `ReadDir`, no `RemoveAll`.**
- `OSFileSystem.Remove` of a missing file returns an `fs.ErrNotExist` error; the fake
  `FakeFileSystem.Remove` (`sysdeptest/filesystem.go:95-103`) returns `nil` for a
  missing path. **Invalidation code must treat `errors.Is(err, fs.ErrNotExist)` as
  "wasn't cached" (count 0), not a failure** — and tests must not depend on the
  divergent missing-file behaviour.
- Manifest → package names already lives in `generator/manifest.go`:
  `packageJSON.deps` / `composerJSON.deps` (`manifest.go:29-62`), via the unexported
  `ecosystem` interface. `composerJSON` already filters platform/meta requirements
  (`isComposerPackage`, `manifest.go:69-71`) so only real Packagist packages remain.

## Recommended approach (for the plan to refine)

Per-package, project-scoped invalidation driven from the compiler, reusing its existing
config/manifest resolution and adding three small seam-mirroring invalidation methods:

1. **`registry.Client.Invalidate(pkg string) (existed bool, err error)`** — computes
   `cachePath(pkg)` (reusing `validatePackage`), `Remove`s it, maps `fs.ErrNotExist`
   to `existed=false`.
2. **`generator.Generator.Invalidate(manifest []byte) (pkgsInvalidated int, err error)`**
   — `Remove`s its output-cache file for the manifest hash, then parses
   `eco.deps(manifest)` and calls the registry seam's `Invalidate` for each, summing
   the count. This needs the `lookuper` seam extended with `Invalidate` (faked in the
   generator's tests, satisfied by `*registry.Client`).
3. **`compile.Compiler.Refresh(ctx, dir) (RefreshResult, error)`** — resolves the
   layout + generators + manifests (reusing existing helpers), invalidates per
   generator via an extended `generatorRunner` seam (add `Invalidate(ctx, name,
   manifest) (int, error)` alongside `Run`), then forces a rebuild+write (bypassing the
   #3 gate) and returns counts (generators refreshed, packages invalidated, allow/deny
   counts). The fake runner in the compiler's tests records invalidation calls.
4. **`internal/cli/policy.go`**: `newPolicyRefreshCmd` calls `Refresh` and renders the
   counts to `app.Stdout`; exits 0 on success.

This respects the cross-project cache (only this project's packages are touched), yields
the AC's counts naturally, needs no `ReadDir`/`RemoveAll` on the seam, and each new
method mirrors an existing read/write pattern. Tests: table-driven for the new
registry/generator/compiler methods + a hermetic `policy_refresh.txtar`.

## Open design decisions (for the checkpoint / plan)

1. **Ticket's explicit open question: refresh-all vs. a filter argument.** The ticket
   asks "Refresh all generators or accept a package/generator filter argument in v0.1?"
   Out-of-scope notes defer per-generator config. Leaning: **refresh all (no filter)**
   for v0.1 — `policy refresh` with `cobra.NoArgs`. Needs user confirmation.
2. **Defeat cache #3 by removing `policy.json`, or by a compiler "force" flag?** A
   `Compiler.Refresh` that calls `buildRuleSet`+`write` directly (bypassing the gate)
   is cleaner than the CLI reaching in to delete `policy.json`, and keeps all state
   knowledge inside the compile package. Leaning: **`Refresh` forces the rebuild
   internally.**
3. **Report shape.** Minimal: counts of generators refreshed + packages invalidated +
   the resulting allow/deny totals, exit 0. A `--json` flag would match the siblings'
   convention but isn't required by the AC — propose **plain text only for v0.1** unless
   the user wants `--json` for parity.

## Code references

- `internal/generator/registry/registry.go:35,110-147,168-189,217-236` — registry cache.
- `internal/generator/cache.go:31-81` — generator output cache.
- `internal/generator/generator.go:43-78,86-109` — generator construction + cached Generate.
- `internal/generator/manifest.go:11-71` — manifest → package-name parsing.
- `internal/policy/compile/compile.go:96-206,243-260,266-283` — compiler + input-hash gate.
- `internal/state/state.go:110-138,154-155` — RegistriesRoot/GeneratorsRoot/PolicyJSON.
- `internal/cli/policy.go:20-119` — `policy` command tree + `resolvePolicy` idiom.
- `internal/cli/cli.go:20-32,54,62-72` — `App` seams + command registration + wiring.
- `internal/sysdep/filesystem.go:18-38` — FileSystem seam (no ReadDir/RemoveAll).
- `internal/sysdep/sysdeptest/filesystem.go:95-103` — fake Remove (nil on missing).
- `internal/cli/testdata/script/policy_show.txtar` — testscript template (hermetic).

## Related thoughts docs

- `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md` — WP-2.7 (line 206), dep table (line 377).
- `thoughts/shared/research/2026-06-05-AC-0011-registry-clients-cache.md` — cache #1 design.
- `thoughts/shared/research/2026-06-05-AC-0013-policy-compiler.md` — cache #3 / recompile.
- `thoughts/shared/research/2026-06-05-AC-0012-allowlist-generators.md` — generators / cache #2.
- `thoughts/shared/plans/2026-06-05-AC-0015-policy-show-explain.md` — sibling-subcommand template.
