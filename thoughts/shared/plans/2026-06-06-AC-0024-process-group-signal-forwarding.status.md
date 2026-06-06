---
plan: thoughts/shared/plans/2026-06-06-AC-0024-process-group-signal-forwarding.md
ticket: AC-0024
status: complete
started: 2026-06-06
completed: 2026-06-06
---

# Status: AC-0024 — Process group & signal forwarding

- [x] Phase 1 — Implement `OSProcessGroup.Start` seam + extend the fake
- [x] Phase 2 — `cage.Runner` forwarding loop
- [x] Phase 3 — Wire `ProcessGroup` into `App`/`cli.Main`
- [x] Phase 4 — Integration tests + final verification (commit pending)

## Notes

- `make test` (race) and `make lint` green. `make test-integration`:
  `TestOSProcessGroupTearsDownChildTree` passes on the dev box (real syscall-level
  group teardown); the safehouse composition test skips (can't nest sandbox-exec here).
- One pre-existing, **unrelated** integration failure on this sandboxed host:
  `internal/proxy` `TestLifecycleStartAttachTeardownRealProxy` (real `mitmdump` can't
  bind under the sandbox; it only skips when mitmdump is absent). `git diff 007a4c3 HEAD`
  touches no `internal/proxy` file nor any seam it uses — not an AC-0024 regression.
- Resolved the ticket's open question empirically: safehouse 0.10.1 does not detach its
  child (no exec/setsid), so one `Setpgid` group covers the whole tree.
