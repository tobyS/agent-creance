# AC-0069b: Unix-socket secret broker — Go-side custody + rotation channel

**Status:** In Progress
**Estimated Complexity:** Medium
**Created:** 2026-06-29
**Updated:** 2026-07-13

> Sub-ticket of **AC-0069** (Credential injection, Phase 2). **Deferred** hardening.
> **Depends on AC-0068 (Phase 1) complete.** Resolves the Phase-1 Open Decision on
> delivery-channel evolution. Read the Phase 2 epic and research doc.

## Problem Statement

Phase 1 delivers the secret to the `mitmdump` addon over an inherited fd, and the
Python addon then holds it in memory with weak zeroization. That is simpler and
sufficient for static tokens, but it is not the right custody for rotating tokens
(AC-0069a) and leaves the secret in Python memory. A host-side Go broker can hold
the secret in `mlock`-able, zeroizable memory and serve it over a unix socket — the
custody and rotation channel Phase 2 wants.

## Desired Outcome

- A host-side **Go broker** holds the resolved/minted secret in `mlock`-able,
  zeroizable memory, never handing the raw long-lived secret to the Python addon for
  longer than a request needs.
- The addon obtains the current credential from the broker over a **unix socket**
  (correct socket permissions; addon authenticates), replacing the inherited-fd
  channel.
- The channel supports **rotation**: AC-0069a's refresh loop can update the served
  credential without restarting the proxy and without racing in-flight requests.
- The IMDS lesson is respected: the broker is host-side only and never reachable
  from inside the cage (no in-cage token endpoint / SSRF surface).

## Acceptance Criteria

- [ ] The broker custodies the secret in `mlock`-able, zeroizable Go memory; the
      value is wiped on shutdown.
- [ ] The addon fetches the credential from the broker over a unix socket with
      restrictive permissions; the cage cannot reach the socket.
- [ ] Rotating the served credential takes effect for subsequent requests without a
      proxy restart and without corrupting in-flight requests.
- [ ] Behavior on broker-unavailable is fail-closed (472), consistent with Phase 1.
- [ ] `make test` green; broker protocol unit-tested; real socket path behind
      `integration`.

## Out of Scope

- The minting logic that produces rotating tokens (AC-0069a) — this ticket provides
  the channel it uses.
- Replacing Phase-1 static injection where a broker adds no value (static tokens may
  continue to use the simpler path if justified at planning).

## Open Questions

- **Delivery-channel design** (the Phase-1 Open Decision): confirm the unix-socket
  broker protocol and custody model at planning. — **Resolved 2026-07-13**, see
  Notes & Updates.

## Questions for Research/Planning

- [ ] Broker ↔ addon protocol (request/response shape, framing) and how the addon
      authenticates to the socket.
- [ ] Socket location given security-critical state lives under
      `~/.cache/agent-creance/` and the cage mounts `./` read-write — keep the
      socket unreachable from the cage.
- [ ] `mlock`/zeroization approach in Go and interaction with the proxy refcount
      lifecycle (one broker per project proxy?).

## References

- Phase 2 epic: AC-0069. Phase 1 epic: AC-0068 (esp. AC-0068c delivery). Research:
  `thoughts/shared/discussions/2026-06-28-credential-injection.md`
- Code: `internal/proxy/lifecycle.go` (spawn, refcount), `internal/proxy/enforcer/`.
- Related: AC-0061 (proxy refcount integrity), AC-0059 (host-side integrity).
- IMDSv2 SSRF defense-in-depth:
  https://aws.amazon.com/blogs/security/defense-in-depth-open-firewalls-reverse-proxies-ssrf-vulnerabilities-ec2-instance-metadata-service/

## Implementation Plan

(Filled when planned.)

## Notes & Updates

### 2026-07-13
Researched and planned (`/tce:work`). Research:
`thoughts/shared/research/2026-07-13-AC-0069b-secret-broker.md`; plan:
`thoughts/shared/plans/2026-07-13-AC-0069b-secret-broker.md`. Status → In Progress.

The Phase-1 Open Decision (delivery-channel evolution) is **resolved**: a
detached Go broker daemon, sibling of the `mitmdump` daemon, refcounted in
`proxy.lock`, serving newline-delimited JSON (carrying `expires_at`, per
AC-0069a decision 3) over a `0600` unix socket in a `0700` dir; the addon
fetches per injected request. Decisions taken with the user:

1. **Detached sibling daemon**, not a run-session goroutine — the proxy outlives
   its spawning CLI when a second agent is attached, so a session-owned broker
   would vanish under a live proxy and 472 forever.
2. **Per-request fetch, no addon cache** — instant rotation, no in-flight race,
   token out of Python memory except while a request needs it. Makes the
   enforcer's `request` hook a coroutine.
3. **One channel for all credentials** — static tokens move to the broker too;
   the fd-3 payload now goes Go→broker, and the (non-secret) socket path rides
   `mitmdump`'s argv.
4. **Best-effort `unix.Mlock` + wipe + `RLIMIT_CORE=0`**, not `memguard`.
   Research verdict: with encrypted swap, a same-uid adversary, Go's stack
   copying, and `runtime/secret` being a darwin no-op, memory locking is honest
   hygiene, not a control — TTL and scope (AC-0069a) are the real bounds. This
   is to be documented as a bounded claim, not sold as protection.
5. **Filesystem permissions as the sole auth control** (the ssh-agent model) — a
   peer-uid check is theatre because the caged agent shares `mitmdump`'s uid.
6. **Broker death is fail-closed** (472) and surfaced as a doctor/status warning;
   no automatic restart.

### 2026-06-29
Created (deferred) as the broker-hardening sub-ticket of AC-0069. Carries the one
recorded Open Decision from the discussion.
