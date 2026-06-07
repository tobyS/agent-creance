# Status: AC-0029 init command (WP-5.4)

Plan: thoughts/shared/plans/2026-06-07-AC-0029-init-command.md

- [x] Phase 1 — Command + template rendering, wired into the tree
- [x] Phase 2 — Tests (golden render, runInit unit, init.txtar)

## Log

### Phase 1 (2026-06-07)
Added internal/cli/init.go (newInitCmd → runInit; detectGenerators,
renderConfigTemplate/generatorsBlock, writeFileAtomic) and wired newInitCmd into
cli.go. Confirmed null/empty egress validates, so both template variants parse.
Smoke-tested: empty dir → commented generators; both manifests → both listed;
re-run refuses (exit 1); --force overwrites. make test + make lint + go build green.
Null-egress validity guard from the plan: resolved — validate() only inspects
allow/deny rules, so the commented template is valid (verified by smoke run + the
Phase 2 parse assertion to follow).

### Phase 2 (2026-06-07)
Added internal/cli/init_test.go: golden render tests (none/package_only/both
under testdata/init/), a parse+validate assertion over every variant (confirms
the null-egress template is valid), runInit unit tests against the sysdep fakes
(empty/package-only/both/refuse/force/perm/no-gitignore), and a hermetic
init.txtar covering the CLI surface. make test (race) + make lint + make golden
(no drift) all green.
