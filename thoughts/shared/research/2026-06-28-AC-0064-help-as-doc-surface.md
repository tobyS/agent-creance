---
date: 2026-06-28
ticket: AC-0064
title: "Research — make --help a real doc surface (Long/Example, root overview, command groups)"
status: complete
git_commit: 8cd1c231b756e0c677380a22b3067d549e54539d
branch: main
---

# Research: AC-0064 — make `--help` a real doc surface

## Research question

For the agent-creance CLI, `--help` is the documentation surface its audience
(shell engineers) actually reads, but it is nearly empty. The ticket asks to:
add a root `Long:` with the happy-path sequence; add `Long:`+`Example:` to at
least `run`, `setup`, `init`, `allow`, `doctor`; organize commands into labelled
groups via cobra command groups; place the auto-generated `completion`/`help`
sensibly; keep help accurate to behavior; and add/refresh test coverage so help
output stays stable. This research establishes the exact cobra wiring, the
source material for accurate help text, the group taxonomy, and what test
coverage exists or is missing.

## Summary

- The cobra tree is assembled by `newRootCmd(app *App)` in
  `internal/cli/cli.go:88`, which registers **16** top-level subcommands (the
  ticket/audit say 13 — that count is stale; `domain`, `service`, `mount` were
  added since). Every command uses a `newXxxCmd(app *App) *cobra.Command` factory
  that inlines a `cobra.Command{...}` literal. **No** command groups, custom
  help/usage templates, or `Example:` exist anywhere; **only `include` has a
  `Long:`** (`internal/cli/include.go:23-26`), which is the style reference.
- cobra is **v1.10.2** (go.mod), so the full grouping API
  (`cobra.Group{ID,Title}`, `AddGroup`, `Command.GroupID`,
  `SetHelpCommandGroupID`, `SetCompletionCommandGroupID`) is available. Note the
  panic hazard: a `GroupID` with no matching `AddGroup` panics at execution time
  via `checkCommandGroups()`.
- Test coverage of help output is **thin and net-new territory**: 6 subcommand
  `.txtar` files assert on individual flag strings in `<cmd> --help`, but
  **nothing** asserts on root help, the command list, group headings, `Long:`,
  or `Examples:`. There are **no help/usage golden files** — only init-scaffold
  goldens. Adding stable assertions on the new surface is mostly additive.
- Accurate source material for `Long:`/`Example:` lives in `docs/design.md:406-479`
  (command reference + preconditions) and the existing `Short:` strings. The
  README (`README.md`, recently rewritten under AC-0065) already states the
  canonical happy-path wording — root `Long:` should stay consistent with it.

## Detailed findings

### 1. Cobra command tree assembly (`internal/cli/`)

Source: codebase-analyzer over `internal/cli/`.

- **Root** — `cli.go:90-112`. Fields set: `Use: "agent-creance"`,
  `Short: "Run a coding agent inside an isolated, egress-filtered cage"`,
  `SilenceUsage: true`, `SilenceErrors: true`, `PersistentPreRunE` (resolves
  `--color` per stream). **No `Long`, no `Example`, no `Version`, no `RunE`.**
  Output wiring + `--color` persistent flag at `cli.go:114-117`.
- **Registration** — 16 sequential `root.AddCommand(...)` calls at
  `cli.go:119-134`, in this (non-alphabetical) order: `init, version, doctor,
  policy, logs, run, setup, allow, deny, domain, service, mount, include,
  import, status, clean`. cobra then alphabetizes them in help output. Plus
  cobra's auto-added `completion` and `help`.
- **Factory pattern** — every command is `func newXxxCmd(app *App) *cobra.Command`
  that builds the literal inline, attaches flags via `cmd.Flags()`, and wires
  `RunE` to a separate testable `runXxx(...)` body. `version`/`include` return
  `&cobra.Command{...}` directly. The four parent commands (`policy`, `domain`,
  `service`, `mount`) build a `Use`+`Short` literal and call `cmd.AddCommand(...)`.
