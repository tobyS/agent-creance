---
date: 2026-06-05
ticket: AC-0011
title: "Registry clients & metadata cache (WP-2.2)"
status: complete
git_commit: 58161fb88df5361728cf8c1ba84a238caeb245ef
branch: main
repository: github.com/tobyS/agent-creance
---

# Research: AC-0011 — Registry clients & metadata cache (WP-2.2)

## Research Question

How should `internal/generator/registry` provide npm + Packagist clients that
fetch a package's `homepage` + `repository` metadata and cache it at
`~/.cache/agent-creance/registries/<registry>/<package>.json` with a 30-day lazy
refresh, fully unit-testable against a fake HTTP transport — following the
project's existing seam, cache-path, and testing conventions?

## Summary

This is a **net-new package** (`internal/generator/` does not exist yet). Its two
upstream dependencies already exist: the `sysdep.Clock` and `sysdep.FileSystem`
seams from AC-0009 (WP-1.4), and the `internal/state` cache-path precedent. The
work decomposes into three pieces:

1. **A new HTTP seam.** There is *no* HTTP abstraction in the codebase today; this
   ticket is the first network consumer. Per the mandatory convention ("new
   side-effecting deps get a new interface in `internal/sysdep` + a fake in
   `sysdeptest/`"), AC-0011 introduces an HTTP-doer interface there.
2. **A registry client** per registry (npm, Packagist) that fetches, parses each
   registry's distinct JSON shape into a common normalized `Metadata{Homepage,
   Repository}`, and caches it.
3. **A cache layer** keyed per package, with a self-describing `fetched_at`
   timestamp checked against a 30-day threshold via `clock.Since()`.

The single most consequential finding for the design: **the `FakeFileSystem` does
not model file timestamps** — `Stat().ModTime()` returns the zero time
(`internal/sysdep/sysdeptest/filesystem.go:126-138`). Therefore cache age **cannot**
be derived from file mtime in unit tests; the cache file must carry its own
`fetched_at` timestamp inside the JSON, which the client compares with the injected
`Clock`. This is also the cleaner design (self-describing cache, no reliance on
filesystem mtime semantics).

## Detailed Findings

### What the design specifies

From `docs/design.md`, "Allowlist generators" (`docs/design.md:155-189`):

- Two registries in v0.1, each named per its generator (`docs/design.md:159-160`):
  - `package_json` generator → looks up each direct dependency on the **npm
    registry, `registry.npmjs.org`**.
  - `composer_json` generator → looks up each direct dependency on **Packagist,
    `packagist.org`**.
- The fields consumed are **`homepage`** and **`repository`**
  (`docs/design.md:162-166`). AC-0011 only *surfaces* these; turning them into allow
  rules is downstream AC-0012 (WP-2.3).
- **Manifest as source of truth** (`docs/design.md:182`): "fetches the latest
  version's metadata matching whatever version range is declared." Read the
  manifest, not the lockfile. Rationale: homepage/repository URLs rarely change
  between versions; lockfile-based generation is a deferred refinement.
- **Trust model** (`docs/design.md:180`): registry-reported `homepage`/`repository`
  are trusted verbatim — no validation of the *content*.
- **Cache path, quoted verbatim** (`docs/design.md:186`):
  `~/.cache/agent-creance/registries/<registry>/<package>.json`. It "survives across
  projects" — i.e. a sibling of the per-project `projects/<hash>/` tree, **not**
  under it. Restated in the spec at WP-2.2.
- **30-day lazy refresh** (`docs/design.md:186`): the per-package cache is
  "refreshed lazily (default: 30 days)". "Lazy" = checked at lookup time, not on a
  timer. Steady-state cost is zero; first run on ~50 npm deps is a "~10 second
  one-time cost" (`docs/design.md:189`).

The spec (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
WP-2.2 row confirms package home `internal/generator/registry`, the cache path,
the 30-day refresh, and "Unit-tested against a **fake HTTP transport**; live calls
integration-tagged."

**Scope boundary** — these belong to AC-0012, *not* this ticket
(`docs/design.md:162-178`): turning metadata into host/path-scoped rules; the forge
content-host companion table (raw.githubusercontent.com, codeload, github.io …);
the second "manifest-hash output cache" layer; "missing homepage/repository → emit
nothing."

### The seams this builds on (AC-0009)

**`sysdep.Clock`** (`internal/sysdep/clock.go:12-18`) — `Now()` and
`Since(t) time.Duration`. The doc comment explicitly names "the 30-day
registry-cache expiry" as its motivating consumer (`clock.go:5-7`). The fake
(`internal/sysdep/sysdeptest/clock.go`) is a frozen clock with `Advance(d)` — a test
seeds a cache file with `fetched_at = clock.Now()`, then `clock.Advance(31*24*h)` to
force the stale branch.

**`sysdep.FileSystem`** (`internal/sysdep/filesystem.go:18-38`) — `ReadFile`,
`WriteFile(name, data, perm)`, `Stat`, `MkdirAll`, `Remove`, `Rename`. Contract:
absent files yield an error satisfying `errors.Is(err, fs.ErrNotExist)`
(`filesystem.go:19-21,27-29`), which lets the client distinguish "cache miss" from a
real read error. `Rename` is documented as the primitive behind atomic
"write-temp-then-rename" updates (`filesystem.go:35-37`).

**The fake's timestamp gap** (`internal/sysdep/sysdeptest/filesystem.go:126-138`):
`fakeFileInfo.ModTime()` returns `time.Time{}`. Confirmed via the comment "the fake
does not model timestamps." → **decision-forcing**: store `fetched_at` *inside* the
cache JSON.

### The constructor/DI pattern to mirror

`internal/state` is the closest precedent for a struct-with-injected-seam
(`internal/state/state.go:51-58`):

```go
type Resolver struct { paths sysdep.PathResolver }
func New(paths sysdep.PathResolver) *Resolver { return &Resolver{paths: paths} }
```

AC-0011's client mirrors this: a `Client` struct holding `fs sysdep.FileSystem`,
`clock sysdep.Clock`, an HTTP doer, and a cache-root string (or a path helper),
constructed via `New(...)`.

`internal/state` is also the **only** place `~/.cache/agent-creance` paths are
computed today, via `cacheRoot()` (`internal/state/state.go:111-120`): honors
`XDG_CACHE_HOME`, else `$HOME/.cache` (deliberately XDG-style on macOS, not
`~/Library/Caches`). Constants `appCacheSubdir = "agent-creance"` and
`projectsSubdir = "projects"` at `state.go:33-34`. There is **no** existing helper
for the sibling `registries/` path — AC-0011 must compute
`<cache>/agent-creance/registries/<registry>/<package>.json`. Two options: add a
`registries` helper to `internal/state`, or compute it locally in the registry
package using the same `PathResolver` seam. (Plan recommends extending `state` so
all cache-path knowledge stays in one place — see Open Questions.)

### House conventions for the new package

The most recent package, `internal/policy` (AC-0010), sets the file-layout and test
style: flat files in one package (`policy.go` types, `match.go` algorithm,
`*_test.go` co-located), table-driven subtests with `stretchr/testify/require`, and
**strict JSON decoding** with `dec.DisallowUnknownFields()` for fixtures
(`internal/policy/vectors_test.go:52-53`). The hermeticity guard pattern: tests use
only `sysdeptest` fakes, never the real filesystem (`internal/state/state_test.go`).

The `sysdep` interface idiom to copy for the new HTTP seam
(`internal/sysdep/commander.go`, `filesystem.go`): doc comment → interface →
production `OS*`/`Exec*` struct → compile-time assertion `var _ Iface =
(*Impl)(nil)` → thin delegating methods. The production HTTP impl wraps
`net/http` (a real `*http.Client` with a timeout); the fake in `sysdeptest` is
scripted (URL → response/error), mirroring `FakeCommander.WithTool`.

### npm registry JSON shape (verified against live responses)

Endpoint: `GET https://registry.npmjs.org/<package>` (the "packument"). For scoped
names `@scope/name`, URL-encode the `/` as `%2f`.

