# AC-0014: Seatbelt profile compiler → network.sb (WP-2.5)

**Status:** Open
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

- [ ] Generated `.sb` contains a `deny network*` baseline and `(remote tcp "127.0.0.1:<port>")` allows for each `host_services` entry (address forced to IPv4 `127.0.0.1` regardless of the entry's label).
- [ ] The proxy-port allow is produced by a separate function that takes a port argument and is **not** part of the input-hash-cached body (regenerated each launch).
- [ ] Output is deterministic and golden-tested for a representative `host_services` set.
- [ ] (Per S3 outcome) ships/asserts the localhost-refusal self-test design from AC-0003.
- [ ] Composition strategy matches the S5 decision (`--append-profile` fragment vs fully-generated profile).

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

- [ ] Final composition per S5: appended fragment or whole generated profile?
- [ ] Exact SBPL syntax for the deny baseline that composes with Safehouse's base.

## References

- `docs/design.md` — "Config compilation" (network.sb), "What the cage prevents".
- Spec WP-2.5, §14.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification. Enforcement code waits on S3 + S5.
