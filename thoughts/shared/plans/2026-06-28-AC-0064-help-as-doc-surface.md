---
date: 2026-06-28
ticket: AC-0064
title: "Plan — make --help a real doc surface (Long/Example, root overview, command groups)"
status: ready
research: thoughts/shared/research/2026-06-28-AC-0064-help-as-doc-surface.md
git_commit: ee52bfa
branch: main
---

# Plan: AC-0064 — make `--help` a real doc surface

## Overview

Turn the agent-creance CLI's `--help` into self-contained documentation: a root
`Long:` with the setup→init→run happy path and the setup/init distinction; a
four-group command list (Setup / Daily / Inspect / Maintenance) via cobra
command groups; and `Long:`+`Example:` on **every leaf command** plus a `Long:`
on the four parent commands. All help strings stay inline in the `cobra.Command`
literals, matching the existing `include.go` style. Help output gains stable
testscript assertions.

## Decisions (from the question checkpoint)

1. **Scope** — `Long:`+`Example:` on **all leaf commands** (the 12 top-level leaf
   commands and the 9 nested subcommands), not just the five named in the
   ticket. Parent commands (`policy`, `domain`, `service`, `mount`) get a
   `Long:` but no `Example:` (a bare parent invocation prints subcommand help).
2. **Group taxonomy** — S4's **Setup / Daily / Inspect / Maintenance**:
   - **Setup Commands:** `setup`, `init`
   - **Daily Commands:** `run`, `allow`, `deny`, `domain`, `service`, `mount`,
     `include`, `import`
   - **Inspect Commands:** `doctor`, `status`, `logs`, `policy`, `version`
   - **Maintenance Commands:** `clean` (+ `completion`, `help` via
     `SetHelpCommandGroupID`/`SetCompletionCommandGroupID`)
3. **Where help strings live** — inline in each `cobra.Command` literal, using
   the `\n`-joined Go string-fragment style of `include.go:23-26`. No extracted
   constants or embedded files.

## Current state

- Root `cli.go:90-112` sets `Use`/`Short`/`SilenceUsage`/`SilenceErrors`/
  `PersistentPreRunE` — **no `Long:`**. 16 subcommands registered flat at
  `cli.go:119-134`; cobra alphabetizes them; `completion`/`help` auto-added.
- Only `include` has a `Long:` (`include.go:23-26`); **no command has an
  `Example:`**; no command groups or custom templates anywhere.
- cobra is **v1.10.2** — grouping API available. Hazard: a `GroupID` with no
  matching `AddGroup` panics at execution (`checkCommandGroups()`).
- Help test coverage is thin: 6 `.txtar` files assert individual flag strings in
  `<cmd> --help`; **no** assertions on root help, the command list, group
  headings, `Long:`, or `Examples:`; **no** help golden files.

## Desired end state

- `agent-creance --help` shows: a `Long:` orientation block (what the tool does
  + setup→init→run + the once-per-machine/once-per-project distinction), then
  four titled group sections in Setup→Daily→Inspect→Maintenance order, with
  `completion`/`help` under Maintenance (not "Additional Commands:").
- Every leaf command's `--help` shows a `Long:` describing what it does and its
  preconditions, plus an `Examples:` section with at least one real invocation.
- Parent commands' `--help` shows a `Long:` framing their subcommands.
- All help text is accurate to current behavior (no claims the code contradicts).
- New testscript assertions lock the root help (Long lines + group headings +
  completion/help placement) and the `Long:`/`Examples:` presence on key
  commands; existing flag-string assertions still pass.
- `make test`, `make lint`, `go build ./...` green; `bin/agent-creance` rebuilt.

## What we are NOT doing

- No man-page / static-doc generation (cobra can do it later — out of scope).
- No README/quickstart/install changes (AC-0065, already done) — but root
  `Long:` wording stays **consistent** with the README Quickstart.
- No `setup` "Next:" pointer or other ergonomics (AC-0066).
- No rewording of existing `Short:` strings beyond what accuracy requires.
- No custom help/usage templates — cobra's default template renders groups and
  `Examples:` natively.

---

## Phase 1 — Command groups + root `Long:`

Establish the group infrastructure and the root orientation, so the command list
restructures before any per-command prose is added.

### Changes — `internal/cli/cli.go`

1. Define group-ID constants (package-level or near `newRootCmd`):
   ```go
   const (
       groupSetup = "setup"
       groupDaily = "daily"
       groupInspect = "inspect"
       groupMaint = "maintenance"
   )
   ```
2. Add a root `Long:` to the root `cobra.Command` literal (`cli.go:90-112`),
   `\n`-joined fragments in the `include.go` style. Content (accurate per
   research §4; mirror README Quickstart wording):
   - one line on what the tool does (cage + egress filter);
   - the happy path: `setup` (once per machine) → `init` (once per project) →
     `run`;
   - the note that `run` refuses early with a pointer if `setup`/`init` haven't
     been done (no stack trace).
