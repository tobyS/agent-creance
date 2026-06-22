---
date: 2026-06-22
ticket: AC-0061
title: "Plan — Proxy-refcount integrity: tear down on signal and survive PID recycling"
status: ready
branch: main
research: thoughts/shared/research/2026-06-22-AC-0061-proxy-refcount-integrity.md
tags: [plan, proxy, lifecycle, signals, pid-reuse, sysdep]
---

# Implementation Plan: AC-0061 — Proxy-refcount integrity (F5 + F13)

## Overview

Close two Medium proxy-lifecycle gaps from the 2026-06-22 review:

- **F5** — the deferred `Detach` in `runRun` is skipped if a SIGINT/SIGTERM lands in
  the window *after* `cage.Run` returns (signal disposition has reverted to the Go
  default = terminate). Fix: install a wrapper-level signal subscription that spans
  the **whole** `runRun`, suppressing default termination through teardown so the
  deferred `Detach` always runs.
- **F13** — `pruneDead` trusts a bare agent PID via `kill(pid,0)` only; a recycled
  macOS PID keeps the agent "alive", pinning the proxy and blocking `clean`. Fix:
  record each agent's **process start time** in the lock and verify it in
  `pruneDead`; a recycled PID has a different start time and is pruned.

Decisions confirmed at the Phase-2 checkpoint:

1. F13 second factor = **process start time** (new `ProcessManager.StartTime` seam over
   `unix.SysctlKinfoProc`; `golang.org/x/sys` is already a direct dep).
2. `StartTime` read error in `pruneDead` ⇒ **treat the entry as dead / prune**.
3. F5 mechanism = **span-the-whole-run** wrapper subscription (gap-free).
4. Old-format lock (`agents:[1,2,3]`) ⇒ **accept cold-start** (no tolerant
   migration; `readLock` already maps an unparseable lock to a zero `lockState`).

## Current state

- Lock schema: `lockState.Agents []int` — bare PIDs, no second factor
  (`internal/proxy/lifecycle.go:47-62`).
- `pruneDead` keeps a PID iff `m.proc.Alive(pid)` (`lifecycle.go:390-398`);
  `OSProcessManager.Alive` is `kill(pid,0)` (`internal/sysdep/processmanager.go:62-72`).
- The proxy already gets a second factor (TCP port probe) at `lifecycle.go:135,280,325`;
  agents get nothing.
- `runRun` delegates all signals to `cage.Run`, which calls `signal.Stop` on return
  (`internal/cage/run.go:63`); `runRun` itself imports no `os/signal` and tears down
  purely via `defer mgr.Detach(...)` (`internal/cli/run.go:153-157`).
- `Diagnosis.LiveAgents` and `CleanResult.LiveAgents` are `[]int` (PIDs), consumed
  for display/refusal by `status`, `doctor`, and `clean`.

## Desired end state

