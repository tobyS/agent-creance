---
date: 2026-06-23
ticket: AC-0060
topic: "Untrusted-input hardening — audit-log credential leak and hostile-manifest allowlisting"
status: complete
repo_head: 5ecdf40
---

# Research: AC-0060 — Untrusted-input hardening

## Goal

Harden three places where untrusted external data (request URLs, project
dependency manifests) flows into a credential-leak surface or an over-broad
egress rule, plus tighten the `FakeFileSystem` test double so the bug class is
catchable in unit tests:

- **F7** — egress audit log can leak credentials carried in query-string params.
- **F11** — registry package names are concatenated raw into outbound registry URLs.
- **F12** — a bare-host homepage on a shared-apex host emits a host-wide allow.
- **F14** — `FakeFileSystem` is too lenient on missing-parent semantics.

Source: `thoughts/shared/reviews/2026-06-22-codebase-quality-review.md` (F7, F11, F12, F14).

---

## F7 — Audit URL credential leak

### How it works today

The Python enforcer writes one compact JSONL line per egress decision. The full
request URL arrives whole from mitmproxy as `flow.request.pretty_url`
(`internal/proxy/enforcer/enforcer.py:320`) and is handed to `request_entry`,
which scrubs it via `scrub_url` — the **single** URL-transformation point.

`scrub_url` (`internal/proxy/enforcer/audit.py:80-94`) splits the URL, and for
each query pair replaces the value with `REDACTED` **iff the param name is in a
fixed denylist** `REDACT_QUERY_PARAMS` (`audit.py:56-71`, 12 lowercase names:
`token, access_token, api_key, apikey, key, secret, client_secret, password,
code, sig, signature, auth`). Scheme/host/path/fragment are left byte-for-byte.

The leak: any credential under a param name **not** in those 12 (e.g. `session`,
`jwt`, `bearer`, `refresh_token`, `private_token`, `pat`, `x-amz-signature`) is
logged in clear. It is a pure name-denylist with no value-shape heuristic and no
allowlist mode.

### Headers

**No HTTP headers are logged at all.** The audit entry schema (`request_entry`,
`audit.py:97-114`) is `{ts, method, url, decision, rule, status}` — there is no
header field on the Python side or the Go mirror (`internal/audit/entry.go:46-54`).
The module docstring states this explicitly (`audit.py:11-18`). So the
`docs/design.md:506` claim that "sensitive headers (`Authorization`, `Cookie`,
`X-Api-Key`, etc.) filtered before logging" describes filtering code that does
not exist — safer than the spec, but out of sync.

### Read side consumes the URL opaquely

`internal/audit/` treats `URL` as an opaque string end to end — no `net/url`
import, no query parsing anywhere:
- `ParseLine` (`entry.go:62-68`) unmarshals; `URL` is a plain `string`.
- `FormatEntry` (`format.go:19-26`) prints the URL verbatim for `logs` / `logs --follow`.
- `Summarize`/`tally` (`summary.go:60-82`) for `logs --summary` only tallies
  `Decision` and the passthrough/intercepted split — never touches `URL`.

**No consumer parses query params out of the logged URL.** Changing how the
query is logged has no downstream parsing dependency; the only contract is that
the value stays a valid JSON string.

### Cleanest change point

Everything is contained in `scrub_url` + `REDACT_QUERY_PARAMS`
(`audit.py:56-94`). Switching to an allowlist, stripping the whole query, or
redacting all values by default can be done entirely inside that function. The
only artifacts to regenerate are the two enforcer goldens
(`internal/proxy/enforcer/testdata/egress_request_entry.jsonl.golden`,
`egress_passthrough_entry.jsonl.golden`, via `--update`) and the
`test_scrub_url` parametrization.

### Test structure

`internal/proxy/enforcer/test_audit.py` — pytest functions, `update_golden`
fixture from `conftest.py`. `test_scrub_url` (`:62-81`) is a 5-case
`@pytest.mark.parametrize`; **all** its cases use names already on the denylist
— there is no test for a credential under a non-denylisted name, and no test for
full-query stripping or an allowlist. Run via `make test-enforcer`.

