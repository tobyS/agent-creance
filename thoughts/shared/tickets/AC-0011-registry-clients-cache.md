# AC-0011: Registry clients & metadata cache (WP-2.2)

**Status:** Done
**Estimated Complexity:** Medium
**Created:** 2026-06-04
**Updated:** 2026-06-05
**Plan reference:** WP-2.2 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** AC-0009 (WP-1.4, filesystem/clock seam)
**Spike gate:** none

## Problem Statement

Generators need package metadata (homepage, repository) from npm and Packagist. Hitting the network on every run is slow and bit-rotty, so lookups must be cached per package with lazy refresh, and the network calls must be isolated behind the seam so unit tests never touch the internet.

## Desired Outcome

`internal/generator/registry` provides npm + Packagist clients that fetch a package's metadata and cache it at `~/.cache/agent-creance/registries/<registry>/<package>.json` with a 30-day lazy refresh, fully unit-testable against a fake HTTP transport.

## User Stories / Use Cases

- As an operator, I want a fast steady-state `run` so that repeated runs don't re-fetch metadata.
- As a developer, I want registry lookups behind a fake so that unit tests are hermetic.

## Acceptance Criteria

- [x] npm client fetches from `registry.npmjs.org`; Packagist client from `packagist.org`; both extract `homepage` + `repository`.
- [x] Metadata is cached per package; a cache entry younger than 30 days is reused without a network call; older triggers a refresh.
- [x] HTTP access is injectable (fake transport in tests); no unit test performs a real network call.
- [x] Live calls exist only under `//go:build integration`.

## Verification & Test Steps

1. `go build ./...` → compiles.
2. `go test -race ./internal/generator/registry/...` → pass with a fake transport: assert a cache miss calls the transport once and writes the cache file; a second call within 30 days does **not** call the transport; an entry aged past 30 days (via the fake `Clock`) refetches.
3. Hermeticity guard: run the unit suite with networking unavailable (e.g. `GOFLAGS=-mod=mod go test ./internal/generator/registry` offline) → still passes.
4. Integration (optional, real network): `make test-integration` exercises one real npm + one real Packagist lookup.
5. `make lint` → clean.

## Out of Scope

- Turning metadata into allow rules (AC-0012).
- `policy refresh` command wiring (AC-0016).

## Dependencies & Sequencing

Phase 2. Depends on AC-0009. Foundation for AC-0012.

## Questions for Research/Planning

- [x] Exact npm + Packagist JSON shapes for `homepage`/`repository` (and version-range resolution to "latest matching")? — **Resolved (research).** npm hoists `homepage`/`repository` to the packument top level (copied from the latest publish); `repository` is polymorphic (string or `{type,url}`), handled by a custom `UnmarshalJSON`, with a fallback to `versions[dist-tags.latest]`. Packagist's p2 endpoint returns versions newest-first; element `[0]` is fully populated and carries `homepage` + `source.url`.
- [x] Cache-poisoning concern: do we validate the cache file's integrity on read? — **Resolved.** No checksums (the cache lives in the user's own `~/.cache` and registry content is trusted verbatim per the design). A malformed/unparseable cache file is treated as a miss and refetched. Package names are validated against path traversal before becoming cache paths.

## References

- `docs/design.md` — "Allowlist generators" (Caching, Manifest as source of truth).
- Spec WP-2.2.

## Implementation Plan

Plan: `thoughts/shared/plans/2026-06-05-AC-0011-registry-clients-cache.md`
(research: `thoughts/shared/research/2026-06-05-AC-0011-registry-clients-cache.md`).
Delivered in four phases: (1) `sysdep.HTTPGetter` seam + `OSHTTPGetter` +
`FakeHTTPGetter`; (2) `state.RegistriesRoot()` cache-path helper; (3)
`internal/generator/registry` (`Client` over npm/Packagist `source` strategies,
self-describing `{fetched_at,homepage,repository}` cache, 30-day lazy refresh,
atomic write, miss/404/error taxonomy); (4) integration-tagged live lookups.

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.

### 2026-06-05
Implemented and closed (WP-2.2). All four acceptance criteria met; all five
verification steps pass (`make test`, `make test-integration`, `make lint`,
`go build ./...`). Cache age is read from a `fetched_at` field inside the cache
JSON (not file mtime, which the `FakeFileSystem` reports as zero), checked via the
injected `Clock`. **Status: Done.**
