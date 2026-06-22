---
date: 2026-06-22
ticket: AC-0061
title: "Research — Proxy-refcount integrity: tear down on signal and survive PID recycling"
status: complete
branch: main
commit: 536ddee7c2177e355fbb19453bdebee8f3ddb0ac
tags: [research, proxy, lifecycle, signals, pid-reuse, sysdep]
---

# Research: AC-0061 — Proxy-refcount integrity (F5 + F13)

## Research question

How does the multi-agent proxy lifecycle currently (a) tear down on wrapper exit
and (b) decide whether an attached agent is still alive, and what is the minimal,
seam-respecting way to close the two gaps the 2026-06-22 review found:

- **F5** — the deferred `Detach` is skipped if a SIGINT/SIGTERM arrives in the
  window *after* `cage.Run` returns (signal disposition has reverted to the Go
  default = terminate), leaking an orphan proxy with a stale agent PID in the lock.
- **F13** — `pruneDead` trusts a bare agent PID via `kill(pid,0)` only; a recycled
  macOS PID keeps `len(alive) > 0`, pinning the proxy alive forever and making
  `clean` refuse with a spurious "live agents attached".

## Summary / recommendation

Both fixes are tractable within the existing `sysdep` seam model and touch a small,
well-bounded surface. Recommended directions (details and open choices in the
sections below):

- **F5 — span-the-whole-run wrapper-level signal subscription.** Install a
  process-level `signal.Notify(SIGINT, SIGTERM)` in `runRun` that stays registered
  for the *entire* function — registered early so its `defer signal.Stop` runs
  *after* the deferred `Detach`. While the agent runs, `cage.Run` keeps its own
  subscription and does the forwarding; `runRun`'s subscription does nothing
  actionable but keeps the Go default disposition suppressed, so a signal in the
  post-`Run` teardown window can no longer terminate the wrapper before `Detach`
  runs. This is gap-free (no re-arm race) and composes cleanly with `cage.Run`
  (both subscribe; only `cage.Run` forwards).