- **No groups / no templates** — package-wide grep for `AddGroup`, `GroupID`,
  `cobra.Group`, `SetHelpTemplate`, `SetUsageTemplate`, `SetHelpFunc`,
  `SetUsageFunc` returns only the one `Long` at `include.go:23`.

Per-command struct literals (file:line of the literal; all set `Short`, most set
`Args`):

| Command | Literal | Long? | Example? |
|---|---|---|---|
| init | `init.go:30-37` | no | no |
| version | `version.go:12-28` | no | no |
| doctor | `doctor.go:23-30` | no | no |
| policy (parent) | `policy.go:19-22` | no | no |
| logs | `logs.go:23-52` | no | no |
| run | `run.go:52-59` | no | no |
| setup | `setup.go:27-41` | no | no |
| allow | `allow.go:18-25` | no | no |
| deny | `deny.go:16-23` | no | no |
| domain (parent) | `domain.go:37-40` | no | no |
| service (parent) | `service.go:22-25` | no | no |
| mount (parent) | `mount.go:20-23` | no | no |
| include | `include.go:20-31` | **yes** (23-26) | no |
| import | `import.go:30-37` | no | no |
| status | `status.go:19-26` | no | no |
| clean | `clean.go:19-26` | no | no |

Nested subcommands (on their parents): `policy show/explain/refresh`
(`policy.go:68/101/34`), `domain add/remove` (`domain.go:47/69`), `service
add/remove` (`service.go:31/43`), `mount add/remove` (`mount.go:30/44`).

**The style reference** — `include.go:23-26`, verbatim (hard-wrapped prose,
`\n`-joined string fragments, not a backtick raw string):

```go
Long: "Append PATH to the project config's include: list (preserving comments and\n" +
    "formatting) and recompile the policy. PATH may be relative to the project\n" +
    "config's directory, absolute, or ~/-relative. The target is validated before\n" +
    "the entry is written, so a missing or unparseable include is reported up front.",
```

### 2. Cobra grouping API (v1.10.2 — confirmed available)

Source: web-search-researcher (pkg.go.dev / command.go) + `go.mod` (`cobra v1.10.2`).

```go
type Group struct {
    ID    string // referenced by a subcommand's GroupID
    Title string // printed verbatim as the heading — include your own trailing ":"
}

func (c *Command) AddGroup(groups ...*Group)              // append-only, no validation; takes *Group
// on cobra.Command:
GroupID string                                            // tag a subcommand into a group
func (c *Command) SetHelpCommandGroupID(groupID string)   // call on root
func (c *Command) SetCompletionCommandGroupID(groupID string) // call on root
func (c *Command) ContainsGroup(groupID string) bool
```

Behavior:
- **No groups defined** → commands print under `Available Commands:` (current
  state).
- **Groups defined** → each group prints under its `Title` in `AddGroup`
  definition order (not alphabetical). Any command with an empty `GroupID`
  (including `help`/`completion` unless assigned) collects under
  **`Additional Commands:`**.
- **Panic hazard**: a non-empty `GroupID` with no matching `AddGroup` panics at
  execution via `checkCommandGroups()`:
  `panic("group id '%s' is not defined for subcommand '%s'")`. Every `GroupID`
  must have a registered group.
- `Long` renders as the prose block at the top of `<cmd> --help` (falls back to
  `Short` if empty). `Example` renders under an `Examples:` section. Default
  template order: `Usage:` → `Aliases:` → `Examples:` → command listing (grouped
  `Title` blocks + `Additional Commands:`) → `Flags:` → `Global Flags:` → footer.
- Identifier spelling caution: the real exported names are
  `SetHelpCommandGroupID` / `SetCompletionCommandGroupID` (capital `ID`); the
  user guide's `...GroupId()` spelling is wrong.

Code sketch to write against:

