# AC-0027 Skill install — implementation status

Plan: `thoughts/shared/plans/2026-06-07-AC-0027-skill-install.md`

- [x] Phase 1: Share the skill-path constant (export `setupcheck.SkillFileRel`)
- [x] Phase 2: Embed SKILL.md + implement `InstallSkill` (with tests)

**Ticket complete.** Status → Done.

## Log

### Phase 1 (2026-06-07)
Renamed `setupcheck.skillFileRel` → exported `SkillFileRel`; updated in-package use.
build + `make test` + `make lint` green.

### Phase 2 (2026-06-07)
Added `internal/setup/SKILL.md` (embedded) + `internal/setup/skill.go`
(`InstallSkill` + `writeSkillIfChanged`, atomic idempotent write) + `skill_test.go`
(write-to-path, idempotent no-op, drift-rewrite, CLAUDE.md guard, content markers,
error propagation). `go build ./...`, `go test -race ./internal/setup/...`,
`make test`, `make lint`, and `-tags=integration` build all green.