- **F13 — process start-time as the second identity factor.** Record `{pid,
  start_time}` per agent in the lock; `pruneDead` keeps an entry iff
  `Alive(pid) && StartTime(pid) == recorded`. A recycled PID has a newer start
  time → mismatch → pruned. macOS start time is read via the already-present
  `golang.org/x/sys/unix.SysctlKinfoProc("kern.proc.pid", pid).Proc.P_starttime`
  (a `Timeval`), wrapped behind a new `ProcessManager.StartTime(pid)` seam method.
  **A pure random "start token" written only to the lock does *not* solve F13** —
  there is nothing on the recycled live process to compare it against; the ticket's
  own testing note ("the fake may need a 'PID alive but different process at same
  PID' capability") points at the start-time/process-identity approach. This is the
  primary open decision for the user (see Open Questions).

The lock schema change (`Agents []int` → a slice of `{pid, start_time}` records) is
**unversioned** and ripples through ~15 test mirror structs and 2 integration tests
(enumerated below). Per the ticket, schema migration beyond what the identity factor
requires is out of scope; an old `agents: [1,2,3]` lock fails to unmarshal and
`readLock` already treats that as a cold start (self-heals, at the cost of orphaning
a proxy started by a *previous binary version* mid-upgrade — an accepted edge).

## F5 — the post-`Run` teardown window

### How it works today

`runRun` (`internal/cli/run.go:53`) delegates **all** signal handling to
`cage.Run`; `run.go` imports no `os/signal`. The teardown is purely defer-based:

- `internal/cli/run.go:153-157` — `defer ... mgr.Detach(layout, selfPID) ...`
  (the refcount decrement; doc-comment at `:137-139`: "Detach is deferred
  immediately so every exit path decrements; the last agent out kills the proxy").
- `internal/cli/run.go:216` — `defer watcher.Stop()` (only if the watcher started).
- `internal/cli/run.go:96` — `defer prog.Close()`.
- LIFO execution on return: `watcher.Stop()` → `mgr.Detach(...)` → `prog.Close()`.
- The agent is launched at `internal/cli/run.go:222`: `cage.NewRunner(app.ProcessGroup).Run(ctx, inv)`, which **blocks until the whole process group is reaped**, so the deferred `Detach` runs strictly after.

`cage.Run` (`internal/cage/run.go:53-85`) owns the signal lifetime:

```go
sigCh := make(chan os.Signal, 1)
r.pg.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
defer signal.Stop(sigCh) // cage/run.go:63 — torn down when Run returns
...
case sig := <-sigCh:
    _ = proc.Signal(sig)       // forward to the agent group (cage/run.go:74)
    if killTimer == nil { killTimer = time.After(r.grace) }
case <-killTimer:
    _ = proc.Signal(syscall.SIGKILL)
case werr := <-waitCh:
    return werr                // cage/run.go:82 — the only return
```

The signal seam is `sysdep.ProcessGroup` (`internal/sysdep/processgroup.go:30-43`):
`Notify(ch, sigs...)` (prod wraps `signal.Notify`, `:99-101`) and
`Process.Signal(sig)` (prod = `syscall.Kill(-pgid, s)`, `:114-128`). There is **no**
`Stop`/`Reset` on the seam — `cage.Run` calls `signal.Stop` directly.

### The exact gap

1. While the agent runs, SIGINT/SIGTERM is caught by `cage.Run`'s loop and
   forwarded to the agent group — the wrapper does not die.
2. The instant the agent group is reaped, `cage.Run` executes `return werr`
   (`cage/run.go:82`) and its `defer signal.Stop(sigCh)` (`cage/run.go:63`) fires.
   **From this point SIGINT/SIGTERM revert to the Go runtime default = terminate.**
3. Control returns to `runRun` between `cage/run.go` returning and the deferred
   `mgr.Detach` (`run.go:153-157`) executing.
4. A signal delivered to the wrapper in this window terminates it immediately;
   Go does **not** run deferred functions on signal-driven termination, so
   `mgr.Detach` never runs → the refcount is never decremented and the proxy /
   session overlay is not torn down by this exiting agent (leak; self-heals only on
   the next run's dead-PID prune in `Attach`).

### Fix mechanism and seams

- The minimal, gap-free fix is a wrapper-level subscription spanning all of
  `runRun`. Use `app.ProcessGroup.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)`
  (the seam already exists and is process-wide, not group-specific — prod just
  calls `signal.Notify`) and `defer signal.Stop(sigCh)`. Register this **before**
  the `Detach` defer (`run.go:153`) — ideally at the top of `runRun` — so, by LIFO,
  `signal.Stop` runs *after* `Detach`, keeping default disposition suppressed across
  `watcher.Stop()` + `Detach`.
- **Composition with `cage.Run`:** both subscribe via `signal.Notify` (additive — each
  gets a copy). During the agent's life `cage.Run` forwards; `runRun`'s handler is a
  no-op (nothing read / discarded), so there is **no double-handling** (only
  `cage.Run` forwards to the group). After `Run` returns, `cage.Run`'s `signal.Stop`
  removes *its* subscription but `runRun`'s remains → disposition still overridden.
- **Do not use `signal.Ignore`** — per Go docs it *undoes* any prior `Notify` for
  those signals, which would break `cage.Run`'s forwarding during the agent's life.
- Re-arming a handler only around the teardown defers (the other option in the
  ticket) has a race: a tiny window between `cage.Run`'s `signal.Stop` and the
  re-`Notify` where default disposition applies. Span-the-whole-run has no such gap.
- **Testability:** `FakeProcessGroup` records `Notify` channels via `NotifyChans()`
  (`sysdeptest/processgroup.go`), and `signal.Stop` is a no-op against the fake's
  channel. A new `run_test.go` test runs `runRun` in a goroutine, polls until
  `runRun`'s own channel is registered, releases the cage `FakeProcess.WaitGate` so
  `Run` returns, then pushes a signal into `runRun`'s channel during the teardown
  window and asserts `Detach` still ran (lock agents emptied + proxy SIGTERM'd via
  the existing `signaled(...)` / `lockAgents(...)` helpers). Note: against fakes the
  signal cannot actually terminate the process, so the test asserts the *subscription
  exists and absorbs* the signal (disposition is suppressed) and that teardown
  completes — the real-OS default-termination is what the production change prevents.

### F5 references

- `internal/cli/run.go:53` (`runRun`), `:140-148` (`Attach`), `:153-157` (deferred
  `Detach`), `:216` (`watcher.Stop`), `:222` (`cage.Run`).
- `internal/cage/run.go:53-85` (`Run`, `Notify`, `signal.Stop`, forwarding loop).
- `internal/sysdep/processgroup.go:30-56` (seam), `:99-101` (`Notify`), `:114-128`
  (`Signal`).
- `internal/sysdep/sysdeptest/processgroup.go` (`FakeProcessGroup`, `NotifyChans`).
- Tests to extend: `internal/cli/run_test.go` (`runFixture`, `TestRunHappyPath`),
  pattern in `internal/cage/run_test.go:29-74` (`startedGroup`,
  `TestRunForwardsSignalToGroup`).

## F13 — recycled agent PID pins the proxy

### How it works today

The lock schema (`internal/proxy/lifecycle.go:47-62`):

```go
type lockState struct {
	ProxyPID      int    `json:"proxy_pid"`
	Port          int    `json:"port"`
	PolicyHash    string `json:"policy_hash"`
	Agents        []int  `json:"agents"`         // bare PIDs — no second factor
	CanonicalPath string `json:"canonical_path"`
}
```

`pruneDead` (`internal/proxy/lifecycle.go:390-398`) is the entire liveness logic:

```go
func (m *Manager) pruneDead(pids []int) []int {
	var alive []int
	for _, pid := range pids {
		if m.proc.Alive(pid) {           // kill(pid, 0) only
			alive = append(alive, pid)
		}
	}
	return alive
}
```

`m.proc.Alive` is the `kill(pid,0)` oracle (`OSProcessManager.Alive`,
`internal/sysdep/processmanager.go:62-72`; `EPERM` ⇒ alive). **No identity check** —
any process that recycled the PID number satisfies it.

The **proxy** already has a second factor — the conjunction with a TCP port probe,
appearing identically in `Attach`/`Inspect`/`CleanOrphan`:

```go
proxyUp := cur.ProxyPID != 0 && m.proc.Alive(cur.ProxyPID) && m.ports.Probe(cur.Port)
// lifecycle.go:135, :280, :325 — via sysdep.PortAllocator.Probe (loopback TCP dial)
```

Agents get nothing analogous. The consequences (both real, both Medium):
- `Attach`/`Inspect`/`Clean`/`CleanOrphan` all call `pruneDead`; a ghost survives →
  `len(alive) > 0` → last-out teardown never fires → proxy leaks indefinitely.
- `Clean` refuses: `if len(live) > 0 && !force { return CleanResult{Refused:true,
  LiveAgents:live} }` (`lifecycle.go:368-370`), surfaced by `internal/cli/clean.go:47-51`
  as "agent(s) still attached … re-run with --force" — strands the operator.

### Why a lock-only "start token" is insufficient

The ticket lists "start token / start time" as alternatives. A random token written
**only to the lock** at attach cannot detect recycling: when PID 1234 is reused by an
unrelated live process X, `pruneDead` has nothing on X to compare the recorded token
against (X has no token). It would prove "this entry was written by some agent",
not "the process at PID 1234 is still that agent". The factor must be something the
OS reports about the *live* process at that PID — i.e. the **process start time**
(or an OS-corroborated per-agent lock held for the wrapper's lifetime, heavier). The
ticket's testing note that `FakeProcessManager` needs a "PID alive but different
process at same PID" capability only makes sense for the start-time / identity-read
approach, so that is the recommended one.

### Fix mechanism and seams

- **New seam:** add `StartTime(pid int) (int64, error)` to `ProcessManager`
  (`internal/sysdep/processmanager.go:25-38`). Prod reads
  `unix.SysctlKinfoProc("kern.proc.pid", pid)` and returns
  `.Proc.P_starttime` (a `unix.Timeval`) as e.g. unix-microseconds `int64`.
  **Verified available:** `golang.org/x/sys v0.45.0` is a direct dependency;
  `SysctlKinfoProc` is at `.../syscall_darwin.go:502` and `P_starttime Timeval` at
  `.../ztypes_darwin_arm64.go:766`. No new dependency required. The start time is
  wall-clock and stable for the life of a process; a recycled PID gets a new value.
- **Schema:** change `lockState.Agents` from `[]int` to a slice of records, e.g.
  `[]agentRef{ PID int; StartTime int64 }` (JSON `{"pid":…, "start":…}`). `addPID`
  /`removePID`/`formatPIDs` (`lifecycle.go:478-506`) adapt to the record type.
- **`pruneDead`:** keep an entry iff `m.proc.Alive(ref.PID) && start == ref.StartTime`
  where `start, err := m.proc.StartTime(ref.PID)`; on `err` treat as **dead**
  (prune) — these are same-user wrapper processes, so `StartTime` should always be
  readable; failing safe toward "ghost" prevents pinning. (Confirm this failure-mode
  choice with the user — it is the one behavioral judgment call.)
- **Capture at attach:** `Attach` records `StartTime(cfg.SelfPID)` for its own entry,
  read through the *same* seam used by `pruneDead` so values are consistent.
  `SelfPID` comes from `os.Getpid()` at `internal/cli/run.go:141` (not seam-routed;
  fine to keep, or read start time inside `Attach`).
- **Fake:** extend `FakeProcessManager` (`sysdeptest/processmanager.go`) with a
  `StartTimes map[int]int64` (absent ⇒ error or 0) and a `StartTime` method.
  Recycling is simulated by `AlivePIDs[1234]=true` while `StartTimes[1234]` differs
  from the value seeded in the lock — exactly the "different process at same PID"
  capability the ticket calls for.

### Lock-schema ripple (everything that hard-codes `Agents []int`)

Schema definition + helpers (production):
- `internal/proxy/lifecycle.go:47-62` (`lockState`), `:449-476` (`readLock`/`writeLock`),
  `:478-506` (`addPID`/`removePID`/`formatPIDs`).
- `lockState{...}` literals to update: `Attach :168`, `Detach :224` & `:228`,
  `CleanOrphan :336`, `Clean :383`.
- `pruneDead` call sites: `Attach :131`, `Inspect :279`, `CleanOrphan :324`,
  `Clean :367`.

Consumers (read agents via `Diagnosis.LiveAgents`, mostly count-only — likely
`[]int` of PIDs preserved in `Diagnosis` for display):
- `internal/proxy/lifecycle.go:233-258` (`Diagnosis.LiveAgents []int`).
- `internal/status/report.go:49,79` (AGENTS column = `len(LiveAgents)`),
  `internal/status/status.go:52` (`Inspect`).
- `internal/doctor/report.go:154,156` (`len(LiveAgents)`), `internal/doctor/doctor.go:78`
  (`Inspect`), `:84` (`CleanOrphan`).
- `internal/cli/clean.go:41,47-51` (`Clean`, refusal message via `LiveAgents`).

Test mirror structs / seeds that hard-code `Agents []int` (must change in lockstep):
- `internal/proxy/lifecycle_test.go:19-26` (`lockJSON`), `clean_test.go`,
  `diagnose_test.go`, `lifecycle_race_test.go:71-72`.
- `internal/status/status_test.go:17-23`, `internal/cli/status_test.go:65`.
- `internal/doctor/doctor_test.go:33-38`, `internal/cli/doctor_test.go:19-24`.
- `internal/cli/once_lifecycle_test.go:20`, `internal/cli/run_test.go:170-171,423`
  (`lockAgents` helper), `internal/cli/clean_test.go:58`,
  `internal/cli/script_test.go:129` (`seedlock`).
- Integration (inline `map` with `"agents": []int{...}`):
  `internal/cli/clean_integration_test.go:61,85-89`,
  `internal/cli/doctor_fix_integration_test.go:70,98-102`.
- Testscript fixtures: `internal/cli/testdata/script/clean_idempotent.txtar`,
  `status_lists.txtar` (seed `proxy.lock` JSON with an `agents` array).

### F13 references

- `internal/proxy/lifecycle.go:47-62`, `:115-173` (`Attach`), `:199-229` (`Detach`),
  `:264-289` (`Inspect`/`Diagnosis`), `:313-340` (`CleanOrphan`), `:350-387` (`Clean`),
  `:390-398` (`pruneDead`), `:478-506` (slice helpers).
- `internal/sysdep/processmanager.go:25-38` (seam), `:62-72` (`Alive`).
- `internal/sysdep/sysdeptest/processmanager.go` (`AlivePIDs` oracle, no identity today).
- `golang.org/x/sys@v0.45.0/unix/syscall_darwin.go:502` (`SysctlKinfoProc`),
  `ztypes_darwin_arm64.go:766` (`P_starttime Timeval`).
- Tests to extend: `internal/proxy/lifecycle_test.go` (`TestDeadAgentPidPruned`
  pattern, `:189-202`), `internal/proxy/clean_test.go`,
  `internal/proxy/lifecycle_race_test.go`.

## Testing patterns to follow (from the existing suite)

- Proxy-lifecycle tests are blackbox `package proxy_test` built on a shared
  `harness` (`lifecycle_test.go:39-68`): `proxy.NewManager(fs, fl, pm, pa, sl, warn)`
  wired from `sysdeptest` fakes. The lock is an in-memory map in `FakeFlock.Contents`
  keyed by `h.lay.ProxyLock()`; seed via `h.seedLock(lockJSON{...})`, inspect via
  `h.readLock(t)`.
- PID liveness oracle: `h.proc.AlivePIDs[pid] = true` (absent key ⇒ dead). A spawned
  proxy needs `SpawnPID` + `AlivePIDs[pid]=true` + `h.ports.Listening[port]=true`.
- Kill assertion: `h.proc.Signaled` (`[]SignaledPID{PID, Sig}`), expect
  `{pid, syscall.SIGTERM}`. Refcount assertion: re-read lock, check `ls.Agents` /
  `ls.ProxyPID`.
- Race test (`lifecycle_race_test.go`): one shared `Manager` + `FakeFlock`, N
  goroutines `Attach`→`Detach`; invariants `len(Spawned)==len(Signaled)` (no
  leak/double-kill), `len(Acquired)==len(Released)`, final `Agents` empty. Keep green
  under `-race`; if the identity field changes the schema, update the mirror at
  `:71-72`.
- CLI run tests (`run_test.go`): `package cli`, `newRunFixture(t)`, drive
  `runRun(ctx, f.app, ".")`; cage runs through the *real* `cage.Runner` over
  `FakeProcessGroup`. Signal injection mirrors `cage/run_test.go:29-36`
  (`startedGroup`): run in a goroutine, poll `NotifyChans()`, push the signal, gate
  the child exit with `FakeProcess{WaitGate: …}`.
- Testscript: `seedlock <srcfile>` resolves the out-of-tree state dir and writes a
  `proxy.lock` JSON fixture; `proxy_pid: 999999` is a reliably-dead proxy.

## Open questions for the user (Phase 2 checkpoint)

1. **F13 identity factor.** Confirm **process start time** (read via a new
   `ProcessManager.StartTime` seam over `unix.SysctlKinfoProc`) vs. an alternative.
   Research finding: a lock-only random token cannot detect recycling; start time
   is the approach the ticket's fake-capability note implies. (Recommended:
   start time.)
2. **F13 `StartTime` read-error policy.** When `StartTime(pid)` errors (or the
   process vanishes between `Alive` and `StartTime`), treat the entry as **dead**
   (prune — fail toward not pinning) vs. **alive** (conservative — don't tear down
   under uncertainty). Recommended: dead, since agents are same-user and readable.
3. **F5 mechanism.** Confirm **span-the-whole-run** wrapper subscription in `runRun`
   vs. **re-arm around the teardown defers**. Recommended: span-the-whole-run
   (gap-free, composes cleanly with `cage.Run`).
4. **Lock schema migration.** Accept that an old `agents: [1,2,3]` lock (written by a
   previous binary) fails to unmarshal and is treated as a cold start (self-heals,
   may orphan a cross-version proxy mid-upgrade) — per the ticket's "no migration
   beyond the identity factor" scope. vs. add a tolerant custom unmarshaller. The
   ticket scopes migration out; recommended: accept cold-start.

## tce config drift

None observed. `profile.md` and `tickets.md` match the repo (Go 1.26 module, the
`internal/` layout, `sysdep` seam convention, tmt ticket system) as encountered
during research.