```go
const groupSetup = "setup"
rootCmd.AddGroup(&cobra.Group{ID: groupSetup, Title: "Setup Commands:"})
setupCmd.GroupID = groupSetup
// ...
rootCmd.SetHelpCommandGroupID(groupMaint)        // or wherever they belong
rootCmd.SetCompletionCommandGroupID(groupMaint)
```

### 3. Group taxonomy (answers a "Questions for Research/Planning" item)

S4's own wording recommends a **setup / daily / inspect / maintenance**
hierarchy and to signpost `setup`+`init`+`run` as "the path." Mapping the real
16 commands:

- **setup / onboarding**: `setup`, `init`
- **daily use**: `run`, `allow`, `deny`, `domain`, `import`, `include`, `mount`,
  `service`
- **inspect**: `doctor`, `status`, `logs`, `policy`, `version`
- **maintenance**: `clean` (and a sensible home for `completion`/`help`)

This is the recommended default; the plan/checkpoint can adjust bucket
membership (e.g. whether `version` is "inspect" or "maintenance", whether
`run` should be visually first). This taxonomy is a proposal, not a constraint.

### 4. Source material for accurate `Long:`/`Example:` text

Source: thoughts-analyzer over `docs/design.md` and the UX audit, plus the
existing `Short:` strings. Use these so help text matches behavior (AC criterion:
"no claims contradicted by the code").

Preconditions / happy path (load-bearing wording):
- Sequence is **setup → init → run**.
- `run` precondition (design.md:447-450): "if setup hasn't been run yet (no
  trusted CA, no skill), prints a clear pointer to `agent-creance setup` and
  exits non-zero rather than failing with a stack trace." Reports startup
  progress on **stderr** (stdout belongs to the agent).
- Prereq gating (design.md:374): the prerequisite check runs only on `run` and
  `doctor` (the commands that exec the tools); config-write/inspection commands
  don't gate.
- `setup` is **once per machine** (design.md:409-420): trust mitmproxy CA into
  keychain, install skill into `~/.claude/skills/`, scaffold
  `~/.config/agent-creance.yaml` baseline if none exists (never touches an
  existing file). Flags: `--no-skill`, `--no-ca-install`, `--no-global-config`.
- `init` is **once per project** (design.md:422-446): writes
  `.agent-creance.yaml`, scans for manifests (package.json/composer.json up to 2
  levels, skipping node_modules/vendor), pre-populates `generators:` entries,
  writes static allow entries for the project's git remotes. Flag: `--git-push`
  (grant push access; default read-only). Interactive `init` chains `setup`
  inline, so a user can start at `init`.
- `allow URL` (design.md:452-454): append a soft-allow rule. Flags: `--once`
  (session-scoped), `--global` (write to `~/.config/agent-creance.yaml`).
- `doctor` (design.md:465-468): check Safehouse install, CA trust (live
  curl-verify), prerequisite versions (patch-level skew), orphan proxies,
  exposed host services. Flag: `--fix`.

Ready-to-lift `Example:` invocations:
- `run`: `agent-creance run`
- `setup`: `agent-creance setup`, `... --no-skill`, `... --no-ca-install`,
  `... --no-global-config`
- `init`: `agent-creance init`, `agent-creance init --git-push`
- `allow`: `agent-creance allow https://example.com`, `... --once URL`,
  `... --global URL`
- `doctor`: `agent-creance doctor`, `agent-creance doctor --fix`

Existing `Short:` strings (the voice to match) are catalogued in the agent
output; key ones: `run.go:54`, `setup.go:29`, `init.go:32`, `doctor.go:25`,
`allow.go:20`.

The README (`README.md` Quickstart, post-AC-0065) already states the canonical
phrasing: "`setup` is a one-time, per-machine step; `init` is one-time per
project. If you skip them, `run` won't fail with a stack trace — it refuses
early with a pointer to whichever command you still need." The root `Long:`
should mirror this wording so help and README don't drift.

### 5. Test / golden coverage to update

Source: codebase-locator over `internal/cli/testdata/` and `*_test.go`.

