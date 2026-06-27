---
date: 2026-06-27
ticket: AC-0067
topic: "config-editing commands (domain/service/mount) with interactive TUI fallback"
status: draft
research: thoughts/shared/research/2026-06-27-AC-0067-config-editing-commands-tui.md
git_commit: 8930710
branch: main
repository: github.com/tobyS/agent-creance
---

# Implementation Plan: AC-0067 — config-editing commands with interactive TUI fallback

## Overview

Add three noun-verb command groups — `domain` (egress allow/deny), `service`
(host-port binds), `mount` (filesystem mounts) — each with `add`/`remove`
subcommands, each fully specifiable via flags, each falling back to an
interactive prompt for any omitted choice, and each preserving the YAML file's
comments and formatting. The work layers over the existing edit pipeline
(`applyAndRecompile` + `withConfigLock` + atomic write + recompile) and adds the
two capabilities the codebase lacks today: a **richer domain-rule flag surface**
and **removal** of config entries (the editor is append-only). `allow`/`deny` are
kept as aliases that route into the shared `domain add` implementation.

## Current state

- **Edit pipeline (reusable as-is for adds).** `allow`/`deny`/`include`/`import`
  funnel through `applyAndRecompile` (`internal/cli/mutate.go:97-133`):
  `read → apply(closure) → atomic-write → recompile` inside `withConfigLock`. The
  `apply` closure is `func(src []byte) (out []byte, changed bool, err error)`. Any
  new add/remove of that shape inherits locking, atomic write, the hot-reload
  recompile, and "already present; nothing to do" no-op handling.
- **Editor is append-only, text-splice based.** `internal/config` exports only
  `AppendRule` (`edit.go:52`), `AppendHostService` (`edit_hostservice.go:32`),
  `AppendInclude` (`edit_include.go:23`). They parse to a `yaml.Node` tree only for
  line positions, then splice rendered text into the raw `[]byte`; comments survive
  because the tree is never re-marshalled. `renderRuleItem` (`edit.go:177-193`)
  already renders `paths`/`methods` (flow style) / `mode` / `reason`, so the config
  layer can already express a full rule — but the CLI builder `ruleFromURL`
  (`mutate.go:46-57`) only ever sets a single path and never methods/mode (comment
  at `:44-45`: "v0.1 has no --method flag"). There is **no removal of anything** and
  **no append/render for `safehouse.add_dirs_*`** (`import` ignores `safehouse:`).
- **Schema** (`internal/config/config.go`): `Rule{Host; Paths *[]string;
  Methods *[]string; Mode; Reason}` (`:89-95`, pointer slices so omitted ≠ empty);
  `HostService{Label; Port}` (`:60-63`, keyed by port); `Safehouse{AddDirsRW,
  AddDirsRO, Enable []string}` (`:45-49`). Mode constants `ModeIntercept` (default) /
  `ModePassthrough` (`:98-101`). Passthrough+paths/methods is rejected at compile
  time (`validate.go:37-42`).
- **Interactive seam exists, unused beyond `init`.** `confirm()` /`readLine()`
  (`init.go:207-240`) read `App.Stdin` / write `App.Stdout`; TTY-gated at call sites
  by `App.Terminal.IsInteractive()` with a non-interactive "fail with hint" branch
  (`import.go:82-86`, `init.go:173-179`). Faked by `sysdeptest.FakeTerminal` +
  `strings.NewReader`/`bytes.Buffer`. No selection/free-text prompt helper exists yet.
- **Command idiom**: noun-verb parent with no `RunE` + child `newXxxCmd`
  constructors is `policy` (`internal/cli/policy.go:18-25`); registered via
  `root.AddCommand(...)` in `cli.go:119-131`.
- **Live-cage probe**: `proxy.Manager.Inspect(layout)` (`internal/proxy/lifecycle.go:286-313`)
  → `Diagnosis.ProxyUp`; built the way `status.go:30-42` does.
