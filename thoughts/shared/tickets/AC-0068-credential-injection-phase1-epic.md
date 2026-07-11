# AC-0068: Credential injection (Phase 1) — general substrate, GitHub flagship [EPIC]

**Status:** Done
**Estimated Complexity:** High
**Created:** 2026-06-29
**Updated:** 2026-07-11

## Problem Statement

An agent inside the cage needs to use authenticated services — first and foremost
`gh`, which the tce workflow relies on for GitHub Issues. But most `gh` operations
go over GraphQL (`POST api.github.com/graphql`), which the cage blocks (470): the
egress allowlist is host+path based, and GraphQL is a single endpoint whose real
target lives in the request body. The naive fix (allow `/graphql`) is a security
regression — today's per-path REST allowlist boxes even a broad keychain token to
one repo, but opening `/graphql` with that broad token in reach hands the agent the
whole account.

The deeper finding: **GraphQL access cannot be scoped at the URL layer — it must be
scoped at the credential layer.** And the cage's keychain is readable cage-wide *by
mechanism* (the Seatbelt `mach-lookup` grant reaches `securityd`, which gates all
keychain items and cannot be scoped to one), so a prompt-injected agent can read a
broad token. The only model robust against that is to keep the secret host-side and
**inject it at the proxy**, overwriting whatever the agent sends, scoped per host —
across multiple concurrent per-project cages.

Full research, prior art, and rationale: see
`thoughts/shared/discussions/2026-06-28-credential-injection.md`.

## Desired Outcome

A general, host-side credential-injection substrate ships, **validated end-to-end
with a single flagship: Bearer + GitHub**, closing GH-1 (allow `/graphql`,
token-scoped, multi-project-safe) without exposing broad long-lived credentials to
the agent. The substrate is built generally (because it is nearly free to do so),
but only the Bearer/GitHub path is gold-plated in this phase.

When Phase 1 is complete:

- The agent can run `gh` over GraphQL against an allowlisted repo, using a
  repo-scoped token it never sees.
- A prompt-injected agent that reads a broad token from the keychain cannot exceed
  the injected scope against the injected host (overwrite semantics).
- A credential that fails to resolve fails closed, and the agent receives a signal
  (472) actionable enough to tell the human to unlock the secret store.
- Other injection shapes (custom-header, Basic) are present via the value-template
  but not validated; non-injectable cases (SigV4, `op`) are declared `in-cage`.

## Scope — sub-tickets and dependency order

This epic decomposes into five sub-tickets. Build bottom-up; each is an
independently reviewable unit.

- **AC-0068a — `SecretResolver` sysdep seam.** Host-side resolution
  (`op://`/`keychain://`/`env://`) via a new `internal/sysdep` interface + `OS*`
  impl + `sysdeptest` fake. Foundation; no dependencies.
- **AC-0068b — Config model: transport×auth two-axis + `credentials:` indirection +
  value-template + `in-cage` lint.** Pure schema + validation, golden-tested. No
  live injection. No dependencies (pairs conceptually with a).
- **AC-0068c — Proxy injection engine: in-memory inherited-fd delivery, overwrite,
  fail-closed, phantom priming, status 472 + `X-Cage-Injected` annotation.** The
  core mechanism and the only place real secrets flow. **Depends on a + b.**
- **AC-0068d — CLI: credential management (`add`/`list`/`rm`) + `--inject` binding.**
  Reuses the recompile + hot-reload path; Long/Example `--help`. **Depends on b**
  (and a for `add --source`).
- **AC-0068e — GitHub flagship: open `/graphql` token-scoped, end-to-end validation,
  multi-project scoping, `docs/design.md` section.** The "ship it" deliverable that
  closes GH-1. **Depends on a–d.**

### Sequencing conditions

- a and b have no inter-dependency and may proceed in either order or in parallel.
- c is the integration point and must not start until a and b are merged.
- d may proceed once b is merged (independently of c).
- e is last: it is the validation gate for the whole stack and the only ticket that
  exercises a real upstream (behind the `integration` build tag).
- The 472 status code and the `SKILL.md` / `briefing.md` updates live in c, because
  the desired agent action is meaningful only once injection can fail.

## Acceptance Criteria (epic-level — satisfied when all sub-tickets are Done)

- [x] All of AC-0068a–e are Done.
- [x] `gh` GraphQL operations succeed in-cage against an allowlisted repo with a
      repo-scoped injected token, with `/graphql` allowlisted. *(Proven capability —
      live `POST /graphql → 200` + Go integration test `TestInjectGitHubGraphQLRealUpstream`.
      Shipped as a documented opt-in, not the default: see the 2026-07-11 note.)*
