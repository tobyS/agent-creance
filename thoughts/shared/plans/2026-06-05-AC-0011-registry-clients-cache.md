---
date: 2026-06-05
ticket: AC-0011
title: "Registry clients & metadata cache (WP-2.2)"
status: ready
research: thoughts/shared/research/2026-06-05-AC-0011-registry-clients-cache.md
git_commit: 9553a4e
branch: main
---

# Implementation Plan: AC-0011 — Registry clients & metadata cache (WP-2.2)

## Overview

Add `internal/generator/registry`: npm + Packagist clients that fetch a
package's `homepage` + `repository` metadata and cache it per package at
`~/.cache/agent-creance/registries/<registry>/<package>.json` with a 30-day lazy
refresh. HTTP access goes through a **new `sysdep` seam** (the first network
consumer in the codebase), so every unit test is hermetic against a fake
transport; real network lookups exist only under `//go:build integration`.

## Current State

- `internal/generator/` **does not exist** — fully net-new package.
- The two upstream seams exist (AC-0009): `sysdep.Clock`
  (`internal/sysdep/clock.go`) and `sysdep.FileSystem`
  (`internal/sysdep/filesystem.go`), with fakes in `sysdep/sysdeptest/`.
- There is **no HTTP seam** anywhere — this ticket introduces it.
- Cache-path computation lives only in `internal/state`
  (`internal/state/state.go:111-120`, `cacheRoot()` → `XDG_CACHE_HOME` else
  `$HOME/.cache`), which today models only the per-project `projects/<hash>/`
  tree — no `registries/` sibling helper.
- Constructor/DI precedent: `internal/state` (`Resolver`/`New(seam)`). Test/parse
  precedent: `internal/policy` (table-driven `require`, strict JSON decode).

## Desired End State

- `registry.NewNPM(...)` and `registry.NewPackagist(...)` return a `*Client`
  whose `Lookup(ctx, pkg)` returns a `Metadata{Homepage, Repository}`.
- A cache miss (absent / stale / unparseable file) triggers exactly one HTTP GET
  and writes a self-describing cache entry (`{fetched_at, homepage, repository}`)
  atomically; a hit within 30 days returns the cached value with **zero** HTTP
  calls; an entry aged past 30 days (advanced via the fake `Clock`) refetches.
- Unit suite passes offline with the race detector; no unit test touches the
  network. One real npm + one real Packagist lookup pass under
  `make test-integration`.
- `make lint` clean; `go build ./...` compiles.

### Key design decisions (from research)

- **Cache age is read from a `fetched_at` field inside the JSON**, not file mtime
  — the `FakeFileSystem` returns a zero `ModTime`
  (`internal/sysdep/sysdeptest/filesystem.go:126-138`), and a self-describing
  cache is the cleaner design regardless.
- **New HTTP seam in `sysdep`**: `sysdep.HTTPGetter` (`Get(ctx, url, headers) →
  status, body, err`) + `OSHTTPGetter` (production) + `FakeHTTPGetter`
  (`sysdeptest`, scripted + records calls). Returning the status code lets the
  client treat a 404 as `ErrNotFound` distinctly.
- **Path split**: `internal/state` gains `RegistriesRoot()` →
  `<cache>/agent-creance/registries`; the `Client` joins
  `<registriesRoot>/<registry>/<pkg>.json` itself. This keeps the client
  hermetic (its tests pass a plain base-dir string) while cache-root knowledge
  stays in `state`.
- **Registry segment strings**: `npm` and `packagist`.
- **Normalized cache envelope**: cache the extracted `{homepage, repository}` +
  `fetched_at`, not the raw upstream JSON — stable shape, decouples AC-0012.
- **npm**: read `homepage`/`repository` from the packument top level (hoisted),
  with a custom `UnmarshalJSON` for the polymorphic `repository`
  (string or `{type,url}`).
- **Packagist**: p2 endpoint `repo.packagist.org/p2/<vendor>/<pkg>.json`, read
  element `[0]` (fully populated; sidesteps the minified-delta encoding);
  `homepage` + `source.url`.