- **Design basis**: `host_services` + mounts compile into the **frozen Seatbelt
  profile at launch** (`docs/design.md` "Config compilation" / "Crash recovery"), so
  they cannot affect a running cage; `allow`/`deny_always` → `policy.json`,
  hot-reloaded via mtime poll.

## Desired end state

A user can maintain every security-relevant part of `.agent-creance.yaml` via
commands, never recalling the YAML schema:

```
agent-creance domain add api.github.com --path /repos/ --method GET
agent-creance domain add react.dev --all-paths
agent-creance domain add w3schools.com --deny --reason "Low-quality source"
agent-creance domain remove api.github.com                 # whole rule
agent-creance domain remove api.github.com --path /repos/  # one path (drops rule if last)
agent-creance service add mysql:3306
agent-creance service remove 3306
agent-creance mount add ./data --rw
agent-creance mount remove ./data
```

Omitted choices prompt interactively; a non-TTY with a missing choice fails with a
flag-naming hint and never hangs. Removal preserves comments/formatting. `domain`/deny
edits hot-reload; `service`/`mount` edits write-and-warn when a cage is live. `allow`/
`deny` continue to work.

### Decisions locked at the planning checkpoint

1. **Last-path removal → drop the whole rule.** `domain remove HOST --path P` where
   `P` is the rule's last path deletes the entire rule. (An empty `paths: []` is not
   neutral — it would widen the rule to host-wide intercept.)
2. **Removing a non-existent entry → error, exit non-zero.** `domain`/`service`/
   `mount` `remove` of an absent entry prints a clear error and returns a non-nil
   error (the centralized `cli.Main` exit path prints `error: …`, exit 1). This is
   *stricter* than the add path's no-op; deliberate per the user's choice.
3. **`mount remove PATH` present in both lists → remove from both.** Fully detach.
4. **`allow`/`deny` are aliases routing into `domain add`.** A single shared
   implementation backs all three. Note the cobra mechanics (below): a top-level
   `allow` cannot use cobra's `Aliases` field to point at the *nested* `domain add`
   subcommand, so the aliases are realized as **thin top-level commands whose `RunE`
   delegates to the same `runDomainAdd` body** (`deny` presets `--deny`; `allow`
   additionally exposes `--once`, which `domain` does not). This honors the
   "unified surface / single implementation" intent within cobra's constraints, and
   they continue to print `allow`/`deny`-style result lines.

## What we're NOT doing

- No editing of `env:` / `include:` (has its own command) / `generators:` / `bundle:` /
  `plugins:` via these commands.
- No change to the security model, the config cage, or the in-cage write deny
  (`config-ro.sb`).
- No live profile updates for a running cage — `service`/`mount` stay frozen at
  launch; "write + warn" is the accepted limitation.
- No `--once` on `domain` — session-overlay stays on the `allow` alias only.
- No new third-party TUI dependency (hand-rolled prompt over the existing seam).
- No leading-comment re-attribution on removal — remove only the element's own lines
  (conservative, golden-stable).

## Implementation phases

The five phases are independently committable and testable; each leaves the tree
green. Phases 1–2 are config-layer (pure, golden-tested); 3–5 are command-layer.

---

### Phase 1: Config-layer add for `safehouse.add_dirs_*`

The richer **domain rule** add needs no config-layer change — `renderRuleItem`
already renders paths/methods/mode/reason; only the CLI builder changes (Phase 3).
The one missing add is the safehouse mount append.

#### Changes
- **`internal/config/edit_safehouse.go`** (new): `AppendDir(src []byte, dir string,
  rw bool) (out []byte, changed bool, err error)`, mirroring `AppendHostService`
  (`edit_hostservice.go`). It targets `safehouse.add_dirs_rw` or `add_dirs_ro`,
  synthesizes the missing `safehouse:`/`add_dirs_*:` keys when absent (reuse the
  `planInsertHostService` shape: `mappingChild`, `endOfRegion`, `renderNested`,
  `leadingSpaces`), and renders a `- <dir>` item via a `scalar`-quoted path. Add a
  `containsDir(dirs []string, dir string) bool` dedup check and a `validateAppendDir`
  gate that re-parses and asserts only the target list changed. Decide path
  normalization: store the dir **as given** (matching how `add_dirs_rw: [.]` appears
  in the design) — do not abs/clean, so `./data` and `~/x` round-trip verbatim.
