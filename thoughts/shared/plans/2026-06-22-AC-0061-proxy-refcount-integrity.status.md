# Implementation status — AC-0061

Plan: `thoughts/shared/plans/2026-06-22-AC-0061-proxy-refcount-integrity.md`

- [x] Phase 1 — StartTime sysdep seam
- [x] Phase 2 — Lock schema + pruneDead identity check
- [x] Phase 3 — Span-the-whole-run signal subscription (F5)
- [x] Phase 4 — Verify, build, close ticket

## Log

- Phase 1 done: added `ProcessManager.StartTime(pid)` (interface + `OSProcessManager`
  over `unix.SysctlKinfoProc`), `FakeProcessManager.StartTimes`/`StartTimeErr` oracle,
  real `StartTime` test. `make test` + `make lint` green.
- Phase 2 done: lock `Agents` is now `[]agentRef{PID,StartTime}`; `Attach` records its
  own start time; `pruneDead` prunes on PID-gone OR start-time mismatch OR read error;
  `addRef`/`removeRef`/`pids` helpers; `Diagnosis`/`CleanResult.LiveAgents` stay `[]int`
  so status/doctor/clean are externally unchanged. Updated all lock test mirrors
  (proxy, status, doctor, cli) and added recycled-PID + own-start-time-unreadable
  cases. `make test` (race) + `make lint` green.
- Phase 3 done: `runRun` installs its own process-level `signal.Notify(SIGINT,SIGTERM)`
  before the Detach defer (so `signal.Stop` runs after Detach by LIFO), keeping default
  disposition suppressed across the post-`Run` teardown window. New `run_test.go` test
  drives a gated cage in a goroutine, injects a signal into the wrapper channel while
  the agent runs, and asserts teardown (Detach) still completes. `make test` (race) +
  `make lint` green.
- Phase 4 done: `make test` (race) + `make lint` + `make build` green. Integration
  run surfaced a real bug — `unix.SysctlKinfoProc` fails EIO on this kernel — fixed by
  parsing the leading `p_starttime` from raw `kern.proc.pid` bytes; the real-proxy
  lifecycle integration test then passed. Updated the doctor-fix integration fixture to
  the record lock shape. The `verify` battery's `kc-read`/`kc-write` vectors fail for
  lack of a keychain credential in this environment (unrelated to AC-0061). Ticket set
  to Done with a transition note.
