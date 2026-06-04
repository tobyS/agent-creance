# AC-0003: Spike S3 — Seatbelt port-level localhost filtering across address families (WP-0.3)

**Status:** Open
**Estimated Complexity:** Medium
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-0.3 / Spike S3 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** none
**Spike gate:** gates AC-0014 (WP-2.5), AC-0020 (WP-3.4)
**Kind:** Investigation (time-boxed, produces a findings note + a decision)

## Problem Statement

The "even on localhost" guarantee rests entirely on Seatbelt `(remote tcp "127.0.0.1:N")` allow rules genuinely refusing a non-allowlisted localhost port — over **both** IPv4 and IPv6 — when proxy and services are pinned to one family. If a `::1` path slips past an IPv4-only rule, the host-service isolation is an illusion.

## Desired Outcome

A findings note proving (or disproving) that with IPv4 pinned end-to-end, a non-allowlisted localhost port is refused over both v4 and v6, and that allowlisted ports work. Includes the self-test design that AC-0014 will ship.

## User Stories / Use Cases

- As the maintainer, I want hard evidence the port guarantee holds so that the threat model isn't overstated.
- As an operator, I want unrelated localhost services to be unreachable from the cage even if they're IPv6.

## Acceptance Criteria

- [ ] Research note exists at `thoughts/shared/research/2026-06-DD-s3-localhost-ports.md`.
- [ ] The note records PASS/FAIL for: allowlisted `127.0.0.1:<allowed>` reachable; non-allowlisted `127.0.0.1:<other>` refused; non-allowlisted `[::1]:<other>` refused; (and `[::1]:<allowed>` refused when only v4 is allowed).
- [ ] A `Decision:` line confirms IPv4-pinning is sufficient, or specifies the additional rule needed.
- [ ] The reproducible self-test commands are captured for reuse in AC-0014's shipped self-test.

## Verification & Test Steps

> Manual/integration spike; deliverable is the note + reusable self-test snippet.

1. Bind two trivial TCP listeners on the host: one on `127.0.0.1:<allowed>`, one on `127.0.0.1:<other>`; also an IPv6 listener on `[::1]:<other>`.
2. Write a Seatbelt profile that denies network by default and allows only `(remote tcp "127.0.0.1:<allowed>")`.
3. Run probes inside the sandbox and record results:
   - `sandbox-exec -f probe.sb nc -z 127.0.0.1 <allowed>` → **expected:** success.
   - `sandbox-exec -f probe.sb nc -z 127.0.0.1 <other>` → **expected:** refused.
   - `sandbox-exec -f probe.sb nc -z ::1 <other>` → **expected:** refused.
   - `sandbox-exec -f probe.sb nc -z ::1 <allowed>` → **expected:** refused (v4-only rule).
4. Self-check: `test -f thoughts/shared/research/2026-06-*-s3-localhost-ports.md && grep -q '^Decision:' thoughts/shared/research/2026-06-*-s3-localhost-ports.md` → exit 0.

## Out of Scope

- Implementing the `.sb` compiler (AC-0014).
- Non-localhost network rules.

## Dependencies & Sequencing

Phase 0. Gates AC-0014 and AC-0020.

## Questions for Research/Planning

- [ ] Should the shipped self-test run on every `run`, only on `setup`/`doctor`, or once-and-cache?

## References

- `docs/design.md` — "Open spikes" (S3), "What the cage prevents — address family caveat".
- Spec WP-0.3.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification. Gating spike.