- **`internal/config/config.go`**: no struct change (fields exist). Confirm
  `applyDefaults` leaves `add_dirs_*` as plain `[]string` (it does).

#### Tests
- `internal/config/edit_safehouse_test.go`: table + golden, mirroring
  `edit_hostservice_test.go`. New `testdata/edit/` `.in.yaml`/`.golden.yaml` pairs:
  append to an existing `add_dirs_rw`, create `safehouse:` from scratch, create
  `add_dirs_ro` when only `add_dirs_rw` exists, dedup no-op, comment preservation.
  Regenerate via `make golden`; review the diff.

#### Success criteria
**Automated:** `make test` green; `go build ./...` clean; `make lint` clean;
`make golden` produces only the intended new fixtures.
**Manual:** none (pure logic).

---

### Phase 2: Removal infrastructure in `internal/config`

The one genuinely new primitive: bounding an element's **end** line (inserts only
needed a start). Build it once and reuse for all four removal granularities.

#### Changes
- **`internal/config/remove.go`** (new):
  - `endOfItem(lines []string, anchors []anchor, itemLine, itemIndent int) int` — the
    end-bounding primitive: walk anchors to the first one at indent `<= itemIndent`
    after `itemLine` (or EOF), then `backOverBlanks` so trailing blanks aren't
    swallowed. Adapted from `endOfRegion` (`edit.go:136-148`); reuse `collectAnchors`.
  - `RemoveRule(src []byte, list RuleList, host string, path string) (out []byte,
    changed bool, err error)` — when `path == ""`, remove the whole matching rule
    (match by host + identity via `ruleIdentity`); splice `[itemLine, endOfItem)` out.
    When `path != ""`, locate the rule, and:
      - if the rule has multiple paths, re-render just the `paths:` line with `P`
        removed (reuse `flowSeq`); if `P` was the rule's **last** path, remove the
        **whole rule** (locked decision 1).
      - **not found** (host absent, or path absent from the rule) → return
        `changed=false` with a sentinel error `ErrNotFound` (new in
        `internal/config/errors.go`) so the CLI can turn it into a non-zero exit
        (locked decision 2). NB: this differs from append's silent no-op — removal
        signals absence explicitly.
  - `RemoveHostService(src []byte, port int) (...)` — match by port
    (reuse `containsPort` logic / `parseHostService`); single-line splice; `ErrNotFound`
    when absent.
  - `RemoveDir(src []byte, dir string) (out []byte, removed []bool/*rw,ro*/, changed
    bool, err error)` — remove `dir` from `add_dirs_rw` **and** `add_dirs_ro` (locked
    decision 3); report which list(s) it was in so the command can word its message;
    `ErrNotFound` when in neither.
  - A shared `validateRemove`-style gate per function: re-parse the candidate and
    assert exactly the targeted entry vanished and nothing else changed (mirror
    `validateAppend`, `edit.go:232-257`).
- **Comment policy**: remove only the element's own lines (its `-` line through its
  last continuation line). Do not reattach or delete neighboring `#` comments/blank
  lines. Document this in a function comment.

#### Tests
- `internal/config/remove_test.go`: table + golden. Fixtures under `testdata/remove/`
  (new dir): whole-rule removal (allow + deny_always), single-path from a multi-path
  rule, single-path that is the last path (→ whole rule gone), host-service by port,
  mount in rw only / ro only / both, not-found cases (assert `ErrNotFound`), and
  **comment-preservation** cases (a `#` comment above and below the removed item must
  survive verbatim). Include a flow-style `paths:` re-render golden.

#### Success criteria
**Automated:** `make test` green (new table + golden tests); `go build ./...` clean;
`make lint` clean; `make golden` diff reviewed.
**Manual:** none (pure logic).

---

### Phase 3: `domain` command group + `allow`/`deny` aliases (flags only)

