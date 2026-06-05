---
date: 2026-06-05
ticket: AC-0013
status: ready
research: thoughts/shared/research/2026-06-05-AC-0013-policy-compiler.md
tags:
  - plan
  - AC-0013
  - policy-compiler
  - WP-2.4
---

# AC-0013 — Policy compiler → `policy.json` with input-hash cache (WP-2.4)

## Overview

Build the compiler that unions explicit + global + generated + session-overlay rules
into an out-of-tree, source-annotated, versioned `policy.json`, gated by an input-hash
cache so unchanged-config runs skip regeneration. This is the WP-2.4 convergence point;
nearly all dependencies (config loader, generators, state paths, matcher schema, atomic
write + golden patterns) already exist and are reused.

Checkpoint decisions (confirmed): **(1)** the artifact carries a top-level
`version: 1`; **(2)** provenance is recovered by a *compiler-owned layered load* (a thin
additive method on `config.Loader`, no change to the fused `Load`); **(3)** the cache key
is `sha256` over a canonical serialization of the resolved config layers + referenced
manifest bytes, stored as an in-artifact `input_hash` field. Plus two self-decided
points carried from research: the artifact preserves the generator `lower_trust` flag,
and "touching the overlay" (criterion 4) is interpreted as an overlay *content* change.

## Current State

- `internal/policy` (pure: no I/O) holds `Rule`/`RuleSet`/`FromConfig` — already the
  de-facto `policy.json` shape, but with **no `source` and no `version`**
  (`policy.go:60-138`). The decision-vector corpus consumes `RuleSet`
  (`vectors_test.go:20`).
- `config.Loader.Load` fuses global+project+includes and **discards provenance**
  (`load.go:48`, `merge.go`); the recursive `resolve` is unexported (`load.go:77`).
- `generator.New(name, fs, clock, getter, registriesRoot, generatorsRoot)` +
  `(*Generator).Generate(ctx, manifest []byte) ([]Rule, error)` exist; `generator.Rule`
  carries `Source` + `LowerTrust` (`generator/generator.go:43,62`, `rule.go:31`).
- `state.Resolver.Resolve` → `Layout{Canonical,Hash,Root}` with `PolicyJSON()`,
  `SessionOverlay()`, `GeneratorsRoot()`, `RegistriesRoot()` (`state.go:77-171`).
- Atomic-write idiom + content-hash cache patterns exist in `generator/cache.go`; golden
  `-update` pattern in `prereq/report_test.go`. `sysdep.FileSystem` is the I/O seam;
  `FakeFileSystem` does **not** model mtime.

## Desired End State

A new package compiles a project's effective config to
`~/.cache/agent-creance/projects/<hash>/policy.json`:
```json
{
  "version": 1,
  "input_hash": "<sha256-hex>",
  "allow": [ { "host": "...", "paths": ["..."], "mode": "intercept",
              "source": "explicit" | "global" | "once" | "generated:package_json:react",
              "lower_trust": true } ],
  "deny_always": [ { "host": "...", "reason": "...", "source": "global" } ]
}
```
Re-compiling with identical inputs is a no-op (no generator run, no file rewrite, no
mtime bump). Mutating any input (YAML, manifest, or overlay content) forces a rebuild.
Nothing is ever written inside the project tree.

## What We're NOT Doing

- No Seatbelt `.sb` compilation (AC-0014). No matcher changes beyond additive schema
  fields (AC-0010 owns matching). No `allow --once` *writer* (AC-0030) — we only *read*
  the overlay. No `policy show`/`policy explain` CLI command and no `App` wiring (later
  tickets) — AC-0013 ships the library compiler and its tests only. No lock-file /
  proxy-hot-reload integration (lifecycle ticket).

## Implementation Approach

A new sub-package `internal/policy/compile` holds the side-effecting `Compiler`
(honoring the ticket's "internal/policy compiles…" while keeping the parent pure; the
`generator/registry` sub-package sets the precedent). The compiler is driven through
injected `sysdep` seams plus a small `generatorRunner` interface so its unit tests are
hermetic (no HTTP, no real generator — those are already covered by AC-0012 and an
opt-in integration test here).

---

## Phase 1 — Artifact schema (`internal/policy`)

Extend the pure schema so the compiled artifact can carry provenance + version.

### Changes — `internal/policy/policy.go`
1. Add two `omitempty` fields to `Rule`, with a doc note that they are
   **compiler-populated and matcher-ignored**:
   ```go
   Source     string `json:"source,omitempty"`      // provenance; ignored by Decide
   LowerTrust bool   `json:"lower_trust,omitempty"`  // generator low-trust flag; ignored by Decide
   ```
2. Add the on-disk artifact type + version constant:
   ```go
   // CompiledVersion is the policy.json schema version (cross-language contract with
   // enforcer.py). Bump only on a breaking change to the artifact shape.
   const CompiledVersion = 1

   // Compiled is the on-disk policy.json: a versioned, input-hash-keyed RuleSet whose
   // rules carry source annotations. The embedded RuleSet promotes allow/deny_always to
   // the top level, so the enforcer reads {version,input_hash,allow,deny_always}.
   type Compiled struct {
       Version   int    `json:"version"`
       InputHash string `json:"input_hash"`
       RuleSet
   }
   ```