3. After building `root` and before/after the `AddCommand` block, register the
   four groups in display order:
   ```go
   root.AddGroup(
       &cobra.Group{ID: groupSetup, Title: "Setup Commands:"},
       &cobra.Group{ID: groupDaily, Title: "Daily Commands:"},
       &cobra.Group{ID: groupInspect, Title: "Inspect Commands:"},
       &cobra.Group{ID: groupMaint, Title: "Maintenance Commands:"},
   )
   ```
4. Assign `GroupID` to each of the 16 top-level commands. Cleanest approach:
   set `cmd.GroupID = groupX` after each `AddCommand` (or set the field in each
   command's factory — but doing it centrally in `cli.go` keeps the taxonomy in
   one reviewable place). Mapping per Decision 2.
5. Route the auto-generated commands into Maintenance:
   ```go
   root.SetHelpCommandGroupID(groupMaint)
   root.SetCompletionCommandGroupID(groupMaint)
   ```

**Panic-guard**: every assigned `GroupID` must have a matching `AddGroup` entry,
or cobra panics at execution. The `make test` run (which executes help via
testscript) will surface any miss immediately.

### Test — new `internal/cli/testdata/script/root_help.txtar`

Hermetic (`env PATH=$CREANCE_BIN`), asserts on `agent-creance --help`:
- a distinctive line from the root `Long:` (e.g. the happy-path sentence);
- each group heading: `stdout 'Setup Commands:'`, `'Daily Commands:'`,
  `'Inspect Commands:'`, `'Maintenance Commands:'`;
- that `completion` and `help` appear (under Maintenance) and **not** under an
  `Additional Commands:` heading: `! stdout 'Additional Commands:'`.

Follow the `setup_help.txtar` template (leading `#` comment explaining intent).

### Phase 1 success criteria

Automated:
- [ ] `go build ./...` passes.
- [ ] `make test` passes (including the new `root_help.txtar` and the existing 6
      help `.txtar` files).
- [ ] `make lint` passes.

Manual:
- [ ] `bin/agent-creance --help` shows the four group sections in
      Setup→Daily→Inspect→Maintenance order with `completion`/`help` under
      Maintenance, and the root `Long:` orientation block above the list.

---

## Phase 2 — `Long:`+`Example:` on the five priority commands

The ticket's named minimum, done fully first so the highest-value help lands
early: `run`, `setup`, `init`, `allow`, `doctor`.

### Changes (each in its factory's `cobra.Command` literal)

Add `Long:` (`\n`-joined fragments) and `Example:` to:
- `run.go:52-59` — Long: starts the cage + agent; requires `setup` + `init`;
  refuses early with a pointer if not; progress on stderr, agent owns stdout.
  Example: `agent-creance run`.
- `setup.go:27-41` — Long: once per machine; trust mitmproxy CA, install skill,
  scaffold global baseline (never touches an existing file). Example: `setup`,
  `setup --no-skill`, `setup --no-ca-install`, `setup --no-global-config`.
- `init.go:30-37` — Long: once per project; writes `.agent-creance.yaml`, scans
  manifests, seeds generator entries + git-remote allow rules; interactive init
  chains setup. Example: `init`, `init --git-push`.
- `allow.go:18-25` — Long: append a soft-allow rule and recompile; `--once` is
  session-scoped, `--global` writes the global config. Example:
  `allow https://example.com`, `allow --once <URL>`, `allow --global <URL>`.
- `doctor.go:23-30` — Long: diagnose Safehouse install, CA trust (live
  curl-verify), prerequisite versions (patch-level skew), orphan proxies,
  exposed host services; `--fix` auto-fixes what it can. Example: `doctor`,
  `doctor --fix`.

Source wording: research §4 (design.md:406-479). Keep each `Long:` to ~2-4
sentences; do not contradict the code.

### Tests

- Extend existing `.txtar` where present, else add assertions:
  - `setup_help.txtar` — add `stdout 'Examples:'` and a Long-line assertion.
  - `init.txtar` — add `stdout 'Examples:'` to the `init --help` block.
  - `allow_deny.txtar` — add `stdout 'Examples:'` to the `allow --help` block.
  - `run` has no help `.txtar` (only `run_missing_prereq.txtar`); add a
    `run --help` block to a new `run_help.txtar` (or fold into an existing run
    file) asserting `stdout 'Examples:'` + a Long line. **Do not** invoke bare
    `run` (it would exec real tools) — only `run --help`.
  - `doctor` — add a `doctor --help` assertion (`stdout 'Examples:'`) to one of
    the doctor `.txtar` files or a new `doctor_help.txtar`.

### Phase 2 success criteria

Automated:
- [ ] `go build ./...`, `make test`, `make lint` pass.

Manual:
- [ ] `bin/agent-creance run --help` / `setup --help` / `init --help` /
      `allow --help` / `doctor --help` each show a multi-sentence description and
      an `Examples:` section with real invocations.

---

## Phase 3 — `Long:`+`Example:` on the remaining commands

