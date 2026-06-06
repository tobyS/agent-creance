# AC-0022: Credential detection (Keychain vs file fallback) (WP-4.1)

**Status:** In Progress
**Estimated Complexity:** Medium
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-4.1 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** AC-0009 (WP-1.4, Keychain seam)
**Spike gate:** **S2 (AC-0002)** — exact service/item name + ACL come from S2

## Problem Statement

The caged agent must reach Claude's OAuth token. On macOS that lives in the login Keychain. v0.1 only supports the Keychain path; a file-based `~/.claude/.credentials.json` cannot be refreshed under a read-only `~/.claude`, so the credential model would fail closed. We must detect the situation up front and refuse with a clear message instead of failing mid-session.

## Desired Outcome

`internal/cred` detects whether the Anthropic Keychain item is present; if absent but a file-based credential exists, it refuses with the documented message; if neither, it points the user at host `claude` login.

## User Stories / Use Cases

- As an operator on a Keychain machine, I want `run` to just work so that I don't think about credentials.
- As an operator with file-based creds, I want a clear "not supported in v0.1, do X" message so that I'm not debugging a TLS error.

## Acceptance Criteria

- [ ] Keychain item present → detection returns "ok, use Keychain."
- [ ] Keychain absent + `~/.claude/.credentials.json` present → returns a refusal with the exact documented message (file creds out of scope; run `claude` login differently).
- [ ] Neither present → returns a refusal pointing at host `claude` login.
- [ ] All Keychain access goes through the `sysdep.Keychain` seam (hermetic tests).

## Verification & Test Steps

1. `go build ./...` → compiles.
2. `go test -race ./internal/cred/...` with the fake Keychain + fake FS — three table cases mapping the matrix above to the exact returned outcome/message (golden message strings).
3. Grep guard: `! grep -rn 'os/exec\|"github.com/.*keychain"' internal/cred/*.go` → access is via the seam.
4. Integration (`make test-integration`, gated S2): on a real machine, detection finds the actual item (service name from S2).
5. `make lint` → clean.

## Out of Scope

- Implementing file-based credential *support* (deferred — detect-and-refuse only).
- Granting Keychain access in the Seatbelt profile (that's AC-0023/AC-0014 using S2's findings).

## Dependencies & Sequencing

Phase 4. Gated by S2. Feeds AC-0025 (`run` precondition).

## Questions for Research/Planning

- [ ] Exact Keychain service/account from S2; exact refusal wording (align with design).
- [ ] Should `doctor` (AC-0031) surface the same detection?

## References

- `docs/design.md` — "The proxy and the credential story" (incl. file-based fallback paragraph).
- Spec WP-4.1.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.
