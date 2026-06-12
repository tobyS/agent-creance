# Implementation Status: AC-0049 — Skill trigger for body-blind WebFetch 403s

## Phase 1: Skill text, tests, design.md sync
- **Status**: ✅ Complete
- **Started**: 2026-06-12 17:35
- **Completed**: 2026-06-12 17:38

### Steps Performed
1. `internal/setup/SKILL.md`: frontmatter description gained the third trigger
   clause (fetch tool fails with bare 403 / "response body was not retrieved"
   inside the cage) and the widened "Covers …" sentence; inserted new
   §4 "Body-blind clients — WebFetch hides the refusal" (don't assume
   site-blocking/auth, don't try mirrors, curl the URL for the structured
   refusal, then apply §2/§3); auth section renumbered §4 → §5.
2. `internal/setup/skill_test.go`: `TestSkillContentMentionsTriggers` extended
   with `WebFetch`, `response body was not retrieved`, `Do NOT try mirrors`;
   new `TestSkillWebFetchTriggerInFrontmatter` (mirrors the AC-0045
   frontmatter test) pins the trigger in the description.
3. `docs/design.md` activation sentence (skill paragraph in "Network refusal
   handling") now names the body-blind WebFetch case.

### Issues Encountered
- None. Plan applied verbatim.

### Verification
- ✅ `make test` (full suite, race) passes — internal/setup re-ran, all green
- ✅ `make lint` (go vet + golangci-lint) passes
- ✅ `make build` run after the final commit (CLAUDE.md convention)

### Commit
- (filled after commit) feat(AC-0049): skill triggers on body-blind WebFetch 403s