3. Add a tiny exported converter the compiler reuses (avoids duplicating the
   `*[]string` deref): `func RuleFromConfig(r config.Rule) Rule` returning a `policy.Rule`
   with Source left empty (the caller stamps it). Refactor the existing
   `rulesFromConfig` to call it.

### Verify
- `go build ./...`; `go test -race ./internal/policy/...` — decision vectors must still
  pass (new fields are `omitempty`, absent in the corpus). `make lint`.

### Success criteria
- [ ] `policy.Rule` has `Source`/`LowerTrust`; `policy.Compiled` + `CompiledVersion` exist.
- [ ] All existing policy tests + vectors green; build + lint clean.

---

## Phase 2 — Layered config loading (`internal/config`)

Give the compiler per-layer access without touching the fused `Load` or `merge`.

### Changes — `internal/config/load.go`
1. `func (l *Loader) GlobalPath() (string, error)` — returns
   `~/.config/agent-creance.yaml` (extract the literal currently inline in `Load`;
   refactor `Load` to call it — behavior unchanged).
2. `func (l *Loader) ResolveLayer(path string, optional bool) (*Config, error)` — wraps
   the unexported `resolve(path, home, optional, nil, 0)`, clears `Include`, returns the
   single layer (no implicit-global merge). Doc: used by the policy compiler to annotate
   provenance; `optional=true` for the global baseline and the session overlay.

### Tests — `internal/config/load_test.go` (extend)
- `ResolveLayer` resolves a file + its `include:` chain but does **not** pull in the
  implicit global; `optional=true` + missing file → empty `*Config`; `optional=false` +
  missing → error; cycle/depth errors still surface.
- `GlobalPath` returns the expected path via the fake home dir.

### Success criteria
- [ ] New methods exist; `Load`'s behavior is unchanged (existing loader tests green).
- [ ] `go test -race ./internal/config/...`, build, lint clean.

---

## Phase 3 — The compiler (`internal/policy/compile`) + hermetic tests

### New file — `internal/policy/compile/compile.go`

**Types & construction**
```go
type generatorRunner interface {
    Run(ctx context.Context, name string, manifest []byte) ([]generator.Rule, error)
}

type Compiler struct {
    fs      sysdep.FileSystem
    loader  *config.Loader
    state   *state.Resolver
    runner  generatorRunner
}

type Result struct {
    PolicyPath string
    InputHash  string
    Skipped    bool   // true == cache hit, nothing regenerated
    AllowCount int
    DenyCount  int
}

func New(fs sysdep.FileSystem, paths sysdep.PathResolver, clock sysdep.Clock,
         getter sysdep.HTTPGetter) (*Compiler, error)
```
`New` builds `config.NewLoader(fs,paths)`, `state.New(paths)`, and the production
`realGenerators` runner (closure over `generator.New(name, fs, clock, getter,
RegistriesRoot, GeneratorsRoot)` → `Generate`). A second constructor
`newWithRunner(...)` (unexported, test-only) injects a fake runner.

**`Compile(ctx, projectDir) (Result, error)`** flow (cache check precedes generator run,
so a hit yields runner call-count 0 — criterion 3):
1. `layout := state.Resolve(projectDir)`.
2. Layered load (provenance): `global := loader.ResolveLayer(loader.GlobalPath(), true)`,
   `project := loader.ResolveLayer(layout.Canonical, false)`,
   `overlay := readOverlay(layout.SessionOverlay())` (parse with `config.Parse` if the
   file exists via `fs.ReadFile`, else empty `*Config`).
3. Resolve the generator set: union of `global` + `project` generators (dedupe, stable
   order). Validate each with `generator.Known` → error on unknown.
4. Read manifests: map `package_json`→`package.json`, `composer_json`→`composer.json`;
   `fs.ReadFile(filepath.Join(layout.Canonical, file))`. A missing manifest is a no-op
   for that generator (empty bytes; contributes nothing) — not an error.
5. `hash := inputHash(global, project, overlay, manifests)` — `sha256` over
   `json.Marshal(struct{Global,Project,Overlay *config.Config; Manifests map[string]string})`
   (map keys sorted by `json.Marshal`; manifest bytes as strings). Deterministic and
   environment-independent (resolved `Config` carries no absolute paths).
6. Cache check: `fs.ReadFile(layout.PolicyJSON())`; if it unmarshals to `policy.Compiled`
   with `.Version == CompiledVersion` and `.InputHash == hash` → return
   `Result{Skipped:true,…}` **without** running generators or rewriting (no mtime bump).
