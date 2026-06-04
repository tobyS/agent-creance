# AC-0012: Allowlist generators (package_json, composer_json) (WP-2.3)

**Status:** Open
**Estimated Complexity:** Large
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-2.3 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** AC-0011 (WP-2.2)
**Spike gate:** none

## Problem Statement

Manually allowlisting every dependency's docs/repo hosts is tedious and bit-rotty. v0.1 ships two generators that read `package.json`/`composer.json`, look up each direct dependency, and emit allow rules for homepages, repositories, and — crucially — the forge *content* hosts (raw, codeload, pages, release CDN) that a bare repo rule misses.

## Desired Outcome

`internal/generator` turns a manifest into a deterministic, source-annotated set of allow rules, including correctly path-scoped homepage rules and the GitHub companion-host set, cached by manifest hash.

## User Stories / Use Cases

- As an operator, I want `generators: [package_json]` to allow my deps' docs/repos automatically so that I don't hand-write dozens of rules.
- As a security-conscious user, I want shared-host packages path-scoped so that one tenant's allowlist doesn't open another's.

## Acceptance Criteria

- [ ] `package_json` walks `dependencies` + `devDependencies` (direct only); `composer_json` walks `require` + `require-dev` (direct only).
- [ ] Per dep: emit a homepage rule (host-wide for a bare host, path-scoped for a path-carrying/shared host) and a repository rule scoped to `<org>/<repo>`.
- [ ] For GitHub repositories, emit the companion content hosts: `raw.githubusercontent.com/<org>/<repo>/`, `codeload.github.com/<org>/<repo>/`, `<org>.github.io/<repo>/`, and host-wide `objects.githubusercontent.com` (flagged lower-trust).
- [ ] Missing homepage/repository → no rule emitted (no error).
- [ ] Each rule is annotated with its source (`generated:package_json:<pkg>`).
- [ ] Output is cached keyed on the manifest hash; unchanged manifest → cache hit, no registry calls.

## Verification & Test Steps

1. `go build ./...` → compiles.
2. `go test -race ./internal/generator/...` → pass with a fake registry: golden rule-set output for a fixture `package.json` (incl. a bare-host homepage, a `github.io` path-scoped homepage, and a GitHub repo with full companion set) and a fixture `composer.json`.
3. Golden review: `make golden` then `git diff internal/generator/testdata` → diffs are intentional and match the design's examples.
4. Cache behavior: a test asserts an unchanged manifest hash reuses output and makes zero registry calls (fake records call count == 0 on second run).
5. Negative case: a dep with no homepage/repository yields no rules (assert empty, no panic).
6. `make lint` → clean.

## Out of Scope

- Ecosystems beyond npm/Packagist (deferred).
- Per-generator options / transitive deps / lockfile mode (deferred).
- Forges beyond GitHub (table is extensible but only GitHub ships in v0.1 per design; add GitLab only if trivial).

## Dependencies & Sequencing

Phase 2. Depends on AC-0011. Feeds AC-0013.

## Questions for Research/Planning

- [ ] The exact forge content-host table (data, not code) — confirm GitHub entries and any GitLab rows.
- [ ] Path-scoping algorithm for shared hosts (`github.io`, `*.readthedocs.io`) — how is the tenant path derived?

## References

- `docs/design.md` — "Allowlist generators" (Forge content hosts, Trust model, Visibility).
- Spec WP-2.3.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.
