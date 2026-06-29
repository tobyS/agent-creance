---
date: 2026-06-29
ticket: AC-0068a
title: "SecretResolver sysdep seam (op:// / keychain:// / env://) — implementation plan"
status: ready
research: thoughts/shared/research/2026-06-29-AC-0068a-secretresolver-seam.md
---

# AC-0068a — SecretResolver sysdep seam: implementation plan

## Overview

Add a host-side `SecretResolver` seam to `internal/sysdep` that resolves a
reference string (`op://…`, `keychain://…`, `env://NAME`) to a secret value held
in memory only — mirroring the `keychain.go` seam (interface + `OS*` impl +
`sysdeptest` fake + pure table tests + integration test). Nothing downstream
(proxy, CLI) is built here; the seam is wired into the composition root ready for
AC-0068c/d.

Decisions locked at the question checkpoint:

- **Exec strategy — extend the `Commander` seam.** `Commander.Output` is
  `CombinedOutput` (stdout+stderr merged), which is unsafe for a secret because
  `op` can write update/deprecation notices to stderr on a *successful* `op read`.
  So we add a **separated-stream, stdout-only** capture method to the `Commander`
  seam and have `OSSecretResolver` go through it — honoring the AC's "use the
  process-exec sysdep, no inline `os/exec`" while keeping the secret clean.
- **keychain:// reuses the existing `Keychain` seam** (`FindGenericPassword`),
  inheriting `ErrKeychainLocked` detection and an existing fake/integration test.
- **env:// composes `PathResolver.Getenv`** (unit-testable env path), following
  from the compose-seams decision.

Resolved by research (not user questions): `op read` takes the `op://…` reference
**verbatim** as one argument (no internal vault/item/field parsing); pass
`--no-newline`; map any non-zero exit to "not resolvable" using stderr (never
stdout) in the error.

## Current state

- `internal/sysdep/keychain.go:25-195` is the seam template: interface + typed
  sentinels (`ErrItemNotFound`, `ErrKeychainLocked`, unexported
  `errUnexpectedSecurity`), an `OS*` impl with **separated** stdout/stderr buffers
  and a bounded `context`, a pure table-tested mapper (`interpretSecurityErr`,
  `keychain_test.go`), an integration test that skips cleanly and never prints the
  secret (`keychain_integration_test.go`), and a scripted fake returning defensive
  copies (`sysdeptest/keychain.go`).
- `internal/sysdep/commander.go:24-51` — `Commander` has `LookPath` + `Output`
  (`CombinedOutput`, `commander.go:50`). Only `ExecCommander` (prod, compile-time
  asserted `commander.go:41`) and `FakeCommander` (`sysdeptest/fake.go:18-63`,
  keys on tool name, **ignores args**) implement it. `Output` callers: `run.go:83`
  (prereq), `doctor.go:54` — both want combined output, so `Output` stays as-is.
- `internal/sysdep/pathresolver.go:20-58` — `PathResolver.Getenv` wraps
  `os.Getenv`; `sysdeptest/pathresolver.go:66-68` `FakePathResolver.Getenv` reads
  an `Env` map.
- `internal/cli/cli.go:21-79` — `App` composition root, one field per seam;
  `Keychain` (`cli.go:42-45`) was added with "consumers … land in later phases",
  the precedent for wiring ahead of use. `Main()` (`cli.go:201-227`) wires real
  `OS*` impls. Tests build `App{}` inline with only the fakes they need.
- sysdeptest fakes follow a fake-testing convention (`*_test.go` per fake, e.g.
  `keychain_test.go`).

## Desired end state

`internal/sysdep` exposes a `SecretResolver` interface; `OSSecretResolver`
resolves `op://`/`keychain://`/`env://` references to in-memory bytes via the
`Commander` (new stdout-only method), `Keychain`, and `PathResolver` seams;
`sysdeptest.FakeSecretResolver` lets downstream code table-test success and
failure (unresolvable reference, tool missing). The resolver is wired into `App`
and `Main()`, consumed by no command yet. `make test` and `make lint` are green;
errors never include the secret value.

## What we're NOT doing