- [x] An auth header set by the agent on an inject-host is overwritten by the proxy
      (verified by test). *(enforcer overwrite test + e2e.)*
- [x] A non-resolvable secret produces a 472, not a forwarded request. *(enforcer
      `test_inject_missing_secret_returns_472` + `responses.injection_unavailable`.)*
- [x] Four concurrent project cages each resolve and hold their own scoped token
      with no shared state. *(Python `test_concurrent_proxies_hold_distinct_secrets_e2e`.)*
- [x] `make test` green; the GitHub end-to-end path covered behind `integration`.
      *(Verified 2026-07-11: `make test` exit 0, 148 enforcer tests pass.)*

## Out of Scope (Phase 1)

- Minted short-lived tokens (GitHub App installation tokens, OAuth2 refresh) — see
  the Phase 2 epic **AC-0069**.
- Unix-socket secret broker for Go-side memory hygiene and rotation — Phase 2
  (**AC-0069b**); Phase 1 uses the simpler inherited-fd channel.
- Gold-plating custom-header and Basic shapes — present via template only until a
  real consumer appears.
- Docker Hub / ghcr.io token-exchange dance; private package registries;
  AWS/GCP/Azure SigV4/SDK-minted auth (declared `in-cage`); SSH-based git.

## Open Questions

None blocking. The one recorded Open Decision (delivery-channel evolution from
inherited-fd to a broker) is deferred to Phase 2 planning (**AC-0069b**).

## Questions for Research/Planning

- [ ] Exact phantom-token shape `gh` will accept for priming (`GH_TOKEN=<phantom>`),
      and whether any SDK-style format check applies — resolve in AC-0068c research.
- [ ] Whether the `credentials:` indirection block lives in the main config or a
      sibling file given the cage mounts `./` read-write — resolve in AC-0068b.

## References

- Research/discussion: `thoughts/shared/discussions/2026-06-28-credential-injection.md`
- Phase 2 epic: AC-0069
- Related: AC-0045 (in-cage credential access), AC-0062 (doctor credential
  preconditions), AC-0047 (WebFetch visible refusals), AC-0050 (refusal reason
  phrases), AC-0064 (help-as-doc-surface), AC-0053 (config hot-reload).
- Design: `docs/design.md` (514–534 credential story, 286–327 refusals, 516 proxy
  model).

## Implementation Plan

(Per sub-ticket — filled when each is planned.)

## Notes & Updates

### 2026-06-29
Epic created from the 2026-06-28 credential-injection discussion. Slicing: five
Phase-1 sub-tickets (a–e) building the general substrate bottom-up with Bearer +
GitHub as the single validated flagship. Phase 2 (minted tokens, broker) split into
sibling epic AC-0069. Complexity mapping for sub-tickets follows the discussion's
estimates (a: Low/Med, b: Med, c: High, d: Med, e: Med).

### 2026-07-11 — Done (closed after epic-level review)
All five sub-tickets (a–e) are Done and the substrate ships: `SecretResolver` seam,
transport×auth config model + `credentials:` block, proxy injection engine
(inherited-fd delivery, overwrite, fail-closed 472, `X-Cage-Injected` upstream
annotation), CLI `credential` group + `allow --inject`/`--in-cage`, and the GitHub
flagship with `docs/design.md` + `README.md`. Verified this pass: `make test` exit 0
(all packages) and 148 Python enforcer tests pass; real-tool/GitHub paths behind
`integration`. Review: `thoughts/shared/reviews/2026-07-11-AC-0068-review.md`.

**Capability delivered; conclusion refined.** The epic was framed as "close GH-1 by
opening `/graphql` token-scoped." The flagship (AC-0068e) validated the mechanism
end-to-end but surfaced that a repo-scoped PAT does **not** bound *public* reads, so
opening `/graphql` re-opens the public-repo ingestion vector the cage exists to close.
Agreed resolution: keep the mechanism, ship **scoped REST** as the safe default and
treat `/graphql` as a documented opt-in risk (see AC-0068e's 2026-07-11 note,
`docs/design.md`, `README.md`). So GH-1's original goal ("open `/graphql` safely") is
*investigated and deliberately not shipped as default*, not literally achieved — the
substrate it required is complete. Phase 2 (minted tokens, broker) continues in AC-0069.
