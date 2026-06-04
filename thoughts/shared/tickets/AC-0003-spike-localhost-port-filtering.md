# AC-0003: Spike S3 — Seatbelt port-level localhost filtering across address families (WP-0.3)

**Status:** Done
**Estimated Complexity:** Medium
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Resolution:** `thoughts/shared/research/2026-06-04-s3-localhost-ports.md`
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

- [x] Research note exists at `thoughts/shared/research/2026-06-04-s3-localhost-ports.md`.
- [x] The note records PASS/FAIL for: allowlisted `127.0.0.1:<allowed>` reachable (PASS); non-allowlisted `127.0.0.1:<other>` refused (PASS, EPERM); non-allowlisted `[::1]:<other>` refused (PASS, EPERM); `[::1]:<allowed>` "refused when only v4 is allowed" — **MOOT/overturned** (a v4-only rule is unbuildable; literal IPs do not compile, so the only writable rule, `localhost:<allowed>`, deliberately covers `::1`).
- [x] A `Decision:` line specifies the rule needed: emit `(remote tcp "localhost:N")` (literal-IP "force IPv4" is impossible and unnecessary; `localhost` spans both families, port-enforced).
- [x] The reproducible self-test commands are captured for reuse in AC-0014's shipped self-test.

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

- [x] Should the shipped self-test run on every `run`, only on `setup`/`doctor`, or once-and-cache? → **Decided: run on `setup` and every `doctor`, uncached** (always fresh, no `run`-path latency).

## References

- `docs/design.md` — "Open spikes" (S3), "What the cage prevents — address family caveat".
- Spec WP-0.3.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification. Gating spike.

### 2026-06-04 — Resolved
Ran the probe matrix on macOS 26.5. Findings note:
`thoughts/shared/research/2026-06-04-s3-localhost-ports.md`. Headline: the
port-level localhost guarantee **holds over both v4 and v6**, but the design's
literal-IP rule form (`127.0.0.1:N`) **does not compile** — the only buildable
per-port rule is `(remote tcp "localhost:N")`, which spans both families with the
port strictly enforced (non-allowlisted port → EPERM on v4 and v6). `localhost` =
all of this machine's addresses (loopback + interface IPs), never external hosts.
Design corrections flagged for AC-0014/AC-0020; AC-0014 must emit `localhost:N` (never
`*:N`) and ship the captured self-test on `setup`/`doctor`.