- **Miss taxonomy**: absent (`fs.ErrNotExist`), stale, and unparseable all →
  refetch. A genuine non-ErrNotExist read error is surfaced. A 404 → `ErrNotFound`.
  A network/transport error on refresh is surfaced (no stale-fallback in v0.1).

## What We're NOT Doing

- Turning metadata into allow rules, the forge content-host table, the
  manifest-hash output cache, "missing fields → emit nothing" (all AC-0012).
- `policy refresh` command / cache invalidation wiring (AC-0016/WP-2.7).
- Wiring the client into any CLI command or the `App` composition root (no
  consumer exists until AC-0012).
- Lockfile-based version resolution, conditional GET / ETag revalidation,
  stale-on-network-failure fallback (noted future refinements).

---

## Phase 1 — HTTP seam in `sysdep`

### Changes

**`internal/sysdep/http.go`** (new) — mirror the `Commander` idiom (doc comment →
interface → `OS*` impl → `var _` assertion):

```go
// HTTPGetter abstracts an HTTP GET: the first network touchpoint in the codebase.
// Registry lookups (npm/Packagist) go through this so unit tests stay hermetic
// against a fake transport; production wires OSHTTPGetter, integration tests the
// real one.
type HTTPGetter interface {
    // Get performs a GET for url with the given request headers and returns the
    // response status code and fully-read body. A non-nil error means the request
    // could not be completed (DNS, connection, timeout, body read); an HTTP error
    // status (404, 5xx) is reported via status with err == nil.
    Get(ctx context.Context, url string, headers map[string]string) (status int, body []byte, err error)
}

type OSHTTPGetter struct{ Client *http.Client }
var _ HTTPGetter = (*OSHTTPGetter)(nil)
```

`OSHTTPGetter.Get` builds an `http.NewRequestWithContext`, applies headers, uses
`g.Client` (or a default `&http.Client{Timeout: 30s}` when nil), and reads the
body with `io.ReadAll` under a sane cap. Closes the body.

**`internal/sysdep/sysdeptest/http.go`** (new) — `FakeHTTPGetter`:

- Fields: `Responses map[string]FakeHTTPResponse` (url → `{Status int; Body []byte}`),
  `Errs map[string]error` (url → transport error), and `Calls []string` (URLs
  requested, in order — lets tests assert call count, satisfying the
  "called once" / "not called" acceptance criteria).
- `NewFakeHTTPGetter()` + builder `WithResponse(url, status, body)` /
  `WithError(url, err)`.
- `Get` appends to `Calls`, returns the scripted error/response, or a default
  (e.g. status 0 + a "no scripted response" error) for an unscripted URL.

### Tests

**`internal/sysdep/sysdeptest/http_test.go`** — fake returns scripted
response/error; records calls.

**`internal/sysdep/http_test.go`** — `OSHTTPGetter` against
`httptest.NewServer` (loopback only, hermetic — not external network): asserts
headers are forwarded, body + status returned, and context cancellation aborts.

### Success criteria

#### Automated
- [ ] `go build ./...`
- [ ] `go test -race ./internal/sysdep/...` passes
- [ ] `make lint` clean

---

## Phase 2 — `registries/` cache-path helper in `internal/state`

### Changes

**`internal/state/state.go`**:
- Add const `registriesSubdir = "registries"` alongside `projectsSubdir`
  (`state.go:33-34`).
- Add method:
  ```go
  // RegistriesRoot returns <cache>/agent-creance/registries — the cross-project
  // home of per-package registry metadata caches (sibling of projects/<hash>/).
  func (r *Resolver) RegistriesRoot() (string, error) {
      cache, err := r.cacheRoot()
      if err != nil { return "", err }
      return filepath.Join(cache, appCacheSubdir, registriesSubdir), nil
  }
  ```

### Tests

**`internal/state/state_test.go`** — add `TestRegistriesRootHonoursXDGThenHome`
mirroring the existing `TestCacheRootHonoursXDG...` table: XDG set →
`/xdg/cache/agent-creance/registries`; XDG empty →
`/home/u/.cache/agent-creance/registries`; and the home-error path returns an
error.

