# AC-0068a: SecretResolver sysdep seam (op:// / keychain:// / env://)

**Status:** Done
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

- [x] `internal/sysdep` defines a `SecretResolver` interface mirroring the
      `keychain.go` seam pattern (interface + `OS*` impl + fake in `sysdeptest`).
- [x] Backends resolve `op://…`, `keychain://…`, and `env://NAME` reference forms;
      an unknown scheme is a clear error (`ErrUnknownSecretScheme`).
- [x] Resolution returns the value in memory only — never written to disk, argv, or
      a logged field; errors do not include the secret value (op errors carry only
      `op`'s stderr via the stdout-only `Commander.OutputStdout`).
- [x] The real impl invokes `op`/`security` only through sysdep seams (no inline
      `os/exec`): `op` via `Commander.OutputStdout`, `security` via the existing
      `Keychain` seam; the real path is exercised only under `integration`.
- [x] The fake (`sysdeptest.FakeSecretResolver`) supports table-driven success and
      failure (unresolvable reference → `ErrSecretNotFound`, scripted tool-missing)
      for downstream unit tests.
- [x] `make test` green; pure parsing table-driven, OS path behind `integration`.

## Out of Scope

- Wiring the resolver into the proxy or CLI (AC-0068c / AC-0068d).
- Caching/refresh/rotation and per-request resolution (rejected in the discussion:
  `op read` is 200–500ms and rate-limited; resolve once at startup).
- Additional backends beyond op/keychain/env.

## Open Questions

None.

## Questions for Research/Planning

- [x] Exact `op://` reference grammar / error mapping. **Resolved:** `op read`
      accepts the whole `op://vault/item[/section]/field` reference as one argument
      and validates it itself, so the resolver forwards it verbatim (with
      `--no-newline`) — no internal parsing. `op read` returns a uniform non-zero
      exit on any failure with diagnostics on stderr, so the resolver maps any
      failure to `ErrSecretNotFound` and a missing `op` (LookPath) to
      `ErrSecretToolMissing`.
- [x] Whether `keychain://` reuses the existing accessor. **Resolved:** reuses the
      `Keychain` seam (`FindGenericPassword`), inheriting `ErrKeychainLocked`
      detection; `keychain://service[/account]` maps onto service+account.

## References

- Epic: AC-0068. Research: `thoughts/shared/discussions/2026-06-28-credential-injection.md`
- Pattern: `internal/sysdep/keychain.go` + `internal/sysdep/sysdeptest/`
- 1Password secret references / rate limits:
  https://www.1password.dev/cli/secret-references ,
  https://developer.1password.com/docs/service-accounts/rate-limits/

## Implementation Plan

- Research: `thoughts/shared/research/2026-06-29-AC-0068a-secretresolver-seam.md`
- Plan: `thoughts/shared/plans/2026-06-29-AC-0068a-secretresolver-seam.md`

## Notes & Updates

### 2026-06-29
Created as the foundation sub-ticket of AC-0068.

Implemented and merged to `main` in three commits:

- `feat(AC-0068a): add a secret-safe stdout capture to the Commander seam` —
  `Commander.OutputStdout` (separated stdout/stderr; `Output`'s `CombinedOutput`
  is unsafe for a secret because `op` may write notices to stderr on success).
- `feat(AC-0068a): add the SecretResolver sysdep seam (op:// / keychain:// /
  env://)` — interface, sentinels, pure parsers, `OSSecretResolver` (composing
  `Commander`/`Keychain`/`PathResolver`), `sysdeptest.FakeSecretResolver`, unit +
  integration tests.
- `feat(AC-0068a): wire SecretResolver into the App composition root` — `App`
  field + `Main()` wiring, no consumer yet (AC-0068c/d).

Verification: `make test`, `make lint`, `make build` green; `make test-integration`
— the SecretResolver live tests pass (keychain://) or skip cleanly (op:// when no
reference/tool), and the only failing battery (`internal/verify` `kc-read`/
`kc-write`) is a pre-existing environmental cage-keychain issue that fails
identically on the pre-ticket commit, unrelated to this change.