- `homepage` and `repository` are **hoisted to the top level** of the packument
  (copied from the latest published version), per npm's official
  `package-metadata` response doc. So the client can read them directly from the top
  level, falling back to `versions[dist-tags.latest]` if a top-level field is absent.
- **`repository` is polymorphic** — either a JSON string (`"github:user/repo"` or a
  bare URL) or an object `{ "type": "git", "url": "git+ssh://git@github.com/u/r.git"
  }`. The `url` often carries a `git+` prefix. → requires a custom `UnmarshalJSON`
  that peeks the first non-space byte (`"` = string, `{` = object).
- "Latest" = `dist-tags.latest` (a semver string) → key into `versions`.

Trimmed example (`left-pad`):

```json
{
  "name": "left-pad",
  "dist-tags": { "latest": "1.3.0" },
  "homepage": "https://github.com/stevemao/left-pad#readme",
  "repository": { "type": "git", "url": "git+ssh://git@github.com/stevemao/left-pad.git" },
  "versions": { "1.3.0": { "homepage": "...", "repository": { "type": "git", "url": "..." } } }
}
```

### Packagist JSON shape (verified against live responses)

Preferred endpoint (per Packagist API docs): the **p2 static metadata** endpoint
`https://repo.packagist.org/p2/<vendor>/<package>.json`. Shape:

```json
{
  "minified": "composer/2.0",
  "packages": {
    "monolog/monolog": [
      { "version": "3.10.0", "homepage": "https://github.com/Seldaek/monolog",
        "source": { "url": "https://github.com/Seldaek/monolog.git", "type": "git", "reference": "..." } }
    ]
  }
}
```

- `packages["<vendor>/<package>"]` is an **array, newest-first**. There is **no**
  top-level `homepage`/`repository`. Read per-version: `homepage` (string) and the
  VCS URL at `source.url` (there is no `repository` key in p2).
