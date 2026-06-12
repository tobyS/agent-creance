# AC-0045: In-cage credential access via the shared Keychain item

**Status:** In Progress
**Estimated Complexity:** Medium
**Created:** 2026-06-12
**Updated:** 2026-06-12

## Problem Statement

A caged Claude Code session cannot authenticate, even though the host is
logged in and `run`'s pre-launch credential gate passes. Observed live on
2026-06-12 (first fully-launching caged session after AC-0042/0043): Claude
Code presented its login flow, and attempting the Pro-subscription login
failed with `OAuth error: Failed to start OAuth callback server: Failed to
start server. Is port 0 in use?`.

Three layers, diagnosed in-session:

1. **The designed mechanism is unimplemented.** `docs/design.md:466` states
   "The generated Seatbelt profile grants the cage access to that one item
   (mach-lookup to `securityd` plus the item's ACL)" — but no generated
   fragment contains the S2-specified grant (mach-lookup
   `com.apple.SecurityServer` + `login.keychain-db` write), and safehouse's
   `--enable=keychain` is never passed. The S2 spike assigned the grant to the
   profile tickets (AC-0014/AC-0023); both shipped without it.
2. **Empty config state masks any credential.** The cage's ephemeral
   `CLAUDE_CONFIG_DIR` (seeded `{}` only) carries no account/login state, so
   Claude Code considers itself logged out regardless of Keychain
   reachability.
3. **The in-cage fallback is impossible by construction** — correctly so. The
   network baseline is `(deny network*)` with outbound-only allows, so the
   OAuth callback server can never bind; design.md:461 mandates "Login happens
   on the host, never in the cage." But the resulting error message is
   cryptic, not actionable.

## Desired Outcome

A caged session is authenticated **out of the box** from the host login: the
caged agent reads and refreshes the **same login-Keychain item**
(`Claude Code-credentials`) as host Claude Code — no token copies, no
host/cage credential divergence when refresh rotates tokens. The minimal,
non-executable auth/account state Claude Code needs to recognize the login is
seeded into the ephemeral config dir. When the credential is missing or
unusable, the user gets actionable "log in on the host" guidance instead of a
broken in-cage OAuth attempt. An automated verification vector protects the
mechanism from future profile regressions.

## User Stories / Use Cases

- As a developer logged into Claude Code on my host, I want a caged session to
  be authenticated immediately so that I can start working without any in-cage
  login ceremony.
- As a developer running long caged sessions, I want in-cage token refresh to
  work against the same Keychain item so that neither the caged session nor my
  host session gets logged out by token rotation.
- As a security-conscious user, I want the cage's Keychain access scoped to
  exactly the one credential item (per the S2 spike) so that the sandbox
  doesn't gain general access to my secrets.
- As a user whose credential expired or whose keychain is locked, I want a
  clear pointer to "log in on the host, then restart the cage" so that I don't
  chase a misleading port error.

## Acceptance Criteria

- [ ] With a valid host login, `agent-creance run` produces a caged Claude
  Code that reaches a working prompt without any in-cage login or onboarding
  login step (verified live).
- [ ] In-cage token refresh succeeds against the shared Keychain item; after a
  caged session that refreshed the token, host Claude Code is still logged in
  (no divergence).
- [ ] The Seatbelt grant is exactly the S2-scoped one — mach-lookup to
  `com.apple.SecurityServer` plus write access to the login keychain file —
  and nothing broader; the grant is visible/reviewable in the generated
  profile artifacts.
- [ ] Only minimal, non-executable auth/account state is seeded into the
  ephemeral config dir; no settings, hooks, or other DX state (AC-0044's
  scope) crosses the boundary.
- [ ] A missing credential still refuses pre-launch with the existing
  actionable message; an unusable credential (expired beyond refresh, locked
  keychain) surfaces guidance to log in on the host — via the shipped skill
  and/or docs — rather than only the raw OAuth/port error.
- [ ] The cage-verification battery gains an automated in-cage credential
  vector (integration-tagged), so a profile regression that breaks Keychain
  reachability fails `make test-integration`.
- [ ] `docs/design.md`'s keychain passages (:68, :466) are corrected to match
  the implemented mechanism (S2 already flagged the "item's ACL" wording as
  inaccurate).
- [ ] Existing tests continue to pass (`make test`, `make lint`).

## Out of Scope

- Broader developer-experience state in the cage (global `CLAUDE.md`, hooks,
  settings) — AC-0044 (skeleton, to be detailed).
- Non-Claude secret injection (1Password, env) — explicitly v0.2 roadmap
  (design.md:509).
- Allowing inbound binds / making in-cage OAuth possible — login stays
  host-only by design.
- API-key-based auth flows — v0.1 remains OAuth-only (per `internal/cred`).

## Open Questions

None — posture and scope decided during ticket creation (see Notes).

## Questions for Research/Planning

- [ ] Exact SBPL grant shape and delivery: a new appended fragment (like the
  CA read grant) vs. safehouse's `--enable=keychain` — and how it composes
  with safehouse's base profile (S2 research is the starting point).
- [ ] What exactly does Claude Code consult to consider itself logged in —
  which minimal `.claude.json`/config fields constitute "auth/account state",
  and how stable are they across Claude Code versions?
- [ ] Does the Keychain item's own ACL admit the caged process (same `claude`
  binary, same user) — and what happens with a locked keychain (S2 §failure
  modes)?
