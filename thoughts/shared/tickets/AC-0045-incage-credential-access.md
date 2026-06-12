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

A caged session is authenticated **out of the box** from the host login. The
approach was revised after research (see Notes, 2026-06-12): **v0.1 deliberately
skips the config cage and mounts the real `~/.claude` (and `~/.claude.json`)
read-write into the cage**, and does **not** redirect `CLAUDE_CONFIG_DIR`. The
caged agent therefore reads its normal account state from the real
`~/.claude.json` and reads/refreshes the **same plain login-Keychain item**
(`Claude Code-credentials`) as host Claude Code — no token copies, no host/cage
divergence on refresh, and no dependency on undocumented Claude internals. The
Seatbelt profile is the only piece that must be built: it grants the cage the
scoped Keychain access the read+refresh need. When the credential is missing or
unusable, the user gets actionable "log in on the host" guidance instead of a
broken in-cage OAuth attempt. An automated verification vector protects the
mechanism from future profile regressions.

The accepted cost of mounting `~/.claude` read-write is that the
config-persistence vector is re-opened (a prompt-injected agent can plant a
hook/MCP/skill that fires on a later un-caged host run). This is a deliberate
v0.1 scope cut documented in `docs/design.md`; re-introducing config isolation
without breaking auth is tracked as **AC-0046**.

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
  `com.apple.SecurityServer` plus write access to the login keychain file
  (`~/Library/Keychains/login.keychain-db*`) — and nothing broader; the grant
  is visible/reviewable in the generated profile artifacts.
- [ ] The real `~/.claude` (and `~/.claude.json`) is mounted **read-write**
  into the cage and `CLAUDE_CONFIG_DIR` is **not** redirected, so the caged
  agent uses the host's account state and the plain `Claude Code-credentials`
  Keychain item. The previously-seeded ephemeral config dir is removed from the
  cage build (or its redirect/seed is no longer applied for v0.1).
- [ ] A missing credential still refuses pre-launch with the existing
  actionable message; an unusable credential (expired beyond refresh, locked
  keychain) surfaces guidance to log in on the host — via the shipped skill
  and/or docs — rather than only the raw OAuth/port error.
- [ ] The cage-verification battery gains an automated in-cage credential
  vector (integration-tagged) that probes both the read grant (mach-lookup)
  and the refresh-write grant (file-write to the login keychain db) against a
  **throwaway** keychain item — never the developer's real credential — so a
  profile regression that breaks Keychain reachability or refresh fails
  `make test-integration`.
- [ ] The battery's config-isolation vectors are reconciled with the mounted
  config: the vector that asserted the real `~/.claude` is unreachable, and the
  one asserting planted config does not persist, are updated to reflect that
  v0.1 mounts `~/.claude` read-write (they document the deferred config cage,
  per AC-0046, rather than failing).
- [ ] `docs/design.md`'s keychain/config passages are corrected to match the
  implemented mechanism (done in the decision commit: the "item's ACL" wording
  is fixed to the file-level grant, and the config-cage deferral is recorded).
- [ ] Existing tests continue to pass (`make test`, `make lint`).

## Out of Scope

- Re-introducing executable-config isolation (an ephemeral/redirected
  `CLAUDE_CONFIG_DIR` that still reaches the shared credential) — **AC-0046**.
  Mounting the real `~/.claude` read-write in this ticket means global
  `CLAUDE.md`, hooks, and settings reach the cage for free, which moots
  AC-0044's in-cage DX-state scope for v0.1.
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
- `docs/design.md` — credential/config story (corrected 2026-06-12 to record
  the v0.1 config-cage deferral and the file-level keychain grant)
- Sibling: `thoughts/shared/tickets/AC-0044-incage-dx-state.md` (mooted for
  v0.1 by mounting `~/.claude`)
- Follow-up: `thoughts/shared/tickets/AC-0046-config-cage-revisit.md`

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

### 2026-06-12 (research checkpoint — /work paused before planning)
- Research committed
  (`thoughts/shared/research/2026-06-12-AC-0045-incage-credential-access.md`).
  New load-bearing finding, verified in the installed claude 2.1.175 binary:
  with `CLAUDE_CONFIG_DIR` set, Claude Code derives a hash-suffixed keychain
  service name (`Claude Code-credentials-<sha256(dir)[:8]>`), so the S2
  Seatbelt grant alone cannot deliver the shared item. The undocumented
  `CLAUDE_SECURESTORAGE_CONFIG_DIR=""` (set-but-empty) forces the plain shared
  name and is the only mechanism honoring the "same item, no copies" posture.
- Checkpoint decisions already made with the user:
  - `.claude.json` auth fields (`hasCompletedOnboarding`,
    `lastOnboardingVersion`, `oauthAccount`): **merge every launch** (host wins
    for those fields, agent-written keys preserved) — needed under every
    posture (onboarding gate ignores credentials, claude-code#4714).
  - Integration credential vector: **read + write probe** on a throwaway
    keychain item (never the real credential).
- **Open — posture decision deferred to a live discussion** (`/tce:discuss`)
  before planning: A) shared item via the undocumented env var, hardened with
  a tested-against claude version in `internal/buildinfo` (recommended in
  research; benign visible failure mode), vs C) rewrite the ticket around
  `CLAUDE_CODE_OAUTH_TOKEN` (documented, no keychain grant at all, but
  long-lived un-rotated token in cage env + claude-code#37512 regression
  history). Option B (copy into the derived item) ruled out: same
  reverse-engineered dependency plus the refresh-divergence the ticket
  rejects.

### 2026-06-12 (decision — supersedes the posture options above)
- The user chose neither A nor C: **for v0.1, deliberately skip the config cage
  and mount the real `~/.claude` (and `~/.claude.json`) read-write into the
  cage; do not redirect `CLAUDE_CONFIG_DIR`.** Rationale: the headline value of
  agent-creance is network egress filtering, not config isolation, and the
  ephemeral config dir was actively in the way — it strips global DX state and
  triggers the hash-suffixed Keychain name. Mounting the real config makes auth
  "just work" via the plain `Claude Code-credentials` item and the host's
  account state, with **no dependency on undocumented Claude internals** (no
  `CLAUDE_SECURESTORAGE_CONFIG_DIR`).
- This removes the auth-state *seeding* work from the ticket (the real
  `~/.claude.json` already carries it) but **keeps** the Seatbelt keychain
  grant, the failure-UX/skill work, and the verification vector.
- Accepted trade-off: the config-persistence escape vector is re-opened
  (agent can plant a host-executing hook/MCP/skill). Documented honestly in
  `docs/design.md` and the tech-spec discussion; re-isolation tracked as
  **AC-0046**. Mounting `~/.claude` also moots AC-0044 for v0.1.
- Docs updated in the decision commit: `docs/design.md` (credential/config
  story, threat-model bullet, config example) and
  `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`
  (deferred-scope list, WP-4.2, WP-4.5 vector). Ticket acceptance criteria
  rewritten to match. Ready to re-plan from here.
