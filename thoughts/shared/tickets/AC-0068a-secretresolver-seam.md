# AC-0068a: SecretResolver sysdep seam (op:// / keychain:// / env://)

**Status:** Open
**Estimated Complexity:** Medium
**Created:** 2026-06-29
**Updated:** 2026-06-29

> Sub-ticket of **AC-0068** (Credential injection, Phase 1). Foundation layer; no
> dependencies. Read the epic and the research doc for full context.

## Problem Statement

Credential injection needs the long-lived secret to be resolved **host-side, before
the sandbox applies**, from the user's existing secret store — never materialized in
the source tree or the cage. The project has no seam for this today. Per the
project's testing convention, side-effecting OS access must go through an
`internal/sysdep` interface (injected, faked in tests), exactly like `keychain.go`.

## Desired Outcome

A new `SecretResolver` seam exists in `internal/sysdep`: an interface, an `OS*`
real implementation, and a `sysdeptest` fake, supporting three reference backends —
`op://` (1Password CLI), `keychain://` (macOS keychain), and `env://` (process
env). It resolves a reference string to a secret value held only in memory, and is
constructed/injected like the other sysdep seams. Nothing downstream of it (proxy,
CLI) is built here.

## User Stories / Use Cases

- As the host-side proxy spawner, I want to resolve an `op://vault/item/field`
  reference to a value at startup so that the long-lived secret stays in 1Password
  and only the resolved value lives transiently in memory.
- As a test author, I want a `sysdeptest` fake so that injection logic can be tested
  without invoking `op`/`security`.

## Acceptance Criteria

- [ ] `internal/sysdep` defines a `SecretResolver` interface mirroring the
      `keychain.go` seam pattern (interface + `OS*` impl + fake in `sysdeptest`).
- [ ] Backends resolve `op://…`, `keychain://…`, and `env://NAME` reference forms;
      an unknown scheme is a clear error.
- [ ] Resolution returns the value in memory only — never written to disk, argv, or
      a logged field; errors do not include the secret value.
- [ ] The real impl invokes `op`/`security` only through the existing process-exec
      sysdep (no inline `os/exec`), and is exercised only under `integration`.
- [ ] The fake supports table-driven success and failure (unresolvable reference,
      tool missing) for downstream unit tests.
- [ ] `make test` green; new pure logic table-driven, OS impl behind `integration`.

## Out of Scope

- Wiring the resolver into the proxy or CLI (AC-0068c / AC-0068d).
- Caching/refresh/rotation and per-request resolution (rejected in the discussion:
  `op read` is 200–500ms and rate-limited; resolve once at startup).
- Additional backends beyond op/keychain/env.

## Open Questions

None.

## Questions for Research/Planning

- [ ] Exact `op://` reference grammar to support (vault/item/field vs. full secret
      reference syntax) and how `op read` errors map to resolver errors.
- [ ] Whether `keychain://` reuses the existing `keychain.go` accessor or is a
      distinct generic-password lookup.

## References

- Epic: AC-0068. Research: `thoughts/shared/discussions/2026-06-28-credential-injection.md`
- Pattern: `internal/sysdep/keychain.go` + `internal/sysdep/sysdeptest/`
- 1Password secret references / rate limits:
  https://www.1password.dev/cli/secret-references ,
  https://developer.1password.com/docs/service-accounts/rate-limits/

## Implementation Plan

(Filled when planned.)

## Notes & Updates

### 2026-06-29
Created as the foundation sub-ticket of AC-0068.