### Success criteria

#### Automated
- [ ] `go test -race ./internal/state/...` passes
- [ ] `make lint` clean

---

## Phase 3 — Registry client: cache machinery + npm/Packagist sources

### Changes (new package `internal/generator/registry`)

**`registry.go`** — common core:

- Public types:
  ```go
  type Metadata struct {
      Homepage   string `json:"homepage"`
      Repository string `json:"repository"`
  }
  type Client struct {
      src           source
      fs            sysdep.FileSystem
      clock         sysdep.Clock
      http          sysdep.HTTPGetter
      registriesRoot string
  }
  var ErrNotFound = errors.New("registry: package not found")
  ```
- Unexported `source` strategy interface:
  ```go
  type source interface {
      name() string                 // "npm" / "packagist" — cache dir segment
      url(pkg string) string        // upstream metadata URL
      parse(body []byte) (Metadata, error)
  }
  ```
- Constructors `NewNPM(fs, clock, http, registriesRoot)` and
  `NewPackagist(...)` injecting `npmSource{}` / `packagistSource{}`.
- Cache envelope (unexported): `type cacheEntry struct { FetchedAt time.Time
  json:"fetched_at"; Metadata }` (embeds Metadata so the file is
  `{fetched_at, homepage, repository}`).
- `cachePath(pkg)` = `filepath.Join(registriesRoot, src.name(), pkg+".json")`;
  validate `pkg` (non-empty, no `..`/absolute segments) → guard path traversal.
