---
plan: thoughts/shared/plans/2026-06-05-AC-0007-config-schema-loader.md
ticket: AC-0007
started: 2026-06-05
---

# AC-0007 — Implementation status

## Phase 1 — Schema types, strict loader, example round-trip
Status: done (commit pending)

- internal/config: config.go (structs + Parse + applyDefaults), errors.go
  (ValidationError + reformat), validate.go (parseHostService + validate stub).
- testdata/example.yaml copied from docs/design.md; round-trip + empty + mode-default
  tests pass.
- yaml.v3 promoted to direct require (same v3.0.1).
- go build / go test -race ./internal/config / make lint all green.

## Phase 2 — Validation + golden error messages
Status: done (commit pending)

- validate.go: validate() rejects passthrough+paths/methods, unknown mode, empty
  host (uniform across allow + deny_always); reformat() turns yaml errors into
  type-name-free messages.
- 7 golden fixtures + .golden files; TestValidate table test; TestGoldenErrors;
  TestValidate_ReportsAllIssues (accumulates, not fail-fast).
- go test -race ./... green; make lint clean; make golden clean.
- Fixed a pre-existing `make golden` defect (cli/state/sysdep lack the -update
  flag and rejected `go test ./... -update`): the target now discovers
  golden-bearing packages dynamically and runs -update against just those.

## Phase 3 — Close-out
Status: done (commit pending)

- Ticket AC-0007 marked Done; acceptance criteria ticked; both open questions
  answered inline; implementation plan + dated note added.
- Plan success criteria ticked. Final verification green (see below).
</content>
