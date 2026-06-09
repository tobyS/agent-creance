---
date: 2026-06-09
ticket: AC-0036
title: "Monorepo support — multiple manifests per generator type"
status: ready
branch: main
research: thoughts/shared/research/2026-06-09-AC-0036-monorepo-multi-manifest-generators.md
---

# Implementation Plan: AC-0036 — Monorepo support (multiple manifests per generator type)

## Overview

Generalize the two allowlist generators (`package_json`, `composer_json`) so a
generator **type may be listed multiple times, each scoped to a manifest path**,
and `init` auto-discovers a monorepo's packages via a bounded directory scan that
skips installed-dependency directories declared by the generators themselves. The
bare-string form keeps meaning the root manifest; the path is additive/optional.

## Decisions (from the question checkpoint)

1. **Schema:** object form — `{type: package_json, path: apps/web/package.json}`,
   with the bare string `package_json` still valid (= root manifest).
2. **`init` emission:** all entries parameterized (every entry carries an explicit
   `path:`, including the root one).
3. **Attribution:** the manifest path is woven into the source label **only when
   the manifest is not at the project root** (so single-repo output is
   byte-identical to today); rule **dedupe is unchanged** (identical-host rules
   collapse, first source wins).
4. **Scan depth:** root + 2 nested levels (manifest at depth 0, 1, or 2 inclusive
   — covers `apps/web/package.json`); symlinked dirs are not followed.

## Current State

- `Egress.Generators` is `[]string` of bare type names (`config.go:68,119`),
  strict-decoded through a raw mirror with **no custom `UnmarshalYAML`**
  (`config.go:10-15,131`); structured entries are parsed in a **post-decode pass**
  (`host_services` precedent: `applyDefaults` → `parseHostService`,
  `config.go:162-170`, `validate.go:56-73`).
- Merge dedupes generators by **exact string** (`merge.go:34,101-115`;
  `compile.mergeGenerators`, `compile.go:340-354`).
- The `type → filename` map is **hardcoded in two places** (`compile.go:55-58`
  `manifestFiles`, and inline in `init.go:80-83`); the generator package exposes
  neither its filename nor any dependency-dir metadata. The `ecosystem` interface
  is `name()` + `deps()` only (`manifest.go:19-22`).
- Compiler reads **one manifest per type** (`readManifests` → `map[type][]byte`,
  `compile.go:358-375`) and fans out **once per type** (`runGenerators`,
  `compile.go:442-467`). The runner seam takes `(name, []byte)` and is already
  path-agnostic (`compile.go:64-70,82-88`).
- The generator-output cache is **content-addressed** (`<type>/<sha256(bytes)>`,
  `cache.go:36-39`) — no change needed. The input hash hashes manifest bytes but
  keys its map by type (`compile.go:381-398`).
- Source label is `generated:<type>:<pkg>` with **no path** (`generator.go:158-160`);
  renderers consume `r.Source` verbatim with dynamic widths (`render.go:271-276`).
- `init` detects manifests only at the **root** via two `Stat`s
  (`detectGenerators`, `init.go:75-89`); it emits its own YAML template
  (`init.go:117-160`), with a per-element loop already in place
  (`init.go:130-135`). No-clobber via `Stat` + `--force` (`init.go:42-55`).
- There is **no dependency-directory concept** anywhere in the codebase.

## Desired End State

- A config lists `package_json`/`composer_json` any number of times, each with an
  optional `path:`; the compiled `policy.json` contains allow rules for **every**
  listed manifest, with non-root manifests attributed by path in `policy show`.
- The bare-string form parses, merges, compiles, and resolves to the root manifest
  unchanged; **single-repo output (policy show labels, existing goldens) is
  byte-identical** to before this change.
- `init` on a monorepo writes one **object-form** generator entry per detected
  manifest (root + ≤2 levels deep), skipping `node_modules/`/`vendor/` and
  symlinked dirs; the skip-set is sourced from generator-declared metadata.
- `make test`, `make lint`, `make golden` (reviewed) are green.

## What We're NOT Doing

