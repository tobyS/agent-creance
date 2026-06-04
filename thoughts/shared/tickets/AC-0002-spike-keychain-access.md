# AC-0002: Spike S2 — Keychain access from the sandbox + concurrent refresh (WP-0.2)

**Status:** Done
**Estimated Complexity:** Medium
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-0.2 / Spike S2 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** none
**Spike gate:** gates AC-0022 (WP-4.1), AC-0023 (WP-4.2)
**Kind:** Investigation (time-boxed, produces a findings note + a decision)

## Problem Statement

On macOS the Claude OAuth token lives in the login Keychain, not a file. The credential design requires that a `sandbox-exec`-confined process can read and refresh the specific Anthropic Keychain item without an interactive prompt or denial, and that two concurrent caged sessions sharing one item don't corrupt each other's refresh-token rotation. The exact service/item name the Seatbelt profile must allow is unknown and must be pinned down.

## Desired Outcome

A findings note stating the exact Keychain service/item name(s), the Seatbelt rules required to grant access (mach-lookup + item ACL), confirmation that read+refresh work non-interactively from inside the sandbox, and confirmation that concurrent refresh behaves no worse than two un-caged sessions.

## User Stories / Use Cases

- As the maintainer, I want the exact Keychain item name so that AC-0014's profile grants precisely the right access and nothing more.
- As an operator running two caged sessions, I want token refresh to not break either session.

## Acceptance Criteria

- [x] Research note exists at `thoughts/shared/research/2026-06-04-s2-keychain.md`.
- [x] The note records the exact Keychain service name + account/item the Anthropic token uses.
- [x] The note records the minimal Seatbelt allowances (mach-lookup target, ACL requirement) that let a confined process read it without a prompt.
- [x] The note confirms (PASS/FAIL) a non-interactive read **and** a refresh from inside a `sandbox-exec` profile.
- [x] The note records the concurrent-refresh outcome and whether v0.1's "single shared item" assumption holds.
- [x] A `Decision:` line states the service/item name and ACL strategy that AC-0014/AC-0022 will encode.

## Verification & Test Steps

> Manual/integration spike on real macOS Keychain; deliverable is the note.

1. Locate the item: `security find-generic-password -s "Claude Code-credentials" -g` (try plausible service names; record the one that returns the token).
2. Write a minimal Seatbelt profile allowing `(allow mach-lookup (global-name "com.apple.SecurityServer"))` (+ whatever S2 finds necessary) and run `sandbox-exec -f probe.sb /usr/bin/security find-generic-password -s "<found-service>" -w` → **expected:** prints the secret with no GUI prompt.
3. Trigger a refresh path (run caged `claude` until it rotates, or call the refresh endpoint) and re-read the item; confirm the rotated value is readable.
4. Concurrency: run two caged reads/refreshes overlapping; record whether either session is invalidated.
5. Self-check: `test -f thoughts/shared/research/2026-06-*-s2-keychain.md && grep -q '^Decision:' thoughts/shared/research/2026-06-*-s2-keychain.md` → exit 0.

## Out of Scope

- Implementing `internal/cred` or the Seatbelt grant (AC-0022 / AC-0014).
- File-based credential support (explicitly deferred; detect-and-refuse only in AC-0022).

## Dependencies & Sequencing

Phase 0. Gates AC-0022 and the credential-grant portion of AC-0023.

## Questions for Research/Planning

- [x] Does the Keychain ACL need the caged binary to be the same signed identity as host Claude Code? — **No.** A non-Claude binary (`/usr/bin/security`) read and updated the item non-interactively; access is gated by `securityd` reachability + keychain unlock state, not code signature.
- [x] Is there a non-interactive failure mode (locked keychain) we must detect in `doctor`? — **Yes.** A locked keychain raises a blocking GUI unlock prompt (not a clean error); `doctor`/`run` must pre-flight the unlock state.

## References

- `docs/design.md` — "Open spikes" (S2), "The proxy and the credential story".
- Spec WP-0.2.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification. Gating spike.

### 2026-06-04 — Resolved
Spike run on real macOS 26.5 Keychain. Findings note:
`thoughts/shared/research/2026-06-04-s2-keychain.md`. Summary:

- **Item:** login-Keychain generic-password `Claude Code-credentials`, account = login
  short name (`$(id -un)`); service name is a unique lookup key. Value is JSON
  (`claudeAiOauth.{accessToken,refreshToken,expiresAt,scopes,subscriptionType,rateLimitTier}`).
- **Minimal Seatbelt allowances:** read needs `(allow mach-lookup (global-name
  "com.apple.SecurityServer"))` (necessary + sufficient; no GUI-agent grant → non-interactive).
  Refresh additionally needs file-write to `~/Library/Keychains/login.keychain-db*` (legacy
  `SecKeychain`/keytar path writes the db file directly).
- **PASS/FAIL:** non-interactive read PASS; non-interactive update (keychain half of refresh)
  PASS with the file-write grant; FAIL without it (EPERM).
- **Concurrency:** `securityd` serializes overlapping caged writers — one wins, one gets a
  benign transient race error; no corruption/deadlock. "Single shared item" assumption HOLDS.
- **Gates:** AC-0014 (profile grant), AC-0022 (detection keys off the service name; file-only
  → refuse), and a new `doctor`/`run` keychain-unlocked pre-flight.
