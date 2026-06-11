# AC-0040: npm registry lookup fails on large packuments (vite)

**Status:** In Progress
**Estimated Complexity:** Small
**Created:** 2026-06-11
**Updated:** 2026-06-11

## Problem Statement

Running `agent-creance run` in a real-world project (organAIze.eu, a Nuxt frontend)
fails during policy compilation:

```
error: compile policy: compile: run generator "package_json": generator: lookup "vite": registry: parse "vite": unexpected end of JSON input
```

Root cause (confirmed empirically):

- `internal/generator/registry/npm.go` fetches the **full packument** from
  `https://registry.npmjs.org/<pkg>`. For popular packages this document is huge —
  `vite` is **38.9 MB** (verified via curl).
- `internal/sysdep/http.go` caps response bodies at `maxBodyBytes = 16 MiB` via
  `io.LimitReader`, which **silently truncates** larger bodies.
- The truncated JSON then fails to parse with the misleading
  "unexpected end of JSON input" error.

Two distinct defects: (a) we fetch a far bigger document than we need, and
(b) hitting the body cap is indistinguishable from a malformed response.

## Desired Outcome

- npm lookups use the per-version document at `https://registry.npmjs.org/<pkg>/latest`
  (vite: 5.2 KB; scoped names work too: `https://registry.npmjs.org/@vueuse%2fcore/latest`,
  2.3 KB). The doc carries both `homepage` and `repository` (string-or-object form),
  which is all this project consumes.
- A response body that hits the `maxBodyBytes` cap returns a clear error
  ("response body exceeds N MiB") instead of silently truncated bytes, so this failure
  class can never masquerade as a JSON parse error again.
- Generator and registry test fixtures become representative of real-world manifests
  (scoped packages, peerDependencies, polymorphic repository fields), derived from the
  user's real project manifests and prominent OSS projects.

## User Stories / Use Cases

- As an agent-creance user, I want policy compilation to succeed in a real Nuxt/Vue
  project so that I can run my agent in the cage without manual workarounds.
- As a maintainer, I want truncated HTTP bodies to fail loudly so that future
  registry issues produce actionable errors.

## Acceptance Criteria

- [ ] `npmSource.url` targets `/<pkg>/latest` (scoped names keep the `%2f` escaping
      before the `/latest` suffix).
- [ ] `npmSource.parse` reads the version-doc shape directly; the
      dist-tags/versions fallback logic is removed (obsolete — the `/latest` doc *is*
      the latest version manifest the old code fell back to).
- [ ] Polymorphic `repository` handling (string or `{type,url}` object) is retained.
- [ ] 404 still maps to `ErrNotFound` (emit-no-rules semantics unchanged).
- [ ] `OSHTTPGetter.Get` returns a descriptive error when the body reaches
      `maxBodyBytes`, with a regression test.
- [ ] npm registry fixtures in `internal/generator/registry/testdata/` use the
      `/latest` response shape.
- [ ] Generator fixtures (`internal/generator/testdata/package.json`, `composer.json`)
      are enriched to reflect real-world manifests (scoped deps, peerDependencies,
      Laravel-style composer.json), with goldens regenerated and reviewed.
- [ ] Existing tests continue to pass (`make test`, `make lint`).

## Out of Scope

- Packagist client changes — its p2 endpoint serves small minified docs (verified)
  and already reads only the newest version entry.
- Raising `maxBodyBytes` — fetching smaller documents is the fix; the cap stays as a
  guard against hostile/misbehaving endpoints.
- Committing the user's raw reference manifests (`frontend_package.json`,
  `backend_composer.json`, `backoffice_composer.json` in the repo root) — they are
  untracked scratch references; fixtures are *derived* from them.

## Open Questions

None — this is a well-understood quickfix.

## Questions for Research/Planning

- [ ] Which tests/goldens consume the npm packument fixtures
      (`npm-left-pad.json`, `npm-string-repo.json`, `npm-version-fallback.json`) and
      what asserts on the fallback logic that becomes obsolete?
- [ ] Does anything else consume `sysdep.HTTPGetter` whose behavior changes when the
      cap becomes an error?
- [ ] Do the integration tests (`live_integration_test.go`) hit the full-packument
      URL and need updating?
- [ ] What do the current generator fixtures cover, and which real-world manifest
      features (scoped packages, peerDependencies, `latest` version specs,
      Laravel-style composer require/require-dev) are missing?

## References

- Quickfix initiated via `/quickfix` command
- Error observed: `compile policy: compile: run generator "package_json": generator: lookup "vite": registry: parse "vite": unexpected end of JSON input`
- npm registry endpoints: full packument `https://registry.npmjs.org/<pkg>` vs
  per-version `https://registry.npmjs.org/<pkg>/latest`
- User reference manifests (untracked, repo root): `frontend_package.json`,
  `backend_composer.json`, `backoffice_composer.json`

## Implementation Plan

## Notes & Updates

### 2026-06-11
- Quickfix ticket auto-created from `/quickfix` command
- Diagnosis pre-confirmed by curl measurements: vite packument 38.9 MB (> 16 MiB cap),
  `/latest` docs 5.2 KB (vite) / 2.3 KB (@vueuse/core), both containing
  homepage + repository.