7. Miss → build the annotated `RuleSet`:
   - `annotate(project.Egress.Allow, "explicit")`, `annotate(global…, "global")`,
     `annotate(overlay…, "once")` (helper derefs `*[]string`, stamps `Source`).
   - generated: for each generator `runner.Run` → `[]generator.Rule`; each `gr.Rule`
     with `Source=gr.Source`, `LowerTrust=gr.LowerTrust` (generated rules are allow-only).
   - `Allow = global ++ explicit ++ generated ++ once`;
     `DenyAlways = global ++ explicit ++ once`. Then **dedupe keeping first occurrence**
     (compare matcher fields host/paths/methods/mode/reason, ignore source — global-first
     ordering makes the dedupe outcome mirror `merge.go`). Normalize empty → nil.
8. Marshal `policy.Compiled{Version:CompiledVersion, InputHash:hash, RuleSet:rs}` with
   `json.MarshalIndent(…, "", "  ")` + trailing newline; atomic write to
   `layout.PolicyJSON()`: `MkdirAll(layout.Root, 0o755)` → `WriteFile(path+".tmp", …,
   0o644)` → `Rename` → best-effort `Remove` on failure (mirror `generator/cache.go`).
9. Return `Result{Skipped:false, …}`.

### Tests — `internal/policy/compile/compile_test.go` (+ `testdata/`)
All hermetic via `FakeFileSystem`/`FakePathResolver`/`FakeClock` and a fake
`generatorRunner` returning canned `generator.Rule`s. Seed the fake fs with a project
`.agent-creance.yaml`, a global `~/.config/agent-creance.yaml`, a `session-overlay.yaml`
in the layout root, and a `package.json`; configure the fake path resolver so
`EvalSymlinks(projectDir)` succeeds.

1. **Golden** (`testdata/policy.golden`, `-update` flag): representative config
   (explicit allow+deny, one global rule, generated rules from the fake runner, one
   overlay `once` rule) → marshal `Compiled` → compare. Asserts version, deterministic
   `input_hash`, per-rule `source`, `lower_trust` passthrough, and union/dedupe order.
2. **Cache hit** (criterion 3): compile, then compile again with identical inputs →
   second `Result.Skipped == true` **and** fake runner call-count == 0 on the second
   call; assert `policy.json` bytes unchanged.
3. **Cache miss** (criterion 4): from a compiled state, in separate sub-cases mutate
   (a) a project `allow` rule, (b) a `package.json` byte, (c) the overlay content →
   each recompiles (`Skipped==false`, runner called).
4. **C4 guard** (criterion 5): after a full compile, assert no path written in the fake
   fs is under `projectDir` (everything lives under `layout.Root`).
5. **Annotation**: explicit/global/once/generated sources land on the right rules;
   generated `lower_trust` is preserved.
6. **Unknown generator** → error; **absent manifest** → generator contributes no rules
   (no error).

### Success criteria
- [ ] `go build ./...`; `go test -race ./internal/policy/...` green (incl. golden).
- [ ] `make golden` diff reviewed; `make lint` clean.
- [ ] Cache-hit test proves runner call-count == 0; cache-miss covers YAML, manifest,
      and overlay mutations; C4 guard proves no in-tree write.

---

## Phase 4 — Integration test + close ticket

### New file — `internal/policy/compile/compile_integration_test.go` (`//go:build integration`)
Mirror AC-0012's live-generator test: build a `Compiler` with the **real** runner
(`OSHTTPGetter`, real `state` roots under a temp `XDG_CACHE_HOME`), compile a tiny
fixture project containing a minimal `package.json` (e.g. one `react` dep), and assert
the artifact contains `generated:package_json:react`-sourced rules and the GitHub
companion hosts. Robust assertions (host presence, not exact paths) per AC-0012 style.

### Close-out
- Run `make test`, `make test-integration`, `go build ./...`, `make lint`,
  `make golden` (review diff). Tick all AC-0013 acceptance criteria.
- Update the ticket: set **Status: Done**, fill Implementation Plan/Notes, link this plan
  and the research doc.

### Success criteria
- [ ] All four acceptance criteria in the ticket satisfied; all six verification steps
      pass. Integration test green. Ticket marked Done.

---

## Testing Strategy

- **Automated (from profile):** `make test` (= `go test -race ./...`), `go build ./...`,
  `make lint` (= `go vet` + `golangci-lint`), `make golden` (review diff),
  `make test-integration` for the live path.
- **Pure logic → table tests** (loader methods, annotate/dedupe, input hash). **Generated
  artifact → golden** (`policy.json`). **No testscript** — no CLI surface in scope.
- **Manual:** review the golden `policy.json` diff for correct version, sources,
  `lower_trust`, and union order.

## Risks / Notes

- **Schema additions to `policy.Rule`** could in principle perturb the cross-language
  vectors — mitigated by `omitempty` (absent in the corpus); Phase 1 verifies.
- **`input_hash` in the golden** is deterministic but recomputes if canonicalization
  changes — regenerate via `-update` and review.
- **Missing-manifest leniency** (no-op, not error) is a deliberate interpretation; an
  unknown *generator name* is still a hard error.
- **mtime not modeled** by the fake fs, so the cache is content-hash based (criterion 4
  tested as content mutation), consistent with the registry layer's Clock-based design.