---

## F11 — Registry URL injection from hostile package names

### Data flow (manifest key → outbound URL)

1. Manifest bytes → `Generate` (`internal/generator/generator.go:101`).
2. Dependency names extracted via `g.eco.deps(manifest)`:
   - npm: `manifest.go:45-54` (map keys, **no filter**).
   - composer: `manifest.go:64-80`, filtered only by `isComposerPackage`
     (`:87-89`) = "contains a `/`" — no charset check.
3. Each `pkg` → `g.lookup.Lookup(ctx, pkg)` (`generator.go:176`).
4. `Lookup` (`registry/registry.go:118`) calls `cachePath(pkg)` then `fetch`.
5. `fetch` builds the URL via `c.src.url(pkg)` and GETs it (`registry.go:210`).

So `pkg` reaching `url(pkg)` is the **raw manifest map key** from a (potentially
cloned, untrusted) `composer.json` / `package.json`.

### `validatePackage` is a cache-path guard, not a URL guard

`registry/registry.go:166-179` blocks empty name, leading `/`, and any
`/`-segment equal to `""`/`.`/`..`. It does **not** block the URL charset (`?`,
`#`, `@`, `:`, `%`, whitespace, control chars all pass). Crucially it is called
only from `cachePath` (`:155-160`); the URL is built separately from the same
raw `pkg` by `c.src.url(pkg)` (`:210`), which does no validation.

### URL construction

- **Packagist** (`registry/packagist.go:17-19`): `"https://repo.packagist.org/p2/" + pkg + ".json"` — **raw concatenation, zero escaping**.
- **npm** (`registry/npm.go:19-31`): `"https://registry.npmjs.org/" + npmPackagePath(pkg) + "/latest"`. `npmPackagePath` replaces only the **first** `/` with literal `"%2f"` and only when the name starts with `@` (scoped case); unscoped names pass through verbatim. **No `url.PathEscape`** anywhere; only one character class (the single scope slash) is handled.

### Real registry name charsets (not enforced today)

- **Packagist**: `vendor/package`, each part `[a-z0-9]([_.-]?[a-z0-9]+)*`, lowercase, exactly one `/`.
- **npm**: lowercase, ≤214 chars, URL-safe `[a-z0-9-._~]`; scoped `@scope/name` joined by one `/` (encoded `%2f` in the registry URL).

### HTTP getter test seam

`sysdep.HTTPGetter` (`internal/sysdep/http.go:30-37`): `Get(ctx, url, headers)`.
`OSHTTPGetter.Get` passes the URL straight to `http.NewRequestWithContext` with
no rewriting (`http.go:49-79`).

Fake: `FakeHTTPGetter` (`internal/sysdep/sysdeptest/http.go:20-63`) records every
requested URL into `Calls []string` and looks up scripted responses **keyed by
the exact URL string** (an unmatched URL returns `"<url>: no scripted response"`).
Tests assert the requested URL via `require.Equal(t, []string{wantURL}, http.Calls)`
(e.g. `registry_test.go:55`).

### Test structure

- `registry/npm_test.go` — `TestNPMSourceURL` is a `map[string]string` table
  (`pkg → URL`); covers `left-pad` and `@types/node` (→ `%2f`), **no**
  URL-significant chars.
- `registry/packagist_test.go` — `TestPackagistSourceURL` single assertion, no adversarial names.
- `registry/registry_test.go` — `TestLookupRejectsPathTraversal` (`:166-173`)
  iterates `["../evil", "", "/abs", "a/../../b"]` asserting each errors and
  `http.Calls` stays empty — path-segment only, not URL charset.

---

## F12 — Shared-apex homepage over-allow

### `homepageRule` today (`internal/generator/homepage.go:15-29`)

The host-wide vs path-scoped decision is driven by **one** thing: whether the
parsed URL path is empty after trimming slashes.

```go
r := Rule{Rule: policy.Rule{Host: host}, Source: src}   // host-wide base
if path := strings.Trim(u.Path, "/"); path != "" {
	r.Rule.Paths = []string{"/" + path + "/"}            // path-scoped only if path present
}
```

