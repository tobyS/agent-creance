---
date: 2026-06-05
ticket: AC-0012
title: "Allowlist generators (package_json, composer_json) (WP-2.3)"
status: complete
git_commit: 813f3314267103f558af47dd1cfbd448306f7d1b
branch: main
depends_on: AC-0011
feeds: AC-0013
researcher: Claude (Opus 4.8)
---

# Research: AC-0012 — Allowlist generators (`package_json`, `composer_json`) (WP-2.3)

## Research Question

How do we turn a project's dependency manifest (`package.json` / `composer.json`)
into a deterministic, source-annotated set of egress **allow rules** — including
correctly path-scoped homepage rules and the GitHub companion content-host set —
cached by manifest hash, building on the AC-0011 registry client?

## Summary

Everything below the rule-emission layer already exists and is exactly the shape
AC-0012 needs:

- **Registry lookups** (`internal/generator/registry`, AC-0011) return a
  `Metadata{Homepage, Repository}` per package, fully cached on disk (30-day lazy
  refresh) behind the `sysdep.HTTPGetter` seam. Steady-state lookups make **zero**
  HTTP calls.
- **The rule model** (`internal/policy.Rule` + `internal/config.Rule`) already
  expresses host-wide and path-scoped allow rules with the exact
  `Host`/`Paths`/`Methods`/`Mode` fields the design's homepage/repository/companion
  rules require. The matcher's path semantics are **prefix-by-default segment
  matching**, which is precisely what `/<org>/<repo>/` scoping wants.
- **Cache-path plumbing** (`internal/state`) has the `RegistriesRoot()` pattern to
  mirror for a new generator-output cache helper.
- **Config** already parses + merges `network.egress.generators []string`.

**What does NOT exist yet (this ticket builds it):**

1. Any `package_json` / `composer_json` generator code — `internal/generator/`
   has only the `registry/` subpackage; no top-level `.go` files.
2. Manifest parsing (`package.json` dependencies+devDependencies; `composer.json`
   require+require-dev).
3. Repository-URL normalization (strip `git+`, `git://`, scp-style `git@host:`,
   `.git` suffix) → `host` + `<org>/<repo>`.
4. The **forge content-host table** (data) — GitHub's `raw.`/`codeload.`/`<org>.github.io`/`objects.githubusercontent.com`.
5. A **rule type carrying a source annotation** (`generated:package_json:<pkg>`) —
   neither `config.Rule` nor `policy.Rule` has a provenance field today.
6. The **generator-output cache keyed on manifest hash** (the second cache layer;
   AC-0011 built only the per-package metadata cache).
7. Validation/dispatch of generator names (config parses the list but nothing maps
   `package_json` → an implementation).

