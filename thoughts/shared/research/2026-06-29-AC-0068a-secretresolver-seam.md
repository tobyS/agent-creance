---
date: 2026-06-29
ticket: AC-0068a
title: "SecretResolver sysdep seam (op:// / keychain:// / env://) — research"
status: complete
git_commit: 0a12ff79b6cf12d1d7bc2b12d4a380e43c62efb3
branch: main
---

# Research: AC-0068a — SecretResolver sysdep seam

## Research question

How do we add a host-side `SecretResolver` seam to `internal/sysdep` — an
interface + `OS*` real impl + `sysdeptest` fake — that resolves `op://`,
`keychain://`, and `env://` references to an in-memory secret, following the
existing seam conventions (mirroring `keychain.go`)? What is the right way to
shell out to `op`/`security` given the project's "no direct OS in logic
packages" rule, and how do the two `Questions for Research/Planning`
(op:// grammar; keychain:// reuse vs. distinct lookup) resolve?

## Summary

The seam is a small, self-contained addition that has **one genuine design
decision** plus two smaller ones; everything else is mechanical and matches an
established, well-tested pattern.

- **Pattern is fully precedented.** `internal/sysdep/keychain.go` is the exact
  template: an interface with documented contract sentinels, an `OS*` impl that
  shells out to a macOS security tool, a *pure* error-mapping helper that is
  table-tested in the unit suite, an integration test (`//go:build integration`)
  that exercises the real tool and skips cleanly when unavailable, and a
  scripted fake in `sysdeptest/`. We mirror it directly.

- **op:// grammar resolves cleanly: pass the reference through verbatim.**
  `op read` accepts the whole `op://vault/item[/section]/field` string as a
  single positional argument and does its own validation; we do **not** parse
  vault/item/field ourselves. (Web research, §"op:// grammar" below.)

- **The one real decision: how `OSSecretResolver` shells out.** The ticket's AC
  says "invokes `op`/`security` only through the existing process-exec sysdep
  (no inline `os/exec`)" — but (a) the named exemplar `keychain.go` *does* inline
  `exec`, and (b) the existing `Commander.Output` returns **`CombinedOutput`**
  (stdout+stderr merged), which is **unsafe for capturing a secret**: web
  research confirms `op` can emit update/deprecation notices to stderr on
  success, which would corrupt a combined capture. So "just use `Commander` as
  it stands" is not viable. The choice is between (1) mirroring `keychain.go`
  with its own separated-stream inline `exec`, or (2) extending the `Commander`
  seam with a stdout-only capture method and composing it. This needs a user
  decision (it contradicts an explicit AC). See Open Questions.

- **keychain:// should reuse the existing `Keychain` seam.** `Keychain.Find­Generic­Password(service, account)` already exists, is faked, is integration-tested,
  and maps a locked keychain to `ErrKeychainLocked` — semantics that the later
  fail-closed / 472 "human must unlock the store" story (AC-0068c) wants for
  free. A `keychain://service[/account]` reference maps straight onto it.

- **Wiring ahead of consumers is precedented.** `App.Keychain` was added to the
  composition root with a comment that "its consumers ... land in later phases"
  (`cli.go:42-45`). So adding `App.SecretResolver` now (wired in `Main()`, unused
  by any command until AC-0068c/d) matches the AC's "constructed/injected like
  the other sysdep seams" without violating the "nothing downstream built here"
  boundary. Adding a nil-by-default field breaks no existing test (each test
  constructs `App{}` inline with only the fakes it needs).

## Detailed findings

### The seam pattern to mirror (`internal/sysdep/keychain.go`)

`keychain.go` is the canonical "shell out to a macOS security tool, return
secret bytes, map exit code/timeout to typed sentinels" seam. Its structure,
which AC-0068a should reproduce:

- **Interface + contract sentinels** (`keychain.go:25-66`): `Keychain` interface;
  package-level `ErrItemNotFound`, `ErrKeychainLocked`, and an unexported
  `errUnexpectedSecurity` wrapping a tool failure with the tool's stderr.
- **`OS*` impl shelling out** (`keychain.go:87-144`): `OSKeychain` runs
  `/usr/bin/security` with **separated** `stdout`/`stderr` buffers
  (`runSecurity`, `keychain.go:151-179`), a bounded `context.WithTimeout`, and
  trims the trailing newline the tool appends (`keychain.go:163`). It maps a
  locked keychain (which blocks on an out-of-process unlock prompt) to a timeout
  → `ErrKeychainLocked`.
- **Pure, table-tested mapper** (`keychain.go:186-195`, `keychain_test.go`):
  `interpretSecurityErr(exitCode, timedOut)` is pure so the risky exit-code →
  sentinel mapping is unit-tested without invoking the tool. This is the
  project's "pure logic → table-driven test" convention applied to the one
  testable part of an otherwise OS-bound impl.
- **Integration test** (`keychain_integration_test.go`, `//go:build integration`):
  exercises the real tool, asserts presence only (never prints the secret),
  and `t.Skip`s cleanly when the item/keychain is unavailable. Runs only under
  `make test-integration`.
- **Scripted fake** (`sysdeptest/keychain.go`): `FakeKeychain` with
  pre-loaded `Items`, per-key `Errs`, a `Locked` flag, recorded `Lookups`, and
  builder helpers (`WithItem`). Returns defensive copies of secret bytes
  (`keychain.go:72`). Compile-time `var _ sysdep.Keychain = (*FakeKeychain)(nil)`.

AC-0068a's `SecretResolver` should reproduce every one of these elements.

### Process-exec seam today (`commander.go`) — and why `Output` is unsafe for secrets

`sysdep.Commander` (`commander.go:24-31`) has two methods:

- `LookPath(name) (string, error)` — "is the tool installed?"
- `Output(ctx, name, args...) ([]byte, error)` — **`exec.CommandContext(...).CombinedOutput()`** (`commander.go:47-51`). The doc comment states it captures combined output *deliberately* "because some tools print their version banner to stderr" — correct for prereq/version probing (its only current callers: `run.go:83` via `prereq.Check`, `doctor.go:54`), wrong for a secret.

`FakeCommander` (`sysdeptest/fake.go:18-63`) keys outputs purely on the **tool
name** and **ignores args entirely** (`Output(_ , name, _ ...string)`,
`fake.go:55`). So it cannot distinguish two `op read` references, and cannot
record what args were passed. (This is fine if downstream tests use a
`FakeSecretResolver` at the resolver level rather than a `FakeCommander`.)

Only two `Commander` implementations exist (`ExecCommander` prod, `FakeCommander`
test), so extending the interface with a new method would touch exactly those
two types.

**Web research confirms the hygiene problem is real, not theoretical:** on
success `op read` writes only the secret to stdout, but `op` (like most CLIs)
can emit update-available / deprecation notices to **stderr** out of band — which
a `CombinedOutput` capture would splice into the returned secret. The robust fix
is to capture stdout and stderr into **separate** buffers and use only stdout
(exactly what `keychain.go:runSecurity` already does for `security`).

### op:// reference grammar and `op read` behavior (web research)

Sources: 1Password CLI docs — secret-reference syntax, `op read` reference,
service-account rate limits (all reachable; no cage refusals).

- **Grammar:** `op://<vault>/<item>/[<section>/]<field>` — 3 or 4 path segments,
  section optional. Case-insensitive. Supported name chars: alphanumerics, `-`,
  `_`, `.`, and whitespace; names with unsupported chars must be referred to by
  their 1Password **ID** instead (no percent-encoding scheme). Optional query
  params (`?attribute=…`, `?ssh-format=openssh`) may be appended.
- **Pass-through:** `op read` takes the whole reference as **one positional
  argument** (`op read "op://vault/item/field"`), designed for `VAR=$(op read …)`
  capture. So the resolver forwards the user's `op://…` string verbatim and lets
  `op read` validate it — **no internal vault/item/field parsing needed.**
- **Trailing newline:** `op read` appends a trailing `\n` by default; pass
  `-n`/`--no-newline` (or trim one `\n` in Go). We will pass `--no-newline`.
- **Failure:** uniform non-zero exit (observed `1`) for all failure classes
  (not signed in, item/field/vault not found); diagnostics go to **stderr** with
  an `[ERROR]` prefix, stdout stays clean. So map "non-zero exit → not
  resolvable", carrying the trimmed stderr in the error (it never contains the
  secret); do **not** rely on distinct numeric codes.
