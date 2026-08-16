# AC-0046: Revisit config isolation — restore a config cage without breaking auth

**Status:** Rejected
**Estimated Complexity:** Large
**Maturity:** Skeleton — to be detailed in a dedicated session
**Created:** 2026-06-12
**Updated:** 2026-08-16

> **Rejected 2026-08-16:** the agent-creance project was abandoned in favour of
> [nono](https://nono.sh). No further work will be done here.

> **Skeleton ticket.** Records the deliberate v0.1 scope cut and what a future
> version must solve. The desired outcome, scope, and acceptance criteria need
> a dedicated discussion (`/tce:discuss` or a full `/tce:create_ticket` pass)
> before research starts. Target: a version *after* v0.1.

## Problem Statement

To ship v0.1 with a working out-of-the-box login, AC-0045 deliberately
**dropped the config cage**: the cage now mounts the real `~/.claude` (and
`~/.claude.json`) **read-write** and does not redirect `CLAUDE_CONFIG_DIR`
(see the AC-0045 decision and `docs/design.md`, "v0.1 deliberately ships
without the config cage"). This was the right call for v0.1 — network egress
filtering is the headline guarantee, and an ephemeral config dir was actively
in the way (it stripped global DX state and, because `CLAUDE_CONFIG_DIR`
changes the Keychain service name Claude Code derives, broke credential lookup
outright).

But it re-opens a security property the cage was originally built to provide:

- **Config-persistence escape.** With `~/.claude` writable from inside the
  cage, a prompt-injected or misbehaving agent can plant a hook, MCP server, or
  skill into the host's global Claude config that then **executes on the next
  un-caged Claude run on the host** — code execution outside the cage. This is
  in-scope for the tool's threat model (the agent is treated as
  prompt-injectable; that is why there is an adversarial verification battery).
- **Confidentiality.** The caged agent can read everything in `~/.claude` —
  other projects' session history, MCP configs that may carry secrets, etc.

The original design (`docs/design.md`, "The target design (deferred past
v0.1)") splits executable config from the credential: point the agent at a
throwaway `CLAUDE_CONFIG_DIR` (closing config-persistence) while still reaching
the one shared Keychain item. The blocker that made this hard — and that this
ticket must solve — is the **Keychain service-name derivation**.

## The hard constraint (verified, must be solved here)

Verified against the installed Claude Code binary (2.1.175) and corroborated by
research (`thoughts/shared/research/2026-06-12-AC-0045-incage-credential-access.md`):

- When `CLAUDE_CONFIG_DIR` is **set**, Claude Code looks up the Keychain item
  `Claude Code-credentials-<sha256(NFC(configDir))[:8]>`, **not** the host's
  plain `Claude Code-credentials`. So an isolated config dir and the shared
  host credential pull against each other.
- The binary contains an undocumented escape hatch:
  `CLAUDE_SECURESTORAGE_CONFIG_DIR=""` (set-but-empty) forces the plain,
  host-shared service name while `CLAUDE_CONFIG_DIR` keeps redirecting file
  state. This is the lever that would let a config cage coexist with the shared
  credential — but it is reverse-engineered and unstable across Claude Code
  releases, so relying on it needs (a) a tested-against Claude Code version
  pinned in `internal/buildinfo` and (b) an integration tripwire that fails
  loudly when a Claude Code upgrade changes the scheme.
- Onboarding is a separate gate: Claude Code shows onboarding unless the config
  dir's `.claude.json` carries `hasCompletedOnboarding` / `oauthAccount`
  (account metadata, no tokens) — so a config cage must seed/merge those host
  fields too (the "merge auth fields every launch" idea from the AC-0045
  checkpoint).

## Desired Outcome (sketch — to be refined)

A future version restores meaningful config isolation — a prompt-injected agent
cannot plant host-executing config, and the caged agent does not get blanket
read of the host's whole `~/.claude` — **without** regressing the v0.1 win:
login still works out of the box against the one shared credential, with no
fragile dependency that fails silently. The mechanism, the exact subset of
config projected into the cage, and the handling of the service-name
derivation are the design work.

## Open Questions (for the upcoming discussion)

- Re-adopt the ephemeral `CLAUDE_CONFIG_DIR` + `CLAUDE_SECURESTORAGE_CONFIG_DIR=""`
  approach (and accept the pinned-version + tripwire maintenance cost), or find
  a sturdier path (proxy-side / host-brokered credential, or a different
  isolation primitive)?
- Which classes of host DX state should be *projected* into the cage and how
  (copy-once, sanitized non-executable subset, read-only, live share)? This
  absorbs AC-0044's per-class sharing model.
- How to keep executable config (hooks, MCP, skills) from crossing the
  host boundary while still giving the developer their global `CLAUDE.md` and
  safe settings.
- What does the verification battery assert once isolation is back? (Re-invert
  the `~/.claude` reachability / config-persistence vectors that AC-0045
  relaxed.)
- How resilient must this be to Claude Code internals changing? What is the
  detection + graceful-degradation story on a Claude Code upgrade?

## Out of Scope (preliminary)

- The v0.1 credential mechanism itself — AC-0045 (this ticket is the revisit).
- Non-Claude secret injection — v0.2 roadmap.

## Questions for Research/Planning

[To be filled after the detailed discussion.]

## References

- Decision + trade-off: `thoughts/shared/tickets/AC-0045-incage-credential-access.md`
  (Notes, 2026-06-12 decision)
- Research with the verified service-name derivation:
  `thoughts/shared/research/2026-06-12-AC-0045-incage-credential-access.md`
- `docs/design.md` — "v0.1 deliberately ships without the config cage" and
  "The target design (deferred past v0.1 — AC-0046)"
- `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md` —
  deferred-scope list + WP-4.2/WP-4.5 notes
- Absorbs: `thoughts/shared/tickets/AC-0044-incage-dx-state.md` (DX-state
  sharing, superseded for v0.1)
- Original mechanism: `thoughts/shared/research/2026-06-04-s2-keychain.md`

## Implementation Plan

[Leave empty — skeleton.]

## Notes & Updates

### 2026-06-12
- Created alongside the AC-0045 decision to deliberately defer config isolation
  for v0.1 (mount real `~/.claude` read-write). This ticket owns restoring it
  properly in a later version and absorbs AC-0044's DX-state sharing scope.