- **Minified-delta gotcha**: `minified: composer/2.0` means only element `[0]` is
  fully populated; later entries are field-level deltas. **Reading `packages[name][0]`
  gives a complete `homepage`/`source` and sidesteps the minifier entirely** — that
  is the correct strategy for "latest release metadata".

(The legacy `https://packagist.org/packages/<vendor>/<package>.json` endpoint adds a
single top-level `package.repository` string plus stats, but is dynamically
generated and 12h-cached; p2 element `[0]` is the clean choice.)

### HTTP client etiquette

- Set a descriptive **User-Agent** on every request. Packagist explicitly asks for
  contact info, e.g. `agent-creance/<version> (+https://github.com/tobyS/agent-creance; mailto:tobias@schlitt.info)`.
- Both registries support conditional GETs (`ETag`/`Last-Modified`); not required
  for v0.1 (the 30-day cache already minimizes traffic) but a noted future
  refinement. Use a bounded request timeout on the production client.

## Code References

- `docs/design.md:155-189` — Allowlist generators; cache path & 30-day refresh.
- `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md` — WP-2.2 spec row.
- `internal/sysdep/clock.go:12-18` — `Clock` seam (names the 30-day expiry as its reason).
- `internal/sysdep/filesystem.go:18-38` — `FileSystem` seam (ReadFile/WriteFile/Stat/MkdirAll/Rename; ErrNotExist contract).
- `internal/sysdep/sysdeptest/filesystem.go:126-138` — `fakeFileInfo.ModTime()` returns zero time (forces `fetched_at` in cache).
- `internal/sysdep/sysdeptest/clock.go` — `FakeClock` with `Advance(d)`.
- `internal/sysdep/commander.go` — interface idiom + `FakeCommander.WithTool` precedent for the new HTTP seam.
- `internal/sysdep/errors.go:11` — `ErrNotImplemented` deferral idiom.
- `internal/state/state.go:51-58,99-120` — `New(seam)` constructor + the only `~/.cache` path computation.
- `internal/policy/vectors_test.go:52-53` — strict JSON decode (`DisallowUnknownFields`) house style.
- `internal/cli/cli.go:20-26,55-68` — `App` composition root where production deps are wired.
- `Makefile:53-56` — `test-integration` target (`-tags=integration`).
- `internal/buildinfo/buildinfo.go:34` — `TestedVersions` map.

## Architecture Insights

- **Layering**: `registry.Client` is a host-side, pure-logic-plus-seams component. It
  is *not* run inside the cage — registry lookups happen on the host during policy
  compilation. So the proxy/egress allowlist does not gate these fetches.
- **Normalize at the boundary**: each registry's parser converts its distinct shape
  into a common `Metadata{Homepage, Repository string}`. Caching the *normalized*
  envelope (plus `fetched_at`) — rather than the raw upstream JSON — keeps the cache
  shape stable and decouples downstream AC-0012 from registry quirks.
- **Atomic cache writes**: follow the `Rename` idiom (write temp, rename into place)
  so a crash mid-write never leaves a truncated cache file.
- **Cache miss taxonomy**: absent file (`fs.ErrNotExist`), stale (`fetched_at` older
  than 30 days), and *unparseable* all collapse to "refetch". Treating a malformed
  cache file as a miss (refetch + overwrite) is the pragmatic answer to the ticket's
  cache-integrity question — no checksums needed, since the cache lives in the user's
  own `~/.cache` and registry content is trusted verbatim by design
  (`docs/design.md:180`).

## Open Questions / Decisions for Planning

These are engineering decisions with clear best answers (no blocking product
judgment); the plan records the choices:

1. **`<registry>` directory segment string** — design implies `npm` / `packagist`.
   *Decision: use `npm` and `packagist`* (short, registry-identifying, matches the
   design's examples). This is internal to the package (downstream consumes the
   in-memory `Metadata`, not the file path), and lives in `~/.cache`, so it is
   low-stakes and reversible.
2. **Cache-path ownership** — extend `internal/state` with a `RegistryCache(registry,
   pkg)` helper vs compute locally. *Decision: extend `internal/state`* so all
   `~/.cache/agent-creance` path knowledge stays in one package (consistent with how
   `projects/<hash>/` is centralized there).
3. **HTTP seam location** — *Decision: new `sysdep.HTTPDoer` (or `HTTPGetter`)
   interface + `sysdeptest` fake*, per the mandatory "new side-effecting dep → new
   sysdep interface" rule.
4. **Cache integrity** (ticket Q2) — *Decision: malformed/unparseable cache file →
   treat as a miss and refetch*; no checksums.
5. **Packagist endpoint** — *Decision: p2 static endpoint, read element `[0]`.*
6. **npm `repository` polymorphism** — *Decision: custom `UnmarshalJSON` handling
   both string and `{type,url}` object forms.*
</content>
</invoke>
