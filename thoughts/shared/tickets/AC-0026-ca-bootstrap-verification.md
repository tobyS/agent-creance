# AC-0026: CA bootstrap + post-install verification (WP-5.1)

**Status:** Done
**Estimated Complexity:** Large
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-5.1 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** AC-0009 (WP-1.4, seams)
**Spike gate:** **S1 (AC-0001)**

## Problem Statement

For the cage to intercept TLS, the mitmproxy CA must be trusted. `security add-trusted-cert` returns 0 even when the user cancels the auth dialog — a silent failure that surfaces later as confusing TLS errors. Setup must install the CA and then *prove* it's actually trusted with a live test.

## Desired Outcome

`internal/setup` generates the mitmproxy CA if absent, installs it into the login keychain, and runs a live post-install verification (spawn a short-lived proxy, `curl https://example.com` through it, validate the chain), erroring explicitly on the silent-cancel failure mode.

## User Stories / Use Cases

- As an operator, I want setup to confirm the CA is really trusted so that my first `run` doesn't fail mysteriously.
- As an operator who cancelled the prompt, I want an explicit error so that I know to re-run.

## Acceptance Criteria

- [x] Generates the CA via mitmproxy if `~/.mitmproxy/` lacks one; idempotent if present. (`setup.EnsureCA`)
- [x] Installs the CA into the login keychain (`security add-trusted-cert`). (`Keychain.AddTrustedCert` + `setup.InstallCA`)
- [x] Post-install verification: short-lived proxy on a random port, `curl https://example.com` through it, assert the chain validates against the system trust store. (`setup.Verify` + `sysdep.TLSProber`; curl omits `--cacert` so the verdict reflects system-wide trust.)
- [x] Verification failure → explicit, actionable error naming the likely cause (cancelled prompt / missing trust), non-zero exit. (`Result{StatusUntrusted}.Message()`, golden-pinned; `Bootstrap` returns it as an error.)
- [x] The same verification is reusable by `doctor` (AC-0031) and `run`'s precondition check. (`setup.Verify`/`Bootstrap` are the reusable library; **run** keeps the cheap `setupcheck.Verify` per the AC-0025 decision — see Notes.)

## Verification & Test Steps

1. `go build ./...` → compiles.
2. `go test -race ./internal/setup/...`: with fakes (Commander for `security`, fake curl/proxy), assert the success path and that a verification failure yields the documented error string (golden) + non-zero result.
3. Integration (`make test-integration`, gated S1): on a real machine, run the CA generate+install+verify end to end and assert verification reports trusted.
4. Negative integration (manual/optional): simulate an untrusted cert and assert verification fails loudly.
5. `make lint` → clean.

## Out of Scope

- Skill install (AC-0027); the `setup` command wiring (AC-0028).
- `--no-ca-install` env-only mode (AC-0028).

## Dependencies & Sequencing

Phase 5. Gated by S1. Required for the live M3 run.

## Questions for Research/Planning

- [x] Exact `security add-trusted-cert` invocation + how to read back trust status programmatically.
  → `security add-trusted-cert -r trustRoot -p ssl -k <login.keychain-db> <cert>` (per-user
  domain, no `-d`/`sudo`). Trust is **not** read back via `security` (its exit code is
  unreliable — returns 0 even on a cancelled dialog); it is proven *functionally* by the live
  curl-through-proxy verification against the system trust store.
- [x] Can verification reuse the project's own enforcer-less mitmproxy, or a bare `mitmdump`?
  → A **bare `mitmdump`** (`--listen-host 127.0.0.1 --listen-port <ephemeral> -q`, no enforcer
  addon, no policy). Same binary the runtime proxy uses; the default `~/.mitmproxy` confdir
  means it presents the very CA we installed.

## References

- `docs/design.md` — "The proxy and the credential story" (Post-install CA verification).
- Spec WP-5.1.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.

### 2026-06-07 — Implemented (library + tests)

Research: `thoughts/shared/research/2026-06-07-AC-0026-ca-bootstrap-verification.md`;
plan: `thoughts/shared/plans/2026-06-07-AC-0026-ca-bootstrap-verification.md`.

Delivered `internal/setup` (`Installer` with `EnsureCA` / `InstallCA` / `Verify` /
`Bootstrap`) plus three sysdep primitives: `Keychain.AddTrustedCert`, a `TLSProber` curl
seam (with the pure, table-tested `ClassifyCurlExit`), and a `Sleeper` for bounded polling.
Each ships a real impl, a `sysdeptest` fake, and tests; the untrusted-verification message is
golden-pinned (`internal/setup/testdata/verify_untrusted.golden`).

Decisions taken at the research checkpoint:

- **Scope = library + tests only.** No cobra command this ticket — the `setup` command
  (AC-0028) and `doctor` extension (AC-0031) wire `internal/setup` into `App`. The new seams
  are therefore not yet on `App`; AC-0028 adds that wiring.
- **`run` keeps the cheap `setupcheck.Verify`** (keychain-presence), per the AC-0025 design
  ("run must not pay the live cost every launch"). AC5's "reusable by run's precondition" is
  read as "the same code is reusable" — the live `setup.Verify` is for setup/doctor.
- **Verification omits `--cacert`** so it proves *system-wide* keychain trust (the failure
  this ticket exists to catch), not env-var trust.

Verification status: `make test` (hermetic, race), `make lint`, `go build ./...`, and
`go build -tags=integration ./...` all green. The `//go:build integration` live tests
(`TestVerifyLive` non-destructive; `TestBootstrapLive` opt-in via `CREANCE_LIVE_CA_INSTALL=1`)
are build/vet-verified but were **not executed in the dev harness** — `~/.mitmproxy` is
EPERM-restricted in this environment even to a plain `stat`. Run them on an unrestricted
machine with `make test-integration` (satisfies verify-step 3; the negative case in step 4
stays manual).
