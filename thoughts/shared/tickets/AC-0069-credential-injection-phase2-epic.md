# AC-0069: Credential injection (Phase 2) — minted tokens + broker hardening [EPIC]

**Status:** Open
**Estimated Complexity:** High
**Created:** 2026-06-29
**Updated:** 2026-06-29

## Problem Statement

Phase 1 (**AC-0068**) ships static-reference credential injection: a long-lived
token resolved host-side and injected at the proxy with overwrite + fail-closed. A
static token leak is repo-scoped but **not time-bounded**, and in v1 the Python
addon holds the secret in memory with weak zeroization, delivered over an
inherited fd. Phase 2 closes both gaps: mint **short-lived** tokens host-side, and
move secret custody into a **Go-side broker** with `mlock`-able, zeroizable memory
and a delivery channel built for rotation.

This epic is **deferred** — the discussion's audits found no near-term agent-tooling
need beyond GitHub, and both gaps are accepted trade-offs for Phase 1. These tickets
exist so the work and the one recorded Open Decision are not lost.

## Desired Outcome

When Phase 2 is complete:

- Injected credentials for supported services are **minted short-lived**
  (≤1h, scoped) and refreshed automatically, so a leak is time-bounded — modeled on
  GitHub Actions `GITHUB_TOKEN` (app private key stays host-side; per-job scoped
  installation token dies with the job).
- The secret lives in a **host-side Go broker** with `mlock`-able zeroizable memory,
  replacing the inherited-fd + Python-memory custody of Phase 1, and providing the
  rotation-friendly delivery channel.

## Scope — sub-tickets

- **AC-0069a — Minted short-lived tokens.** Host-side GitHub App installation tokens
  (JWT-sign with the app key, ≤1h repo-scoped, refresh loop) and OAuth2 refresh-grant
  minting (e.g. Google Drive). Functional expansion; needs Phase 1's resolve +
  reload substrate. **Depends on AC-0068 complete.**
- **AC-0069b — Unix-socket secret broker (hardening).** Replace inherited-fd delivery
  with a host-side Go broker holding the secret in `mlock`-able, zeroizable memory
  and serving it to the proxy over a unix socket; the rotation-capable channel that
  AC-0069a's refresh loop needs. **Depends on AC-0068 complete.** Resolves the
  Phase-1 Open Decision on delivery-channel evolution.

### Sequencing conditions

- Both sub-tickets require all of Phase 1 (AC-0068) Done.
- a and b are independent of each other in principle, but b (the rotation-capable
  broker channel) is the natural substrate for a's refresh loop; if both are taken,
  prefer b before (or alongside) a so minting writes into a channel built for it.
- Each sub-ticket starts with its own research/planning round (deferred; not
  pre-resolved here).

## Acceptance Criteria (epic-level)

- [ ] AC-0069a and AC-0069b are Done.
- [ ] At least one service uses a minted ≤1h scoped token with automatic refresh.
- [ ] The secret is custodied in the Go broker (zeroizable memory), not the Python
      addon, and delivered over a unix socket.
- [ ] The Phase-1 Open Decision (delivery channel) is resolved and documented.

## Out of Scope

- Anything already shipped in Phase 1 (AC-0068).
- Services with no audited near-term need (revisit per real demand).

## Open Questions

- **Delivery-channel evolution** (the Phase-1 Open Decision): confirm the
  unix-socket broker design at Phase-2 planning — held here, resolved in AC-0069b.

## Questions for Research/Planning

- [ ] GitHub App registration/installation flow and where the app private key is
      custodied host-side.
- [ ] OAuth2 refresh-grant storage and refresh cadence for the first target.
- [ ] Broker protocol, socket permissions, and how the addon authenticates to it.

## References

- Phase 1 epic: AC-0068. Research:
  `thoughts/shared/discussions/2026-06-28-credential-injection.md`
- GitHub App installation tokens:
  https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/authenticating-as-a-github-app-installation
- GitHub Actions `GITHUB_TOKEN` / runner auth:
  https://docs.github.com/en/actions/concepts/security/github_token ,
  https://github.com/actions/runner/blob/main/docs/design/auth.md
- RFC 8693 OAuth token exchange:
  https://datatracker.ietf.org/doc/html/rfc8693

## Implementation Plan

(Per sub-ticket — filled when each is planned.)

## Notes & Updates

### 2026-06-29
Epic created alongside AC-0068 from the 2026-06-28 discussion. Deferred: no
near-term agent-tooling need beyond GitHub. Holds the two Phase-2 work items
(minted tokens, broker) and the one recorded Open Decision.