- **6 subcommand-help `.txtar` files** (under
  `internal/cli/testdata/script/`) invoke `<cmd> --help` and assert on
  individual flag/arg strings only:
  - `setup_help.txtar` — `--no-skill`, `--no-ca-install`
  - `init.txtar` — `--force`, `--no-setup`
  - `include.txtar` — `include PATH`
  - `allow_deny.txtar` — `--once`, `--global`
  - `domain.txtar` — `--all-paths`, `--method`, `--mode`, `--deny`
  - `import.txtar` — `--yes`

  These break only if those specific flags/args are renamed/removed — adding
  `Long:`/`Example:` won't break them.
- **No golden files capture help/usage.** Only init-scaffold goldens exist
  (`testdata/init/*.golden`). The grep for `Usage:` / `Available Commands:`
  outside `.txtar` hit only source comments.
- **No Go `*_test.go` asserts on help, root command, or command registration.**
- Harness: `internal/cli/script_test.go`; testscript conventions = leading `#`
  comment, `agent-creance <cmd>` invocations with `stdout`/`stderr` regex
  matchers, optional `-- file --` archive sections. `setup_help.txtar` is a good
  template: it sets `env PATH=$CREANCE_BIN` so only the CLI (not host tools) is
  found, then asserts `stdout '...'`.

**Implication**: asserting the new doc surface (root `Long:` happy-path lines,
group headings like `Setup Commands:`, an `Examples:` block on a key command) is
mostly **net-new** assertions, not edits to existing ones. A new
`root_help.txtar` (assert root `Long:` text + group headings + that
`completion`/`help` are placed sensibly) plus `Examples:`/`Long:` assertions
folded into the existing per-command `.txtar` files is the natural shape.

## Code references

- `internal/cli/cli.go:88-135` — root command (no `Long:`), 16-command flat
  registration; where `AddGroup`/`GroupID`/`Set*GroupID` go.
- `internal/cli/include.go:20-31` — the only existing `Long:` (style reference).
- `internal/cli/run.go:52-59`, `setup.go:27-41`, `init.go:30-37`,
  `allow.go:18-25`, `doctor.go:23-30` — the five priority commands' literals.
- `internal/cli/{deny,logs,import,status,clean,version,policy,domain,service,mount}.go`
  — remaining command literals.
- `internal/cli/script_test.go` — testscript harness.
- `internal/cli/testdata/script/{setup_help,init,include,allow_deny,domain,import}.txtar`
  — existing help assertions.
- `docs/design.md:406-479` — command reference + preconditions (Long/Example
  source).
- `thoughts/shared/research/2026-06-25-ux-audit.md:140-161` — finding S4;
  onboarding cluster S1/S2/S3/S5/S8.
- `README.md` — Quickstart wording to stay consistent with.

## Open questions / decisions for the checkpoint

From the ticket's "Questions for Research/Planning":

1. **Group taxonomy & membership** — research recommends setup / daily / inspect
   / maintenance (mapping in §3). Confirm the buckets and any reassignments,
   plus where `completion`/`help` land (recommend: maintenance, via
   `Set*CommandGroupID`).
2. **Scope: five named commands vs. all 16** — the ticket's minimum is `run`,
   `setup`, `init`, `allow`, `doctor`. Confirm whether to (a) do only those five,
   (b) add `Long:`/`Example:` to all leaf commands now, or (c) do the five fully
   + `Example:` on the rest opportunistically. Grouping must cover all 16
   regardless (every command needs a `GroupID` or it falls to "Additional
   Commands:").
3. **Where help strings live** — research found the established convention is
   inline `cobra.Command` literals with `\n`-joined Go string fragments
   (`include.go` is the only precedent; no extracted-constants or embedded-file
   pattern exists for help, unlike the cage briefing). Recommend continuing
   inline to match. Confirm, or opt for extracted constants if reviewability is
   a concern.

No blocking unknowns; the API is confirmed available and the source material is
complete.
