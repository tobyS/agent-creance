---
plan: thoughts/shared/plans/2026-06-07-AC-0028-setup-command.md
ticket: AC-0028
updated: 2026-06-07
---

# Status: AC-0028 setup command

- [x] Phase 1: Command + seam wiring
- [ ] Phase 2: Tests

## Notes
- Phase 1 (commit pending): App gained TLSProber/Sleeper, wired OSTLSProber{}/OSSleeper{}
  in Main; internal/cli/setup.go adds newSetupCmd + runSetup; registered in newRootCmd.
  build + lint green; `setup --help` shows both flags.
