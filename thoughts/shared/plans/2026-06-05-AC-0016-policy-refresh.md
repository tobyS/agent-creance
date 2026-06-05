---
date: 2026-06-05
ticket: AC-0016
work_package: WP-2.7
topic: "`policy refresh` command — implementation plan"
status: ready
research: thoughts/shared/research/2026-06-05-AC-0016-policy-refresh.md
git_commit: 5386fca
branch: main
---

# Implementation Plan: AC-0016 `policy refresh` (WP-2.7)

## Overview

Add `agent-creance policy refresh`: a sibling of `policy show`/`explain` that forces a
re-fetch of generator registry metadata and recompiles the project's policy, then
reports counts and exits 0. Per the research, a real re-fetch requires invalidating
**three** caches — the 30-day registry metadata cache, the no-TTL generator output
cache, and the compiler's input-hash gate — scoped to **this project's** packages only
(the registry/generator caches are intentionally cross-project shared).

**Decisions (confirmed at the checkpoint):**
- **Refresh all, no filter** — `policy refresh` takes `cobra.NoArgs`.
- **Add `--json`** for parity with the sibling subcommands (text default + JSON flag,
  golden-tested like `show`/`explain`).

## Current state

- No invalidation/delete API exists; all cache methods are unexported read/write pairs.
- `sysdep.FileSystem` has `Remove` (single file) but **no `ReadDir`/`RemoveAll`**, so we
  compute exact paths rather than enumerate. (No seam change is needed.)
- `FakeFileSystem.Remove` returns `nil` for a missing path while `OSFileSystem.Remove`
  returns `fs.ErrNotExist`. **Existence must be detected with `Stat`, not by inspecting
  `Remove`'s error**, so the "was it cached?" count is reliable under both.
- `Compiler.Compile` resolves layout/config/generators/manifests, then gates on the
  input hash before building. We will refactor it to share that resolution + the
  build/write step with a new `Refresh`.

## Desired end state

`policy refresh` (and `--json`) invalidates this project's registry + generator-output
cache entries, forces a recompile, and prints what was refreshed. Verified by:
table-driven unit tests on the new registry/generator/compiler methods, a hermetic
real-stack refetch test (fake HTTP + fake FS) proving the registry is hit again, render
goldens for text + JSON, and a hermetic testscript for the command. `make test`,
`go build ./...`, `make lint`, `make golden` (no diff) all green.

---

## Phase 1 — `registry.Client.Invalidate`

**File:** `internal/generator/registry/registry.go` (+ `registry_test.go`)

- Add `func (c *Client) Invalidate(pkg string) (existed bool, err error)`:
  - `path, err := c.cachePath(pkg)` (reuses `validatePackage` — rejects traversal).
  - `existed, err := removeIfPresent(c.fs, path)` — Stat-then-Remove helper; an
    `fs.ErrNotExist` from Stat → `(false, nil)`.
- Add a small unexported `removeIfPresent(fs sysdep.FileSystem, path string) (bool, error)`
  in this package (or inline) — Stat to decide existence, then `Remove`.

