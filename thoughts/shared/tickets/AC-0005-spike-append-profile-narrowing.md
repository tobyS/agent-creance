# AC-0005: Spike S5 — Appended-profile network narrowing (WP-0.5)

**Status:** Open
**Estimated Complexity:** Medium
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-0.5 / Spike S5 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** none
**Spike gate:** gates AC-0014 (WP-2.5), AC-0023 (WP-4.2)
**Kind:** Investigation (time-boxed, produces a findings note + a decision)

## Problem Statement

The entire network model assumes an `--append-profile` fragment can *narrow* Safehouse's "network: open by default" base: the fragment denies `network*` then re-allows only the proxy + host-service ports. This rests on three stacked assumptions — the fragment is concatenated *after* Safehouse's base `(allow network*)`, Seatbelt's last-match-wins precedence lets the fragment's deny override the base allow, and subsequent specific allows re-open only intended ports. If this doesn't compose, the whole approach changes (a fully-generated profile instead of an append).

## Desired Outcome

A findings note proving whether `--append-profile` yields a working deny-all-then-reopen, with the exact fragment that works — or, if it fails, a recommended alternative composition strategy.

## User Stories / Use Cases

- As the maintainer, I want certainty that appending a deny baseline actually takes effect so that I don't ship a cage that silently allows all egress.

## Acceptance Criteria

- [ ] Research note exists at `thoughts/shared/research/2026-06-DD-s5-append-profile.md`.
- [ ] The note records PASS/FAIL: with the append fragment in place, arbitrary egress is denied while the proxy port + a host-service port are reachable.
- [ ] The exact working `.sb` fragment (or the failure evidence) is captured.
- [ ] A `Decision:` line states whether v0.1 uses `--append-profile` or a fully-generated profile (reshaping AC-0014).

## Verification & Test Steps

> Manual/integration spike against real `agent-safehouse`; deliverable is the note.

1. Construct an append fragment: `(deny network*)` then `(allow network* (remote tcp "127.0.0.1:P"))` and a host-service port.
2. Launch a caged shell via Safehouse with `--append-profile fragment.sb`.
3. Inside the cage, probe and record:
   - `curl -sS -m 5 https://example.com` (no proxy) → **expected:** refused/blocked (deny baseline in effect).
   - `curl -sS -m 5 -x http://127.0.0.1:P https://example.com` → **expected:** succeeds (proxy port allowed).
   - `nc -z 127.0.0.1 <host-service-port>` → **expected:** success; `nc -z 127.0.0.1 <other>` → refused.
4. If step 3a *succeeds* (egress not blocked), the append does not narrow → record FAIL and the alternative.
5. Self-check: `test -f thoughts/shared/research/2026-06-*-s5-append-profile.md && grep -q '^Decision:' thoughts/shared/research/2026-06-*-s5-append-profile.md` → exit 0.

## Out of Scope

- Implementing the `.sb` compiler (AC-0014) or Safehouse invocation (AC-0023).

## Dependencies & Sequencing

Phase 0. Highest-leverage spike on the critical path — gates AC-0014 and AC-0023.

## Questions for Research/Planning

- [ ] What is Safehouse's documented ordering for `--append-profile` relative to its base rules?
- [ ] If a fully-generated profile is needed, does Safehouse expose a way to supply one wholesale?

## References

- `docs/design.md` — "Open spikes" (S5), "Config compilation".
- Spec WP-0.5, §14 (open risks).

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification. Gating spike; a FAIL reshapes AC-0014.
