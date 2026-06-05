---
date: 2026-06-05
ticket: AC-0012
title: "Allowlist generators (package_json, composer_json) (WP-2.3)"
status: complete
research: thoughts/shared/research/2026-06-05-AC-0012-allowlist-generators.md
git_commit: 8cfd78e
branch: main
depends_on: AC-0011
feeds: AC-0013
---

# Implementation Plan: AC-0012 — Allowlist generators (`package_json`, `composer_json`) (WP-2.3)

## Overview

Add the top-level `internal/generator` package: two generators (`package_json`,
`composer_json`) that read a dependency manifest, look up each direct dependency
through the AC-0011 registry client, and emit a deterministic, **source-annotated**
set of egress allow rules — a host-wide-or-path-scoped homepage rule, an
`<org>/<repo>`-scoped repository rule, and the forge **content-host companion set**
(GitHub + GitLab, table-driven). Output is cached keyed on the manifest hash so an
unchanged manifest reuses the prior rule set with zero registry calls.

This ticket stops at "manifest bytes + registry → annotated rules". Wiring the
output into the compiled `policy.json` and the `App`/CLI is AC-0013 (WP-2.4); this
plan deliberately adds no CLI consumer, mirroring how AC-0011 stopped short of
wiring.

## Current State

- `internal/generator/` contains **only** the `registry/` subpackage (AC-0011); no
  top-level generator code, no manifest parsing, no rule emission.
- `registry.Metadata{Homepage, Repository}` + `NewNPM`/`NewPackagist` +
  `(*Client).Lookup(ctx, pkg)` + `ErrNotFound` are in place
  (`internal/generator/registry/registry.go:56,61,88,118`). Lookups are cached
  per-package (30-day lazy refresh); steady state is zero HTTP.
- The rule model is ready: `policy.Rule{Host, Paths, Methods, Mode, Reason}`
  (`internal/policy/policy.go:68`) with **prefix-by-default** path matching
  (`internal/policy/glob.go:30`) and `*`/`*.suffix`/exact host matching
  (`glob.go:10`). **No provenance/source or trust field exists on any Rule type.**
- `state.RegistriesRoot()` (`internal/state/state.go:116`) is the cache-path helper
  pattern to mirror; there is no `generators/` sibling helper yet.
- `config.Egress.Generators []string` is parsed + include-merged
  (`config.go:67`, `merge.go:34`) but **not validated** and **not dispatched** to any
  implementation.

## Desired End State

- `generator.New(name, fs, clock, http, registriesRoot, generatorsRoot)` returns a
  `*Generator` for `"package_json"` / `"composer_json"`, wired to the right registry
  client; an unknown name returns an error. `generator.Known(name) bool` reports
  recognised names.
- `(*Generator).Generate(ctx, manifest []byte) ([]generator.Rule, error)` returns the
  annotated allow rules for a manifest's direct deps, deterministically ordered.
- Each `generator.Rule` carries the emitting `policy.Rule`, a `Source`
  (`generated:package_json:react`), and a `LowerTrust` flag (for
  `objects.githubusercontent.com`).
- Homepage rules are host-wide for a bare-host homepage, path-scoped for a
  path-carrying one. Repository rules are `<org>/<repo>`-scoped; GitHub/GitLab repos
  additionally emit the forge companion hosts. Missing homepage/repository (or a
  `registry.ErrNotFound`) emits **no** rule for that facet (no error).
- The emitted rule set is cached at
  `<cache>/agent-creance/generators/<name>/<sha256(manifest)>.json`; an unchanged
  manifest is a cache hit that makes **zero** `Lookup` calls.
- `make test` (race), `go build ./...`, `make lint` clean; golden rule-sets reviewed.
  One real npm + one real Packagist lookup pass under `make test-integration`.

### Key design decisions (from research)

