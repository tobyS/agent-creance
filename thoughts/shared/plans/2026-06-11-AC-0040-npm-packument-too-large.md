# AC-0040: npm `/latest` endpoint + loud body cap + realistic fixtures — Implementation Plan

## Overview

Switch the npm registry client from the full packument endpoint (38.9 MB for
vite — over the 16 MiB body cap, silently truncated, fails as a bogus JSON
parse error) to the tiny per-version `/<pkg>/latest` endpoint; make hitting the
body cap a loud, descriptive error; and enrich the generator/registry test
fixtures so they reflect real-world manifests.

## Current State Analysis

See `thoughts/shared/research/2026-06-11-AC-0040-npm-packument-too-large.md`
(all file:line references below come from it):

- `npmSource.url()` (`internal/generator/registry/npm.go:17-19`) fetches the
  full packument; `parse()` (`npm.go:77-99`) reads hoisted top-level fields and
  falls back through `dist-tags.latest` → `versions[latest]`.
- `OSHTTPGetter.Get` (`internal/sysdep/http.go:67`) truncates bodies at
  `maxBodyBytes = 16 MiB` via `io.LimitReader` and returns truncated bytes with
  a nil error. No test covers the cap.
- Generator golden tests use `fakeLookuper` (no HTTP); registry fixtures and
  three test files hardcode the packument URL/shape, including the hidden
  `internal/policy/compile/compile_test.go:360`.
- Current manifest fixtures are synthetic and thin; the user's real manifests
  and prominent OSS projects show features the fixtures don't exercise
  (peerDependencies tolerance, `latest` specs, multiple `ext-*` platform reqs,
  structural fields like `config`/`scripts`/`packageManager`).

## Desired End State

- npm lookups GET `https://registry.npmjs.org/<pkg>/latest` (scoped:
  `@scope%2fname/latest`) and decode the version-doc shape directly; 404 still
  maps to `ErrNotFound`; string-or-object `repository` still handled; missing
  `homepage`/`repository` tolerated (both are publisher-optional).
- A response body larger than 16 MiB returns
  `sysdep: response body of %q exceeds 16 MiB` instead of truncated bytes.
- Fixtures in `internal/generator/testdata/` and
  `internal/generator/registry/testdata/` are realistic; goldens regenerated
  and reviewed; `make test` and `make lint` green.

### Key Discoveries

- `npmVersionDoc` (`npm.go:41-44`) is exactly the `/latest` response shape —
  the packument type and fallback block become dead code.
- The registry disk cache is keyed by package name, not URL
  (`registry.go:155-160`) — existing user caches stay valid.
- `HTTPGetter.Get` has a single production call site
  (`registry.go:210`) — the cap-as-error change cannot regress anything else.
- Generator goldens are driven by `fakeLookuper` scripted metadata
  (`generator_test.go:26-52`) — fixture enrichment requires scripted metadata
  for each new looked-up package, then `make golden`.

## What We're NOT Doing

- No Packagist changes (p2 endpoint verified small; reads newest entry only).
- No raising of `maxBodyBytes` — smaller documents are the fix; the cap stays
  as a guard.
- No behavior change to manifest parsing: `peerDependencies` /
  `optionalDependencies` stay ignored, and no spec-based filtering of
  `workspace:*` / `file:` / `npm:` aliases (noted in research as future ticket
  material).
- Not committing the user's raw reference manifests (`frontend_package.json`,
  `backend_composer.json`, `backoffice_composer.json` in the repo root) —
  fixtures are derived, the originals stay untracked.

## Implementation Approach

Two phases: (1) the functional fix — endpoint switch + loud cap — with its
test/fixture refits, committed as the bug fix; (2) fixture enrichment +
golden regeneration, committed separately so the golden churn is reviewable on
its own.

---

## Phase 1: npm `/latest` endpoint + loud body-cap error

### Overview

Fix both defects: fetch the small document, and make truncation impossible to
misdiagnose.

### Changes Required

#### 1. `internal/generator/registry/npm.go`

- `url()`: append `/latest` — `"https://registry.npmjs.org/" + npmPackagePath(pkg) + "/latest"`.
  Keep `npmPackagePath` (`%2f` for scoped names) unchanged.
- Delete `npmPackument` and the fallback block in `parse()`; decode straight
  into `npmVersionDoc`:

```go
func (npmSource) parse(body []byte) (Metadata, error) {
	var doc npmVersionDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return Metadata{}, err
	}
	return Metadata{Homepage: doc.Homepage, Repository: doc.Repository.URL}, nil
}
```

- Keep `npmRepository.UnmarshalJSON` (version docs carry the same polymorphic
  `repository`; npm normalizes to object form at publish, but old documents
  may be strings).
- Rewrite the doc comments (`npm.go:9-12`, `31-33`, `84-85`): the source now
  fetches the latest version's manifest, both fields publisher-optional,
  full packuments deliberately avoided (vite = 38.9 MB > 16 MiB cap).

#### 2. `internal/sysdep/http.go`

- Read `maxBodyBytes + 1` and error when over:

```go
body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
if err != nil { ... unchanged ... }
if len(body) > maxBodyBytes {
	return resp.StatusCode, nil, fmt.Errorf("sysdep: response body of %q exceeds %d MiB", url, maxBodyBytes>>20)
}
```

- Update the `maxBodyBytes` comment (`http.go:11-14`): the cap now *rejects*
  oversized bodies rather than truncating.

#### 3. Test refits