- A SIGINT/SIGTERM delivered to the wrapper at any point — including the post-`Run`
  teardown window — still results in `Detach` running (agent removed from the lock;
  proxy SIGTERM'd iff last out). Idempotent; no double-decrement / no killing a proxy
  another agent uses.
- Each attached agent is recorded as `{pid, start_time}`. `pruneDead` prunes an entry
  whose PID is gone **or** whose live process start time does not match the recorded
  one (or is unreadable). A recycled-PID ghost no longer pins the proxy, and `clean`
  (without `--force`) no longer refuses on it.
- `Diagnosis.LiveAgents` / `CleanResult.LiveAgents` remain `[]int` (PIDs) so
  `status`/`doctor`/`clean` are unchanged externally.
- `make test` (race) and `make lint` green; `bin/agent-creance` rebuilt.

## What we are NOT doing (out of scope)

- The proxy crash-restart port-reclaim / stranded-agent behavior (already tested).
- Concurrent first-start serialization under the flock (accepted latency trade-off).
- Lock-file schema versioning/migration beyond what the start-time field requires
  (old locks cold-start by design).

---

## Phase 1 — `ProcessManager.StartTime` seam (F13 foundation)

Add a process-start-time read to the OS abstraction, with a real macOS implementation
and a fake capability that can model "same PID, different process".

### Changes

1. **Interface** — `internal/sysdep/processmanager.go:25-38`: add
   ```go
   // StartTime returns pid's process start time as unix microseconds — a stable
   // per-process identity that changes when a PID is recycled. An error means the
   // start time could not be read (e.g. the process is gone).
   StartTime(pid int) (int64, error)
   ```

2. **OS impl** — `OSProcessManager` in the same file:
   ```go
   func (OSProcessManager) StartTime(pid int) (int64, error) {
       if pid <= 0 {
           return 0, fmt.Errorf("invalid pid %d", pid)
       }
       kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
       if err != nil {
           return 0, err
       }
       tv := kp.Proc.P_starttime // Timeval{Sec int64, Usec int32}
       return tv.Sec*1_000_000 + int64(tv.Usec), nil
   }
   ```
   Add the `golang.org/x/sys/unix` import (module already required; no `go.mod` change).

3. **Fake** — `internal/sysdep/sysdeptest/processmanager.go`: add
   `StartTimes map[int]int64` (initialised in `NewFakeProcessManager`) and an optional
   `StartTimeErr error`, plus:
   ```go
   func (f *FakeProcessManager) StartTime(pid int) (int64, error) {
       f.mu.Lock(); defer f.mu.Unlock()
       if f.StartTimeErr != nil { return 0, f.StartTimeErr }
       st, ok := f.StartTimes[pid]
       if !ok { return 0, fmt.Errorf("fake: no start time for pid %d", pid) }
       return st, nil
   }
   ```
   Recycling is modelled by `AlivePIDs[p]=true` while `StartTimes[p]` differs from the
   value recorded in the lock; an absent `StartTimes` key ⇒ error ⇒ treated as dead.

### Tests

- Table test in `internal/sysdep/sysdeptest/processmanager_test.go` (or the existing
  test file) for the fake: present key returns value; absent key errors; `StartTimeErr`
  honoured.
- Real `OSProcessManager.StartTime` is exercised under the `integration` build tag
  only (it makes a real syscall). Extend `internal/sysdep/processmanager_test.go`'s
  integration coverage: read `StartTime(os.Getpid())` twice → equal and non-zero;
  `StartTime` of a reaped child PID → error or a value that differs from a freshly
  spawned one. Keep non-integration unit tests free of real syscalls.

### Success criteria

#### Automated
- [ ] `go build ./...` compiles (new interface method implemented by both impls).
- [ ] `make test` green (fake test passes; nothing else broken yet — interface
      addition forces the fake + OS impl to compile).

#### Manual
- [ ] `StartTime` returns unix-micros and is stable across two reads of the same live
      process.

---

## Phase 2 — Lock schema + `pruneDead` identity check (F13 core)

Carry the start time per agent in the lock and enforce it in `pruneDead`. Keep the
public `Diagnosis`/`CleanResult` shape (`[]int` PIDs) so consumers are untouched.

### Changes (all in `internal/proxy/lifecycle.go` unless noted)

1. **Schema** — replace `Agents []int` with a record slice:
   ```go
   type agentRef struct {
       PID       int   `json:"pid"`
       StartTime int64 `json:"start"`
   }
   type lockState struct {
       ProxyPID      int        `json:"proxy_pid"`
       Port          int        `json:"port"`
       PolicyHash    string     `json:"policy_hash"`
       Agents        []agentRef `json:"agents"`
       CanonicalPath string     `json:"canonical_path"`
   }
   ```
   An old `agents:[1,2,3]` lock fails to unmarshal into `[]agentRef`; `readLock`
   (`:449-462`) already returns a zero `lockState` on a parse error, so this is the
   confirmed cold-start behavior — no migration shim.

2. **Slice helpers** (`:478-506`):
   - `addRef(refs []agentRef, ref agentRef) []agentRef` — append iff `ref.PID` not
     already present (idempotent on PID, as `addPID` was).
   - `removePID(refs []agentRef, pid int) []agentRef` — drop entries whose `PID==pid`.
   - `pids(refs []agentRef) []int` — project to PIDs for `Diagnosis`/`CleanResult`/warnings.
   - keep `formatPIDs([]int)` working off the projected PID slice.

3. **`pruneDead`** (`:390-398`) — now takes/returns `[]agentRef`, with the identity check:
   ```go
   func (m *Manager) pruneDead(refs []agentRef) []agentRef {
       var alive []agentRef
       for _, ref := range refs {
           if !m.proc.Alive(ref.PID) {
               continue
           }
           st, err := m.proc.StartTime(ref.PID)
           if err != nil || st != ref.StartTime {
               continue // dead, or a recycled PID now owned by a different process
           }
           alive = append(alive, ref)
       }
       return alive
   }
   ```

4. **`Attach`** (`:115-173`) — capture our own start time and store a record:
   - After computing `alive := m.pruneDead(cur.Agents)`, read
     `selfStart, err := m.proc.StartTime(cfg.SelfPID)`; on error, return a wrapped
     error (we must be able to identify ourselves — a same-process read should never
     fail; failing the attach is safer than storing a bogus identity).
   - `agents := addRef(alive, agentRef{PID: cfg.SelfPID, StartTime: selfStart})`.
   - Write `lockState{... Agents: agents ...}` (`:168`).
   - `warnPortChanged` / stranded-agent messaging uses `pids(alive)`.

5. **`Detach`** (`:199-229`) — `removePID(cur.Agents, selfPID)`; last-out decision still
   `len(agents) == 0`; rewrite the two `lockState` literals (`:224`, `:228`) with the
   record slice. No start-time needed to detach (PID removal is unambiguous for our own
   exit).

6. **`Inspect`** (`:264-289`) — `live := m.pruneDead(cur.Agents)`; set
   `Diagnosis.LiveAgents = pids(live)` (unchanged `[]int` type).

7. **`CleanOrphan`** (`:313-340`) — `live := m.pruneDead(cur.Agents)`; orphan iff
   `up && len(live) == 0`; rewrite the cleared `lockState{PolicyHash:…}` literal (`:336`).

8. **`Clean`** (`:350-387`) — `live := m.pruneDead(cur.Agents)`; refusal uses
   `pids(live)` for `CleanResult{Refused:true, LiveAgents: pids(live)}` (`:368-370`);
   teardown unchanged; cleared `lockState{}` literal (`:383`).

9. **`StartConfig`** (`:84-94`) — keep `SelfPID`; the start time is read inside `Attach`
   via the seam (no new field needed, keeps the value consistent with `pruneDead`'s oracle).

Consumers (`internal/status/*`, `internal/doctor/*`, `internal/cli/clean.go`) need **no
change** — they read `Diagnosis.LiveAgents` / `CleanResult.LiveAgents`, which stay `[]int`.

### Tests (update mirrors + add identity cases)

Update every lock mirror to the record shape and seed matching start times:

- `internal/proxy/lifecycle_test.go:19-26` (`lockJSON` → `Agents []agentRefJSON{PID,Start}`),
  `clean_test.go`, `diagnose_test.go`, `lifecycle_race_test.go:71-72`.
- `internal/status/status_test.go:17-23`, `internal/cli/status_test.go:65`.
- `internal/doctor/doctor_test.go:33-38`, `internal/cli/doctor_test.go:19-24`.
- `internal/cli/once_lifecycle_test.go:20`, `internal/cli/run_test.go:170-171,423`
  (`lockAgents` helper now reads records → projects PIDs), `internal/cli/clean_test.go:58`.
- `internal/cli/script_test.go:129` (`seedlock`) and the testscript fixtures
  `clean_idempotent.txtar`, `status_lists.txtar` — give each seeded agent a `start`
  value (and, for "alive" agents, the test must also seed `StartTimes[pid]` to match).
- Integration inline maps: `internal/cli/clean_integration_test.go:61,85-89`,
  `internal/cli/doctor_fix_integration_test.go:70,98-102` — `"agents":[{"pid":…,
  "start":…}]`; real `StartTime` will be read for live agents, so prefer dead agents
  (`proxy_pid`/PIDs that aren't alive) in these fixtures where they only assert
  pruning, or accept that a live self-PID's real start time is recorded.

New behavioral cases (mirror `TestDeadAgentPidPruned`, `lifecycle_test.go:189-202`):

- **Recycled-PID pruned** (`lifecycle_test.go` / `clean_test.go`): seed
  `agentRef{PID:1234, StartTime:1000}`; set `AlivePIDs[1234]=true`,
  `StartTimes[1234]=2000`. Assert `Attach`/`Clean` treat it as dead — last-out
  teardown proceeds and `Clean(force=false)` does **not** refuse.
- **Matching identity survives**: same PID with `StartTimes[1234]=1000` → retained.
- **`StartTime` error ⇒ pruned**: `AlivePIDs[1234]=true`, no `StartTimes` entry →
  pruned.
- **`clean` no longer refuses on a ghost**: extend `clean_test.go`'s refusal test so
  the only "alive" entry is a recycled ghost → `res.Refused == false`, proxy torn down.

Race test (`lifecycle_race_test.go`): pre-seed `StartTimes[1+i]` for every worker PID
(matching what `Attach` will read) so no worker is spuriously pruned mid-flight; keep
the invariants `len(Spawned)==len(Signaled)`, `len(Acquired)==len(Released)`, final
`Agents` empty. Run under `-race`.

### Success criteria

#### Automated
- [ ] `make test` green (all updated mirrors compile; new identity cases pass).
- [ ] `go test -race ./internal/proxy/...` green (race test updated for the schema).
- [ ] `make lint` green.

#### Manual
- [ ] A lock seeded with a recycled-PID ghost is pruned; `clean` without `--force`
      tears the proxy down instead of refusing.

---

## Phase 3 — Span-the-whole-run signal subscription (F5)

Keep SIGINT/SIGTERM disposition suppressed across the entire `runRun`, so a signal in
the post-`Run` teardown window can no longer terminate the wrapper before `Detach`.

### Changes — `internal/cli/run.go`

1. Add imports: `os`, `os/signal`, `syscall`.
2. Early in `runRun` (before the `Detach` defer at `:153`, ideally right after the
   manager/`app` are in scope), install a process-level subscription and a teardown
   that runs *after* `Detach` (registered earlier ⇒ runs later by LIFO):
   ```go
   // Keep our own SIGINT/SIGTERM subscription for the whole run. During the agent's
   // life cage.Run forwards these to the agent group; this subscription does nothing
   // actionable but keeps the Go default disposition (terminate) suppressed, so a
   // signal arriving in the post-Run teardown window cannot kill the wrapper before
   // the deferred Detach runs. Registered before the Detach defer so signal.Stop runs
   // after it (LIFO).
   sigCh := make(chan os.Signal, 1)
   app.ProcessGroup.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
   defer signal.Stop(sigCh)
   ```
   The channel is intentionally left undrained — a buffered, unread Notify channel
   still suppresses default disposition (further signals are dropped, which is fine).
   Do **not** use `signal.Ignore` (it would undo `cage.Run`'s `Notify` and break
   forwarding during the agent's life).

   Composition: both `runRun` and `cage.Run` subscribe via `signal.Notify` (additive);
   only `cage.Run` forwards, so there is no double-handling. After `cage.Run` returns
   and calls its own `signal.Stop`, `runRun`'s subscription remains active through
   `watcher.Stop()` + `Detach`.

### Tests — `internal/cli/run_test.go`

Because fakes cannot perform the real default-termination, the test verifies the fix
**mechanism** plus teardown completeness:

- Extend `FakeProcessGroup` (if needed) so `Notify` records the subscribed signal set
  alongside the channel, then assert that `runRun` installed a wrapper-level
  subscription covering `SIGINT` and `SIGTERM` (distinct from the cage runner's),
  established before the cage launch.
- Behavioral: with the happy-path fixture, after `runRun` returns, push a `SIGINT`
  onto `runRun`'s recorded channel (simulating a teardown-window signal) and assert
  `Detach` still completed — lock agents emptied and the proxy SIGTERM'd (reuse the
  `signaled(...)` / `lockAgents(...)` helpers). Mirror the goroutine + `NotifyChans()`
  polling pattern from `internal/cage/run_test.go:29-36` if a live window is needed.
- Frame the test comment as defense-in-depth (matches the review: the common Ctrl-C
  path already tears down via `cage.Run`).

### Success criteria

#### Automated
- [ ] `make test` green (new F5 test passes; `TestRunHappyPath` still green).
- [ ] `make lint` green.

#### Manual
- [ ] `runRun` holds a SIGINT/SIGTERM subscription that outlives `cage.Run`; teardown
      (`Detach`) completes when a signal is delivered after `Run` returns.

---

## Phase 4 — Full verification & ticket close

### Steps

1. `make test` (race; the default gate) — green.
2. `make lint` — green.
3. `make test-integration` if reachable in this environment (exercises the real
   `OSProcessManager.StartTime` syscall and the live lifecycle); otherwise note it as
   run-on-a-mac follow-up.
4. `make build` so `bin/agent-creance` reflects the final commit.
5. Re-check the ticket acceptance criteria (F5 + F13 boxes) and tick them.
6. Set the ticket `**Status:** Done`, bump `**Updated:**`, and append a dated note
   under `## Notes & Updates` summarising the two fixes and the checkpoint decisions.

### Success criteria

#### Automated
- [ ] `make test` and `make lint` green.
- [ ] `make build` produces `bin/agent-creance`.

#### Manual
- [ ] All AC-0061 acceptance-criteria boxes satisfied.
- [ ] Ticket marked Done with a transition note.

---

## Testing strategy summary

- Pure logic (`pruneDead` identity, slice helpers) → table/blackbox `proxy_test`
  cases on the shared `harness`.
- Schema is exercised through `Attach`/`Detach`/`Clean`/`Inspect` (no exported
  `pruneDead`), asserting via `h.proc.Signaled` (kill) and `h.readLock(t)` (refcount).
- Concurrency invariants kept green under `-race` via `lifecycle_race_test.go`.
- Real syscall (`StartTime`) and real lifecycle only under the `integration` tag.
- F5 verified at the seam level (subscription present + teardown completes), framed as
  defense-in-depth.

## Risks / notes

- **Test ripple is the bulk of the work**: ~15 mirror structs + 2 integration maps
  hard-code `agents` as `[]int`; each must move to records and (for "alive" agents)
  seed a matching `StartTimes` oracle entry. Mechanical but wide — do it package by
  package and lean on the compiler.
- **Old-lock cold-start**: a proxy started by a previous binary version is orphaned on
  the first run of the new binary (its `[]int` lock won't parse). Accepted per scope;
  the orphan is reclaimed by `clean` / next-run prune.
- **`StartTime` self-read failure** in `Attach` is treated as fatal for that attach —
  defensible because a process can always read its own `kinfo_proc`; surfacing it beats
  silently storing a mismatched identity.