- **Output type is a generator-local wrapper, not a change to `policy.Rule`.** AC-0012
  is the first place provenance enters the codebase; folding `Source` into
  `policy.json` is AC-0013's call. Wrapper: `Rule{ Rule policy.Rule; Source string;
  LowerTrust bool }`.
- **Homepage scoping is purely path-presence** (`design.md:162-167`): URL has a
  non-empty path → scope to it; else host-wide. No shared-host table.
- **Forge content-hosts are data** (`design.md:169-176`), keyed by repository web
  host. Ship **GitHub and GitLab** rows (per the checkpoint decision), structured so
  another forge is a one-row edit; the same table backs a future
  `agent-creance allow <repo-url>`.
- **Output cache is content-addressed on the manifest bytes** (+ generator name);
  no TTL (per-package freshness is the registry client's 30-day cache; `policy
  refresh`/AC-0016 is the manual escape hatch). Atomic write-then-rename, mirroring
  `registry.writeCache` (`registry.go:217`).
- **Lookups go through a small `lookuper` interface**, not the concrete
  `*registry.Client`, so the generator is unit-testable with a call-counting fake and
  no HTTP/fs.
- **`Mode` is left unset** on generated rules (default-intercept semantics); AC-0013
  normalises modes when compiling, exactly as `config.applyDefaults` does for
  explicit rules. Keeps generator output minimal and golden files clean.
- **Generator name validation lives in the `generator` package** (`Known`/`New`), not
  in `config.validate`, so `config` does not import `generator`.

## What We're NOT Doing

- No CLI/`App` wiring and no `policy.json` compilation (AC-0013). No `policy
  show`/`explain` rendering of the annotations (AC-0015).
- No transitive deps, lockfile mode, or per-generator options (deferred per design).
- No ecosystems beyond npm/Packagist; no forges beyond GitHub/GitLab.
- No change to `policy.Rule`/`config.Rule`. No `policy refresh` output-cache
  invalidation (AC-0016).
- No registry-client changes (AC-0011 is complete and sufficient).

---

## Phase 1 — `generators/` cache-path helper in `internal/state`

### Changes

**`internal/state/state.go`**: add `generatorsSubdir = "generators"` alongside
`registriesSubdir` (`state.go:33-39`) and a method mirroring `RegistriesRoot`
(`state.go:116-122`):

```go
// GeneratorsRoot returns <cache>/agent-creance/generators — the cross-project home
// of generator output caches (content-addressed by manifest hash; sibling of
// registries/ and projects/<hash>/).
func (r *Resolver) GeneratorsRoot() (string, error) {
    cache, err := r.cacheRoot()
    if err != nil { return "", err }
    return filepath.Join(cache, appCacheSubdir, generatorsSubdir), nil
}
```

### Tests

**`internal/state/state_test.go`** — add `TestGeneratorsRootHonoursXDGThenHome`
mirroring the existing `RegistriesRoot` table: XDG set → `…/generators`; XDG empty →
`$HOME/.cache/agent-creance/generators`; home-error path returns an error.

### Success criteria

#### Automated
- [x] `go test -race ./internal/state/...` passes
- [x] `make lint` clean

---

## Phase 2 — Output rule type, URL normalization, forge table (pure logic)

All net-new files in `internal/generator`. This phase is pure, table-tested logic —
no fs, no network.

### Changes

**`internal/generator/rule.go`** — the annotated output type + source helper:

```go
// Rule is an allow rule emitted by a generator, annotated with the source that
// produced it. LowerTrust marks a host-wide companion host (objects.github-
// usercontent.com) that a stricter threat model can drop.
type Rule struct {
    Rule       policy.Rule `json:"rule"`
    Source     string      `json:"source"`      // "generated:package_json:react"
    LowerTrust bool        `json:"lower_trust,omitempty"`
}

func source(generator, pkg string) string // "generated:" + generator + ":" + pkg
```

**`internal/generator/forge.go`** — repository-URL normalization + the forge table:

- `func normalizeRepoURL(raw string) (host, org, repo string, ok bool)` — handles, in
  order: strip a leading `git+`; scp-like `git@host:org/repo(.git)` (no `://`, a `:`
  separating host from path); `git://`, `ssh://`, `http(s)://` via `net/url`; strip a
  trailing `.git`, trailing `/`, and any `#fragment`/`?query`. `org`/`repo` are the
  first two non-empty path segments (lower-cased host; org/repo verbatim). `ok=false`
  when no host or fewer than two path segments can be extracted (caller emits nothing
  for the repo facet — or a host-wide fallback; see below).
- The forge table (data):