- **Auth / rate limits:** non-interactive host-side use is via a service account
  (`OP_SERVICE_ACCOUNT_TOKEN`); reads are rate-limited (1,000/h on
  Teams/Family/Individual, 10,000/h Business; no per-minute burst). This
  reconfirms the epic's "resolve once at startup, never per-request" decision —
  but timing/caching is **out of scope for AC-0068a** (the seam only resolves on
  demand; the proxy spawner in AC-0068c decides when).

### env:// resolution

`env://NAME` → the value of host process env var `NAME`. Env access already has
a seam: `PathResolver.Getenv(key)` (`pathresolver.go:30-32,56-58`, wraps
`os.Getenv`) — used elsewhere via `app.Paths.Getenv("NO_COLOR")` (`cli.go:130`).
For host-side resolution this is the natural backend; whether `OSSecretResolver`
composes `PathResolver` (unit-testable env path) or reads `os.Getenv` directly
(integration-only, zero-dep) follows from the exec-strategy decision below.

### Composition root and wiring (`cli/cli.go`)

`App` (`cli.go:21-79`) is the composition root: one field per sysdep seam, each
with a doc comment naming its consumer(s). `Main()` (`cli.go:201-227`) wires the
real `OS*` impls. Precedent for wiring a seam *before* any command consumes it:
`Keychain` (`cli.go:42-45`) — "its consumers, the run/doctor preconditions, land
in later phases." Tests construct `App{}` inline and set only the fakes they need
(e.g. `run_test.go:369` sets `f.app.Commander`); a new nil-by-default field is
therefore safe across the existing suite.

