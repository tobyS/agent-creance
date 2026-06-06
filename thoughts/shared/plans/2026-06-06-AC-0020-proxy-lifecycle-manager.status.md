# Status: AC-0020 Proxy lifecycle manager (WP-3.4)

Plan: `thoughts/shared/plans/2026-06-06-AC-0020-proxy-lifecycle-manager.md`

## Progress

- [x] Phase 1 — Redesign the `Flock` seam + real `OSFlock`
- [x] Phase 2 — New seams: `ProcessManager` + `PortAllocator`
- [x] Phase 3 — The lifecycle `Manager`
- [x] Phase 4 — Manager tests, race simulation, integration scaffold
- [ ] Phase 5 — Reconcile docs & close the ticket

## Notes

- Phase 1: `Flock.Acquire` now returns `LockedFile{ReadAll/Write/Release}`; real
  `OSFlock` via `unix.Flock(LOCK_EX)`; `FakeFlock` backs `Contents` + per-path
  mutex for real exclusion. `x/sys` promoted to a direct dependency (v0.45.0). No
  production callers existed, so no other packages changed.
- Phase 2: added `ProcessManager` (Spawn detached via Setsid / Alive via kill-0 /
  Signal by pid) and `PortAllocator` (Allocate :0 / TryReclaim / Probe) seams with
  real `OS*` impls + fakes + smoke tests. Updated `ErrNotImplemented` doc (Flock no
  longer deferred). The proxy is killed by PID (last agent out holds no handle), so
  signalling lives on ProcessManager, not the group-targeted ProcessGroup.
- Phase 3+4 (committed together — the riskiest logic ships with its tests):
  `internal/proxy/lifecycle.go` Manager with Attach/Detach; the five mandated
  cases + corrupt-lock/error/C4 cases (blackbox), white-box helper tests, a -race
  attach/detach simulation, and the S3-gated integration scaffold. Added a mutex to
  `FakeFileSystem` so the -race sim is sound (MkdirAll runs before the flock, so it
  is legitimately concurrent). Proxy-alive = PID-liveness AND port probe. mitmproxy
  is launched as `mitmdump --listen-port N -s enforcer.py --set creance_policy=… 
  --set creance_audit_log=…` (option names verified against enforcer.py).
</content>