```go
type forgeHost struct {
    host       string // may contain "<org>" placeholder: "<org>.github.io"
    pathTmpl   string // "/<org>/<repo>/", "/<repo>/", or "" for host-wide
    lowerTrust bool
}

var forges = map[string][]forgeHost{
    "github.com": {
        {host: "github.com",                  pathTmpl: "/<org>/<repo>/"},
        {host: "raw.githubusercontent.com",   pathTmpl: "/<org>/<repo>/"},
        {host: "codeload.github.com",         pathTmpl: "/<org>/<repo>/"},
        {host: "<org>.github.io",             pathTmpl: "/<repo>/"},
        {host: "objects.githubusercontent.com", pathTmpl: "", lowerTrust: true},
    },
    "gitlab.com": {
        {host: "gitlab.com",      pathTmpl: "/<org>/<repo>/"},
        {host: "<org>.gitlab.io", pathTmpl: "/<repo>/"},
    },
}
```

- `func repositoryRules(repoURL, src string) []Rule` — normalize; if `!ok` →
  `nil`. If `host` is a known forge → expand its `forgeHost` rows (substitute
  `<org>`/`<repo>` in host + path, `pathTmpl==""` → host-wide, carry `lowerTrust` and
  `src`). Else → a single generic repo rule `{Host: host, Paths: ["/<org>/<repo>/"]}`
  with `src`. Placeholder substitution is a private helper shared with a future
  `allow <repo-url>` (note in doc comment).

**`internal/generator/homepage.go`** — `func homepageRule(homepage, src string)
(Rule, bool)`: parse with `net/url`; need a host (`ok=false`, no rule, otherwise).
Path trimmed of surrounding `/` — empty → host-wide `{Host: host}`; non-empty →
path-scoped `{Host: host, Paths: ["/<path>/"]}` (store with surrounding slashes for
golden readability; the matcher trims anyway).

### Tests

**`internal/generator/forge_test.go`** (table-driven, `require`):
- `normalizeRepoURL`: `git+https://github.com/facebook/react.git`,
  `https://github.com/facebook/react`, `git://github.com/a/b.git`,
  `git@github.com:a/b.git`, `ssh://git@gitlab.com/a/b.git`, trailing `/`, `#readme`,
  a non-forge `https://example.com/x/y`, and failure cases (no host; single segment).
- `repositoryRules`: GitHub URL → exactly the 5 expected rules in order, with
  `objects.githubusercontent.com` host-wide + `LowerTrust:true` and
  `facebook.github.io /react/`; GitLab URL → the 2 expected rules; a non-forge URL →
  one generic `<org>/<repo>` rule; `!ok` → nil. Assert `Source` on every rule.

**`internal/generator/homepage_test.go`**: `https://react.dev/` → host-wide;
`https://someuser.github.io/coollib/` → `someuser.github.io /coollib/`;
`https://laravel.com` → host-wide; an unparseable/no-host value → no rule.

### Success criteria

#### Automated
- [x] `go build ./...`
- [x] `go test -race ./internal/generator/...` passes
- [x] `make lint` clean

---

## Phase 3 — Manifest parsing, generators, dispatch (no cache yet)

### Changes

**`internal/generator/manifest.go`** — the per-ecosystem strategy:

```go
// ecosystem is the per-manifest strategy: its generator name and how to extract the
// direct dependency names from a manifest.
type ecosystem interface {
    name() string                         // "package_json" / "composer_json"
    deps(manifest []byte) ([]string, error)
}
```

- `packageJSON{}`: decode leniently; union the **keys** of `dependencies` +
  `devDependencies`; return sorted-unique names (scoped `@scope/name` preserved).
- `composerJSON{}`: union the **keys** of `require` + `require-dev`; **drop**
  platform/meta requirements that are not Packagist packages: `php`, `php-*`,
  `hhvm`, `ext-*`, `lib-*`, `composer`, `composer-*` (covers `composer-runtime-api`,
  `composer-plugin-api`); return sorted-unique. (A `composer.json` always lists
  `php`; looking it up would 404.)
- Name constants: `GeneratorPackageJSON = "package_json"`,
  `GeneratorComposerJSON = "composer_json"`.

**`internal/generator/generator.go`** — the type, orchestration, and dispatch:

```go
// lookuper is the registry seam the generator depends on, so it is unit-testable
// with a call-counting fake. *registry.Client satisfies it.
type lookuper interface {
    Lookup(ctx context.Context, pkg string) (registry.Metadata, error)
}

type Generator struct {
    eco            ecosystem
    lookup         lookuper
    fs             sysdep.FileSystem
    generatorsRoot string
}

// Known reports whether name is a recognised generator.
func Known(name string) bool

// New constructs the generator for name, wiring the matching registry client.
func New(name string, fs sysdep.FileSystem, clock sysdep.Clock, http sysdep.HTTPGetter,
    registriesRoot, generatorsRoot string) (*Generator, error)
```