**Tests (`registry_test.go`, table-driven):**
- Present entry → file removed, `existed==true`.
- Absent entry → `existed==false`, no error.
- Invalid package name → error (path-traversal guard).
- Use the existing fake filesystem from `sysdeptest` (mirror the cache tests' setup).

**Success:** `go test ./internal/generator/registry/...` green.

---

## Phase 2 — `generator.Generator.Invalidate` (+ `lookuper` seam)

**File:** `internal/generator/generator.go`, `internal/generator/cache.go`
(+ `generator_test.go`)

- Extend the `lookuper` seam (`generator.go:16-18`) with
  `Invalidate(pkg string) (bool, error)` — satisfied by `*registry.Client` (Phase 1).
- Add `InvalidationStats{ Packages int; CacheEntriesCleared int; OutputCacheCleared bool }`.
- Add `func (g *Generator) Invalidate(manifest []byte) (InvalidationStats, error)`:
  - `OutputCacheCleared, err = removeIfPresent(g.fs, g.cachePath(manifest))` (Stat-then-Remove).
  - `deps, err := g.eco.deps(manifest)`; for each pkg `existed, err := g.lookup.Invalidate(pkg)`;
    `Packages++`, and `CacheEntriesCleared++` when existed.
- Reuse the same Stat-then-Remove helper (factor a tiny shared one, or duplicate the
  4-line idiom — keep it local to each package to avoid a new shared dependency).

**Tests (`generator_test.go`, table-driven):**
- Fake `lookuper` recording `Invalidate` calls + canned existed results, mirroring the
  existing call-counting Lookup fake. Assert: output cache removed, one `Invalidate`
  per dep, counts correct, composer platform/meta keys (`php`, `ext-*`) skipped (reuses
  `isComposerPackage`).
- Absent output cache + absent entries → all-zero stats, no error.

**Success:** `go test ./internal/generator/...` green.

---

## Phase 3 — `compile.Compiler.Refresh` (+ runner seam, refactor)

**File:** `internal/policy/compile/compile.go` (+ `compile_test.go`)

1. **Refactor `Compile` to share resolution + build:**
   - Extract `func (c *Compiler) resolve(dir string) (compileInputs, error)` returning a
     struct with `layout`, `global/project/overlay`, `gens`, `manifests`, `hash`
     (everything `Compile` computes before the gate).
   - Extract `func (c *Compiler) build(ctx, in compileInputs) (Result, error)` = the
     `buildRuleSet` → `write` → `Result{Skipped:false,...}` tail.
   - `Compile` becomes: `resolve` → gate check (`readCompiled` + hash match → `Skipped`)
     → else `build`. Behaviour unchanged (existing tests + goldens must stay green).
2. **Extend the `generatorRunner` seam** (`compile.go:64-66`) with
   `Invalidate(name string, manifest []byte) (generator.InvalidationStats, error)`.
   `realGenerators.Invalidate` constructs `generator.New(name, …)` and calls
   `g.Invalidate(manifest)`.
3. **Add result types + `Refresh`:**
   ```go
   type GeneratorRefresh struct {
       Name string; Packages int; CacheEntriesCleared int; OutputCacheCleared bool
   }
   type RefreshResult struct {
       PolicyPath string; Generators []GeneratorRefresh; AllowCount, DenyCount int
   }
   func (c *Compiler) Refresh(ctx context.Context, dir string) (RefreshResult, error)
   ```
   - `in, err := c.resolve(dir)`.
   - For each `name` in `in.gens` with a present manifest: `stats := c.runner.Invalidate(name, manifest)`;
     append a `GeneratorRefresh`.
   - `res, err := c.build(ctx, in)` — **forces** a rebuild (bypasses the gate), so the
     generators run and (with caches cleared) re-hit the registry.
   - Return `RefreshResult{res.PolicyPath, grs, res.AllowCount, res.DenyCount}`.

**Tests (`compile_test.go`):**
- **Fake-runner test:** assert `Refresh` calls `runner.Invalidate(name, manifest)` for
  every configured generator *before* `runner.Run`, returns the stats, and always
  writes `policy.json` even when an identical artifact already exists (gate bypassed).
- **Hermetic real-stack refetch test** (the ticket's "fake registry called again"):
  wire the **real** `realGenerators` runner with a `FakeFileSystem`, `FakeClock`, and a
  **fake `HTTPGetter`** (reuse the one the registry tests use). Seed a *fresh* registry
  cache entry + a generator output cache + a matching `policy.json`, plus a project
  config with a `package_json` generator and a `package.json`. Run `Refresh` and assert:
  (a) the seeded registry + output cache files are gone, (b) the fake HTTPGetter.Get was
  called (re-fetch happened despite the previously-fresh cache), (c) a new `policy.json`
  was written. This is the hermetic realization of AC verification step 2.
- Refactor regression: existing `Compile` tests + `policy.golden` unchanged.

**Success:** `go test ./internal/policy/compile/...` green; `make golden` no diff.

---

## Phase 4 — render layer (`Refresh` / `RefreshJSON`)

**File:** `internal/policy/render/render.go` (+ `render_test.go`, `testdata/*.golden`)

- Add `render` import of `internal/policy/compile` (acyclic: compile does not import
  render). Add:
  - `func Refresh(r compile.RefreshResult) string` — human-readable: a per-generator
    line (name, packages, entries cleared) and a `Recompiled policy: N allow, M deny.`
    summary; a clear "No generators configured — nothing to refresh." line when empty.
  - `func RefreshJSON(r compile.RefreshResult) (string, error)` — `MarshalIndent` of a
    stable JSON shape (`generators[]`, `allow_count`, `deny_count`) + trailing newline,
    mirroring `ShowJSON`.
- Exact bytes fixed by goldens generated with `-update`; **review the diff**.

**Tests (`render_test.go`):** golden tests for text + JSON, including the
empty-generators case (`refresh_empty.golden`) and a multi-generator case
(`refresh.golden`, `refresh.json.golden`). Mirror the existing show/explain golden
harness.

**Success:** `go test ./internal/policy/render/...` green; goldens reviewed.

---

## Phase 5 — CLI command + testscript + final verification

**File:** `internal/cli/policy.go`, `internal/cli/testdata/script/policy_refresh.txtar`

- Add `newPolicyRefreshCmd(app *App)`:
  - `Use: "refresh"`, `Short: "Force a re-fetch of generator metadata and recompile"`,
    `Args: cobra.NoArgs`, `--json` bool flag.
  - `RunE`: `compiler, err := compile.New(app.FS, app.Paths, app.Clock, app.HTTP)`;
    `res, err := compiler.Refresh(cmd.Context(), ".")`; then
    `render.Refresh(res)` or `render.RefreshJSON(res)` → `fmt.Fprint(app.Stdout, …)`.
  - Register in `newPolicyCmd`'s `AddCommand` (`policy.go:25`).
- **Testscript `policy_refresh.txtar`** (hermetic — **generator-free config**, so no
  HTTP; `env HOME=$WORK/home`, `env XDG_CACHE_HOME=$WORK/cache`, like `policy_show.txtar`):
  - `agent-creance policy refresh` → stdout reports the recompile + "nothing to refresh"
    (no generators), `! stderr .`, exit 0.
  - `agent-creance policy refresh --json` → asserts the JSON shape
    (`"allow_count"`, `"deny_count"`, `"generators"`).
  - Missing-config variant reuses the `policy_no_config` pattern only if cheap; the
    generator-driven invalidation/refetch path is already covered hermetically by the
    Phase 3 real-stack test (testscript can't stub the HTTP seam).
  - `script_test.go` auto-discovers the new `.txtar` — no Go change.

**Final verification (run all):**
1. `go build ./...`
2. `make golden` → review diff (should be only the new refresh goldens)
3. `make test` (race) → green
4. `make lint` → clean

**Ticket close:** set AC-0016 status to **Done**, tick the acceptance criteria, add a
Notes entry; `git add` the ticket; commit (`--no-gpg-sign`).

---

## Success criteria

**Automated:**
- [ ] `go build ./...` compiles.
- [ ] `make test` (race) green, including: registry/generator/compiler invalidation unit
      tests, the hermetic real-stack refetch test, render goldens, and `policy_refresh.txtar`.
- [ ] `make golden` shows no diff after regeneration (new goldens committed).
- [ ] `make lint` clean.

**Manual / behavioural:**
- [ ] `policy refresh` invalidates this project's per-package registry cache entries +
      generator output cache and triggers a recompile (does **not** touch other projects'
      shared cache entries).
- [ ] Works with no cage running (only cache files + `policy.json` are touched).
- [ ] Reports counts (generators refreshed, entries cleared, allow/deny) and exits 0;
      `--json` emits the structured report.

## Notes / risks

- **Refactor risk (Phase 3):** extracting `resolve`/`build` from `Compile` must preserve
  the gate semantics exactly — existing `compile` tests + `policy.golden` are the guard.
- **Fake `Remove` divergence:** always detect existence via `Stat` (counts depend on it).
- **`render`→`compile` import:** new but acyclic; keeps all policy rendering in one place
  consistent with `Show`/`Explain`.
- **Testscript can't exercise refetch** (HTTP isn't PATH-stubbable); that assertion lives
  in the Phase 3 hermetic real-stack test by design.