Complete the surface per Decision 1.

### Leaf top-level commands

Add `Long:`+`Example:` to:
- `deny.go:16-23` — Long: append a `deny_always` rule (+ `--reason`) and
  recompile. Example: `deny https://example.com`.
- `logs.go:23-52` — Long: read the egress audit log; dump / `--summary` /
  `--follow` (rotation-aware tail). Example: `logs`, `logs --summary`,
  `logs --follow`.
- `import.go:30-37` — Long: merge a YAML fragment after strict validation +
  review; `--yes` skips the prompt. Example: `import fragment.yaml`,
  `import --yes fragment.yaml`.
- `status.go:19-26` — Long: list running cages across all projects. Example:
  `status`.
- `clean.go:19-26` — Long: tear down this project's proxy, lock, and session
  overlay. Example: `clean`.
- `version.go:12-28` — Long: print the agent-creance version + the external tool
  versions it was tested against. Example: `version`.
- `include.go:20-31` — already has `Long:`; add `Example:`
  (`include ../shared.yaml`).

### Nested subcommands

Add `Long:`+`Example:` to:
- `policy show` (`policy.go:68`) — dump resolved policy with rule sources;
  `policy show --json`. `policy explain` (`policy.go:101`) — which rule decides a
  URL; `policy explain https://example.com`. `policy refresh` (`policy.go:34`) —
  force generator-metadata re-fetch + recompile.
- `domain add`/`remove` (`domain.go:47/69`), `service add`/`remove`
  (`service.go:31/43`), `mount add`/`remove` (`mount.go:30/44`) — short Long +
  one Example each, lifted from their `Short:` + flag semantics.

### Parent commands (Long only, no Example)

- `policy.go:19-22`, `domain.go:37-40`, `service.go:22-25`, `mount.go:20-23` —
  one or two sentences framing the subcommands.

### Tests

- Add `stdout 'Examples:'` assertions to the existing `include.txtar`,
  `import.txtar`, and `domain.txtar` help blocks.
- Optionally a small `inspect_help.txtar` asserting `Examples:` on
  `logs --help` and `status --help`. Keep additions hermetic.

### Phase 3 success criteria

Automated:
- [ ] `go build ./...`, `make test`, `make lint` pass.

Manual:
- [ ] Spot-check `bin/agent-creance logs --help`, `policy show --help`,
      `domain add --help` — each shows a description and an `Examples:` section.

---

## Phase 4 — Final verification & wrap-up

1. Run the full suite from `profile.md`:
   - [ ] `make test` (race; includes all `.txtar`).
   - [ ] `make lint` (`go vet` + golangci-lint).
   - [ ] `go build ./...`.
   - [ ] `make build` so `bin/agent-creance` reflects the final commit.
2. Manual acceptance pass against the ticket's criteria:
   - [ ] root `--help` has the happy-path `Long:` and the four grouped sections.
   - [ ] `run`/`setup`/`init`/`allow`/`doctor` (and the rest) have `Long:`+
         `Example:`.
   - [ ] groups via `AddGroup`/`GroupID`; `completion`/`help` under Maintenance,
         not interleaved.
   - [ ] help text matches behavior (no contradicted claims).
   - [ ] help output asserted by testscript and stable.
3. Set ticket `**Status:** Done`, bump `**Updated:**`, append a dated note under
   `## Notes & Updates`; commit the ticket change with the final phase.

---

## Testing strategy

- **CLI help behavior** → hermetic testscript `.txtar` (the project's discipline
  for CLI output). New `root_help.txtar` + per-command `Examples:`/`Long:`
  assertions folded into existing files. Use `env PATH=$CREANCE_BIN`; never
  invoke bare `run`/`setup` (they exec real tools) — only `<cmd> --help`.
- **No golden files** for help: the surface is large and prose-heavy; asserting
  on stable anchor strings (group titles, `Examples:`, one distinctive Long
  line) is less brittle than a full-output golden and matches how the existing
  help `.txtar` files already work. (If the reviewer prefers a golden, a single
  `--help` golden could be added, but it would churn on every wording tweak.)
- Existing flag-string assertions in the 6 help `.txtar` files must keep passing
  unchanged.

## Commit plan

One commit per phase, all `feat(AC-0064): …` (help text + groups are user-facing
behavior, not docs-under-thoughts):
- Phase 1: `feat(AC-0064): group commands and add root help overview`
- Phase 2: `feat(AC-0064): add Long/Example help to run, setup, init, allow, doctor`
- Phase 3: `feat(AC-0064): add Long/Example help to remaining commands`
- Phase 4 folds the ticket-status change into the final phase commit (or a
  trailing `chore`).

## References

- Research: `thoughts/shared/research/2026-06-28-AC-0064-help-as-doc-surface.md`
- Ticket: `thoughts/shared/tickets/AC-0064-help-as-doc-surface.md`
- `internal/cli/cli.go:88-135`, `internal/cli/include.go:20-31`
- `docs/design.md:406-479`