- `New`: `package_json` → `newGenerator(packageJSON{}, registry.NewNPM(fs, clock,
  http, registriesRoot), …)`; `composer_json` → `packagistSource` via
  `registry.NewPackagist(...)`; default → `fmt.Errorf("generator: unknown generator
  %q", name)`.
- `(*Generator).Generate(ctx, manifest []byte) ([]Rule, error)`:
  1. `deps := eco.deps(manifest)` (sorted-unique).
  2. For each `pkg`: `md, err := lookup.Lookup(ctx, pkg)`. `errors.Is(err,
     registry.ErrNotFound)` → skip this package (no rules, no error); any other error
     → return it. On success build `src := source(eco.name(), pkg)`, then append
     `homepageRule(md.Homepage, src)` (if `ok` and `Homepage != ""`) and
     `repositoryRules(md.Repository, src)` (empty `Repository` → none).
  3. Return the accumulated `[]Rule` (stable order: deps sorted, then homepage before
     repository, then forge rows in table order).
  - (Output cache is added in Phase 4 — here `Generate` always walks the deps.)

### Tests

**`internal/generator/manifest_test.go`**: parse `testdata/package.json` and
`testdata/composer.json`; assert the dep sets (sorted, scoped name preserved, `php`
/`ext-*` filtered); malformed JSON → error; empty manifest → empty.

**`internal/generator/generator_test.go`** (fake `lookuper`, scripted
`map[string]registry.Metadata` + an `ErrNotFound` package):
- Golden rule-set for `testdata/package.json` → `testdata/package_json.golden`
  (marshal `[]Rule` indented). The fixture exercises **a bare-host homepage**, **a
  `github.io` path-scoped homepage**, **a GitHub repo with the full companion set**,
  **a GitLab repo**, and **a dep with no homepage/repo → nothing**.
- Golden rule-set for `testdata/composer.json` → `testdata/composer_json.golden`
  (incl. `php`/`ext-*` filtered out, a GitHub repo).
- `ErrNotFound` package emits no rules and does not abort the run.
- Empty homepage and empty repository each emit nothing (assert no panic).
- `Known`/`New`: known names construct; unknown name errors.

Golden files via the project's `-update` idiom (`make golden`); fixtures + `.golden`
under `internal/generator/testdata/`.

### Success criteria

#### Automated
- [x] `go build ./...`
- [x] `go test -race ./internal/generator/...` passes
- [x] `make lint` clean

#### Manual
- [x] `make golden` then review `git diff internal/generator/testdata` — the golden
      rule-sets match the design's examples (homepage host-wide vs path-scoped; the
      five GitHub companions incl. lower-trust `objects.githubusercontent.com`; the
      two GitLab rows).

---

## Phase 4 — Manifest-hash output cache

### Changes

**`internal/generator/cache.go`** — content-addressed output cache:

- `cacheKey(manifest []byte) string` = hex `sha256` of the manifest bytes.
- `cachePath` = `filepath.Join(generatorsRoot, eco.name(), key+".json")`.
- On-disk record: `[]Rule` (a stable JSON envelope, e.g.
  `{rules: [...]}` so the file is self-describing and future-extensible).
- `readCache(path) ([]Rule, bool, error)`: `fs.ReadFile`; `fs.ErrNotExist` → miss;
  unparseable → miss (treat like a corrupt registry cache: regenerate); other read
  error → surface.
- `writeCache(path, rules)`: `MkdirAll(dir, 0o755)` → write `<path>.tmp` (`0o644`) →
  `Rename` → best-effort `Remove` on rename failure. Reuse the exact idiom from
  `registry.writeCache` (`registry.go:217-236`).

**`internal/generator/generator.go`** — wrap `Generate` with the cache:
1. `key := cacheKey(manifest)`; `if rules, ok := readCache(...); ok { return rules }`
   — **no dep walk, zero `Lookup` calls**.
2. Otherwise run the Phase-3 dep walk, `writeCache`, return.

### Tests

