# AC-0014: Seatbelt profile compiler → network.sb (WP-2.5)

**Status:** Done
**Estimated Complexity:** Large
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-2.5 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** AC-0007 (WP-1.2), AC-0006 (WP-1.1)
**Spike gate:** **S3 (AC-0003), S5 (AC-0005)** — do not merge enforcement behavior before both resolve
**Cross-cutting:** C3 (golden)

## Problem Statement

The cage's network isolation is a generated Seatbelt profile: deny-all baseline plus narrow allows for host services and the proxy port. The proxy port is *ephemeral* and varies per run, so it cannot be baked into a config-hash-cached artifact — it must be a launch-time fragment. Host-service rules must be pinned to the IPv4 literal `127.0.0.1`.

## Desired Outcome

`internal/profile` generates the `network.sb` deny-all baseline + `host_services`→`127.0.0.1:<port>` allow rules, plus a separately-generated launch-time proxy-port fragment supplied with the live port, with IPv4 enforced.

## User Stories / Use Cases

- As an operator, I want unrelated localhost services unreachable from the cage so that the agent can't poke at them.
- As the lifecycle manager (AC-0020), I want to inject the live proxy port at launch so that a restarted proxy on a new port still works.

## Acceptance Criteria

- [x] Generated `.sb` contains a `deny network*` baseline and per-`host_services` allows. **Corrected (S3/S5):** the rule is `(remote tcp "localhost:<port>")`, not the literal `127.0.0.1:<port>` — the literal form does not compile ("host must be `*` or localhost"); `localhost` covers both v4+v6 and the port is the discriminator. Never emits `*:N`. (`RenderNetworkSB`, golden `internal/profile/testdata/network.golden`.)
- [x] The proxy-port allow is produced by a separate function (`RenderProxyFragment(port)`) taking a port argument, regenerated each launch. **Note:** per the planning checkpoint `network.sb` is not input-hash-cached at all (regenerated every launch), so "not part of the cached body" holds trivially; the separate-function requirement is met.
- [x] Output is deterministic and golden-tested for a representative set (`mysql:3306, redis:6379`).
- [x] Ships the localhost-refusal self-test from AC-0003 as a gated integration test (`internal/profile/live_integration_test.go`): EPERM-refusal on a non-allowlisted port over v4+v6, allowlisted port reachable. (Per checkpoint decision: integration test only; `doctor`/`setup` runtime wiring deferred — AC-0033 is the end-to-end gate.)
- [x] Composition matches S5: `--append-profile` fragment (no wholesale profile), `localhost:N`, deny-before-allows. Ordering contract for AC-0023 documented (append `network.sb` before the proxy fragment).

## Verification & Test Steps

1. `go build ./...` → compiles.
2. `go test -race ./internal/profile/...` → pass. Golden `network.sb` for a fixture with `mysql:3306, redis:6379`; assert each rule uses `127.0.0.1` (never `localhost`/`::1`). `make golden` diff reviewed.
3. Port fragment: a test calls the port-fragment generator with port `P` and asserts the output allows exactly `127.0.0.1:P` and is independent of the cached body.
4. Negative: a test asserts no `localhost` or `::1` literal appears anywhere in generated output (`! grep -E 'localhost|::1' <golden>`).
5. Integration (gated by S3/S5): `make test-integration` runs the compiled profile through `sandbox-exec` and confirms a non-allowlisted localhost port is refused over v4 and v6 (reusing AC-0003's probes).
6. `make lint` → clean.
7. **Systematic verification:** this ticket's localhost-refusal probe is one vector of the full adversarial battery — it folds into **AC-0033**, which is the end-to-end isolation gate.

## Out of Scope

- Passing the profile to Safehouse (AC-0023).
- Lock-file/port allocation (AC-0020) — this only *consumes* a port value.

## Dependencies & Sequencing

Phase 2. Gated by AC-0003 + AC-0005. On the critical path to M3.

## Questions for Research/Planning

- [x] Final composition per S5: **appended fragment** (not a whole generated profile).
- [x] Exact SBPL: `(deny network*)` baseline first, then `(allow network-outbound (remote tcp "localhost:<port>"))` per port; no `(version 1)`/`(deny default)` header (Safehouse's base provides those). Verified to compile against real `sandbox-exec`.

## References

- `docs/design.md` — "Config compilation" (network.sb), "What the cage prevents".
- Spec WP-2.5, §14.

## Implementation Plan

- Research: `thoughts/shared/research/2026-06-05-AC-0014-seatbelt-profile-compiler.md`
- Plan: `thoughts/shared/plans/2026-06-05-AC-0014-seatbelt-profile-compiler.md`

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification. Enforcement code waits on S3 + S5.

### 2026-06-05
Both spike gates resolved (S3 2026-06-04, S5 2026-06-05) — clear to build. Implemented
`internal/profile` in four phases: SBPL renderers + golden tests, the cache-less
out-of-tree compiler, the S3 integration self-test, and the design.md/spec corrections
the spikes assigned to this ticket.

**Key correction carried from the spikes:** the literal `(remote tcp "127.0.0.1:N")`
rule form the ticket/design/spec assumed does **not** compile on macOS 26.5. The
compiler emits `(remote tcp "localhost:N")` (family-agnostic, port-enforced) and never
`*:N`. Verified out-of-band against real `sandbox-exec`: the generated `localhost:N`
profile compiles (reaches `sandbox_apply`) while the literal-IP form is rejected at
compile time. design.md (lines ~53, ~100, ~295), `internal/config/config.go`'s
`HostService` doc, and the spec WP-2.5 bullet were corrected accordingly.

Verification: `go build ./...`, `make test`, `make lint`, `make golden` (no diff) all
green. The integration test skips on hosts that can't apply nested sandbox profiles
(this dev box) and runs the full EPERM probes on a real macOS session — it folds into
**AC-0033** (the end-to-end isolation gate). Runtime self-test wiring into
`doctor`/`setup` was deferred per the planning checkpoint.