- No wiring into the proxy or any CLI command (AC-0068c / AC-0068d).
- No caching / refresh / rotation / per-request resolution (resolve-once-at-startup
  timing is AC-0068c's concern; this seam just resolves on demand).
- No backends beyond `op`/`keychain`/`env`.
- No change to `Commander.Output`'s existing combined-output behavior or its
  callers.

---

## Phase 1 — Extend the `Commander` seam with a secret-safe stdout capture

### Changes

1. `internal/sysdep/commander.go` — add to the `Commander` interface:

   ```go
   // OutputStdout runs name with args and returns ONLY its stdout, keeping
   // stderr out of the returned bytes. Unlike Output (which merges stderr for
   // version banners), this is for the case where stdout carries a secret and
   // the tool may emit unrelated notices to stderr (e.g. `op` update notices on
   // a successful read). On a non-zero exit it returns an error wrapping the
   // trimmed stderr — never stdout, which may hold the secret.
   OutputStdout(ctx context.Context, name string, args ...string) ([]byte, error)
   ```

   Implement on `ExecCommander`: separate `bytes.Buffer` for stdout and stderr
   (mirroring `keychain.go:runSecurity`), `exec.CommandContext`. On success return
   `stdout.Bytes()`. On failure return an error wrapping `bytes.TrimSpace(stderr)`
   (and the exec error), never stdout.

2. `internal/sysdep/sysdeptest/fake.go` — implement `OutputStdout` on
   `FakeCommander`. A fake has no real stream distinction, so reuse the scripted
   `Outputs`/`Errs` maps (same lookup as `Output`). Add a `Calls [][]string`
   recorder appended to by `OutputStdout` (record `append([]string{name}, args...)`)
   so resolver tests can assert the verbatim argv forwarded to `op`. Add a
   compile-time assertion `var _ sysdep.Commander = (*FakeCommander)(nil)`.

### Success criteria

#### Automated
- [ ] `go build ./...` — both implementers satisfy the widened interface.
- [ ] `make test` green (race) — existing `Commander` callers/fakes unaffected.
- [ ] `make lint` clean.

#### Manual
- [ ] `OutputStdout` returns stdout only and routes stderr into the error, not the
      result (verified by the Phase 2 resolver tests + integration).

---

## Phase 2 — `SecretResolver` seam: interface, impl, fake, tests

### Changes

1. `internal/sysdep/secretresolver.go` (new):

   - Interface:
     ```go
     // SecretResolver resolves a secret reference to its value, held in memory
     // only — never written to disk, argv, or a logged field. Supported schemes:
     // op:// (1Password CLI), keychain:// (macOS generic-password), env://NAME.
     type SecretResolver interface {
         // Resolve resolves ref to its secret bytes. ctx bounds a slow backend
         // (op read). Unknown scheme -> ErrUnknownSecretScheme; missing tool ->
         // ErrSecretToolMissing; unresolvable reference -> ErrSecretNotFound
         // (keychain:// may also yield ErrKeychainLocked). Errors never include
         // the secret value.
         Resolve(ctx context.Context, ref string) ([]byte, error)
     }
     ```
   - Sentinels: `ErrUnknownSecretScheme`, `ErrSecretNotFound`,
     `ErrSecretToolMissing` (keychain:// reuses `ErrKeychainLocked` / maps absence
     to `ErrSecretNotFound`).
   - Scheme constants (`op://`, `keychain://`, `env://`) and `op` binary/args
     constants (`"op"`, `"read"`, `"--no-newline"`).
   - **Pure helpers (table-tested):**
     - `splitSecretRef(ref string) (scheme, rest string, ok bool)` — detect a
       known scheme prefix; `ok=false` for anything else.
     - `parseKeychainRef(rest string) (service, account string, err error)` —
       `service` or `service/account`; empty service is an error.
     - `parseEnvRef(rest string) (name string, err error)` — non-empty var name;
       empty is an error.
   - `OSSecretResolver struct { Commander Commander; Keychain Keychain; Paths PathResolver }`,
     `var _ SecretResolver = (*OSSecretResolver)(nil)`. `Resolve` dispatches:
     - **op://** — `Commander.LookPath("op")`; not found → `ErrSecretToolMissing`.
       `Commander.OutputStdout(ctx, "op", "read", "--no-newline", ref)` (pass the
       whole `op://…` ref verbatim). On error → `ErrSecretNotFound` wrapping the
       returned error (which already carries stderr, never the secret). Defensively
       `bytes.TrimRight(out, "\n")`.
     - **keychain://** — `parseKeychainRef`; `Keychain.FindGenericPassword(service,
       account)`. Map `ErrItemNotFound` → `ErrSecretNotFound`; propagate
       `ErrKeychainLocked`; other errors wrapped.
     - **env://** — `parseEnvRef`; `v := Paths.Getenv(name)`; empty → `ErrSecretNotFound`
       (unset or empty is unusable as a secret); else `[]byte(v)`.
     - else → `ErrUnknownSecretScheme`.

2. `internal/sysdep/secretresolver_test.go` (new, package `sysdep`, unit):
   - Table tests for `splitSecretRef` (each scheme + unknown + empty), `parseKeychainRef`
     (service only, service/account, empty service error, trailing slash),
     `parseEnvRef` (name, empty error).
   - `OSSecretResolver.Resolve` dispatch via fakes:
     - op:// success: `FakeCommander.Outputs["op"]` set; assert returned bytes ==
       value and `FakeCommander.Calls` last entry == `["op","read","--no-newline",ref]`
       (verbatim forwarding).
     - op:// tool missing: `op` absent from `FakeCommander.Paths` → `ErrSecretToolMissing`.
     - op:// failure: `FakeCommander.Errs["op"]` set with a stderr-bearing error →
       `errors.Is(err, ErrSecretNotFound)` and **assert the error string does not
       contain a planted secret** (hygiene).
     - keychain:// success / `ErrItemNotFound`→`ErrSecretNotFound` / `Locked`→
       `ErrKeychainLocked`, via `FakeKeychain`.
     - env:// via `FakePathResolver` (`Env` map): set → bytes; unset → `ErrSecretNotFound`.
     - unknown scheme → `ErrUnknownSecretScheme`.
   - A real env:// resolution unit test with `t.Setenv` + `OSSecretResolver{Paths:
     OSPathResolver{}}` (hermetic — no external tool) exercising the real backend.

3. `internal/sysdep/secretresolver_integration_test.go` (new, `//go:build integration`):
   - op://: skip unless `op` on PATH and a test reference is provided (e.g.
     `OP_TEST_SECRET_REF` env); assert non-empty, never print the value.
   - keychain://: resolve a known generic-password item (mirror
     `keychain_integration_test.go`'s `Claude Code-credentials`/`$USER`); skip on
     `ErrSecretNotFound`/`ErrKeychainLocked`; assert non-empty, never print.