**`internal/generator/cache_test.go`** (`FakeFileSystem` + counting fake `lookuper`):
- **Cache miss then hit**: first `Generate` records N `Lookup` calls and writes the
  cache file; a second `Generate` with the **same** manifest returns the identical
  rules and records **zero** additional `Lookup` calls (the acceptance test —
  `fakeLookuper.Calls == 0` on run 2).
- **Changed manifest → miss**: a one-byte change to the manifest produces a different
  key and re-walks (Lookup count > 0).
- **Unparseable cache file → regenerate** (seed garbage at the key path).
- **Atomic write**: a `RenameErr` from the fake fs leaves no partial final file.

### Success criteria

#### Automated
- [x] `go test -race ./internal/generator/...` passes (incl. zero-Lookup-on-hit)
- [x] `make lint` clean

---

## Phase 5 — Live integration test + ticket close

### Changes

**`internal/generator/live_integration_test.go`** (`//go:build integration`) —
construct via `New("package_json", OSFileSystem{}, OSClock{}, OSHTTPGetter{},
t.TempDir(), t.TempDir())` and:
- npm: `Generate(ctx, []byte(` a minimal `{"dependencies":{"react":"*"}}` `))` →
  asserts the rule set contains `github.com /facebook/react/` and the
  `raw.githubusercontent.com` companion (react's repo is on GitHub) and a non-empty
  homepage rule.
- Packagist: a `{"require":{"monolog/monolog":"*"}}` manifest → asserts a
  `github.com /Seldaek/monolog/`-style repository rule.
- A second `Generate` with the same manifest is an output-cache hit (fast; no error).

(Real-world repo casing/orgs may drift; assert on host + the `<org>/<repo>` shape
extracted from the live metadata rather than hard-coding, to avoid brittleness.)

**No `buildinfo` bump** — `TestedVersions` tracks external CLI tools, not registries
(same rationale as AC-0011).

**Ticket**: `thoughts/shared/tickets/AC-0012-allowlist-generators.md` → Status
`Done`; check all six acceptance criteria; tick the resolved research questions.

### Success criteria

#### Automated
- [x] `make test` (full unit suite, race) passes
- [x] `make test-integration` passes (real npm + Packagist generate)
- [x] `make lint` clean
- [x] `go build ./...`

#### Manual
- [x] All six ticket acceptance criteria verified against the implemented tests
- [x] `make golden` diff reviewed once more after all phases; no unintended churn

---

## Testing Strategy

- **Pure logic** (URL normalization, forge expansion, homepage scoping, manifest dep
  walking) → table-driven `require` tests (`internal/prereq/version_test.go` idiom).
- **Generated rule-sets** → golden files with the `-update` flag under
  `internal/generator/testdata/`, driven by fixture manifests + a scripted fake
  `lookuper` (fully offline, deterministic).
- **Cache state machine + call counting** → `FakeFileSystem` + a counting fake
  `lookuper`; the zero-`Lookup`-on-hit assertion is the acceptance test for the output
  cache (mirrors AC-0011's `FakeHTTPGetter.Calls` pattern, `registry_test.go:55,76`).
- **Integration** (`//go:build integration`) → one real npm + one real Packagist
  `Generate` with production seams, behind `make test-integration` only.
- **Hermeticity** — generator logic depends on the `lookuper` interface and the
  `sysdep.FileSystem` seam; no unit test touches the network or real fs.

## Performance Considerations

Steady state is one `ReadFile` (output-cache hit) — no dep walk, no per-package cache
reads, no HTTP. First run on a 50-dep manifest pays N cached-or-live registry lookups
once, then writes a single output-cache file. Atomic write-then-rename avoids torn
cache files.

## References

- Research: `thoughts/shared/research/2026-06-05-AC-0012-allowlist-generators.md`
- Ticket: `thoughts/shared/tickets/AC-0012-allowlist-generators.md`
- Registry client (input): `internal/generator/registry/registry.go:61,88,118,217`
- Rule model + matcher: `internal/policy/policy.go:68`, `internal/policy/glob.go:10,30`
- Cache-path pattern: `internal/state/state.go:116`
- Fakes: `internal/sysdep/sysdeptest/{filesystem,http}.go`
- Design: `docs/design.md:155-210` (algorithm, forge table, caches, trust model)
- Checkpoint decision: ship GitHub **and** GitLab forge rows (table-driven).
