---
plan: thoughts/shared/plans/2026-06-07-AC-0032-status-clean-commands.md
ticket: AC-0032
started: 2026-06-07
---

# Status: AC-0032 status / clean commands

- [x] Phase 1 — Seams & state plumbing (FileSystem.ReadDir, Resolver.ProjectsRoot, LayoutForRoot)
- [x] Phase 2 — proxy.lock canonical_path field
- [x] Phase 3 — proxy.Manager.Clean
- [x] Phase 4 — internal/status package
- [x] Phase 5 — CLI commands (status, clean)
- [x] Phase 6 — Integration test
- [x] Final verification

## Notes

- All phases implemented and committed. `make test` + `make lint` green.
- New `clean` real-proxy integration test (`TestCleanStopsRealProxy`) passes under
  `make test-integration`.
- Two pre-existing integration failures on this host are environmental, not from
  this work — verified identical on base commit 99514a9:
  - `internal/proxy` `TestLifecycleStartAttachTeardownRealProxy`: the mitmproxy
    enforcer addon never comes up (sandbox blocks `timeout`/addon load here).
  - `internal/setup` `TestVerifyLive`: stat of `~/.mitmproxy` is "operation not
    permitted" (sandbox).
- Ticket AC-0032 marked Done.
</content>
