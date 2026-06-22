# Implementation status — AC-0061

Plan: `thoughts/shared/plans/2026-06-22-AC-0061-proxy-refcount-integrity.md`

- [x] Phase 1 — StartTime sysdep seam
- [ ] Phase 2 — Lock schema + pruneDead identity check
- [ ] Phase 3 — Span-the-whole-run signal subscription (F5)
- [ ] Phase 4 — Verify, build, close ticket

## Log

- Phase 1 done: added `ProcessManager.StartTime(pid)` (interface + `OSProcessManager`
  over `unix.SysctlKinfoProc`), `FakeProcessManager.StartTimes`/`StartTimeErr` oracle,
  real `StartTime` test. `make test` + `make lint` green.
