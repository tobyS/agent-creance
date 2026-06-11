---
date: 2026-06-11
researcher: Claude (quickfix pipeline)
git_commit: d670ded40ef34110b47f1f727e0e36097441adeb
branch: main
repository: git@github.com:tobyS/agent-creance.git
topic: "AC-0040: npm registry lookup fails on large packuments (vite)"
tags: [research, codebase, AC-0040, registry, npm, generator, sysdep]
status: complete
last_updated: 2026-06-11
---

# Research: AC-0040 — npm lookup fails on large packuments

**Ticket:** `thoughts/shared/tickets/AC-0040-npm-packument-too-large.md`

## Problem recap (pre-confirmed)

`agent-creance run` in a real Nuxt project failed with
`registry: parse "vite": unexpected end of JSON input`. The vite packument at
`https://registry.npmjs.org/vite` is 38.9 MB; `OSHTTPGetter` truncates bodies at
16 MiB via `io.LimitReader` (`internal/sysdep/http.go:67`) and returns the
truncated bytes with a nil error, so the JSON decoder fails with a misleading
message. The per-version endpoint `https://registry.npmjs.org/<pkg>/latest`
returns 5.2 KB for vite (2.3 KB for `@vueuse/core` via `@vueuse%2fcore/latest`)
and contains both fields this project consumes.

## Findings

### Current npm source (`internal/generator/registry/npm.go`)

- `npmSource.url()` (`npm.go:17-19`) targets the full packument; `npmPackagePath`
  (`npm.go:24-29`) percent-encodes the scoped-name slash (`%2f`).
- `parse()` (`npm.go:77-99`) decodes `npmPackument` (`npm.go:34-39`), reads
  hoisted top-level homepage/repository, and falls back through
  `dist-tags.latest` → `versions[latest]`.
- **Obsolete under `/latest`:** the `npmPackument` type (DistTags/Versions), the
  entire fallback block (`npm.go:84-97`), and the hoisting doc comments
  (`npm.go:9-12`, `31-33`, `84-85`). `npmVersionDoc` (`npm.go:41-44`) is
  *exactly* the new response shape. `npmRepository.UnmarshalJSON`
  (`npm.go:52-75`, string-or-object polymorphism) stays — the version doc's
  `repository` field is just as polymorphic.

### npm `/latest` endpoint semantics (web research, live-verified 2026-06)

- `GET /{package}/{version}` officially accepts `latest`
  ([REGISTRY-API.md](https://github.com/npm/registry/blob/main/docs/REGISTRY-API.md)).
- Scoped names work with `%2f` encoding (and even unencoded); `%2f` is the safe
  canonical client choice. (Historical scoped-version 404s — npm/npm#9164 —
  were fixed years ago.)
- Nonexistent package → 404; existing package with a nonexistent/absent `latest`
  dist-tag → also 404. Treat any 404 as `ErrNotFound` ("emit no rules"), which
  matches current semantics.
- `homepage` and `repository` are publisher-supplied and **both optional** even
  in the full version doc (verified: `express@2.5.11` has repository but no
  homepage). The current code already tolerates empty fields.
- npm normalizes `repository` to object form (`{type,url,directory?}`) at
  publish time, but old documents can carry the string shorthand — keep the
  polymorphic decoder.
- The abbreviated packument (`Accept: application/vnd.npm.install-v1+json`)
  **omits** homepage/repository — confirmed not an option. Keep
  `Accept: application/json`.

### Blast radius of the URL/shape change

Tests and fixtures that hardcode the packument URL or shape:

- `internal/generator/registry/npm_test.go:13-14` — URL assertions
  (`.../left-pad`, `.../@types%2fnode`) must gain `/latest`.
- `npm_test.go:40-45` — table case "falls back to latest version when top level
  missing" exercises only the obsolete fallback; drop it (or repurpose as a
  "no homepage" case, see fixtures below).
- `internal/generator/registry/testdata/npm-left-pad.json`,
  `npm-string-repo.json` — packument-shaped; refit to version-doc shape.
  `npm-version-fallback.json` — exists only for the fallback path; obsolete.
- `internal/generator/registry/registry_test.go:19` — `npmURL` constant keys
  every scripted FakeHTTPGetter response; needs `/latest`. The `npmBody`
  (`registry_test.go:28`) is already version-doc-shaped.
- `internal/policy/compile/compile_test.go:360` — hidden consumer:
  `TestRefresh_RealStackRefetchesRegistry` scripts
  `https://registry.npmjs.org/react`; must become `.../react/latest` or the
  fake returns "no scripted response".
- Integration tests (`internal/generator/registry/live_integration_test.go:24-40`,
  `internal/generator/live_integration_test.go:30-53`,
  `internal/policy/compile/live_integration_test.go:26-64`) assert on
  metadata/hosts, not URLs — they follow the change transparently.
- `internal/sysdep/sysdeptest/http_test.go:10,29` use the old URL as an
  arbitrary fake-map key — behaviorally unaffected.
- Registry disk cache (`registry.go:155-160`) is keyed by package name, not
  URL — existing cached entries stay valid across the endpoint switch.

### HTTP body cap (`internal/sysdep/http.go`)

- `maxBodyBytes = 16 << 20` (`http.go:14`); `Get` reads
  `io.ReadAll(io.LimitReader(...))` (`http.go:67`) — silent truncation.
- **Single production call site** of `HTTPGetter.Get`:
  `registry.Client.fetch` (`internal/generator/registry/registry.go:210`).
  A loud cap error propagates as `registry: fetch %q: ...` → fails that
  package's Lookup → fails the Generate/Compile run (only `ErrNotFound` is
  skipped, `internal/generator/generator.go:173-175`). Acceptable: today the
  same situation fails anyway, with a worse message.