- New ecosystem generators (`pyproject_toml`, etc.) — out of scope.
- Workspace-declaration parsing (npm `workspaces`, pnpm, composer path repos).
- A `run`-time / background re-scan or a "rescan-merge" command.
- Per-generator options beyond the manifest path.
- Changing the rule-dedupe key (decision 3: dedupe stays as-is).

## Key Design Points

- **The generator/runner/cache stay path-unaware.** The path is woven into the
  source label by the **compiler** (in the fan-out stamping loop), *after* the
  content-addressed cache returns rules. This keeps the cache correct and shared
  across identical-byte manifests at different paths, and leaves the runner seam
  signature `(name, []byte)` untouched.
- **Generator-owned metadata** is the single source of truth for (a) the manifest
  filename a type recognizes and (b) its dependency-dir name(s). Both the compiler
  (default-path resolution) and the `init` scanner (which files to look for + the
  skip-set) consume it, collapsing the two duplicated `type→filename` tables.
- **Strict decode preserved.** The object form is captured as `[]yaml.Node` in the
  raw mirror (no custom `UnmarshalYAML`, so top-level `KnownFields` stays
  effective) and converted in a post-decode pass that manually validates the inner
  keys (`type` required, `path` optional, nothing else). A test must confirm both
  an unknown top-level key and an unknown key inside a generator object are still
  rejected.

---

## Phase 1 — Generator-owned metadata

Add the metadata surface the compiler and scanner will consume, without changing
any existing behavior.

### Changes

**`internal/generator/manifest.go`** — extend the `ecosystem` interface and impls:
```go
type ecosystem interface {
    name() string
    manifestFile() string      // e.g. "package.json"
    dependencyDirs() []string  // e.g. []string{"node_modules"}
    deps(manifest []byte) ([]string, error)
}
```
- `packageJSON`: `manifestFile() == "package.json"`, `dependencyDirs() == []string{"node_modules"}`.
- `composerJSON`: `manifestFile() == "composer.json"`, `dependencyDirs() == []string{"vendor"}`.

**`internal/generator/generator.go`** — add a package-level table + public accessors:
```go
// the single registry of known ecosystems
var ecosystems = []ecosystem{packageJSON{}, composerJSON{}}

type Metadata struct {
    Type           string
    ManifestFile   string
    DependencyDirs []string
}

func All() []Metadata { /* map over ecosystems */ }
func Lookup(typ string) (Metadata, bool) { /* by name */ }
```
- Reimplement `Known(name)` via `Lookup` (drop the literal switch). Keep `New`'s
  switch (it must wire the right registry client per ecosystem) but it may locate
  the ecosystem through `ecosystems`/`Lookup` for the name check.

### Tests
- Table test asserting `All()` returns both types with correct filename +
  dependency dirs, and `Lookup` round-trips / rejects unknown.
- Existing generator tests stay green (no behavior change).

### Success criteria
- [x] `make test` green; `go build ./...` clean.
- [x] `generator.All()` / `generator.Lookup` / `generator.Known` all derive from
      the single `ecosystems` table.

---

## Phase 2 — Config schema: object form, merge, validation

