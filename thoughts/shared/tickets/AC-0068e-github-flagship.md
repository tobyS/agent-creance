# AC-0068e: GitHub flagship — open /graphql token-scoped, end-to-end validation, docs

**Status:** In Progress
**Estimated Complexity:** Medium
**Created:** 2026-06-29
**Updated:** 2026-07-10

> Sub-ticket of **AC-0068** (Credential injection, Phase 1). The "ship it"
> deliverable that closes GH-1. **Depends on AC-0068a–d** — this is the validation
> gate for the whole Phase 1 stack. Read the epic and research doc for context.

## Problem Statement

GH-1: `gh` operations go over GraphQL (`POST api.github.com/graphql`), which the
cage blocks (470) because the allowlist is host+path based and GraphQL's real target
is in the body. With injection in place, the credential (not the URL) becomes the
boundary, so `/graphql` can finally be allowlisted safely — but only once the whole
substrate is validated end-to-end with a real repo-scoped token.

## Desired Outcome

Bearer + GitHub is gold-plated and validated as the single Phase-1 flagship:

- `api.github.com` is intercepted with an `inject(github)` Bearer credential
  resolved from the user's secret store.
- `/graphql` is allowlisted; the injected token is repo-scoped, so opening it does
  **not** open the account.
- `gh` operations the tce workflow uses (read/create issues, label, comment, close)
  succeed in-cage through the cage.
- Multi-project scoping is confirmed: concurrent project cages each hold their own
  scoped token, no shared state.
- The `docs/design.md` credential section is written, and any user-facing setup
  (e.g. recommended scopes for a fine-grained PAT) is documented.

## User Stories / Use Cases

- As an agent running the tce workflow in-cage, I want `gh issue create`/`comment`/
  `close` to work so that I can manage tickets without the user disabling the cage.
- As the user, I want the injected GitHub token scoped to the project repo so that a
  compromised agent cannot reach my other repos via `/graphql`.
  > **Correction (2026-07-11):** a repo-scoped token bounds *writes* and *private*
  > reads only. It does **not** bound *public* reads — every public repo is
  > world-readable — so opening `/graphql` still lets the agent read any public repo.
  > See the 2026-07-11 note; the resolution is to keep `/graphql` closed by default.

## Acceptance Criteria

- [x] With `api.github.com` intercepted + `inject(github)` and `/graphql`
      allowlisted, `gh` GraphQL operations succeed in-cage against an allowlisted
      repo. *(Validated live: `gh repo view` / `gh issue list` → `POST /graphql -> 200`;
      Go integration test `TestInjectGitHubGraphQLRealUpstream`. See the 2026-07-11
      note — this is now documented as an opt-in risk, not a default.)*
- [x] The agent never holds the real token; an agent-set auth header is overwritten
      (carried from AC-0068c, asserted in this end-to-end path). *(Go test subtest 1 +
      live cage run: phantom `Authorization` overwritten, GitHub returned 200.)*
- [x] A revoked/invalid token surfaces the upstream 401/403 + `X-Cage-Injected`
      annotation; an unresolvable token surfaces 472 (carried from AC-0068c).
      *(Go test subtests 2 and 3.)*
- [x] Concurrent project cages each authenticate with their own scoped token (no
      shared state) — validated. *(Python `test_concurrent_proxies_hold_distinct_secrets_e2e`.)*
- [x] `docs/design.md` has a credential-injection section reflecting the shipped
      design; `--help` on the new commands references it (AC-0064 style). *(Section
      rewritten risk-first; `--help` doc surfaces shipped in AC-0068d — see 2026-07-11 note.)*
- [x] End-to-end GitHub path covered behind the `integration` build tag; `make test`
      green; `make build` run so `bin/agent-creance` reflects the final commit.

## Out of Scope

- Gold-plating custom-header / Basic shapes (template-only until a real consumer).
- Minted GitHub App installation tokens (Phase 2, AC-0069a) — v1 uses a static
  fine-grained/repo-scoped PAT.
- Any non-GitHub flagship.

## Open Questions

None blocking.

## Questions for Research/Planning

- [ ] Minimum token scopes for the tce `gh` operations — fine-grained PAT GraphQL
      support is GA; `gh issue create` needs Contents:Read (`defaultBranchRef`).
      Confirm the exact scope set and document it.
- [ ] Which `gh` calls still hit REST vs GraphQL, and whether existing REST allow
      rules can be tightened once `/graphql` is the GraphQL path.

## References

- Epic: AC-0068. Research: `thoughts/shared/discussions/2026-06-28-credential-injection.md`
- Fine-grained PAT GraphQL support:
  https://github.blog/changelog/2023-04-27-graphql-improvements-for-fine-grained-pats-and-github-apps/
- `gh issue create` needs Contents:Read: https://github.com/cli/cli/issues/12798
- Design: `docs/design.md` (514–534 credential story, 516 proxy model).

## Implementation Plan

`thoughts/shared/plans/2026-07-10-AC-0068e-github-flagship.md` (research:
`thoughts/shared/research/2026-07-10-AC-0068e-github-flagship.md`).

## Notes & Updates

### 2026-06-29
Created as the validation/flagship sub-ticket of AC-0068. Closing this closes GH-1
and satisfies the epic's acceptance criteria.

### 2026-07-11 — security finding: `/graphql` cannot be safely opened by default
Out-of-cage validation surfaced that the epic's central premise was subtly wrong.
The live `gh` matrix worked (`POST /graphql -> 200`, token injected, phantom
overwritten), but probing the token showed the flaw: a repo-scoped fine-grained PAT
bounds **writes** and **private** reads only. Public repos are world-readable, so an
opened `/graphql` — one endpoint whose target repo lives in the body the enforcer
cannot see — lets the caged agent read **any** public repo's issues/PRs/files. That
is the exact "agent ingests attacker-planted content" vector agent-creance exists to
close, and GitHub is where it would be planted. The repo-scoped token does not offset
it. (Confirmed empirically: the token read `cli/cli` — outside its grant — because it
is public; and a non-mutating probe showed `issues:write` only on `agent-creance`,
403 on other repos.)

**Resolution (agreed with the user):** keep the mechanism — it is valuable and some
users will accept the risk — but make `/graphql` an informed, opt-in choice, not a
default. The safe default is **scoped REST** (`/repos/{owner}/{repo}/…`, path-scopable,
already injectable): `gh api`/`curl` against those endpoints instead of `gh` porcelain,
steered via `CLAUDE.md`. `gh auth status` does not work either way (it pings the
un-scopable root `GET /`). Documented in `docs/design.md` ("Credential injection",
rewritten risk-first) and `README.md` ("Using GitHub in the cage", with a prominent
warning). The dogfood `.agent-creance.yaml` was **not** changed: this project uses
tmt (files) for tickets so it needs no GitHub injection, and committing a personal
`op://` reference to a public repo would 472 every other contributor.

All acceptance criteria are met (the `/graphql` path works when opened; the mechanism,
tests, and docs are done). The change from the original framing is the conclusion, not
the capability: the flagship now demonstrates that `/graphql` is *not* safely openable
and ships the scoped-REST posture as the default, rather than opening `/graphql` in the
shipped config.