### Error model for the resolver

To honor AC ("an unknown scheme is a clear error"; "errors do not include the
secret value"), the seam needs typed sentinels mirroring `keychain.go`:

- `ErrUnknownSecretScheme` — reference is not `op://` / `keychain://` / `env://`.
- A "not resolvable" sentinel — `op read`/lookup failed, env var unset, keychain
  item absent. (keychain:// absence can reuse `ErrItemNotFound`; locked can reuse
  `ErrKeychainLocked` if keychain:// goes through the `Keychain` seam.)
- A "tool missing" sentinel — `op` (or `security`) not on PATH, so downstream
  fakes can table-test the tool-missing case the AC calls out.

Errors must wrap the tool's **stderr** only (never stdout), and never log/format
the resolved value — same discipline as `errUnexpectedSecurity` (`keychain.go:66`,
`173-174`) and the integration test's "assert presence only" comment.

## Code references

- `internal/sysdep/keychain.go:25-195` — the seam to mirror (interface, `OS*`
  impl, separated-stream exec, pure mapper).
- `internal/sysdep/keychain_test.go:13-35` — pure mapper table test pattern.
- `internal/sysdep/keychain_integration_test.go` — `//go:build integration` live
  test that skips cleanly and never prints the secret.
- `internal/sysdep/sysdeptest/keychain.go:9-100` — fake pattern (scripted items,
  errs, recorded lookups, builder helpers, defensive copy).
- `internal/sysdep/commander.go:24-51` — `Commander`; `Output` uses
  `CombinedOutput` (unsafe for secrets).
- `internal/sysdep/sysdeptest/fake.go:18-63` — `FakeCommander` keys on tool name,
  ignores args.
- `internal/sysdep/pathresolver.go:30-58` — `PathResolver.Getenv` (env:// backend).
- `internal/cli/cli.go:21-79` — `App` composition root; `cli.go:42-45` Keychain
  "consumers land later" precedent; `cli.go:201-227` `Main()` real wiring.

## Related decisions from the epic / discussion

- `thoughts/shared/discussions/2026-06-28-credential-injection.md:144-159` — the
  agreed shape: resolve host-side before the sandbox, in memory, never on disk,
  via a `SecretResolver` seam with `op://`/`keychain://`/`env://` backends. AC-0068a
  is exactly this seam, no delivery/injection.
- Epic `AC-0068` Out of Scope: caching/refresh/rotation and per-request
  resolution are rejected here (resolve once at startup — AC-0068c's concern);
  wiring into proxy/CLI is AC-0068c/d.

## Open Questions

These need a decision before planning (the first contradicts an explicit AC, so
it cannot be silently resolved):

1. **How should `OSSecretResolver` shell out to `op`/`security`?** The AC says
   "no inline `os/exec`, use the process-exec sysdep", but `Commander.Output` is
   `CombinedOutput` (unsafe for a secret, confirmed by research) and the named
   exemplar `keychain.go` itself inlines `exec` with separated streams.
   - Option 1 — **mirror `keychain.go`**: `OSSecretResolver` does its own
     separated-stream inline `exec` for `op`/`security`, with a pure table-tested
     exit/stderr mapper and an integration test; zero-dep struct. Deviates from
     the AC's "no inline `os/exec`" bullet.
   - Option 2 — **extend `Commander`**: add a stdout-only (separated-stream)
     capture method to the `Commander` seam; `OSSecretResolver` composes
     `Commander` (+ `Keychain`, + `PathResolver`). Honors the AC; modifies a
     shared interface (`ExecCommander` + `FakeCommander`) and composes 3 seams
     (unusual among the zero-dep `OS*` impls).

2. **keychain:// backend:** reuse the existing `Keychain.FindGenericPassword`
   seam (DRY; gets `ErrKeychainLocked` detection + an existing fake for free —
   recommended) vs. a distinct `security find-generic-password` lookup inside the
   resolver.

3. **env:// backend** (minor, follows from Q1): compose `PathResolver.Getenv`
   (unit-testable env path) vs. read `os.Getenv` directly in the OS impl
   (integration-only). Recommend whichever matches the Q1 choice (compose if Q2/Q1
   lean "compose seams"; direct if Q1 = Option 1 inline).
