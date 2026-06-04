# AC-0025: `run` command (WP-4.4)

**Status:** Open
**Estimated Complexity:** Large
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-4.4 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** AC-0013 (compile), AC-0014 (.sb), AC-0020 (lifecycle), AC-0022 (creds), AC-0023 (Safehouse), AC-0024 (signals)
**Spike gate:** inherits S1–S5 via its dependencies

## Problem Statement

`run` is the headline command that ties everything together: it must check prerequisites, verify setup was done, resolve project state, compile (cache-aware), start/attach the proxy, exec the cage, and tear down cleanly on exit or Ctrl-C — failing with a clear pointer (not a stack trace) when setup is missing.

## Desired Outcome

`agent-creance run` launches the agent inside Safehouse with the proxy in front, enforcing policy and logging egress, with deterministic cleanup and refcount decrement on exit.

## User Stories / Use Cases

- As an operator, I want one command to start a caged session so that isolation is frictionless.
- As an operator who skipped setup, I want a clear "run `agent-creance setup`" message so that I'm not staring at a crash.

## Acceptance Criteria

- [ ] Order of operations: prereq check → setup-precondition check (trusted CA + skill present) → state resolve → cache-aware compile → proxy start/attach → cage exec → trapped teardown.
- [ ] If setup hasn't run (no trusted CA / no skill), it prints a clear pointer to `agent-creance setup` and exits non-zero (no stack trace).
- [ ] Blocks while the agent runs; decrements the proxy refcount on exit; kills the proxy only when last agent leaves.
- [ ] Ctrl-C tears down the caged process tree (via AC-0024) before decrement.

## Verification & Test Steps

1. `go build ./...` → compiles.
2. Hermetic CLI tests (`testscript` under `internal/cli/testdata/script/`) with **stubbed** `agent-safehouse` + `mitmproxy` on `$PATH`:
   - happy path: `agent-creance run` with a valid fixture config + faked setup → stubs invoked with expected args; exits 0; lock file shows attach/detach.
   - setup-missing: with no trusted CA/skill → output contains the `agent-creance setup` pointer; non-zero exit; no proxy started.
   - missing prerequisite: stub absent → refuse-and-suggest message (reuse `internal/prereq`).
3. Integration smoke (`make test-integration`): real tools, a trivial agent command (e.g. `claude --version`-style) runs caged and exits cleanly; egress for a non-allowlisted host is blocked.
4. `make lint` → clean; `make test` → green.
5. **Systematic verification:** the integration smoke's "non-allowlisted host blocked" line is the minimal check; full confinement is proven by the **AC-0033** adversarial battery, which launches its hostile payload through this very `run` command and **gates Milestone M3**.

## Out of Scope

- `setup`/`init` (AC-0026..0029) — `run` only *checks* setup ran.
- Mutation commands (AC-0030).

## Dependencies & Sequencing

Phase 4. The convergence point for Phase 2–4. Reaches **Milestone M3** ("Caged run") once AC-0026 (CA) is also in place.

## Questions for Research/Planning

- [ ] How does the setup-precondition check verify "trusted CA" cheaply (reuse AC-0026's verify)?
- [ ] testscript harness for stubbing `agent-safehouse`/`mitmproxy` — model on existing `internal/cli/testdata/script` patterns.

## References

- `docs/design.md` — "Commands" (run), "Multi-agent lifecycle".
- Spec WP-4.4.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification. Headline command; depends on most of Phases 2–4.