Delivers the full non-interactive flag surface; interactivity is Phase 4.

#### Changes
- **`internal/cli/domain.go`** (new):
  - `newDomainCmd(app)` — parent, no `RunE`, `AddCommand(newDomainAddCmd, newDomainRemoveCmd)`.
  - `newDomainAddCmd(app)` — `Use: "domain add HOST"`, `Args: ExactArgs(1)`, flags:
    `--path` (StringArray, repeatable), `--method` (StringArray, repeatable),
    `--mode` (String, `intercept`|`passthrough`), `--all-paths` (Bool), `--deny`
    (Bool), `--reason` (String), `--global` (Bool). `RunE` → `runDomainAdd(...)`.
  - `runDomainAdd(ctx, app, dir, host, opts)`:
    - **Guards (early, before any edit):** `--all-paths` with `--path` → clear error;
      `--mode passthrough` with any `--path`/`--method` → clear error (the early,
      additive guard backing the compile-time check, `validate.go:37-42`); `--deny`
      with `--mode`/`--method` (deny rules carry only host/paths/reason) → error;
      method strings uppercased + validated against a known set; reason required-or-not
      for deny decided here (allow it optional).
    - Build a `config.Rule` from flags (new builder superseding `ruleFromURL` for this
      path): set `Host`; `Paths` from `--path` (nil when `--all-paths`); `Methods` from
      `--method`; `Mode` from `--mode` (omit when intercept); `Reason`.
    - Resolve target via `mutationTarget(app, dir, /*once*/ false, global)`; choose
      `config.AllowList` or `config.DenyList` from `--deny`.
    - `applyAndRecompile(...)` with `apply = config.AppendRule(src, list, rule)`.
  - `newDomainRemoveCmd(app)` — `Use: "domain remove HOST"`, `Args: ExactArgs(1)`,
    flags `--path` (single String — removal targets one path), `--global` (Bool).
    `RunE` → `runDomainRemove(...)` calling `applyAndRecompile` with
    `apply = config.RemoveRule(src, list, host, path)`; map `config.ErrNotFound` to a
    user-facing "not present in <file>" error (non-zero exit). For remove, the list is
    ambiguous (allow vs deny) — resolve by searching allow first then deny_always, or
    accept `--deny` to disambiguate; **decide in implementation**, defaulting to
    "remove from whichever list contains the host; error if in both" for safety.
