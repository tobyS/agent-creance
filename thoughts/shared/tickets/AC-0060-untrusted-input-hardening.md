# AC-0060: Untrusted-input hardening — audit-log credential leak and hostile-manifest allowlisting

**Status:** In Progress
**Estimated Complexity:** Medium
**Created:** 2026-06-22
**Updated:** 2026-06-23

## Problem Statement

The 2026-06-22 security review
(`thoughts/shared/reviews/2026-06-22-codebase-quality-review.md`) found three **High**
findings (plus one test-infra enabler) that share a theme: untrusted data — request
URLs and project dependency manifests — flowing into a credential leak or an
over-broad/misdirected egress rule. They are grouped because the fixes are all about
treating that external input defensively.

**F7 — Credential leakage in the egress audit log.** `audit.py` redacts a fixed
*denylist* of query-parameter names (`internal/proxy/enforcer/audit.py:56`,
`REDACT_QUERY_PARAMS`) from the logged URL. Any credential carried under a param name
not in the set (`session`, `jwt`, `bearer`, `refresh_token`, `private_token`, `pat`,
`x-amz-signature`, …) is written to the audit log in clear. Because the audit log is
itself a credential-leak surface this module's own docstring worries about, a denylist
is the weak shape. Separately, `docs/design.md:506` promises "sensitive headers
filtered before logging," but the implementation logs **no headers at all** — safer
than the spec, but the doc and code are out of sync and should be reconciled.

**F11 — Generator does not escape the package name into the registry URL.**
`internal/generator/registry/registry.go:166` (`validatePackage`) blocks filesystem
path traversal (`.`/`..`/absolute) but not the URL charset, and
`internal/generator/registry/packagist.go:18` concatenates the package name raw into
the request path (`https://repo.packagist.org/p2/<pkg>.json`). The package name flows
from a project's `composer.json` `require` keys — potentially untrusted in a cloned
repo — so a name containing `?`, `#`, `@`, or encoded separators is injected into the
URL the host-side fetcher requests (an SSRF-flavored / request-shaping risk). The npm
side is more constrained but should be audited for the same.

**F12 — A bare-host homepage allowlists a whole shared-apex host.**
`internal/generator/homepage.go:24` emits a **host-wide** allow when a package's
registry `homepage` is a bare host. That is safe for subdomain-isolated platforms
(each tenant gets its own host) but not for shared-apex hosts where many tenants live
behind one apex (`pages.dev`, `surge.sh`, regional CDNs): one package's homepage then
allowlists every tenant's content — the exact tenant-isolation the path-scoping logic
exists to prevent (and which *is* enforced when the homepage carries a path). There
is no known-shared-host handling and no adversarial test for the bare-apex case.

**F14 (enabler) — `FakeFileSystem` is too lenient to catch the bug class above.**
`internal/sysdep/sysdeptest/filesystem.go`: `WriteFile` succeeds with a missing
parent and `MkdirAll` does not create parents, so the "MkdirAll-then-WriteFile"
ordering the registry atomic-write depends on cannot be exercised by any unit test
using the fake; `Remove` is also a silent no-op on a missing path (diverges from
`os.Remove`). Tightening the fake makes the generator/registry tests able to prove
real filesystem-ordering behavior.

## Desired Outcome

- **F7:** The audit URL cannot leak a credential carried in a query string — via an
  allowlist of safe params, substring redaction of token/secret/signature/password-
  like names, or dropping the query string from the logged URL entirely (decide based
  on debuggability vs. safety). The `docs/design.md` "headers filtered" wording is
  reconciled with the "no headers logged" reality.
- **F11:** Registry package names are validated against the registry's real name
  charset and/or `url.PathEscape`d per path segment, for both Packagist and npm, so a
  hostile manifest cannot shape the outbound registry request.
- **F12:** A bare-host homepage on a known shared-apex host does not produce a
  host-wide allow (it is path-scoped where possible, dropped, or flagged
  lower-trust); the decision and the known-shared-host handling are documented.
- **F14:** `FakeFileSystem` models missing-parent semantics (`WriteFile`/`MkdirAll`)
  and `Remove`-on-missing faithfully enough that a dir-ordering bug fails a test.

## User Stories / Use Cases

- As an operator reviewing the egress audit log, I want it free of bearer tokens and
  signed-URL secrets, so the audit trail isn't itself a place credentials leak.
- As a developer in a repo with an untrusted `composer.json`/`package.json`, I want
  the registry lookups to be unable to be redirected or reshaped by a crafted package
  name.
- As a security-conscious operator, I want a dependency's stated homepage on a
  shared-hosting apex to not silently allowlist every other tenant on that apex.
- As a maintainer, I want the filesystem fakes faithful enough that a missing
  `MkdirAll` is caught in unit tests, not in production.

## Acceptance Criteria

### F7 — audit redaction
- [ ] A credential carried under an arbitrary query-param name does not appear in the
      logged audit URL (allowlist, broadened substring redaction, or query stripped —
      chosen and documented).
- [ ] `docs/design.md` is updated so the "headers filtered before logging" statement
      matches the implementation (no headers logged; query-param handling described).

### F11 — registry URL safety
- [ ] Package names are validated against the registry name charset and/or
      `url.PathEscape`d per segment before URL construction, for **both** Packagist
      and npm.
- [ ] A package name containing URL-significant characters cannot alter the host,
      path, or query of the outbound registry request.