- **Bare host** (`https://react.dev/`, `https://laravel.com`) → `policy.Rule{Host: host}`, no `Paths` — **host-wide** (the F12 case).
- **Path-carrying** (`https://someuser.github.io/coollib/`) → `Host + Paths:["/coollib/"]` — path-scoped.

Single call site: `generator.go:187-191`.

There is **no** awareness of subdomain-isolated vs shared-apex hosts, no
eTLD+1/public-suffix handling anywhere in `internal/generator/` (grep for
`publicsuffix`/`apex`/`pages.dev`/`surge.sh` → no matches). Host-wide vs
path-scoped is purely the presence/absence of `Paths` on `policy.Rule`
(`internal/policy/policy.go:78-86`).

### The reusable "known host as data" pattern — the forge table

`internal/generator/forge.go:23-40` — a package-level `var forges = map[string][]forgeHost{…}`
in the **same package** as `homepageRule`. Element struct (`forge.go:13-17`):

```go
type forgeHost struct {
	host       string
	pathTmpl   string  // "" encodes host-wide
	lowerTrust bool
}
```

`IsKnownForge` (`forge.go:77-80`) is the membership-predicate shape to mirror:
`_, ok := forges[strings.ToLower(host)]`. `pathTmpl == ""` is the table's
existing encoding of "host-wide" (`objects.githubusercontent.com`, flagged
`lowerTrust`). The forge comment (`forge.go:19-22`) frames the table explicitly
as "data, not per-generator code."

`Rule.LowerTrust` (`rule.go:31-35`) already exists as the lower-trust flag — the
host-wide forge CDN rule uses it.

### Test structure & golden coverage

- `homepage_test.go` — `TestHomepageRule` table `{name, homepage, want Rule, ok}`;
  4 cases (bare host, no-trailing-slash, path-carrying, uppercase-lowercased).
  No subdomain-isolated vs shared-apex distinction.
- The **full** generator rule set is golden-tested: `goldenRun` (`generator_test.go:58-78`)
  marshals `[]Rule` to `testdata/<name>.golden` with a `-update` flag. Homepage
  rules appear in `testdata/package_json.golden` (host-wide `react.dev`,
  `barehome.example`, `scope.example`, `bun.com`; path-scoped `someuser.github.io`).
  Any `homepageRule` change surfaces as a golden diff regenerable via `make golden`.

### Docs to reconcile

`docs/design.md:183` describes homepage scoping and explicitly claims
"Subdomain-based shared hosting (`coollib.vercel.app`) is already correctly
scoped" but says nothing about **shared-apex** hosting — this is where the new
known-shared-apex handling must be documented.

---

## F14 — FakeFileSystem missing-parent fidelity

### Current model & the three lenient methods

`internal/sysdep/sysdeptest/filesystem.go` — flat maps keyed by absolute path
(`Files`, `Dirs`, `Perms`, …), no parent-dir checks. Three methods diverge from
`os`:

- **`WriteFile`** (`:83-92`) — succeeds unconditionally, no parent check (should ENOENT on missing parent).
- **`MkdirAll`** (`:155-164`) — records only the leaf `name`, does not create/record parents.
- **`Remove`** (`:166-176`) — silent no-op on a missing path (should `fs.ErrNotExist`).

The real interface (`internal/sysdep/filesystem.go:19-44`) documents each as
mirroring `os`. `RemoveIfPresent` (`:83-102`) is a Stat-then-Remove helper that
**explicitly** exists to paper over the fake's lenient `Remove` — which is why
no production caller relies on bare `Remove`-on-missing.

### Production atomic-write paths are already correctly ordered

Every production atomic-write MkdirAlls the parent before WriteFile and renames a
temp into the now-existing dir: `registry/registry.go:230-249` (`writeCache`),
`generator/cache.go:62-77`, `proxy/extract.go:73-110`, `profile/compile.go:76-85`,
`policy/compile/compile.go:617-626`. All pass under stricter semantics. **Except**
`cage.Prepare` (see below).

### Complete caller-breakage audit