Both ticket "Questions for Research/Planning" are answerable from the design (see
[Open Questions](#open-questions-resolved)). The one genuine human-judgment call is
whether to ship a GitLab forge row now or GitHub-only per the v0.1 design.

## Detailed Findings

### 1. The registry client this builds on (AC-0011)

`internal/generator/registry` — the **only** code under `internal/generator` today.

- **`Metadata`** (`registry.go:61-64`): `{Homepage string; Repository string}` —
  stored verbatim as the registry reports them (the design's trust model). This is
  the sole input AC-0012's emission logic consumes.
- **Constructors**: `registry.NewNPM(fs, clock, http, registriesRoot) *Client` and
  `registry.NewPackagist(...)` (`registry.go:88-95`). The **caller chooses** which
  registry by constructor — there is no runtime name sniffing. So the `package_json`
  generator wires `NewNPM`; `composer_json` wires `NewPackagist`.
- **`(*Client).Lookup(ctx, pkg) (Metadata, error)`** (`registry.go:118-138`): serves
  a fresh per-package cache entry with zero HTTP, else fetches once and caches.
- **`registry.ErrNotFound`** (`registry.go:56`): returned (wrapped) on HTTP 404. The
  package doc comment explicitly states AC-0012 should treat this as **"emit no
  rules"**, not a hard failure (`registry.go:54-56`). Note: a *missing field*
  (empty `Homepage`/`Repository`) is a normal `Metadata{}` with `nil` error — also
  "emit no rule" but NOT an error. ErrNotFound is the package-doesn't-exist case.
- **Repository URL forms** are stored verbatim (`npm.go:46-75`, `packagist.go:25-30`).
  npm's `repository` is polymorphic (string or `{type,url}`), already normalized to
  a URL string. Real-world values AC-0012 must normalize:
  `git+https://github.com/org/repo.git`, `git://github.com/org/repo.git`,
  `git@github.com:org/repo.git` (scp-like SSH), `https://github.com/org/repo`,
  trailing `.git` and/or `/`. Packagist `source.url` is typically
  `https://github.com/Vendor/pkg.git`.

### 2. The rule model AC-0012 emits into

Two `Rule` types exist; generated rules must ultimately become `policy.Rule`s in a
compiled policy (AC-0013), each annotated with a source.

- **`config.Rule`** (`config.go:77-83`): YAML-facing; `Paths`/`Methods` are
  `*[]string` (nil = key omitted).
- **`policy.Rule`** (`policy.go:68-74`): matcher-facing; plain `[]string`, json tags.
  `policy.FromConfig(config.Egress)` bridges config → matcher (`policy.go:112-117`).
- **Neither has a provenance/source field.** The design's
  `[generated:package_json:react]` annotation (`design.md:191-200`) and the
  lower-trust flag for `objects.githubusercontent.com` (`design.md:174`) are
  **not modeled anywhere yet**. AC-0012 introduces the carrier type. AC-0013 (the
  policy compiler) is the consumer and needs: the rule + its source string +
  (optionally) a lower-trust marker.

**Matcher path semantics (`policy/glob.go`)** — confirm the scoping mechanism:
- `matchHost` (`glob.go:10-24`): `*` any; `*.suffix` subdomain (apex excluded);
  else exact. So `<org>.github.io` is a distinct exact host per org — subdomain
  hosting is already correctly isolated.
- `matchPath` (`glob.go:30-63`): **prefix-by-default segment matching**. A rule with
  `Paths: ["/facebook/react/"]` matches `/facebook/react/...` and nothing outside it.
  This is exactly the `/<org>/<repo>/` and `/coollib/` scoping the design wants —
  **no new matching code is needed**, only correct path strings.

### 3. Homepage host-wide vs path-scoped — the algorithm (design-resolved)

Design (`design.md:162-167`): the decision is **purely whether the homepage URL
carries a path**:
- Bare host (`https://react.dev/`, path is empty/`/`) → **host-wide** rule
  (`Host: react.dev`, no `Paths`).
- Path-carrying (`https://someuser.github.io/coollib/`) → **path-scoped** rule
  (`Host: someuser.github.io`, `Paths: ["/coollib/"]`).

The ticket's open question — "how is the tenant path derived for shared hosts
(`github.io`, `*.readthedocs.io`)?" — is answered by this: **the tenant path is just
the homepage URL's own path.** No shared-host table is required for homepages; the
shared-host motivation (`design.md:164`) is *why* path-scoping matters, but the
*mechanism* falls out of "URL has a path → scope to it" and works uniformly for any
host. (`someuser.github.io` serves multiple projects under different path prefixes,
so scoping to `/coollib/` stops allowlisting that user's *other* pages.)

### 4. Repository rule + the forge content-host table (design-resolved data)

For a package's `repository` URL, normalize → `host` + `<org>/<repo>`, then:

- Always emit the repository rule scoped to `<org>/<repo>/` on the forge web host
  (`design.md:165`). For GitHub: `Host: github.com`, `Paths: ["/<org>/<repo>/"]`
  (covers web view + `.git` operations).
- **If the host is a known forge**, emit companion content-host rules
  (`design.md:169-176`). The **GitHub** table (fully specified, this is *data*):

  | Host | Path scope | Trust |
  |------|-----------|-------|
  | `github.com` | `/<org>/<repo>/` | normal |
  | `raw.githubusercontent.com` | `/<org>/<repo>/` | normal |
  | `codeload.github.com` | `/<org>/<repo>/` | normal |
  | `<org>.github.io` | `/<repo>/` | normal |
  | `objects.githubusercontent.com` | **host-wide** (hash-addressed, not org-scoped) | **lower-trust** (separately flagged; a stricter threat model can drop it) |

- The mapping is **data, not per-generator code** (`design.md:176`) — both
  generators (and a future `agent-creance allow <repo-url>`) consult the same table.
  Implement it as a table keyed by forge web host.

GitLab (`gitlab.com` → `*.gitlab.io`, …) is described as "analogous … as they're
added" (`design.md:176`); the ticket's Out of Scope says "only GitHub ships in v0.1
per design; add GitLab only if trivial." → **Genuine decision for the checkpoint.**

### 5. Manifest parsing (net-new)

- **`package.json`**: union of `dependencies` + `devDependencies` keys (direct only,
  no transitives). Keys are the package names (incl. scoped `@scope/name`). The
  registry path-encodes scoped names already (`npm.go:24-29`).
- **`composer.json`**: union of `require` + `require-dev` keys. Filter out the
  platform/meta requirements that are **not Packagist packages**: `php`, `php-*`,
  `ext-*`, `lib-*`, `composer`, `composer-*`, `composer-runtime-api`,
  `composer-plugin-api`. (A `composer.json` `require` always contains `php` and often
  `ext-*`; looking these up on Packagist would 404.) Real package names are
  `vendor/name` — the registry expects exactly that form (`packagist.go:17`).
- Decode leniently (these are third-party files), unlike the project's own strict
  config decode.

### 6. The generator-output cache keyed on manifest hash (net-new)

AC-0011 built only the **per-package** metadata cache. AC-0012 owns the **second
layer** (`design.md:184-187`): keyed on the manifest's content hash, so an unchanged
manifest reuses the prior rule set with **zero** package iteration / registry calls
(acceptance criterion #6; test step 4 asserts `fake call count == 0` on the second
run).

Pattern to mirror: `internal/state` exposes `RegistriesRoot()`
(`state.go:116-122`) → `<cache>/agent-creance/registries`, a cross-project sibling
of `projects/<hash>/`. The natural addition is a `GeneratorsRoot()` →
`<cache>/agent-creance/generators` helper, with the generator joining
`<root>/<generator-name>/<sha256(manifest-bytes)>.json` (storing the emitted rule
set). This keeps the deterministic, atomic write-then-rename idiom AC-0011 already
established (`registry.go:217-236`). Hashing the **manifest bytes** is sufficient and
matches the design's "manifest's hash" wording; the generator name disambiguates the
two generators.

> Note on overlap with AC-0013: AC-0013 (policy compiler, WP-2.4) has its **own**
> input-hash cache that *includes* the manifests. AC-0012's output cache is the
> finer-grained "did this manifest change" gate that lets a compile reuse generator
> output without re-walking deps. They are complementary layers, not duplicates.

### 7. Config wiring (already half-built; AC-0012 finishes dispatch)

- `config.Egress.Generators []string` is parsed (`config.go:67-71`) and union-deduped
  across `include:` layers (`merge.go:34`). The example fixture lists
  `[package_json, composer_json]` (`config_test.go`).
- **No validation** of generator names today (`validate.go:12-15` only checks
  allow/deny rules). AC-0012 owns (a) rejecting unknown generator names and
  (b) dispatching each name to its generator. Whether name validation lives in
  `config.validate` or in the generator dispatch layer is a planning choice; keeping
  it in the generator package (the registry of known names lives there) avoids
  `config` importing `generator`.
- **No CLI wiring yet** — there is no consumer of generator output until AC-0013
  compiles it into `policy.json`. AC-0012 should expose a clean
  "manifest bytes + registry client → annotated rules" API and **not** wire it into
  the `App`/commands (that's AC-0013), mirroring how AC-0011 deliberately stopped
  short of wiring (`AC-0011 plan, "What We're NOT Doing"`).

### 8. Test & convention patterns to mirror

- **Golden rule-set output**: the ticket's verification (steps 2-3) wants golden
  files for a fixture `package.json` and `composer.json`. The project's golden idiom
  is the `-update` flag (`make golden`); fixtures under `testdata/`
  (`prereq/report_test.go` precedent, and `registry/testdata/*.json`).
- **Call-count assertions**: `FakeHTTPGetter.Calls` (`sysdeptest/http.go`) and the
  AC-0011 tests (`registry_test.go:55,76,100`) are the template for "zero registry
  calls on cache hit." For the **output-cache** test, asserting zero `Lookup` calls
  needs either the real `*Client` with a `FakeHTTPGetter` (assert `Calls` empty) or a
  small lookup interface the generator depends on so a fake can count calls — prefer
  an interface (`type lookuper interface{ Lookup(ctx, pkg) (Metadata, error) }`) so
  the generator is unit-testable without constructing a full registry `Client`.
- **Hermetic, fakes-only** unit tests; real registries only under
  `//go:build integration`. Table-driven for pure logic (URL normalization,
  manifest parsing, forge-table expansion).

## Code References

- `internal/generator/registry/registry.go:61` — `Metadata{Homepage, Repository}`
- `internal/generator/registry/registry.go:88` — `NewNPM` / `NewPackagist` (`:93`)
- `internal/generator/registry/registry.go:118` — `Lookup`
- `internal/generator/registry/registry.go:56` — `ErrNotFound` (treat as "no rules")
- `internal/generator/registry/npm.go:46` — polymorphic `repository` normalization
- `internal/generator/registry/packagist.go:25` — `source.url` extraction
- `internal/policy/policy.go:68` — `policy.Rule` (no provenance field)
- `internal/policy/policy.go:112` — `FromConfig` bridge
- `internal/policy/glob.go:30` — prefix-by-default path matching (scoping mechanism)
- `internal/policy/glob.go:10` — host matching (`*.suffix`, exact)
- `internal/config/config.go:67` — `Egress.Generators []string`
- `internal/config/validate.go:12` — validation (no generator-name check)
- `internal/state/state.go:116` — `RegistriesRoot()` (helper pattern to mirror)
- `internal/sysdep/sysdeptest/http.go` — `FakeHTTPGetter` (`Calls` for call counts)
- `docs/design.md:155-210` — "Allowlist generators" (algorithm, forge table, caches,
  trust model, provenance annotations) — the canonical spec
- `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md:175-182` —
  WP-2.3 "Done when" criteria

## Architecture Insights

- The `registry.source` interface (`registry.go:68-75`) is the per-registry strategy
  seam; the *generator* layer adds an analogous per-ecosystem strategy: manifest
  shape (which keys to walk) + which registry constructor to use. Keep them parallel
  and small.
- **Path scoping is "free"** given the matcher's prefix semantics — the engineering
  is entirely in (a) URL normalization and (b) the forge table, not in matching.
- The forge content-host expansion is shared with `agent-creance allow <repo-url>`
  (`design.md:176`) — design it as a reusable `func forgeRules(repoURL) ([]rule,
  bool)` table-lookup, not baked into the manifest walk.
- AC-0012's output type is the first place provenance enters the codebase. Defining
  it well (rule + source string + lower-trust bool) directly shapes AC-0013's
  compiler and `policy show` rendering.

## Open Questions — Resolved

1. **Path-scoping algorithm for shared hosts** (ticket Q2) → Resolved from design:
   the tenant path is the homepage URL's own path; host-wide iff the URL has no path.
   No shared-host table needed.
2. **Forge content-host table — GitHub entries** (ticket Q1) → Fully specified by
   `design.md:169-174` (table reproduced in §4 above).

## Open Question — Needs Human Judgment (checkpoint)

- **GitLab forge row in v0.1?** The design says GitHub ships in v0.1 and other forges
  come "as they're added"; the ticket Out of Scope says "add GitLab only if trivial."
  Adding `gitlab.com` → web `/<org>/<repo>/` + `*.gitlab.io` pages is genuinely cheap
  given the table-driven structure. Decision: **GitHub-only (per design)** vs **also
  add a GitLab row now**. (Either way the table must be structured for trivial
  extension.)

## Related Research

- `thoughts/shared/research/2026-06-05-AC-0011-registry-clients-cache.md` — the
  registry client/cache this builds on.
- `thoughts/shared/research/2026-06-05-AC-0010-rule-model-matcher.md` — the rule
  model + matcher generated rules feed into.
- `thoughts/shared/tickets/AC-0013-policy-compiler.md` — the downstream consumer
  (union explicit + generated + …, source annotations, input-hash cache).