- **`internal/cli/allow.go` / `deny.go`**: re-point at the shared body. `allow` keeps
  `--once`/`--global` and calls `runDomainAdd` with a single path parsed from the URL
  (preserving today's `allow URL` ergonomics) and `once` honored via `mutationTarget`.
  `deny` calls `runDomainAdd` with `--deny` preset and `--reason`. Keep their existing
  result wording. (Mechanics per locked decision 4 — thin delegating commands, not
  cobra `Aliases`.)
- **`internal/cli/cli.go`**: `root.AddCommand(newDomainCmd(app))`.

#### Tests
- `internal/cli/domain_test.go` (unit, `*App`+fakes): rule-builder mapping; each guard
  error; allow/deny alias delegation.
- `internal/cli/testdata/script/domain.txtar` (hermetic, non-TTY): `domain add` with
  `--path`/`--method`/`--mode`; `--all-paths`; `--all-paths`+`--path` error;
  passthrough+path error; `--deny --reason`; `--global` targets the global file;
  `domain remove` whole-rule and `--path`; `remove` of a missing entry → non-zero exit
  + message. Assert the config file content after each (testscript can `cmp`/`grep` the
  file). Reuse the `allow_deny.txtar` setup (stub `agent-safehouse`/`mitmproxy` on PATH
  so recompile path is reachable).
- Extend/keep `allow_deny.txtar` to prove the aliases still behave.

#### Success criteria
**Automated:** `make test` green; `go build ./...` clean; `make lint` clean.
**Manual:** `make build`, then `bin/agent-creance domain add example.com --path /a/
--method GET` in a scratch project writes the expected rule; `domain remove example.com`
removes it; `allow example.com/x` still works.

---

### Phase 4: Interactive prompt helper + `domain add` fallback

#### Changes
- **`internal/cli/prompt.go`** (new): a small helper over the existing seam, gated on
  `app.Terminal.IsInteractive()`:
  - `promptSelect(app, label string, options []string) (int, error)` — numbered
    single-select reading via `readLine(app.Stdin)`, writing to `app.Stdout`;
    re-prompts on invalid input; EOF → error.
  - `promptText(app, label string) (string, error)` — free-text line.
  - `promptRequireInteractive(app, hint string) error` — when `!IsInteractive()`,
    return an error whose message names the flags to supply (the "fail with hint, never
    hang" contract). Every prompt call site checks this first.
  - Reuse `confirm()` for yes/no rather than re-implementing.
- **`internal/cli/domain.go`**: in `runDomainAdd`, when a needed choice is omitted and
  the terminal is interactive, prompt for it; when omitted and non-interactive, return
  the flag-naming hint error. Specifically: neither `--path` nor `--all-paths` →
  prompt "allow all paths, or specific paths?" (collect paths if "specific"); `--method`
  omitted → prompt (allow "any"); `--mode` omitted → default `intercept` without a
  prompt (it has a sensible default; prompting every time is noise) **unless** the
  ticket's "methods and mode prompt the same way when omitted" is read strictly — if
  so, prompt for mode too. **Decide in implementation; default to: prompt for the
  paths decision and methods, default mode silently to intercept**, and note the choice.

#### Tests
- `internal/cli/domain_interactive_test.go` (unit): `FakeTerminal.Interactive = true`
  + `App.Stdin = strings.NewReader("…")` driving the paths/methods prompts; assert the
  resulting rule and the prompt text on the stdout buffer. Cover "specific paths" multi-
  line entry and "all paths".
- Extend `domain.txtar`: under testscript (always non-TTY) assert that `domain add HOST`
  with no path flags fails with the flag-naming hint and non-zero exit (never hangs).

#### Success criteria
**Automated:** `make test` green; `go build ./...` clean; `make lint` clean.
**Manual:** `bin/agent-creance domain add example.com` in a terminal prompts for the
paths decision and writes the chosen rule; the same piped from `/dev/null` fails with a
hint naming `--path`/`--all-paths`.

---

### Phase 5: `service` + `mount` commands with write-and-warn

#### Changes
- **`internal/cli/service.go`** (new): `newServiceCmd` parent + `add`/`remove`.
  - `service add LABEL:PORT` — parse `label:port` (reuse `config.parseHostService` via a
    thin exported wrapper or replicate validation); a missing label prompts (Phase 4
    helper); `apply = config.AppendHostService`. **No `--global`** (host_services are
    project-only per design). **Write + warn, no recompile** (see helper below).
  - `service remove PORT` — `Args` one port; `apply = config.RemoveHostService(port)`;
    `ErrNotFound` → non-zero. Write + warn.
- **`internal/cli/mount.go`** (new): `newMountCmd` parent + `add`/`remove`.
  - `mount add PATH --rw|--ro` — missing rw/ro prompts; `apply = config.AppendDir(src,
    path, rw)`. Write + warn.
  - `mount remove PATH` — `apply = config.RemoveDir(src, path)`; report which list(s) it
    was in; `ErrNotFound` → non-zero. Write + warn.
- **`internal/cli/mutate.go`** (or a new `applyAndWarn` helper): a sibling of
  `applyAndRecompile` that performs the same `withConfigLock` + read + apply +
  atomic-write, but **replaces the `recompile` step** with a live-cage probe and
  warning instead of a policy recompile. The probe: resolve the layout
  (`state.New(app.Paths).Resolve(dir)`), build `proxy.NewManager(app.FS, app.Flock,
  app.ProcessManager, app.PortAllocator, app.Sleeper, app.Stderr)`, call
  `mgr.Inspect(layout)`; if `diag.ProxyUp`, print a `⚠` warning to stdout/stderr:
  "written; takes effect on the next `agent-creance run` — the running cage is
  unchanged." If no live cage, just the normal `✓ … in <file>` line. (`App` already
  holds `ProcessManager`/`PortAllocator`/`Sleeper`.)
- **`internal/cli/cli.go`**: register `newServiceCmd`, `newMountCmd`.

#### Tests
- `internal/config` removal/append for host-service/dir already covered (Phases 1–2).
- `internal/cli/service_test.go` / `mount_test.go` (unit): flag paths, missing-label /
  missing-rw-ro prompt (interactive), `ErrNotFound` → error.
- `internal/cli/testdata/script/service.txtar`, `mount.txtar` (hermetic): add/remove
  via flags, file content assertions, non-TTY missing-choice hint, and the **live-cage
  warning** path using the `seedlock` testscript builtin (as `status_lists.txtar` does)
  to simulate a running proxy, asserting the warning text and that no recompile ran.

#### Success criteria
**Automated:** `make test` green; `go build ./...` clean; `make lint` clean;
`make golden` diff (if any new fixtures) reviewed.
**Manual:** `bin/agent-creance service add mysql:3306` then `service remove 3306`
round-trips in a scratch config; `mount add ./data --rw` / `mount remove ./data`
round-trips; with a cage running, a `service`/`mount` edit prints the "next run" warning.

---

## Testing strategy

- **Pure edit/removal logic** (`internal/config`): table-driven for branch logic +
  golden `.in.yaml`/`.golden.yaml` for rendered output, per the project convention.
  Comment-preservation is asserted by golden fixtures that carry comments around the
  edited entries.
- **Command behavior** (`internal/cli`): hermetic `testscript` `.txtar` for flag paths,
  `--global` targeting, the non-TTY "fail with hint" branch, removal-not-found exit
  codes, and the live-cage warning (via `seedlock`). testscript always runs non-TTY, so
  it covers exactly the non-interactive surface.
- **Interactive prompt flows**: `*App`+fakes unit tests (`FakeTerminal.Interactive =
  true` + preloaded `App.Stdin`), per the `init_test.go` pattern — the positive prompt
  path that testscript cannot reach.
- **Full sweep before done**: `make test`, `go build ./...`, `make lint`, `make golden`
  (review), and `make build` so `bin/agent-creance` reflects the final commit.

## Success criteria (whole ticket)

Maps to the ticket's Acceptance Criteria:

- **Command surface**: `domain`/`service`/`mount` groups with `add`/`remove`; `allow`/
  `deny` aliases still work. (Phases 3, 5.)
- **Domain rules**: `--path`/`--method`/`--mode`/`--all-paths`/`--deny --reason`/
  `--global`; `--all-paths`+`--path` error; passthrough+paths/methods early error;
  `domain remove` whole-rule and single-path (drop-rule on last path). (Phases 2, 3.)
- **Services/mounts**: `service add/remove` by port; `mount add --rw|--ro` / `remove`
  from whichever list (both if in both). (Phases 1, 2, 5.)
- **Interactive fallback**: omitted choice → prompt; non-TTY → flag-naming error, never
  hangs. (Phase 4, applied in 3 and 5.)
- **Edit semantics**: add + remove preserve comments/formatting; remove-missing →
  explicit error; all edits via `withConfigLock` + atomic write. (Phases 1, 2.)
- **Live-session**: `domain`/deny recompile + hot-reload; `service`/`mount` write +
  warn when a cage is live. (Phases 3, 5.)
- **Testing**: table + golden for logic; hermetic testscript for command behavior; the
  prompt uses the injected terminal seam and never touches the real OS in unit tests.

## Open implementation-time decisions (small, non-blocking)

- `domain remove` list disambiguation (allow vs deny_always) — default "whichever
  contains the host; error if in both"; consider a `--deny` selector.
- Whether `--mode` omitted prompts or silently defaults to `intercept` — default to
  silent `intercept` (sensible default; avoid prompt noise); revisit if it reads as
  violating "mode prompts the same way when omitted."
- Path storage normalization for mounts — store verbatim (no abs/clean) so it
  round-trips and golden tests stay stable.