The fake is used in ~38 test files, but most seed state via **direct map writes**
(`fsys.Files[p]=…`, `fsys.Dirs[d]=true`) which bypass the methods. Exactly **three**
locations exercise the lenient behavior through real callers and will fail under
strict semantics:

1. **`internal/sysdep/sysdeptest/filesystem_test.go`** — the fake's own tests;
   several cases (`:11, :35, :69, :79, :92`) `WriteFile` under an uncreated
   parent and encode the current behavior. Must be reworked + add the three new
   missing-parent assertions.
2. **`internal/gitremote/gitremote_test.go:83`** — `WriteFile(/proj/.git/config)`
   with no preceding `MkdirAll` of `/proj/.git`.
3. **`internal/cage/prepare_test.go`** (cases at `:37, :72, :86, :103, :120`) —
   `cage.Prepare` writes five `Layout.Root` fragments (`cage.go:241,254,267,275,287`)
   to `/root/*.sb` but only MkdirAlls `<home>/.claude` (`cage.go:232`), never
   `Layout.Root`. **This is a real production code-smell the fake currently
   hides**: in the full `runRun` flow `Layout.Root` is MkdirAll'd earlier by
   `proxy.Attach` (`lifecycle.go:130`) and `compile` (`compile.go:617`), so it
   works in practice — but `cage.Prepare` in isolation depends on the lenient
   fake. Tightening the fake will force either a `MkdirAll(Layout.Root)` in
   `cage.Prepare` or a `MkdirAll` in the test setup.

The registry test `TestLookupCacheMissFetchesOnceAndWritesCache`
(`registry_test.go:58`) already asserts `fsys.Dirs[npmDir]` is created — a
natural home for the "would fail if MkdirAll omitted" guarantee.

---

## Open Questions (for the checkpoint)

1. **F7 redaction approach** — drop the whole query string from the logged URL
   (safest; host+path keep debugging value), switch to an **allowlist** of safe
   param names (keeps benign `q`/`page`/`version` visible), or broaden the
   denylist with substring matching on token-like names. The ticket proposes
   "drop or allowlist by default." No `logs` consumer needs query params, so
   either is safe technically.
2. **F12 handling for a known shared-apex bare host** — drop the rule entirely
   (require the path-carrying form), or emit it host-wide but flagged
   `LowerTrust`. The ticket proposes "do not emit a host-wide allow." Plus: what
   seeds the known-shared-apex set (a small curated in-package table like the
   forge map — `pages.dev`, `surge.sh`, `netlify.app`?, `vercel.app` is already
   subdomain-isolated, `web.app`/`firebaseapp.com`, `github.io` is apex but
   already handled by the forge path-scoping)?
3. **F11 strategy** — `url.PathEscape` each segment (universal, preserves the
   intended `/` separator structure) and/or add a strict registry-name charset
   validator that rejects non-conforming names before any URL build. Escaping
   alone neutralizes the injection; charset validation additionally rejects
   junk early. Likely do both: validate charset, then escape defensively.
4. **F14 cage.Prepare** — when tightening surfaces the `cage.Prepare` ordering,
   fix `cage.Prepare` to `MkdirAll(Layout.Root)` itself (most faithful), or only
   fix the test setup. The former removes the latent dependency on callers
   pre-creating the dir.

## Anchors (file:line index)

| Finding | Primary anchors |
|---------|-----------------|
| F7 | `internal/proxy/enforcer/audit.py:56-94`, `test_audit.py:59-89`, `docs/design.md:506`, enforcer goldens in `internal/proxy/enforcer/testdata/` |
| F11 | `registry/registry.go:166-210`, `packagist.go:17-19`, `npm.go:19-31`, `sysdeptest/http.go:20-63`, `registry/*_test.go` |
| F12 | `internal/generator/homepage.go:15-29`, `forge.go:13-80`, `rule.go:31-35`, `homepage_test.go`, `generator_test.go:58-78`, `testdata/package_json.golden`, `docs/design.md:183` |
| F14 | `sysdep/sysdeptest/filesystem.go:83-176`, `filesystem_test.go`, `gitremote_test.go:83`, `cage/prepare_test.go` + `cage.go:230-291`, `registry/registry.go:230-249` |
