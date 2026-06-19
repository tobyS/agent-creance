# AC-0053: Hot-reload the source config on change during a run session

**Status:** In Progress
**Estimated Complexity:** Medium
**Created:** 2026-06-18
**Updated:** 2026-06-19

## Problem Statement

The proxy enforcer already hot-reloads the *compiled* `policy.json` by polling
its mtime (`internal/proxy/enforcer/enforcer.py`), which is why `allow`/`deny`
take effect in a running cage without a restart: those commands recompile
`policy.json` and the enforcer re-reads it. But there is a gap — when a user
hand-edits the source config (`agent-creance.yaml`, or any file it `include:`s)
in their editor, nothing recompiles `policy.json`. A live cage therefore keeps
running the stale policy until the session is restarted.

This is inconsistent (command-driven edits reload, hand edits don't) and
surprising: someone tweaking egress rules mid-session expects the change to take
effect, and instead has to tear down and relaunch the agent.

## Desired Outcome

While a cage is running, editing the project config **or any file in its include
graph** (included fragments and the global baseline) automatically recompiles
`policy.json`; the enforcer's existing mtime poll then applies it to the live
proxy — no restart. An invalid edit (syntax/validation error) leaves the
enforced policy unchanged: the last-good compiled policy stays in force, a
visible warning is printed, and a later valid save recompiles and reloads. The
watcher is active **only during an active run session** and is torn down cleanly
when the session ends.

## User Stories / Use Cases

- As a developer iterating on egress rules, I want my hand-edits to
  `agent-creance.yaml` to take effect in the running cage without restarting, so
  I keep my flow while tightening or loosening the policy.
- As a developer who keeps rules in an `include:`d fragment, I want edits to that
  fragment to reload too, so the include graph behaves like one logical config.
- As a developer who saves a half-finished or broken config mid-edit, I want the
  cage to keep working on the last-good policy and tell me it didn't reload,
  rather than silently breaking, widening, or tearing down my session.

## Acceptance Criteria

- [ ] While a cage is running, modifying the project config recompiles
      `policy.json` without a restart, and the live proxy enforces the change;
      a concise feedback line reports the reload.
- [ ] Modifying any file in the resolved include graph (included fragments plus
      the global baseline) also triggers the recompile.
- [ ] An invalid or unparseable edit does **not** change the enforced policy: the
      last-good compiled policy stays in force, a clear warning naming the problem
      is printed, and a subsequent valid save recompiles and reloads.
- [ ] The watcher is active only during an active run session and stops cleanly
      on shutdown — no leftover goroutine/process, no file-descriptor leak.
- [ ] Reload feedback is distinguishable from the agent's own foreground output
      (does not clobber or interleave destructively with it).
- [ ] OS file-watching goes through the `internal/sysdep` seam with a fake; no
      external tool is invoked in unit tests (project convention).
- [ ] `make test`, `make lint` pass; `make build` at the end.

## Out of Scope

- A standalone long-running `watch` command independent of a session — chosen:
  session-only.
- Fail-closed (deny-all) or cage-teardown behavior on an invalid config —
  chosen: keep last-good + warn.
- Recompiling on changes to *referenced manifests* (package.json, composer.json)
  that generators read — that is generator/registry-driven and a separate concern.
- Changing the enforcer's existing mtime-poll reload of `policy.json` — it
  already works and is the delivery mechanism this ticket feeds.
- The `include` command for adding include-list entries (separate ticket,
  AC-0054).

## Open Questions

None — watch scope (full include graph), invalid-config behavior (keep last-good
+ warn), and when the watcher runs (active session only) were all settled during
ticket authoring.

## Questions for Research/Planning

- [ ] File-watch mechanism: native events (e.g. fsnotify) vs. an mtime-polling
      loop consistent with the enforcer's existing approach — and how it fits the
      `internal/sysdep` seam plus a fake.
- [ ] How to enumerate the include graph to watch: the loader resolves
      `include:` directives, so the watcher needs the resolved file set, and must
      re-derive it when an edit adds or removes an include.
- [ ] Where the watcher lives in the run lifecycle (`internal/cage/run.go`,
      `internal/proxy/lifecycle.go`) and how it coordinates with the existing
      `recompile()` path in `internal/cli/mutate.go`.
- [ ] Debouncing rapid successive saves (editors emit multiple write events).
- [ ] Confirm "last-good" is naturally preserved: `recompile()` should overwrite
      `policy.json` only on a successful, atomic compile, so a failed compile
      leaves the previous file untouched.
- [ ] Surfacing the warning/feedback through the progress printer (AC-0041)
      without disrupting the agent's foreground output.

## References

- `internal/proxy/enforcer/enforcer.py` — existing mtime hot-reload of
  `policy.json` (the delivery mechanism this feeds).
- `internal/cli/mutate.go` — `recompile()` and the allow/deny recompile path.
- `internal/cli/allow.go`, `internal/cli/deny.go` — command-driven recompiles
  that already "hot-reload" via the enforcer.
- `internal/config/config.go` — `Include []string`; the include graph to watch.
- `internal/cage/run.go`, `internal/proxy/lifecycle.go` — run/proxy lifecycle.
- Related: AC-0008 (include merge), AC-0030 (allow/deny + recompile), AC-0041
  (run progress output).

## Implementation Plan

[Leave empty — filled when the plan is created.]

## Notes & Updates

### 2026-06-19

- Research complete (`thoughts/shared/research/2026-06-19-AC-0053-config-hot-reload.md`).
  Status → In Progress. Key findings: the delivery half (compiler atomic write +
  enforcer mtime poll) already exists and guarantees last-good on invalid edits;
  the one real gap is that the config loader discards the resolved include-graph
  file list, so a new loader API is needed to enumerate the watch set. One open
  design fork for the checkpoint: fsnotify (new sysdep seam) vs. mtime polling
  (existing seams).

### 2026-06-18

- Created alongside AC-0054 from one request ("hot-reload the config; add include
  commands"), split into two tickets because a runtime file-watcher and a thin
  CLI mutation differ in surface, size, and risk.
- Decisions: (a) watch the **full include graph**, not just the top config;
  (b) on invalid config, **keep the last-good policy and warn** (never widen,
  drop, or tear down); (c) watcher runs **only during an active run session**.
- Complexity Medium: the reload *delivery* already exists (enforcer mtime poll);
  the new work is watching the resolved include graph, debouncing, wiring the
  watcher into the run lifecycle through the sysdep seam, and the last-good
  warning path.
