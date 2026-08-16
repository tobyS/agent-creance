# AC-0069a: Minted short-lived tokens — GitHub App installation + OAuth2 refresh

**Status:** Rejected
**Estimated Complexity:** High
**Created:** 2026-06-29
**Updated:** 2026-08-16

> **Rejected 2026-08-16:** the agent-creance project was abandoned in favour of
> [nono](https://nono.sh). Work on this ticket was left partially implemented
> and unverified; no further work will be done here.

> Sub-ticket of **AC-0069** (Credential injection, Phase 2). **Deferred** — no
> near-term agent-tooling need beyond GitHub (per the discussion audits).
> **Depends on AC-0068 (Phase 1) complete.** Read the Phase 2 epic and research doc.

## Problem Statement

Phase 1 injects static, long-lived tokens: a leak is repo-scoped but not
time-bounded. The state-of-the-art model (GitHub Actions `GITHUB_TOKEN`) keeps the
app private key host-side and injects a per-job, repo-scoped installation token that
expires (≤1h) and dies with the job. Phase 2 brings that minting host-side so
injected credentials are short-lived and auto-refreshed.

## Desired Outcome

- **GitHub App installation tokens:** host-side JWT-sign with the app private key →
  exchange for a ≤1h repo-scoped installation token → inject as Bearer → refresh
  before expiry on a host-side loop. The app key never enters the cage.
- **OAuth2 refresh-grant minting:** host-side refresh-token → short-lived
  access-token exchange (e.g. Google Drive), injected and refreshed similarly.
- Minting plugs into the Phase-1 injection path (resolve → inject → overwrite →
  fail-closed) and uses the rotation-capable delivery channel (AC-0069b if taken).

## Acceptance Criteria

- [ ] A GitHub App installation token is minted host-side, repo-scoped, ≤1h, and
      auto-refreshed before expiry; the app private key never reaches the cage.
- [ ] At least one OAuth2 refresh-grant flow mints and refreshes a short-lived
      access token host-side.
- [ ] Minting reuses the Phase-1 overwrite + fail-closed semantics; a mint/refresh
      failure fails closed (472).
- [ ] Refresh happens without interrupting in-flight cage requests (hot-reload /
      broker channel).
- [ ] `make test` green; mint/refresh logic unit-tested with fakes; real flows
      behind `integration`.

## Out of Scope

- The broker channel itself (AC-0069b) — though this ticket consumes it.
- Services beyond GitHub App + one OAuth2 target until real demand appears.
- Anything in Phase 1 (AC-0068).

## Open Questions

(Deferred to Phase-2 planning.) Resolved 2026-07-12 — see Notes & Updates.

## Questions for Research/Planning

- [ ] GitHub App registration + installation onboarding, and host-side custody of
      the app private key (Keychain? `op://`?).
- [ ] Refresh cadence, clock-skew margin, and behavior when the cage is idle past
      expiry.
- [ ] First OAuth2 target and its refresh-token storage.

## References

- Phase 2 epic: AC-0069. Phase 1 epic: AC-0068. Research:
  `thoughts/shared/discussions/2026-06-28-credential-injection.md`
- GitHub App installation tokens:
  https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/authenticating-as-a-github-app-installation
- GitHub Actions `GITHUB_TOKEN`:
  https://docs.github.com/en/actions/concepts/security/github_token
- RFC 8693 OAuth token exchange:
  https://datatracker.ietf.org/doc/html/rfc8693

## Implementation Plan

`thoughts/shared/plans/2026-07-22-AC-0069a-minted-short-lived-tokens.md` (6 phases).

## Notes & Updates

### 2026-07-22
Planned (`/tce:plan`) now that AC-0069b (the broker) is Done. Confirmed with the
user: **both** flows in this ticket (GitHub App + OAuth2/Drive); minting internals
hand-rolled through a new `sysdep.HTTPClient` seam with `golang-jwt/jwt/v5` for
RS256 signing only (no ghinstallation/x-oauth2); **build `credential authorize`**
(RFC 8252 loopback + PKCE) plus a **cage-start check** that verifies each minted
credential is authorized and warns actionably if not; six phases; single repo per
GitHub App credential. The broker is the refresh-loop owner (`Store.Set` +
`server.go` `expired`→472 already implement decision 3); the Python enforcer is
untouched (it fetches a token by name and renders the existing shape).

### 2026-07-12
Planning round started (`/tce:plan`) against the 2026-07-11 research; **paused
pending AC-0069b**. Decisions resolved with the user (binding for the future
plan round):

1. **Refresh channel: AC-0069b broker first.** The unix-socket broker is
   researched, planned, and implemented before this ticket is planned; this
   ticket's refresh loop then consumes it. Confirms the epic's sequencing
   preference; the fd-3 stream extension (Option B) and per-refresh respawn
   (Option C) are rejected.
2. **First OAuth2 target: Google Drive with the `drive.file` scope** —
   non-sensitive scope, no verification friction, installed-app loopback
   consent host-side.
3. **Refresh-failure semantics: stale-then-472-at-expiry.** Keep injecting the
   still-valid token after a failed refresh; once `expires_at` passes without
   a successful refresh, answer 472 (human-recoverable) instead of sending a
   dead token upstream. Requires expiry metadata at the injection side —
   an input to the AC-0069b broker protocol design.
4. **Revocation: best-effort.** Last-out `Detach` sends
   `DELETE /installation/token` for minted GitHub tokens, ignoring failures.

Next step: `/tce:research AC-0069b`, then plan/implement the broker, then
re-run `/tce:plan AC-0069a`.

### 2026-06-29
Created (deferred) as the minted-token sub-ticket of AC-0069.