### F12 — homepage scoping
- [ ] A bare-host homepage on a known shared-apex host is not emitted as a host-wide
      allow (path-scoped, dropped, or lower-trust-flagged — chosen and documented),
      with the known-shared-host set as data, not per-call code.
- [ ] Subdomain-isolated and path-carrying homepages keep their current, correct
      scoping.

### F14 — fake fidelity
- [ ] `FakeFileSystem.WriteFile` fails on a missing parent dir; `MkdirAll` creates
      parents; `Remove` returns an `os.ErrNotExist`-equivalent on a missing path (or
      the divergence is documented and the callers audited).
- [ ] Existing tests are updated for the stricter fake and continue to pass.

## Testing Protocol

Per `.claude/tce/profile.md`: table-driven for the pure rule-emission logic, fakes
for the registry HTTP/filesystem seams, golden where rule output is rendered, pytest
for the enforcer.

- **F7:** add pytest cases in `internal/proxy/enforcer/test_audit.py` feeding URLs
  with credentials under varied param names (`session`, `jwt`, `x-amz-signature`,
  uppercase variants) and asserting none survive into the logged entry; keep the
  existing redaction tests. Run via `make test-enforcer`.
- **F11:** add adversarial table cases in `internal/generator/registry/*_test.go`
  feeding hostile package names (`vendor/pkg?x=https://evil`, encoded separators,
  `@`, `#`) and asserting the constructed URL's host/path/query are unchanged —
  ideally asserting the exact requested URL via the `FakeHTTPGetter`.
- **F12:** add table cases in `internal/generator/homepage_test.go` for a bare-host
  homepage on a known shared-apex host (asserting it is not host-wide) alongside the
  existing subdomain-isolated and path-carrying cases.
- **F14:** update `internal/sysdep/sysdeptest/filesystem.go` and its `_test.go`;
  re-run the registry/generator suites (which rely on the fake) to confirm the
  stricter semantics surface no real ordering bug — and add a registry test that
  would fail if `MkdirAll` were omitted before `WriteFile`.
- **Gate:** `make test`, `make test-enforcer`, `make lint` green; `make build` at the
  end.

## Out of Scope

- The generator's overall trust model (it deliberately trusts registry-reported
  `homepage`/`repository` verbatim — `docs/design.md` "Trust model"); this ticket
  hardens the *mechanics* (URL construction, shared-apex scoping), not that stance.
- Lockfile-based generation, transitive deps, or new ecosystem generators.
- v0.2 secret injection.

## Open Questions

- **F7 approach:** allowlist params vs. substring-redact token-like names vs. drop the
  query string entirely from the logged URL. Proposed: drop or allowlist the query
  string by default (safest), since the path+host already carry the debugging value;
  confirm no current `logs --summary`/`--follow` consumer needs query params.
- **F12 approach:** path-scope (no path available on a bare host), drop the rule, or
  flag lower-trust. Proposed: do not emit a host-wide allow for a known shared-apex
  bare host; require the path-carrying form. Confirm against real package metadata so
  legitimate bare-host homepages on isolated hosts (`react.dev`) are unaffected.

## Questions for Research/Planning

- [ ] F7 — how `audit.py` builds the logged URL and what `logs --summary`/`--follow`
      and any golden tests assume about it; the cleanest redaction point.
- [ ] F11 — the real name charset rules for Packagist (`vendor/package`) and npm
      (scoped `@scope/name`); whether `npmPackagePath` already escapes adequately.
- [ ] F12 — where to source the known-shared-apex set (reuse the forge table's data
      pattern?) and how `homepageRule` decides host-wide vs path-scoped today.
- [ ] F14 — audit all callers of `FakeFileSystem.WriteFile`/`MkdirAll`/`Remove` for
      reliance on the lenient behavior before tightening.

## References

- Review: `thoughts/shared/reviews/2026-06-22-codebase-quality-review.md` (F7, F11,
  F12, F14).
- F7: `internal/proxy/enforcer/audit.py:56` (`REDACT_QUERY_PARAMS`), `:110`,
  `internal/proxy/enforcer/test_audit.py`, `docs/design.md` ~line 506.
- F11: `internal/generator/registry/registry.go:166` (`validatePackage`),
  `internal/generator/registry/packagist.go:18`, `registry/npm.go`,
  `registry/*_test.go`.
- F12: `internal/generator/homepage.go:24` (`homepageRule`),
  `internal/generator/homepage_test.go`, `docs/design.md` "Allowlist generators"
  (homepage scoping, ~lines 174-196).
- F14: `internal/sysdep/sysdeptest/filesystem.go`.

## Implementation Plan

- Research: `thoughts/shared/research/2026-06-23-AC-0060-untrusted-input-hardening.md`
- Plan: `thoughts/shared/plans/2026-06-23-AC-0060-untrusted-input-hardening.md`

## Notes & Updates

### 2026-06-22

- Created from the 2026-06-22 security review. Groups the **High** untrusted-input
  findings F7 (audit credential leak), F11 (registry URL injection), F12 (shared-apex
  homepage over-allow), plus the F14 fake-filesystem fidelity enabler that lets the
  generator tests prove real filesystem ordering.
- F7, F11, F12 were review-reported; F11/F12 in particular should be confirmed
  against real registry metadata during research before settling the exact charset
  and shared-apex policy.
