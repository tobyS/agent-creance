---
date: 2026-06-23
ticket: AC-0060
topic: "Untrusted-input hardening — audit-log credential leak and hostile-manifest allowlisting"
research: thoughts/shared/research/2026-06-23-AC-0060-untrusted-input-hardening.md
status: ready
---

# Plan: AC-0060 — Untrusted-input hardening

## Overview

Four independent hardening fixes around untrusted external input. The decisions
made at the question checkpoint:

- **F7** — drop the query string entirely from the logged audit URL (no denylist).
- **F11** — validate registry package names against a per-registry charset **and**
  `url.PathEscape` each path segment defensively.
- **F12** — for a bare-host homepage on a curated known-shared-apex host, **drop**
  the rule (no host-wide allow; no path to scope to).
- **F14** — tighten `FakeFileSystem` missing-parent semantics and fix the
  `cage.Prepare` ordering the tightening surfaces.

Phases are ordered enabler-first (F14), then the two generator fixes (F11, F12),
then the Python-only audit fix (F7). Each phase is independently committable and
verifiable.

## Current state

- Audit URL scrubbing is a 12-name denylist in `scrub_url`
  (`internal/proxy/enforcer/audit.py:56-94`); credentials under any other param
  name leak. No headers are logged; `docs/design.md:506` wrongly claims header
  filtering. Read side (`internal/audit/`) treats the URL opaquely.
- Registry names flow raw into URLs: Packagist concatenates
  (`packagist.go:18`), npm escapes only the single scope slash (`npm.go:19-31`).
  `validatePackage` (`registry.go:166`) guards the cache path, not the URL.
- `homepageRule` (`homepage.go:15-29`) emits host-wide for any bare host with no
  shared-apex awareness. The reusable data pattern is `forge.go:23-40`.
- `FakeFileSystem` (`sysdeptest/filesystem.go`) lets `WriteFile` succeed under a
  missing parent, `MkdirAll` not create parents, `Remove` no-op on missing.

## Desired end state

- The logged audit URL never contains a query string; docs match reality.
- A hostile manifest package name is rejected before any URL is built, and even
  a permitted name is percent-escaped per segment so it cannot reshape the URL.
- A bare-host homepage on a known shared-apex host emits no rule; dedicated
  bare hosts (`react.dev`) and path-carrying homepages are unchanged.
- `FakeFileSystem` mirrors `os` for the three methods; `cage.Prepare` creates
  its own root; a missing `MkdirAll` before a registry `WriteFile` fails a test.

---

## Phase 1 — F14: FakeFileSystem fidelity + cage.Prepare

### Changes

**`internal/sysdep/sysdeptest/filesystem.go`**

1. `WriteFile` — before storing, require the parent dir to exist. Compute
   `parent := filepath.Dir(name)`; if `parent` is not a recorded dir and not a
   root sentinel (`/`, `.`, ``), return a `*fs.PathError{Op:"write", Path:name,
   Err: fs.ErrNotExist}`. Treat `/`/`.`/`` as always-existing (mirrors `os`,
   where the root always exists). Keep the existing `WriteErrs` knob taking
   precedence.
2. `MkdirAll` — create every ancestor. Walk from the path up to the root,
   recording each ancestor in `Dirs` (idempotent; like `os.MkdirAll`). Keep
   `MkdirErrs` precedence and `Perms[name]`.
3. `Remove` — if `name` is in none of `Files`/`Dirs`/`Symlinks`, return
   `*fs.PathError{Op:"remove", Path:name, Err: fs.ErrNotExist}`; otherwise delete
   as today. Keep `RemoveErrs` precedence.

Add a short doc comment on each noting it now mirrors `os` semantics, so future
readers don't reintroduce leniency.

**`internal/sysdep/sysdeptest/filesystem_test.go`**

4. Rework the existing cases that write under an uncreated parent
   (`:11, :35, :69, :79, :92`): `MkdirAll` the parent dir first.
5. Add three new assertions:
   - `WriteFile` to a path whose parent was never created → `errors.Is(err, fs.ErrNotExist)`, and nothing stored.
   - `MkdirAll("/a/b/c")` → `Stat` of `/a`, `/a/b`, `/a/b/c` each report a dir.
   - `Remove` of a missing path → `errors.Is(err, fs.ErrNotExist)`.

**`internal/cage/cage.go`**

6. In `Prepare` (`:230`), before the first fragment write (`:241`), `MkdirAll`
   the fragment root dir (the dir the five `Layout.*SB()` paths share —
   `filepath.Dir(dst)` / the Layout root) with `0o700`, wrapping the error as
   `cage: create <dir>: %w`. This removes the latent dependency on
   `proxy.Attach`/`compile` having pre-created the dir.

**`internal/gitremote/gitremote_test.go`**

7. At `:83`, `MkdirAll` the `/proj/.git` parent before `WriteFile`ing the config.

