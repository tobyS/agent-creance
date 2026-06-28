# Status — AC-0065 onboarding README, quickstart, install path

Plan: `thoughts/shared/plans/2026-06-28-AC-0065-onboarding-readme-quickstart-install.md`

## Progress

- [x] Phase 1 — Add a `make install` target
- [ ] Phase 2 — README: fix stale claims, add Quickstart + Install

## Notes

- Started 2026-06-28. Ticket set to In Progress.
- Phase 1 done: `make install` = stamped `go install` into GOPATH/bin. Verified
  `make help` lists it; installed binary reports `59492e7-dirty`, not `dev`.
  Tests/lint green.