- [ ] Where does the seeding live (`cage.Builder.Prepare`) and how does it
  behave on re-launch (stale account state vs. seed-once like settings.json)?
- [ ] How to build the automated credential vector: what can an in-cage probe
  assert hermetically (security CLI? a tiny probe binary?) under the
  integration tag, without touching the developer's real credential?
- [ ] How should the skill text explain the auth-failure case (it already
  covers egress refusals; auth guidance is a new section)?

## References

- Live observation: 2026-06-12 session (login flow + `OAuth error: Failed to
  start OAuth callback server … Is port 0 in use?`)
- `thoughts/shared/research/2026-06-04-s2-keychain.md` — the full designed
  mechanism, required grants, failure modes (incl. the design.md wording fix)
- `thoughts/shared/research/2026-06-06-AC-0022-credential-detection.md` —
  detection-only scope; grant explicitly deferred to AC-0014/AC-0023
- `thoughts/shared/research/2026-06-06-AC-0023-safehouse-invocation.md` — no
  credential seeding; `--enable=keychain` available but unused
- `internal/cred/cred.go` — detection gate (`Claude Code-credentials`)
- `internal/cage/cage.go:171-199,236-259` — ephemeral config seeding + env
- `internal/profile/profile.go:47,58-60` — deny-all baseline, outbound-only
- `docs/design.md:68,461-466` — credential story (to be corrected)
- Sibling: `thoughts/shared/tickets/AC-0044-incage-dx-state.md`

## Implementation Plan

[Leave empty - will be filled when plan is created]

## Notes & Updates

### 2026-06-12
- Created through the full ticket process after live testing surfaced the
  login failure. Decisions made with the user:
  - **Shared Keychain item** posture (finish the S2 mechanism) over host-side
    token injection — avoids token copies and refresh-rotation divergence;
    accepted trade-off is scoped securityd exposure.
  - **Minimal auth-state seeding belongs to this ticket** so it fixes auth
    end-to-end; all other config state is AC-0044.
  - **Failure UX**: keep the pre-launch refusal and add actionable host-login
    guidance for mid-session credential failures (skill/docs).
  - **Automated verification required**: a credential vector joins the
    integration battery; the manual checklist alone is not enough.
- Complexity Medium: the mechanism is fully specified by the S2 spike, but the
  work spans the profile fragment, cage seeding, the verification battery, the
  skill, and docs.