- `const refreshInterval = 30 * 24 * time.Hour`.
- `Lookup(ctx, pkg) (Metadata, error)`:
  1. `readCache(pkg)` → on a fresh, parseable hit (`clock.Since(entry.FetchedAt)
     < refreshInterval`) return `entry.Metadata`, **no HTTP**.
  2. Otherwise `fetch(ctx, pkg)`: build URL, `http.Get` with the User-Agent
     header; `404` → `ErrNotFound`; non-2xx → error; `2xx` → `src.parse(body)`.
  3. `writeCache(pkg, md)`: `MkdirAll(dir, 0o755)`; marshal `cacheEntry{FetchedAt:
     clock.Now(), Metadata: md}`; **atomic write** — `WriteFile(tmp,…,0o644)` then
     `Rename(tmp, final)` (the seam's documented idiom).
  4. Return `md`.
- `readCache`: `ReadFile`; `errors.Is(err, fs.ErrNotExist)` → miss; other read
  error → surface; `json.Unmarshal` error → miss (refetch).
- `userAgent` const built from `buildinfo.Version`:
  `"agent-creance/" + buildinfo.Version + " (+https://github.com/tobyS/agent-creance; mailto:tobias@schlitt.info)"`.

**`npm.go`** — `npmSource`:
- `url(pkg)` = `https://registry.npmjs.org/` + escaped pkg (encode `/` in scoped
  names as `%2f`).
- `parse`: decode the packument; read top-level `homepage` + `repository`, with a
  fallback to `versions[dist-tags.latest]` if a top-level field is empty.
- `repository` decoded via a custom `UnmarshalJSON` type handling both a JSON
  string and `{type,url}` (peek first non-space byte). Normalize the URL string
  as-is (strip a leading `git+` is a downstream AC-0012 concern — store verbatim
  per the trust model).

**`packagist.go`** — `packagistSource`:
- `url(pkg)` = `https://repo.packagist.org/p2/` + pkg + `.json`.
- `parse`: decode `{packages: map[string][]versionDoc}`; take
  `packages[pkg][0]`; `Homepage` = `homepage`, `Repository` = `source.url`.

### Tests (hermetic — `FakeFileSystem` + `FakeClock` + `FakeHTTPGetter`)

**`registry_test.go`** (table-driven, `require`), covering the acceptance criteria:
- **miss → one fetch + cache written**: empty fs, scripted 200; assert returned
  Metadata, `len(http.Calls) == 1`, and the cache file now exists with the
  expected `fetched_at`/perm `0o644` and parent dir created.
- **hit within 30 days → no fetch**: seed a fresh cache file (`fetched_at =
  clock.Now()`); assert returned Metadata and `len(http.Calls) == 0`.
- **stale → refetch**: seed cache, `clock.Advance(31*24h)`; assert a second fetch
  occurred and the file's `fetched_at` advanced.
- **unparseable cache → refetch** (seed garbage bytes) and **absent dir** path.
- **404 → `ErrNotFound`**; **non-2xx → error**; **transport error → surfaced**.
- **atomic write**: a `RenameErr` leaves no partial final file.
- **path traversal**: `Lookup(ctx, "../evil")` → error, no fetch.

**`npm_test.go`** — parse `testdata/npm-left-pad.json` (object `repository`) and a
string-`repository` fixture; assert both forms extract correctly; top-level vs
`versions[latest]` fallback.

**`packagist_test.go`** — parse `testdata/packagist-monolog.json` (p2, element
`[0]`); assert `homepage` + `source.url` extraction; missing-package array → error.

**Hermeticity note**: the package's non-integration code uses the `HTTPGetter`
seam, never `net/http` directly — consistent with the `state` grep-guard
convention.

### Success criteria

#### Automated
- [ ] `go build ./...`
- [ ] `go test -race ./internal/generator/registry/...` passes
- [ ] Offline guard: same suite passes with networking unavailable (hermetic by
      construction — fakes only)
- [ ] `make lint` clean

#### Manual
- [ ] Cache file on disk is `{fetched_at, homepage, repository}` (inspect a
      fixture written by the miss test)

---

## Phase 4 — Live integration test + ticket close

### Changes

**`internal/generator/registry/live_integration_test.go`** (`//go:build
integration`) — construct a `*Client` with `OSFileSystem{}`, `OSClock{}`,
`OSHTTPGetter{}` and `registriesRoot = t.TempDir()`:
- npm: `Lookup(ctx, "left-pad")` → non-empty `Homepage` + `Repository`.
- Packagist: `Lookup(ctx, "monolog/monolog")` → non-empty `Homepage` +
  `Repository`.
- Second `Lookup` of the same package hits the cache (no error; fast).

**No `buildinfo` bump**: `TestedVersions` tracks external *CLI tools*
(agent-safehouse/mitmproxy/security), not registries — leave it untouched.

**Ticket**: set `AC-0011` status → Done; check the acceptance boxes.

### Success criteria

#### Automated
- [ ] `make test` (full unit suite, race) passes
- [ ] `make test-integration` passes (real npm + Packagist lookup)
- [ ] `make lint` clean
- [ ] `go build ./...`

#### Manual
- [ ] All four ticket acceptance criteria verified against the implemented tests

---

## Testing Strategy

- **Unit**: table-driven (`stretchr/testify/require`), fakes only
  (`FakeFileSystem`/`FakeClock`/`FakeHTTPGetter`), parse fixtures under
  `testdata/`. Drives the cache state machine (miss/hit/stale/unparseable),
  call-count assertions, error taxonomy, atomic write, and per-registry parsing
  incl. npm's polymorphic `repository`.
- **Integration** (`//go:build integration`): one real npm + one real Packagist
  lookup with the production seams; behind `make test-integration` only.
- **Hermeticity**: enforced structurally — logic uses the `HTTPGetter` seam; no
  unit test imports `net/http` for real calls.

## Performance Considerations

Steady state is zero HTTP (30-day cache hit = one `ReadFile`). Atomic
write-then-rename avoids torn cache files. Production HTTP client carries a 30s
timeout so a hung registry can't wedge a future `policy compile`.

## References

- Research: `thoughts/shared/research/2026-06-05-AC-0011-registry-clients-cache.md`
- Ticket: `thoughts/shared/tickets/AC-0011-registry-clients-cache.md`
- Seams: `internal/sysdep/clock.go`, `internal/sysdep/filesystem.go`,
  `internal/sysdep/commander.go` (interface idiom)
- Cache path: `internal/state/state.go:111-120`
- Patterns: `internal/state/state_test.go`, `internal/policy/vectors_test.go`
</content>
