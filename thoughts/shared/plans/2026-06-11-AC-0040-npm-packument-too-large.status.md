# Implementation Status: AC-0040 — npm /latest endpoint + loud body cap + realistic fixtures

## Phase 1: npm /latest endpoint + loud body-cap error
- **Status**: ✅ Complete
- **Started**: 2026-06-11
- **Completed**: 2026-06-11

### Steps Performed
1. `internal/generator/registry/npm.go`: url() now targets `/<pkg>/latest`;
   removed `npmPackument` + dist-tags/versions fallback; parse() decodes
   `npmVersionDoc` directly; doc comments rewritten (packument deliberately
   avoided, both fields publisher-optional).
2. `internal/sysdep/http.go`: reads `maxBodyBytes+1` and returns
   `sysdep: response body of %q exceeds 16 MiB` when over the cap (no more
   silent truncation).
3. Tests refit: `npm_test.go` URLs + table (fallback case → "missing homepage
   tolerated"); `registry_test.go` npmURL constant + comment;
   `compile_test.go` scripted react URL → `/latest`.
4. Fixtures: `npm-left-pad.json` / `npm-string-repo.json` rewritten as /latest
   version docs (left-pad now covers `repository.directory`); deleted
   `npm-version-fallback.json`; added `npm-no-homepage.json`.
5. New `TestOSHTTPGetterRejectsOversizedBody` in `http_test.go` (at-cap ok,
   over-cap error, nil body).
6. `docs/design.md`: package_json generator bullet notes the /latest endpoint
   and why.

### Issues Encountered
- `make test-integration`: `internal/verify` cage battery tests fail with
  `mkdir /Users/toby/.agent-creance-battery-*: operation not permitted` —
  confirmed pre-existing on a clean tree (stash → rerun → pop), environmental
  (this shell may not create dirs in $HOME). All registry/generator/compile
  integration tests pass, including live /latest lookups.

### Verification
- ✅ `make test` (race)
- ✅ `make lint`
- ✅ `make test-integration` for generator/registry/compile (verify failures pre-existing, unrelated)

### Commit
- `693f671` fix(AC-0040): npm lookups use /latest; oversized HTTP bodies error loudly

---

## Phase 2: Realistic generator fixtures + regenerated goldens
- **Status**: ✅ Complete
- **Started**: 2026-06-11
- **Completed**: 2026-06-11

### Steps Performed
1. `internal/generator/testdata/package.json`: kept all behavior-coverage
   packages; added structural fields (private, type, packageManager, engines,
   scripts), real-world scoped deps `@vueuse/core` and `@types/bun` (the
   latter with a `"latest"` spec), and a `peerDependencies` section whose
   entry is deliberately unscripted in tests — a lookup of it would fail.
2. `internal/generator/testdata/composer.json`: Laravel-app-shaped — added
   `ext-intl`/`ext-fileinfo` platform reqs, a path `repositories` entry with
   `acme/core: @dev` (scripted as Packagist 404 → skipped, mirroring the
   user's `organaize/core`), `roave/security-advisories: dev-master`,
   exact-pinned `laravel/pint`, plus autoload/scripts/config/minimum-stability
   sections.
3. `generator_test.go` + `cache_test.go`: scripted metadata for the new
   packages (mirroring real registry responses); explicit peerDependencies
   non-parsing assertion in TestDeps_ManifestParsing.
4. `make golden` + diff review: purely additive rules for @types/bun,
   @vueuse/core, laravel/pint, roave/security-advisories. Observation: a
   homepage that is a GitHub readme URL (@vueuse/core) yields the github.com
   repo rule twice (homepage + repository paths coincide) — pre-existing
   generator behavior, harmless.

### Issues Encountered
- gofmt -s alignment failure in cache_test.go after edit → fixed with `make fmt`.

### Verification
- ✅ `make test` (race)
- ✅ `make lint`
- ✅ `make golden` re-run is a no-op (goldens stable)

### Commit
- (recorded below after commit)