**`internal/generator/registry/registry_test.go`**

8. Make the "MkdirAll-before-WriteFile" guarantee explicit: in
   `TestLookupCacheMissFetchesOnceAndWritesCache` (already asserts
   `fsys.Dirs[npmDir]`), add a comment that, under the strict fake, omitting the
   `MkdirAll` in `writeCache` would make this fail. (No separate test needed —
   `writeCache` is unexported and this path already exercises it.)

### Success criteria

- [x] `make test` green (whole module) — especially `internal/sysdep/sysdeptest`,
      `internal/cage`, `internal/gitremote`, `internal/generator/registry`.
- [x] The three new fake assertions exist and pass.
- [x] `make lint` green.

> Note: tightening also surfaced two call sites beyond the research audit, both
> test-fidelity (not prod bugs): `cli/init_test.go` (seed project root as a dir)
> and `proxy/extract_test.go` (allow the enforcer root's MkdirAll-created ancestors).

---

## Phase 2 — F11: registry name charset validation + escaping

### Changes

**`internal/generator/registry/registry.go`**

1. Add `validate(pkg string) error` to the `source` interface (`:68-75`),
   alongside `name`/`url`/`parse`.
2. In `Lookup` (`:118`), after `cachePath` validation and before `fetch`, call
   `c.src.validate(pkg)`; on error return it (no HTTP call). This guarantees a
   rejected name never reaches `url()`/`http.Get`.

**`internal/generator/registry/packagist.go`**

3. Implement `packagistSource.validate`: split on `/`, require exactly two
   non-empty segments (vendor, package), each matching the Packagist charset
   `^[a-zA-Z0-9]([_.-]?[a-zA-Z0-9]+)*$`. Reject anything else (covers `?`, `#`,
   `@`, `%`, extra `/`, whitespace, control chars).
4. Rewrite `url` to escape defensively:
   `"https://repo.packagist.org/p2/" + url.PathEscape(vendor) + "/" + url.PathEscape(pkg) + ".json"`
   (validated names are unaffected by escaping; escaping is the belt-and-suspenders).

**`internal/generator/registry/npm.go`**

5. Implement `npmSource.validate`: accept an optional `@scope/` prefix; validate
   the scope and the unscoped name each against the npm charset
   `^[a-zA-Z0-9][a-zA-Z0-9._~-]*$` (allow legacy mixed-case; reject URL-significant
   chars). Reject more than one `/`, a name starting with `.`/`_`, and empty
   segments.
6. Rewrite `npmPackagePath`/`url` to escape each segment with `url.PathEscape`,
   keeping the `%2f` join for scoped names:
   scoped → `url.PathEscape("@scope") ... "%2f" + url.PathEscape(name)`; unscoped
   → `url.PathEscape(name)`. (For valid names this yields the same URLs the
   current tests expect: `left-pad`, `@types%2fnode`.)

### Tests

**`internal/generator/registry/packagist_test.go` / `npm_test.go`**

7. Add `validate` table tests: valid names pass; hostile names
   (`vendor/pkg?x=https://evil`, `vendor/pkg#frag`, `@`, `../../x`, encoded
   `vendor%2f..`, whitespace) return an error.
8. Keep the existing `url` assertions (valid names unchanged).

**`internal/generator/registry/registry_test.go`**

9. Add an adversarial table (mirroring `TestLookupRejectsPathTraversal`) feeding
   hostile names through `Lookup` and asserting (a) an error and (b)
   `http.Calls` is empty — proving the name never shaped an outbound request.

### Success criteria

- [x] `make test` green; new adversarial cases prove `http.Calls` stays empty for hostile names.
- [x] Existing `left-pad` / `@types/node` / `monolog/monolog` URL tests still pass unchanged.
- [x] `make lint` green.

---

## Phase 3 — F12: shared-apex homepage drop

### Changes

**`internal/generator/sharedapex.go`** (new, mirrors the `forge.go` data pattern)

1. Add `var sharedApexHosts = map[string]bool{…}` — a curated set of hosts where
   many independent tenants share one apex distinguished only by path (so a
   whole-host allow over-allows). Seed conservatively with verified
   path-multiplexed apexes (candidates to confirm against real registry
   metadata: `sourceforge.net`, `pythonhosted.org`). Add a `isSharedApex(host
   string) bool` predicate (case-insensitive lookup, mirroring `IsKnownForge`
   `forge.go:77-80`). Document in a comment that this is data, not per-call code,
   and that subdomain-isolated platforms (`*.vercel.app`, `*.github.io`) are
   intentionally absent — they are already safe.

**`internal/generator/homepage.go`**

2. In `homepageRule`, after computing `host` and finding the path is empty (the
   bare-host branch), if `isSharedApex(host)` return `(Rule{}, false)` — drop the
   rule. Leave the dedicated bare-host and path-carrying branches unchanged.
   Add a comment pointing at F12 / the design-doc section.

**`docs/design.md`** (~line 183, "Allowlist generators")

3. Update the homepage-scoping paragraph: a bare-host homepage on a known
   shared-apex host emits **no** rule (it can't be path-scoped and a whole-host
   allow would cover every co-tenant); dedicated bare hosts and path-carrying
   homepages are unchanged; the shared-apex set is curated data.

### Tests

**`internal/generator/homepage_test.go`**

4. Add table cases: bare host on a shared apex (e.g. `https://sourceforge.net/`)
   → `ok == false`; dedicated bare host (`https://react.dev/`) → host-wide
   (unchanged); path-carrying on a shared apex (`https://sourceforge.net/projects/x/`)
   → path-scoped (unchanged — the path branch wins).

5. No change to `testdata/package_json.golden` is expected (no fixture uses a
   shared-apex bare-host homepage). If a new generator-level fixture is added to
   demonstrate the drop, regenerate with `make golden` and review the diff.

### Success criteria

- [x] `make test` green; new homepage cases pass.
- [x] Existing homepage/generator golden tests unchanged (no fixture used a shared-apex bare-host homepage; kept the coverage at the focused homepageRule unit level).
- [x] `docs/design.md` homepage section reflects shared-apex handling.
- [x] `make lint` green.

---

## Phase 4 — F7: drop audit query string + reconcile docs

### Changes

**`internal/proxy/enforcer/audit.py`**

1. Replace `scrub_url` (`:80-94`) with a function that drops the query (and
   fragment) entirely: `parts = urlsplit(url); return
   urlunsplit(parts._replace(query="", fragment=""))` (return `url` unchanged
   when there is no query, to avoid needless re-encoding). Rename to reflect the
   new behavior if it improves clarity (e.g. `strip_query`), updating the call
   at `:110`.
2. Delete the now-dead `REDACT_QUERY_PARAMS` denylist (`:56-71`) and the
   `REDACTED` sentinel if unused elsewhere (grep first).
3. Update the module docstring (`:11-18`) to state the URL is logged with the
   query string removed (and that no headers are logged).

**`internal/proxy/enforcer/test_audit.py`**

4. Rewrite `test_scrub_url` (`:62-81`): assert the query is removed for
   credentials under arbitrary param names — `session`, `jwt`,
   `x-amz-signature`, an uppercase variant — and that none of those values
   survive. Keep a no-query-unchanged case and a path-preserved case.
5. Update `test_request_entry_scrubs_url` (`:84-89`) to assert the whole query
   is gone from the logged entry.

**`internal/proxy/enforcer/testdata/egress_request_entry.jsonl.golden`**

6. Regenerate via the enforcer `--update` flag (`make test-enforcer` update mode);
   the URL loses its `?q=widgets&api_key=REDACTED` suffix. Review the diff.

**`docs/design.md`** (line 506)

7. Reconcile: remove the false "sensitive headers (`Authorization`, `Cookie`,
   `X-Api-Key`, …) filtered before logging" claim; state that **no** request
   headers are logged at all, and that the logged URL has its query string
   stripped (so query-borne credentials never reach the log). Keep the rest of
   the entry-field description.