Change the config type and parsing; keep the build green by updating the compiler
in the **same phase** (it is the only other reader of `.Generators` besides
`init`, handled in Phase 3 — `init`'s reader is adapted minimally here to compile).

### Changes — config package

**`internal/config/config.go`**
- Public type:
  ```go
  type Generator struct {
      Type string
      Path string // optional; "" = root default manifest
  }
  ```
  `Egress.Generators` becomes `[]Generator`.
- Raw mirror: `rawEgress.Generators` becomes `[]yaml.Node` (captures bare scalar
  or mapping verbatim; no custom unmarshaler → top-level `KnownFields` intact).
- `Parse` (`config.go:140-148`): the whole-struct `Egress(raw.Network.Egress)`
  conversion no longer type-checks (Generators types diverge). Construct `Egress`
  field-by-field (copy `Allow`/`DenyAlways`), and **parse generators in
  `applyDefaults`** (mirrors how `HostServices` is filled there today).

**`internal/config/validate.go`** (or a new `generators.go`)
- `parseGenerator(node *yaml.Node) (Generator, error)`:
  - `ScalarNode` → `Generator{Type: node.Value}`.
  - `MappingNode` → manually walk `node.Content` key/value pairs; allow only
    `type` (required, non-empty) and `path` (optional); reject any other key with
    a message citing `node.Line`. Return `Generator{Type, Path}`.
  - else → validation error.
- Call it from `applyDefaults`, appending to `c.Network.Egress.Generators` and
  recording issues on `verr` (same shape as the `parseHostService` loop).
- Optionally also validate `Type != ""` here (the manual key check already
  enforces it). Do **not** add a dependency on the `generator` package — keep the
  `generator.Known` validity check in the compiler as today.

**`internal/config/merge.go`**
- Replace the `dedupeStrings` call for generators (`merge.go:34`) with a new
  `dedupeGenerators([]Generator) []Generator` keyed on the comparable struct
  (`map[Generator]bool`, the `dedupeHostServices` precedent). Identity = `(Type,
  Path)`; bare (`Path:""`) is distinct from an explicit root path — the compiler
  collapses functionally-equal entries by *resolved* path (Phase 2 compiler).

### Changes — compiler (same phase, to keep build green)

**`internal/policy/compile/compile.go`**
- Delete `manifestFiles` (`:55-58`); resolve the default filename via
  `generator.Lookup(typ).ManifestFile`.
- `mergeGenerators(global, project []config.Generator) []config.Generator`,
  keyed on `(Type, Path)`.
- Introduce a resolved-input model:
  ```go
  type resolvedGenerator struct{ Type, Path string } // Path always concrete
  type manifestInput struct {
      gen  resolvedGenerator
      data []byte
  }
  ```
  - `resolveGenerators([]config.Generator) ([]resolvedGenerator, error)`: fill
    `Path` from `Lookup` default when empty; validate `generator.Known(Type)`;
    **dedupe by resolved `(Type, Path)`** so two entries pointing at the same file
    run once. Preserve config order for determinism.
- `readManifests(projectDir, []resolvedGenerator) ([]manifestInput, error)`: read
  `filepath.Join(projectDir, rg.Path)` via `c.fs.ReadFile`; absent (`ErrNotExist`)
  → skip (contributes nothing). Return only present manifests, order preserved.
- `inputHash`: rekey the `Manifests` map from type to **`Type + ":" + Path`**
  (`compile.go:382-385`) so two manifests of one type don't overwrite each other;
  `json.Marshal` sorts keys → deterministic.
- `runGenerators(ctx, []manifestInput) ([]policy.Rule, error)`: iterate inputs,
  `runner.Run(ctx, in.gen.Type, in.data)`, stamp each rule. **Weave the path into
  the source** here, only for non-root manifests:
  ```go
  src := gr.Source // "generated:<type>:<pkg>"
  if filepath.Dir(in.gen.Path) != "." {
      prefix := "generated:" + in.gen.Type + ":"
      src = prefix + in.gen.Path + ":" + strings.TrimPrefix(gr.Source, prefix)
  }
  r.Source = src
  ```
- `Refresh` (`compile.go:261-...`): its per-type `in.manifests[name]` loop and the
  `runner.Invalidate(name, manifest)` call must iterate `manifestInput`s too (one
  Invalidate per manifest input).
- Thread `[]manifestInput` through `compileInputs`/`build`/`buildRuleSet`
  accordingly.

### Tests
- Config: table tests for `parseGenerator` (bare scalar; `{type,path}`;
  `{type}` only; missing `type`; unknown inner key → error; non-mapping/non-scalar
  → error). A strict-decode test asserting an unknown **top-level** key is still
  rejected when generators use object form (guards the `yaml.Node` capture
  assumption). Merge/dedupe test: `(package_json,"")` + `(package_json,"a/package.json")`
  stay distinct; exact dup collapses.
- Compiler (hermetic, via the `generatorRunner` fake): two `package_json` entries
  with different paths each read their manifest and both contribute rules; bare
  entry resolves to `package.json`; resolved-path dedupe runs a shared file once;
  input hash changes when *either* manifest's bytes change; root manifest keeps
  the bare `generated:package_json:<pkg>` label while a sub-package manifest gets
  `generated:package_json:<path>:<pkg>`.
- Update existing callers/tests that constructed `Generators: []string{...}` to
  `[]config.Generator{{Type: ...}}` (e.g. `config_test.go:53`, compiler tests).

### Success criteria
- [x] `make test` green; `go build ./...` clean; `make lint` green.
- [x] Compiled policy contains rules for every listed manifest (AC #1).
- [x] Bare form resolves to root and is byte-identical in output (AC #2);
      existing compiler/policy goldens unchanged (only input_hash format changed).
- [x] `policy show` attributes a non-root rule by path and disambiguates two
      same-type generators (AC #7); root-only output unchanged.
- [x] Input hash watches every referenced manifest (AC #5).

---

## Phase 3 — `init` bounded scan + object-form emission

### Changes

**`internal/cli/init.go`**
- Replace `detectGenerators(fsys, dir) []string` with
  `scanGenerators(fsys, dir) []config.Generator`:
  - Build a `filename → type` map and the dependency-dir **skip-set** from
    `generator.All()` (union of `DependencyDirs`).
  - Recursive helper `scan(dir, depth)`:
    - `fsys.ReadDir(dir)` (sorted; treat `ErrNotExist` as empty).
    - For each entry: if it is a file whose name is a known manifest filename →
      emit `config.Generator{Type: typ, Path: rel(projectRoot, dir/name)}`.
    - If it is a directory, `depth < 2`, name not in skip-set, name not
      dot-prefixed (scan hygiene; skips `.git` etc.), and **not a symlink**
      (`entry.Type()&fs.ModeSymlink == 0`) → `scan(dir/name, depth+1)`.
  - Start at `scan(projectDir, 0)`; manifests land at depth 0/1/2 inclusive.
  - **Sort** results by `(depth asc, path asc)` for deterministic, root-first
    golden output.
- Emission: all entries object-form with explicit `path` (decision 2). Replace
  `generatorsBlock`'s bare `- <name>` loop (`init.go:130-135`) with an object-form
  renderer:
  ```
        - type: package_json
          path: apps/web/package.json
  ```
  Keep the empty-case commented placeholder (update it to object form for accuracy).
  Remove the inline `{manifest,name}` table (`init.go:80-83`) — filenames now come
  from `generator.All()`.
- `generatorsNote` (`init.go:92-97`): update the summary line to reflect counts
  (e.g. `(generators: 3 manifests across package_json, composer_json)`); keep the
  "no manifests detected" branch.
- No change to the no-clobber gate or atomic write.

**`internal/sysdep/sysdeptest/filesystem.go`** (if needed for symlink test)
- The fake `ReadDir`/`fakeDirEntry` may need a `Symlinks` set so a test can mark a
  directory entry as a symlink and assert the scan does not follow it. Add minimally
  if the symlink-skip path can't otherwise be exercised hermetically.

### Tests
- Unit (`init_test.go`): extend `initFixture` cases —
  - monorepo: root + `apps/web/package.json` + `apps/api/package.json` +
    `services/php/composer.json` → four object-form entries, sorted root-first.
  - depth bound: `apps/web/sub/package.json` (depth 3) is **not** emitted.
  - dependency-dir trap: `node_modules/x/package.json` and `vendor/y/composer.json`
    are **not** emitted (AC #4), skip-set sourced from `generator.All()` (AC #5).
  - symlink: a symlinked subdir is not descended into.
  - Regenerate `testdata/init/*.golden`; `TestRenderConfigTemplateParses` must
    re-parse and equal the scanned `[]config.Generator`.
- Testscript (`init.txtar`): add a monorepo layout (txtar blocks at sub-paths) and
  assert the object-form `type:`/`path:` lines and the absence of `node_modules`/
  `vendor` manifests; keep the existing empty/`--force`/no-clobber scenarios.

### Success criteria
- [ ] `make test` green; `make golden` reviewed.
- [ ] `init` writes one entry per detected manifest ≤2 levels deep (AC #3).
- [ ] No entry for manifests under `node_modules/`/`vendor/` (AC #4); skip-set is
      generator-sourced (AC #5); scan bounded to ≤2 levels (AC #6).
- [ ] No-clobber + `--force` preserved (AC #7 of ticket / AC-0029).

---

## Phase 4 — Docs, edit-robustness, and final review

### Changes
- **`docs/design.md`** — "Allowlist generators": document the object form, the
  monorepo multi-manifest model, the `init` bounded scan + generator-declared
  dependency-dir skip-set, and the path-in-attribution rule (root bare,
  sub-package path-qualified). Update the `generators:` example
  (`design.md:115-119`) and the `init` comment (`design.md:358-361`). Adjust the
  attribution example if showing a monorepo case.
- **`internal/config/edit.go`** — no logic change (it never edits generators), but
  add a golden fixture `with_generators_objform.{in,golden}.yaml` proving the
  comment-preserving `allow` splice still works when `generators` is multi-line
  object form. Add the case to `edit_test.go`.
- **Example config / parse fixtures** — update any example `.agent-creance.yaml`
  used in tests to keep parsing (bare form still valid; add an object-form example
  if one illustrates the schema).
- Final pass: `make test`, `make lint`, `make golden` (review the diff), and a
  manual `policy show` sanity check description in the ticket close-out.

### Success criteria
- [ ] `make test`, `make lint`, `make golden` (reviewed) all green (AC #8).
- [ ] `design.md` documents the object form, scan, and attribution.
- [ ] `edit.go` golden proves object-form generators survive an `allow` splice.

---

## Testing Strategy

- **Pure logic** (parse, merge/dedupe, path resolution, scan) → table-driven tests.
- **Generated artifacts** (`policy.json`, init template) → golden tests with
  `-update`; review every regenerated golden.
- **CLI behavior** (`init` monorepo) → hermetic testscript with txtar manifest
  blocks at sub-paths; `PATH=$CREANCE_BIN`.
- **Compiler** → hermetic via the `generatorRunner` fake; no real registry calls.
- External tools never invoked in unit tests.

### Automated checks (from `.claude/tce/profile.md`)
- `make test` (= `go test -race ./...`)
- `go build ./...` (typecheck)
- `make lint` (= `go vet ./...` + `golangci-lint run`)
- `make golden` (= `go test ./... -update`) — review the diff

### Manual verification
- `init` in a sample monorepo writes one object-form entry per package, none under
  `node_modules/`/`vendor/`.
- `policy show` on a multi-package config attributes each rule to its manifest;
  single-repo `policy show` output is unchanged from before.
- An existing single-repo `.agent-creance.yaml` (bare `generators:`) still parses,
  compiles, and runs untouched.

## Risks / Open Verifications

- **`yaml.Node` + `KnownFields`:** capturing generators as `[]yaml.Node` must not
  disable top-level strict decode. Phase 2 includes an explicit test asserting an
  unknown top-level key is still rejected with object-form generators. If that
  assumption fails, fall back to capturing as `[]interface{}` (string |
  `map[string]interface{}`) with manual key validation — same post-decode shape.
- **Cache-label correctness:** confirmed safe because the path is injected by the
  compiler *after* the content-addressed cache, so identical-byte manifests at
  different paths share cache entries yet get distinct labels.
- **Golden churn:** monorepo additions and the init object-form switch regenerate
  several goldens; single-repo goldens must stay byte-identical (root-bare label
  rule). Review each diff.

## References

- Research: `thoughts/shared/research/2026-06-09-AC-0036-monorepo-multi-manifest-generators.md`
- Ticket: `thoughts/shared/tickets/AC-0036-monorepo-multi-manifest-generators.md`
- Key files: `internal/config/{config,validate,merge,edit}.go`,
  `internal/generator/{generator,manifest,cache}.go`,
  `internal/policy/compile/compile.go`, `internal/policy/render/render.go`,
  `internal/cli/init.go`, `internal/sysdep/{filesystem.go,sysdeptest/filesystem.go}`.