4. `internal/sysdep/sysdeptest/secretresolver.go` (new): `FakeSecretResolver`
   mirroring `FakeKeychain` — `Secrets map[string][]byte` (ref→value),
   `Errs map[string]error` (ref→error), recorded `Resolved []string`, builder
   `WithSecret(ref, value)`, `NewFakeSecretResolver()`. Unregistered ref →
   `sysdep.ErrSecretNotFound`. Returns a defensive copy. `var _ sysdep.SecretResolver
   = (*FakeSecretResolver)(nil)`.

5. `internal/sysdep/sysdeptest/secretresolver_test.go` (new): table-driven test of
   the fake (registered secret, per-ref error, unregistered→`ErrSecretNotFound`,
   defensive-copy independence, `Resolved` recording) — matching `keychain_test.go`.

### Success criteria

#### Automated
- [ ] `make test` green (race) — pure helpers + dispatch + fake covered; the env://
      real path covered by the `t.Setenv` unit test.
- [ ] `make lint` clean; `go build ./...`.

#### Manual
- [ ] `make test-integration` (out-of-cage; see Cage note) exercises real op://
      and keychain:// where available, skipping cleanly otherwise.
- [ ] Code review: errors never embed the resolved value; the seam mirrors the
      `keychain.go` structure (interface + sentinels + `OS*` + fake + pure tests +
      integration test).

---

## Phase 3 — Wire `SecretResolver` into the composition root

### Changes

1. `internal/cli/cli.go` — add to `App`:
   ```go
   // SecretResolver resolves host-side secret references (op:// / keychain:// /
   // env://) to in-memory values for credential injection; its consumers — the
   // proxy spawner (AC-0068c) and the credential CLI (AC-0068d) — land in later
   // phases.
   SecretResolver sysdep.SecretResolver
   ```
   Wire in `Main()`:
   ```go
   SecretResolver: sysdep.OSSecretResolver{
       Commander: sysdep.ExecCommander{},
       Keychain:  sysdep.OSKeychain{},
       Paths:     sysdep.OSPathResolver{},
   },
   ```
   No command reads it yet; the field is nil-by-default in existing tests
   (`App{}` inline construction), so the suite is unaffected.

### Success criteria

#### Automated
- [ ] `go build ./...`; `make test` green; `make lint` clean.
- [ ] `make build` so `bin/agent-creance` reflects the final commit (project rule).

#### Manual
- [ ] `App.SecretResolver` is wired in `Main()` and unused by any command (grep
      shows no consumer outside `cli.go`).

---

## Testing strategy

- **Pure logic → table-driven** (`secretresolver_test.go`): scheme split + keychain/env
  reference parsing, the only branchy logic.
- **Dispatch → fakes** (`FakeCommander` with `Calls` recorder, `FakeKeychain`,
  `FakePathResolver`): each scheme's success + failure mapping, plus the
  secret-hygiene assertion (error excludes a planted secret).
- **Real backends → integration** (`//go:build integration`) for op:// and
  keychain://, skipping cleanly; env:// real path is hermetic (`t.Setenv`) so it
  stays in the unit suite.
- **Fake → its own test** in sysdeptest, matching the per-fake convention.

## Cage note (in-cage vs out-of-cage)

All implementation plus `make test`, `make lint`, `make build` are hermetic and
run **in-cage**. `make test-integration` for op:// and keychain:// needs the real
`op`/`security` tools and a logged-in macOS session, and `op` may not be installed
at all — that verification is **out-of-cage and best-effort/manual**; it is not a
gate for the in-cage phases. The in-cage gate is `make test` + `make lint` +
`make build`.

## References

- Research: `thoughts/shared/research/2026-06-29-AC-0068a-secretresolver-seam.md`
- Ticket: `thoughts/shared/tickets/AC-0068a-secretresolver-seam.md`; epic AC-0068
- Pattern: `internal/sysdep/keychain.go`, `keychain_test.go`,
  `keychain_integration_test.go`, `sysdeptest/keychain.go`
- Discussion: `thoughts/shared/discussions/2026-06-28-credential-injection.md:144-159`