### Success criteria

- [x] `make test-enforcer` green; new pytest cases prove no credential under any
      param name survives into the logged URL.
- [x] The regenerated golden has no query string; diff reviewed.
- [x] `docs/design.md:506` matches the implementation (no headers, query stripped).

---

## Final verification (all phases)

- [x] `make test` green (race).
- [x] `make test-enforcer` green (117 passed).
- [x] `make lint` green.
- [x] `make build` — `bin/agent-creance` reflects the final commit.
- [x] Acceptance criteria F7/F11/F12/F14 in the ticket all satisfied.

## Testing strategy

- F14: table/behavioral tests on the fake itself + the three real callers; the
  registry cache-miss test encodes the MkdirAll-ordering guarantee.
- F11: per-source `validate` tables + an end-to-end adversarial `Lookup` table
  asserting `http.Calls` stays empty (the exact requested-URL seam from
  `FakeHTTPGetter`).
- F12: `homepageRule` table cases (shared-apex drop, dedicated unchanged,
  path-carrying unchanged); generator golden unchanged.
- F7: pytest parametrization over credential-bearing query params (arbitrary
  names, uppercase) asserting removal; enforcer golden regenerated.

## Notes / risks

- F11 strict charset could in principle reject an unusual-but-real legacy name;
  the chosen charsets allow mixed-case and `._~-` to minimize that, and the
  escape layer means even a future loosening can't reshape the URL.
- F12 set membership is the one item to validate against real registry metadata
  during implementation; the drop logic is independent of which hosts are in the
  set. Start minimal and conservative.
- F14 `cage.Prepare` MkdirAll is additive and harmless in the real `runRun` flow
  (the dir is already created earlier); it only removes the hidden test-time
  dependency.