- `internal/generator/registry/npm_test.go`:
  - `TestNPMSourceURL`: expect `.../left-pad/latest` and
    `.../@types%2fnode/latest`.
  - `TestNPMParse`: drop the "falls back to latest version" case; keep
    object-repo and string-repo cases; add a "missing homepage" case
    (real-world: `express@2.5.11` has repository but no homepage).
- Fixtures (`internal/generator/registry/testdata/`):
  - `npm-left-pad.json` → version-doc shape, object `repository` including a
    `directory` field (real-world monorepo form, decoder must tolerate it).
  - `npm-string-repo.json` → version-doc shape, string `repository`.
  - `npm-version-fallback.json` → delete; add `npm-no-homepage.json`
    (repository only).
- `internal/generator/registry/registry_test.go:19`: `npmURL` →
  `https://registry.npmjs.org/left-pad/latest`; fix the "minimal valid
  packument" comment at line 27 (`npmBody` itself already parses as a version
  doc).
- `internal/policy/compile/compile_test.go:360`: scripted URL →
  `https://registry.npmjs.org/react/latest`.
- `internal/sysdep/http_test.go`: new regression test — response body of
  `maxBodyBytes+1` bytes ⇒ `Get` returns an error mentioning the limit, nil
  body; body of exactly `maxBodyBytes` ⇒ succeeds. (Follow the file's existing
  httptest pattern; a 16 MiB in-memory body is acceptable for a unit test —
  write it with `bytes.Repeat`, not a literal.)

#### 4. `docs/design.md:163`

- Note the endpoint: `registry.npmjs.org/<pkg>/latest` (full packuments can
  exceed the HTTP body cap).

### Success Criteria

#### Automated Verification:

- [ ] `make test` passes (race detector)
- [ ] `go build ./...` passes
- [ ] `make lint` passes
- [ ] `make test-integration` passes (live registry lookups now hit `/latest`)

#### Manual Verification:

- [ ] `agent-creance run` in the user's real project (organAIze.eu) compiles
      policy without the `unexpected end of JSON input` error (user-side check;
      deleting the stale vite cache entry is unnecessary — the old failure
      never wrote one)

---

## Phase 2: Realistic generator fixtures + regenerated goldens

### Overview

Derive richer manifest fixtures from the user's real project manifests and
prominent OSS manifests (nuxt, excalidraw, laravel/laravel, monicahq/monica),
keeping every behavior the current synthetic packages cover.

### Changes Required

#### 1. `internal/generator/testdata/package.json`

Keep the existing behavior-coverage packages (react, barehome, pageddocs,
norepo, @scope/tool, gitlablib — each exercises a distinct rule path) and add
realism around them:

- Structural fields the parser must tolerate: `"name"`, `"private": true`,
  `"type": "module"`, `"scripts"`, `"packageManager"`, `"engines"`.
- Real-world-style scoped deps in `dependencies`/`devDependencies` (e.g.
  `@vueuse/core`, `@nuxtjs/i18n` — names from the user's frontend manifest),
  with scripted metadata mirroring their real registry responses
  (`git+https://github.com/vueuse/vueuse.git` etc.).
- A `"latest"` version spec on one entry (`@types/bun: "latest"` pattern —
  specs are ignored by the parser; this documents that).
- A `peerDependencies` section containing a package that gets **no** scripted
  metadata — proving the section is ignored (a lookup would fail the test).

#### 2. `internal/generator/testdata/composer.json`

- Extend platform reqs: `php`, `ext-mbstring`, `ext-intl`, `ext-fileinfo`
  (all filtered by `isComposerPackage`).
- Structural sections to tolerate: `autoload`, `scripts`, `config`
  (`allow-plugins`), `minimum-stability`, `prefer-stable`.
- A branch constraint (`dev-master`) and an exact pin on existing/new entries;
  optionally one more realistic vendor package (e.g.
  `inertiajs/inertia-laravel`) with scripted metadata.

#### 3. `internal/generator/generator_test.go`

- Add scripted `fakeLookuper` metadata for every newly looked-up package;
  assert lookup call counts still match (peerDependencies entry must not
  appear).

#### 4. Regenerate goldens

- `make golden`, then review the diff of `package_json.golden` /
  `composer_json.golden`: new rules appear in sorted-package order, homepage
  rule first, then repository + forge companions; no changes to existing
  packages' rules.

### Success Criteria

#### Automated Verification:

- [ ] `make test` passes
- [ ] `make lint` passes
- [ ] `make golden` after the regen is a no-op (goldens stable)

#### Manual Verification:

- [ ] Golden diff reviewed: only additive rule changes for the new packages

---

## Testing Strategy

- **Unit:** refitted table tests in `npm_test.go` (object repo, string repo,
  missing homepage, invalid JSON); new `http_test.go` cap tests (at-cap ok,
  over-cap error); existing `registry_test.go` cache/404/error suite rides on
  the new URL constant.
- **Golden:** enriched manifests through `fakeLookuper` → reviewed goldens.
- **Integration:** `make test-integration` exercises the real `/latest`
  endpoint via the existing live tests (left-pad, react stack).
- **Manual:** user re-runs `agent-creance run` in organAIze.eu.

## Performance Considerations

Strictly better: ~5 KB instead of up to tens of MB per uncached npm lookup;
less memory, faster cold compiles.

## Migration Notes

None. Registry cache entries are keyed by package name and stay valid; entries
cached under the old endpoint hold the same two fields.

## References

- Ticket: `thoughts/shared/tickets/AC-0040-npm-packument-too-large.md`
- Research: `thoughts/shared/research/2026-06-11-AC-0040-npm-packument-too-large.md`
- npm registry API: https://github.com/npm/registry/blob/main/docs/REGISTRY-API.md
