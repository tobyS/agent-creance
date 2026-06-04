# AC-0026: CA bootstrap + post-install verification (WP-5.1)

**Status:** Open
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

- [ ] Generates the CA via mitmproxy if `~/.mitmproxy/` lacks one; idempotent if present.
- [ ] Installs the CA into the login keychain (`security add-trusted-cert`).
- [ ] Post-install verification: short-lived proxy on a random port, `curl https://example.com` through it, assert the chain validates against the system trust store.
- [ ] Verification failure → explicit, actionable error naming the likely cause (cancelled prompt / missing trust), non-zero exit.
- [ ] The same verification is reusable by `doctor` (AC-0031) and `run`'s precondition check.

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

- [ ] Exact `security add-trusted-cert` invocation + how to read back trust status programmatically.
- [ ] Can verification reuse the project's own enforcer-less mitmproxy, or a bare `mitmdump`?

## References

- `docs/design.md` — "The proxy and the credential story" (Post-install CA verification).
- Spec WP-5.1.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.
