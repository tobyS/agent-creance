---
plan: thoughts/shared/plans/2026-06-07-AC-0028-setup-command.md
ticket: AC-0028
updated: 2026-06-07
---

# Status: AC-0028 setup command

- [x] Phase 1: Command + seam wiring
- [x] Phase 2: Tests

## Notes
- Phase 1: App gained TLSProber/Sleeper, wired OSTLSProber{}/OSSleeper{} in Main;
  internal/cli/setup.go adds newSetupCmd + runSetup; registered in newRootCmd.
- Phase 2: internal/cli/setup_test.go covers default / --no-skill / --no-ca-install /
  verify-failure / both-opt-outs against sysdep fakes; setup_help.txtar covers help + args.
  make test (race) + make lint + go build all green. AC-0028 complete.
