# AC-0027 Skill install — implementation status

Plan: `thoughts/shared/plans/2026-06-07-AC-0027-skill-install.md`

- [x] Phase 1: Share the skill-path constant (export `setupcheck.SkillFileRel`)
- [ ] Phase 2: Embed SKILL.md + implement `InstallSkill` (with tests)

## Log

### Phase 1 (2026-06-07)
Renamed `setupcheck.skillFileRel` → exported `SkillFileRel`; updated in-package use.
build + `make test` + `make lint` green.