- The interface contract comment (`http.go:31-34`) already lists "body read" as
  an error class — a cap error fits without a contract change.
- No existing test exercises the cap (`http_test.go` covers headers, statuses,
  context cancellation). The sysdeptest fake returns scripted bodies verbatim
  (no cap semantics) — unaffected.
- Detection approach: limit to `maxBodyBytes+1` and error when
  `len(body) > maxBodyBytes` — distinguishes "exactly at cap" from "over cap".

### Generator manifest handling & fixtures (`internal/generator/`)

- `packageJSON.deps` (`manifest.go:45-54`) reads **only** `dependencies` +
  `devDependencies`; `composerJSON.deps` (`manifest.go:64-80`) reads `require` +
  `require-dev`, filtering keys without `/` (`isComposerPackage`,
  `manifest.go:87-89`) — so `php`, `ext-*` are already skipped.
- Golden tests (`generator_test.go:80-106`) use `fakeLookuper`
  (`generator_test.go:26-52`) — scripted `registry.Metadata` per package, no
  HTTP. **The endpoint change does not touch the generator goldens.** Fixture
  enrichment does: every newly-listed package needs scripted metadata, and
  goldens are regenerated with `make golden` (rules ordered by sorted package
  name; homepage rule first, then repository + forge companions).
- Current fixtures are synthetic and thin:
  `testdata/package.json` (react, barehome, pageddocs, norepo, @scope/tool,
  gitlablib), `testdata/composer.json` (php, ext-mbstring, monolog/monolog,
  laravel/framework, phpunit/phpunit).

### Real-world manifest features worth covering (web research + user references)

User reference manifests (untracked repo root: `frontend_package.json`,
`backend_composer.json`, `backoffice_composer.json`) and prominent OSS manifests
(nuxt/nuxt, excalidraw/excalidraw, laravel/laravel, monicahq/monica) show:

- **package.json:** scoped deps (`@vueuse/core`, `@nuxtjs/i18n`), a
  `peerDependencies` section (currently ignored by the parser — fixtures should
  prove it is *tolerated*), `"latest"` version specs (`@types/bun: latest`),
  structural fields a parser must tolerate (`private`, `type`, `scripts`,
  `packageManager`, `engines`, `workspaces`, `resolutions`), and
  devDependencies-only manifests (nuxt root has no `dependencies` at all).
- **composer.json:** platform requirements `php`, `ext-fileinfo`, `ext-intl`
  (filtered), branch constraints (`dev-master`), exact pins, `config`
  (`allow-plugins`), `minimum-stability`, `prefer-stable`, `autoload`, `extra`.
- Registry-response features: object `repository` **with `directory`**
  (vite, @vueuse/core — monorepo packages), string repository (legacy), missing
  homepage (express).

## Impact Analysis

- `internal/sysdep/http.go` is shared infrastructure; the cap-as-error change
  affects only registry lookups (sole production caller) and turns an existing
  silent failure into a loud one. No behavioral change for bodies under 16 MiB.
- Registry cache files on user machines remain valid (keyed by package name).
- Generator output (rules/goldens) is unchanged by the endpoint switch itself.

## Out-of-scope observations (future ticket material)

- The manifest parser ignores `peerDependencies` / `optionalDependencies`
  (npm ≥7 auto-installs peers — arguably they deserve rules too) and does no
  spec-based filtering (`workspace:*`, `file:`, `link:`, `npm:` aliases, git
  URLs are looked up by name on the public registry even though they never
  resolve there). Both are behavior changes beyond this quickfix.
- Packagist verified unaffected (p2 endpoint serves small minified docs;
  `packagist.go` already reads only the newest version entry).

## Code references

- `internal/generator/registry/npm.go:17-29` — URL construction + scoped escaping
- `internal/generator/registry/npm.go:34-99` — packument types + fallback parse
- `internal/generator/registry/registry.go:205-225` — fetch/parse error wrapping
- `internal/sysdep/http.go:14,67` — body cap + silent truncation
- `internal/generator/manifest.go:45-89` — manifest dependency extraction
- `internal/generator/generator_test.go:26-52,80-106` — fakeLookuper + goldens
- `internal/policy/compile/compile_test.go:360` — hidden packument-URL consumer
- `docs/design.md:163,204` — registry endpoint wording (minor touch-up)
