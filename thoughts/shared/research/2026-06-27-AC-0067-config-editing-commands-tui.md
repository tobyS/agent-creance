---
date: 2026-06-27
ticket: AC-0067
topic: "config-editing commands (domain/service/mount) with interactive TUI fallback"
status: complete
git_commit: 6481147a53cac5191eeda6e61986f28a9e95ecec
branch: main
repository: github.com/tobyS/agent-creance
---

# Research: AC-0067 — config-editing commands with interactive TUI fallback

**Ticket:** `thoughts/shared/tickets/AC-0067-config-editing-commands-tui.md`
**Related:** AC-0066 (CLI ergonomics bundle), AC-0051 (import / agent-prompt flow),
AC-0055 (init writes own-remote allows), AC-0053 (config hot-reload), AC-0054 (include command).

## Research question

Add noun-verb command groups (`domain`/`service`/`mount`) that let a user maintain
the security-relevant parts of `.agent-creance.yaml` entirely through commands —
each fully specifiable via flags, each falling back to an interactive prompt for any
choice the user omits, and each preserving the file's comments and formatting. This
requires three new capabilities the codebase does not have today: (1) a richer
domain-rule flag surface (`--path`/`--method`/`--mode`/`--all-paths`/`--deny`),
(2) **removal** of config entries (the editor is append-only), and (3) an
interactive prompt that fits the project's hermetic-test seam. This document
establishes exactly where each plugs in and resolves the ticket's open questions.

## Summary

The work decomposes cleanly into four layers, three of which already have a load-bearing
pattern to copy and one of which is genuinely new infrastructure:

1. **Command surface (mechanical).** New `domain`/`service`/`mount` parent commands with
   `add`/`remove` subcommands, registered exactly like the existing `policy` noun-verb
   group (`internal/cli/policy.go:18-25`). `allow`/`deny` stay as top-level aliases.
   Every command is a `newXxxCmd(app *App)` constructor whose `RunE` delegates to a
   testable `runXxx(ctx, app, dir, …)` body — the uniform convention across the CLI.

2. **The edit pipeline already exists and is reusable as-is for adds.** `allow`/`deny`/
   `include`/`import` all funnel through `applyAndRecompile` (`internal/cli/mutate.go:97-133`),
   which runs `read → apply(closure) → atomic-write → recompile` inside `withConfigLock`.
   The `apply` closure has signature `func(src []byte) (out []byte, changed bool, err error)`.
   **Every new add/remove just needs a function of that shape** and it inherits locking,
   atomic write, the hot-reload recompile, and the "already present; nothing to do" no-op
   handling for free.

3. **Removal is the one piece of new infrastructure.** `internal/config` is strictly
   append-only today — `AppendRule`/`AppendHostService`/`AppendInclude` only, no
   `Remove*`, and the code never touches yaml.v3 comment fields. The existing edit
   approach is **text-splice based** (parse to a `yaml.Node` tree only to find line
   positions, then splice rendered text into the raw `[]byte`), so removal is symmetric
   in principle but needs a new primitive the inserts never required: computing the
   **end** line of an element, not just its start.

4. **The interactive prompt needs no new dependency and no new sysdep interface.** The
   project already has the exact "explicit-or-prompt, non-TTY-safe" seam in use by `init`:
   read/write through `App.Stdin`/`App.Stdout`, gate on `App.Terminal.IsInteractive()`,
   and on the non-interactive branch return an error with a flag hint (the established
   `import.go:82-86` / `init.go:173-179` pattern). Web research confirms every off-the-shelf
   TUI library is either archived (`survey`) or not cleanly testscript-drivable without a
   PTY (`bubbletea`), except `huh` v2 accessible mode — which is heavyweight for fallback
   prompts. **Recommend generalizing the existing hand-rolled `confirm()` into a small
   `prompt` helper** (single-select + free-text + yes/no over the injected streams).

5. **`service`/`mount` "write + warn" is grounded in the design.** `host_services` and
   mounts compile into the **frozen Seatbelt profile at launch** (`docs/design.md` "Config
   compilation" / "Crash recovery"), so they cannot affect a running cage. The running-cage
   detection to drive the warning is the read-only `proxy.Manager.Inspect(layout)` liveness
   probe (`internal/proxy/lifecycle.go:286-313`), already used by `status`. `domain`/deny
   edits hot-reload via `policy.json` mtime poll exactly as `allow`/`deny` do today.

This is a large but low-risk ticket: most of it is new command wiring over a proven edit
pipeline; the only research-grade unknown was removal-with-comment-preservation, resolved
below.

## Detailed findings

### 1. Command wiring and the App composition root

The cobra tree is built in `newRootCmd(app *App)` (`internal/cli/cli.go:88-133`); subcommands
are attached with a flat block of `root.AddCommand(...)` calls (`cli.go:119-131`). The
**noun-verb idiom to copy is `policy`** (`internal/cli/policy.go:18-25`): a parent command
with `Use`/`Short` and **no `RunE`** (so bare invocation prints subcommand help), whose
children are attached via `cmd.AddCommand(newPolicyShowCmd(app), …)`. A new `domain` group is
a `newDomainCmd(app)` parent + `newDomainAddCmd`/`newDomainRemoveCmd`, registered with one more
`root.AddCommand(newDomainCmd(app))` in `cli.go`.

**Aliases:** cobra supports `Aliases []string` on a command; it is not currently used anywhere.
The ticket wants `allow`/`deny` "kept as aliases." Two readings are possible (decide in
planning): keep the existing top-level `newAllowCmd`/`newDenyCmd` commands as-is (simplest —
they already work, already have `--once`/`--global`/`--reason`), or re-express them as cobra
aliases of `domain add`. Note `allow --once` has session-overlay behavior the ticket says
`domain` must NOT have, so the cleanest path is **keep `allow`/`deny` as separate thin
commands** (they continue to satisfy "continue to work unchanged") and build `domain` fresh.

**The `App` struct** (`internal/cli/cli.go:21-79`) holds every injected seam. Fields the new
commands need: `FS sysdep.FileSystem`, `Paths sysdep.PathResolver`, `Clock`, `HTTP` (the last
two required to construct the policy compiler), `Flock sysdep.Flock` (config lock), `Stdin`/
`Stdout`/`Stderr`, `Terminal sysdep.Terminal` (TTY detection), `OutStyle`/`ErrStyle *style.Styler`
(nil-safe styled glyphs). For the running-cage probe (`service`/`mount`) the commands also need
`ProcessManager`, `PortAllocator`, `Sleeper` — all already on `App` and used by `status`/`run`.

Handler signature convention (`internal/cli/allow.go:17-32`): the `RunE` closure calls
`runAllow(cmd.Context(), app, ".", args[0], once, global)` — the `"."` literal is the production
project dir, tests pass a temp dir. Uniform across `runAllow`/`runDeny`/`runInclude`/`runImport`.

### 2. The shared edit pipeline (`internal/cli/mutate.go`)

This is the spine the new commands plug into. Walking the key functions:

- **`applyAndRecompile(ctx, app, dir, target, fileLabel, subject, verb, apply)`**
  (`mutate.go:97-133`) — the whole read→apply→write→recompile skeleton, run inside
  `withConfigLock`. It re-reads the file fresh inside the lock (so a concurrently-appended
  rule is preserved), calls `apply(src) → (out, changed, err)`, and on `changed` does
  `MkdirAll` + `writeFileAtomic` + `recompile`. On `!changed` it prints
  "`<subject> is already <verb> in <file>; nothing to do`" and skips the write. Success line:
  "`✓ <verb> <subject> in <file>; policy recompiled`".
- **`withConfigLock(app, target, fn)`** (`mutate.go:143-157`) — resolves a **separate lock
  file** (`state.New(app.Paths).ConfigLock(target)`, keyed by hashed target path, in the
  out-of-tree cache), `Flock.Acquire`, defers `Release`, runs `fn`. Deliberately not the config
  file itself (rename swaps the inode out from under an flock — comment at `mutate.go:137-142`).
- **`recompile(ctx, app, dir)`** (`mutate.go:159-169`) — `compile.New(app.FS, app.Paths,
  app.Clock, app.HTTP, nil).Compile(ctx, dir)`. Rewrites `policy.json` (advancing mtime); the
  running enforcer's mtime poll reloads within ~1s. **No signal, no fsnotify on the proxy side**
  — the coupling is purely the artifact's mtime (consumer loop: `internal/proxy/enforcer/
  enforcer.py:173-187`, `_POLL_INTERVAL_SECONDS = 1.0`).
- **`mutationTarget(app, dir, once, global)`** (`mutate.go:62-79`) — resolves which file +
  human label: `once` → session overlay (out-of-tree); `global` →
  `config.NewLoader(app.FS, app.Paths).GlobalPath()` = `~/.config/agent-creance.yaml`; default →
  `filepath.Join(dir, ".agent-creance.yaml")`. **`domain` should accept `--global` but NOT
  `--once`** (ticket: `--once` stays on the `allow` alias only).
- **`ruleFromURL(raw, reason)`** (`mutate.go:46-57`) — current bare-URL→Rule builder. The
  comment at `mutate.go:44-45` ("v0.1 has no --method flag") is exactly what AC-0067 lifts;
  `Methods` is never set today and only one path (the URL's path) can be expressed. `domain add`
  needs a new builder taking `[]path`, `[]method`, `mode`, `allPaths`, `deny`, `reason`.

For `service`/`mount`, the ticket's "write + warn" means they should **not** call `recompile`
(no policy.json change), and should instead probe for a running cage and print the warning.
They can still use `withConfigLock` + `writeFileAtomic` directly, or a variant of
`applyAndRecompile` with the recompile step replaced by the probe-and-warn step.

### 3. The config editor — how append works, and what removal needs

**Architecture (`internal/config/edit.go`, `edit_hostservice.go`, `edit_include.go`):** the
editor is **text-splice based, not node-tree-mutation based**. `AppendRule` (`edit.go:52-81`):

1. `Parse(src)` for the duplicate check + validation baseline; identical rule → return `src`
   unchanged, `changed=false` (the no-op the CLI reports as "nothing to do").
2. `yaml.Unmarshal(src, &doc)` into a `yaml.Node` tree — **used only for line/column positions,
   never re-marshalled** (re-marshalling would drop comments and reflow).
3. `strings.Split(src, "\n")` into lines.
4. `planInsert(&doc, lines, list, rule)` (`edit.go:86-129`) returns a 0-based `insertAt` index +
   a `block []string` of rendered lines. It descends `network → egress → <list>` via
   `mappingChild`, synthesizing only missing key suffixes via `renderNested`, and places the new
   item at the end of the list region. `endOfRegion` (`edit.go:136-148`) finds where a region
   ends: the first anchor (from `collectAnchors`, which records `{line, indent}` for every node)
   deeper in the file whose `indent <= ownerIndent`, or EOF; `backOverBlanks` steps back over
   trailing blanks so the insert hugs the last real content.
5. Splice `block` between `lines[:insertAt]` and `lines[insertAt:]`, then `validateAppend`
   (`edit.go:232-257`) re-parses the candidate and asserts the only change is the new rule (the
   other list's identities unchanged, target list = old set + new rule). A splice bug can never
   reach disk as a silently-wrong policy.

**Rendering:** `renderRuleItem` (`edit.go:177-193`) emits `- host: <scalar>`, optional `paths:`/
`methods:` in **flow style** (`flowSeq`), optional `mode:` (omitted when intercept/default),
optional quoted `reason:`. `scalar` quotes anything not plainly safe.

**The schema** (`internal/config/config.go`):
- `Rule{ Host string; Paths *[]string; Methods *[]string; Mode string; Reason string }`
  (`config.go:89-95`). **`Paths`/`Methods` are pointers** so an omitted key (nil) is
  distinguishable from an explicit empty list — `validate.go:37-42` rejects a passthrough rule
  that *sets* paths/methods, which length alone can't detect. Modes `ModeIntercept = "intercept"`
  (default, applied in `defaultRuleModes`) / `ModePassthrough = "passthrough"`.
- `HostService{ Label string; Port int }` (`config.go:60-63`) — label cosmetic (non-empty,
  control-char-free), port 1–65535; `label:port` parsed by `parseHostService` (`validate.go:99-125`).
  **Identified by port** (`containsPort`, `edit_hostservice.go:132-139`).
- `Safehouse{ AddDirsRW, AddDirsRO, Enable []string }` (`config.go:45-49`) — plain `[]string`,
  **no edit/render function today**; `import` ignores the `safehouse:` section
  (`ignoredSectionsNote`).
- Node paths: `network.egress.allow`, `network.egress.deny_always`, `network.host_services`,
  `safehouse.add_dirs_rw` / `safehouse.add_dirs_ro`.

**Atomic write** is `writeFileAtomic` (`internal/cli/init.go:349-359`): write `name+".tmp"`,
`Rename` over target, cleanup temp on failure; mode `0o644`. The append/remove functions in
`internal/config` are pure (`[]byte → []byte`) — they never write.

**What removal must add (the new infrastructure):** inserts only need a start line + indent;
**removal needs the element's end line too** (its `- host:` line plus continuation lines
`paths:`/`methods:`/`mode:`/`reason:`, and any attached comment lines), then drop that line
range. The `yaml.Node` tree gives only start positions; the closest existing primitive is
`endOfRegion` (`edit.go:136-148`) — a "find the next sibling-or-shallower anchor" walk that can
be adapted to bound a single sequence item. A removal should reuse `ruleIdentity` (host + sorted
paths + sorted methods, `edit.go:271-273`) to find the matching item, and should carry its own
validation gate analogous to `validateAppend` (re-parse, assert only the targeted deletion
changed). The four granularities:
- **Whole rule** (allow/deny_always item): find `- host:` line, compute end via the adapted
  region walk, splice `[start,end)` out.
- **Single path within a rule:** today `paths:` is rendered **flow style** (`paths: ["/a/", "/b/"]`),
  so dropping one path = re-rendering that single line via `flowSeq`. (See Open Questions for the
  last-path case.)
- **Host service:** single-line `- label:port`, identified by port → drop one line.
- **Mount:** `safehouse.add_dirs_*` has neither append nor remove today — both are new, including
  the rw-vs-ro decision (see Open Questions).

**Comment attribution** (the genuinely fiddly part): `backOverBlanks` trims trailing blanks on
*insert*, but there is no leading-comment-association logic. Whether a `#` comment / blank line
*above* a removed item belongs to it is unsolved. Recommendation for planning: a conservative
default — remove only the element's own lines (from its `-` line through its last continuation
line), leaving surrounding comments in place — is the least surprising and the easiest to make
golden-test-stable. Revisit if it leaves orphaned comments in practice.

### 4. The interactive prompt seam (no new dependency)

The full "explicit-or-prompt, non-TTY-safe" seam already exists and is in use by `init`:

- **`confirm(app, prompt)`** (`internal/cli/init.go:207-219`) — prints `<prompt> [y/N]:` to
  `app.Stdout`, reads via `readLine(app.Stdin)`, EOF → "no". **TTY detection is NOT inside
  `confirm()`** — it is at every call site, guarded by `app.Terminal.IsInteractive()`. The
  non-interactive refusal model (the exact "fail with a hint, never hang" behavior AC-0067 wants)
  is `init.go:173-179` and `import.go:82-86`: when `!IsInteractive()`, print a hint and return an
  error instead of prompting.
- **`readLine(r)`** (`init.go:225-240`) reads byte-by-byte up to `\n`, deliberately not buffering,
  so multiple sequential prompts share one `app.Stdin` without an earlier read swallowing later
  answers — the property a multi-prompt `domain add` flow needs.
- **`sysdep.Terminal`** (`internal/sysdep/terminal.go:24-34`): `IsInteractive()` / `IsStderrTerminal()`
  / `IsStdoutTerminal()`; `OSTerminal` probes via termios ioctl. Faked by
  `sysdeptest.FakeTerminal{Interactive, StderrTerminal, StdoutTerminal}` (`sysdeptest/terminal.go:9-27`).
  **No new sysdep interface is needed.** Stdin/Stdout are plain `io.Reader`/`io.Writer` on `App`
  (faked with `strings.NewReader`/`bytes.Buffer`), not sysdep types.

**Web-research verdict on TUI libraries** (for the ticket's "evaluate a library vs hand-rolled"
question): `AlecAivazis/survey` is **archived (Apr 2024), last release 2023, and untestable
without a PTY** — disqualified. Raw `bubbletea` speaks raw key events + ANSI; testing means a
terminal emulator (`teatest`), not scripted stdin — wrong model for `testscript`. `huh` v2
**accessible mode** (`WithAccessible(true)` + `WithInput`/`WithOutput`) is the only library that
is cleanly PTY-free testscript-drivable, but pulls the full Bubble Tea v2 / Lip Gloss v2 tree —
heavy for fallback prompts. `golang.org/x/term.IsTerminal(fd)` is the canonical isatty check
(the project's `OSTerminal` already does the equivalent via ioctl). **Recommendation: generalize
the existing hand-rolled `confirm()`** into a small prompt helper (single-select, free-text,
yes/no) over `App.Stdin`/`App.Stdout` gated on `App.Terminal.IsInteractive()` — zero new deps,
matches the project's DI philosophy, trivially testscript-able and unit-testable.

### 5. Live-cage detection and the warning (`service`/`mount`)

**Design basis (`docs/design.md`):** `network.sb` "contains … the narrow `(remote tcp
"localhost:<port>")` allow rules derived from `network.host_services` … regenerated on every
launch" ("Config compilation"); filesystem mounts are passed to safehouse as `--add-dirs*` launch
flags; and "An already-running agent's Seatbelt profile is **frozen at its own launch — it cannot
be rewritten mid-flight**" ("Crash recovery"). So `service`/`mount` edits **cannot** affect a
running cage — hence write + warn. By contrast `allow`/`deny_always` → `policy.json`, hot-reloaded
via mtime poll ("In-session config hot-reload"). The new commands introduce **no new schema** —
every field they write is already specified in the design.

**Running-cage probe:** `proxy.Manager.Inspect(layout)` (`internal/proxy/lifecycle.go:286-313`)
is the read-only, non-mutating liveness verdict. It returns a `Diagnosis` whose `ProxyUp` field is
the exact "a cage is running" boolean (`ProxyUp = ProxyPID != 0 && proc.Alive(ProxyPID) &&
ports.Probe(Port)` — PID-alive AND-ed with a port probe, never PID alone). `LiveAgents` lists
attached agent PIDs. Reuse pattern (from `status.go:30-42`): resolve the layout with
`state.New(app.Paths).Resolve(dir)`, build `proxy.NewManager(app.FS, app.Flock, app.ProcessManager,
app.PortAllocator, app.Sleeper, app.Stderr)`, call `mgr.Inspect(layout)`, branch on `diag.ProxyUp`.
Safe to call before the config write (mutates nothing).

**Warning idioms:** advisory non-fatal warning → `fmt.Fprintf(app.Stderr, "%s …", app.ErrStyle.
Warn("⚠"))` (e.g. `run.go:316-326`, `warnVersionSkew`). Success/result line → `fmt.Fprintf(
app.Stdout, "%s …", app.OutStyle.OK("✓"))` (e.g. `mutate.go:130`). The stylers no-op to plain
text when color is off, so they're safe unconditionally. A non-`run` command owns its stdout, so
`service`/`mount` can print their "written; takes effect next run" line on stdout (matching how
`allow`/`deny` print results), using the `⚠` glyph when a live cage makes the caveat a real warning.

### 6. Testing surfaces

The project's three test styles all apply (per CLAUDE.md conventions):

- **Pure edit/removal logic → table-driven + golden tests** in `internal/config`. The append
  golden harness is `edit_test.go` with `.in.yaml`/`.golden.yaml` pairs under
  `testdata/edit/` (regenerate via `make golden` / `-update`). New removal functions and the new
  domain-rule add (with paths/methods/mode) get new fixture pairs here. Sibling harnesses:
  `edit_hostservice_test.go`, `edit_include_test.go`.
- **Command behavior → hermetic `testscript` `.txtar`** under `internal/cli/testdata/script/`.
  Harness: `script_test.go` registers `agent-creance` → `cli.Main()`, exposes `$CREANCE_BIN`;
  scenarios set `env PATH=$CREANCE_BIN`. Models to copy: `allow_deny.txtar`, `import.txtar`,
  `include.txtar`, `status_lists.txtar` (for the live-cage warning, `seedlock` builtin seeds a
  lock file). **Key constraint:** under testscript the CLI always runs non-TTY, so
  `OSTerminal.IsInteractive()` is always `false` — `.txtar` is the right place to test the
  **non-TTY "fail with flag hint" branch** and the all-flags-supplied non-interactive path, but
  the **positive interactive prompt path** must be unit-tested (next bullet).
- **Interactive prompt flows → `*App` + fakes unit tests** (the `init_test.go` pattern):
  set `FakeTerminal.Interactive = true`, preload `App.Stdin = strings.NewReader("specific\n/repos/\n")`
  (concatenate answers for sequential prompts — `readLine`'s non-buffering read consumes one line
  at a time), assert on `bytes.Buffer` stdout. See `init_test.go:420-489` for the established shape.

## Code references

- `internal/cli/cli.go:88-133` — `newRootCmd`, subcommand registration block; `App` struct `:21-79`.
- `internal/cli/policy.go:18-25` — the noun-verb parent/subcommand idiom to copy.
- `internal/cli/allow.go:17-50`, `deny.go:15-42` — thin command wrappers + `runAllow`/`runDeny`.
- `internal/cli/mutate.go:97-133` — `applyAndRecompile` (the edit skeleton); `:143-157`
  `withConfigLock`; `:159-169` `recompile`; `:62-79` `mutationTarget`; `:46-57` `ruleFromURL`.
- `internal/cli/import.go:82-86`, `init.go:173-179` — the non-interactive "fail with hint" model.
- `internal/cli/init.go:207-240` — `confirm()` / `readLine()`; `:349-359` `writeFileAtomic`.
- `internal/config/edit.go:52-81` `AppendRule`; `:86-129` `planInsert`; `:136-148` `endOfRegion`;
  `:177-201` `renderRuleItem`/`flowSeq`; `:232-257` `validateAppend`; `:271-273` `ruleIdentity`.
- `internal/config/edit_hostservice.go:32` `AppendHostService`; `:132-139` `containsPort`.
- `internal/config/edit_include.go:23` `AppendInclude`.
- `internal/config/config.go:89-95` `Rule`; `:60-63` `HostService`; `:45-49` `Safehouse`;
  `:98-101` mode constants.
- `internal/config/validate.go:37-42` passthrough+paths/methods rejection; `:99-125` `parseHostService`.
- `internal/config/load.go:77-83` `GlobalPath`.
- `internal/proxy/lifecycle.go:286-313` `Manager.Inspect` (liveness); `:255-280` `Diagnosis`.
- `internal/cli/status.go:30-42` — how a command builds the manager and probes.
- `internal/sysdep/terminal.go:24-34` `Terminal`; `sysdeptest/terminal.go:9-27` `FakeTerminal`.
- `internal/style/style.go:65-100` `OK`/`Warn`/`Dim` styled glyphs.
- `internal/proxy/enforcer/enforcer.py:173-187` — mtime poll reload (the hot-reload consumer).
- `internal/cli/script_test.go:26-57` — testscript harness; `init_test.go:420-489` — interactive
  unit-test pattern; `internal/config/edit_test.go` — golden harness.
- `docs/design.md` — "The configuration", "Per-host enforcement modes" (passthrough guard, l.277-280),
  "Config compilation" (frozen Seatbelt vs hot-reload policy.json), "In-session config hot-reload",
  "Crash recovery" (l.502, frozen-at-launch), "Commands".

## Open questions resolved by research

These are the ticket's "Questions for Research/Planning," now answerable:

1. **TUI approach.** Generalize the existing hand-rolled `confirm()` into a small prompt helper
   over `App.Stdin`/`App.Stdout` gated on `App.Terminal.IsInteractive()`. No new dependency, no
   new sysdep interface. (Library alternatives: `survey` archived/PTY-bound — rejected;
   `bubbletea` wrong test model; `huh` accessible mode works but is heavyweight.)

2. **Removal infrastructure.** Symmetric to insert but needs a new "end-of-element" computation
   (adapt `endOfRegion`'s sibling-or-shallower-anchor walk) plus a removal validation gate
   (mirror `validateAppend`). Match by `ruleIdentity` / port. **Comment attribution:**
   conservatively remove only the element's own lines; leave surrounding comments untouched
   (least surprising, golden-stable).

3. **`domain remove --path P` when P is the rule's last path.** An empty `paths` list is NOT
   semantically neutral — a non-nil-but-empty `Paths` differs from nil (pointer design,
   `config.go:89-95`), and a rule with paths omitted is host-wide intercept (Mode B). So leaving
   an empty list would silently widen the rule to host-wide. **Recommendation: drop the whole
   rule when removing its last path** (a rule scoped to zero paths is meaningless), and document
   it. Confirm in planning.

4. **Live-cage detection.** Reuse `proxy.Manager.Inspect(layout).ProxyUp` exactly as `status`
   does — no new mechanism.

5. **`mode` default + passthrough guard.** `mode` defaults to `intercept` (design + `defaultRuleModes`).
   The command-layer guard (reject `--mode passthrough` with `--path`/`--method`) is an **early,
   additive** check that must agree with the existing **compile-time** rejection
   (`validate.go:37-42`, design l.277-280) — not a replacement for it. Implement the early guard
   in the `domain add` arg-validation; keep the compile-time check as the backstop.

## Remaining decisions for planning (genuine judgment calls)

- **A. `allow`/`deny` as separate commands vs cobra aliases of `domain`.** Recommendation: keep
  them as separate thin commands (they already carry `--once`/`--global`/`--reason` and `--once`
  must NOT exist on `domain`). Low risk, satisfies "continue to work unchanged."
- **B. `mount remove PATH` when the path is in both `add_dirs_rw` and `add_dirs_ro`.** Options:
  error (force the user to disambiguate), or remove from both. Recommendation: remove from both
  with a note (a path present in both lists is already an odd state; removal should fully detach
  it) — but this is a judgment call worth confirming.
- **C. Removing a non-existent entry.** The ticket asks no-op-with-message vs error. The existing
  add path treats "already present" as a no-op with a message; symmetry suggests **remove of a
  missing entry → no-op with a "not present; nothing to do" message** (mirrors `mutate.go:114-117`).
- **D. Internal phasing of this XL ticket.** Suggested phases: (1) `domain` add with full flag
  surface (paths/methods/mode/all-paths/deny/global) + early passthrough guard + recompile, reusing
  the existing add pipeline; (2) the prompt helper + interactive fallback for `domain add`;
  (3) removal infrastructure in `internal/config` (the new end-of-element primitive + validation
  gate) and `domain remove` (whole-rule + single-path); (4) `service` add/remove + `mount`
  add/remove (incl. the new `safehouse.*` append/remove) with the live-cage write+warn.

## tce config drift

None observed. The profile's stack, command map, code map, and the tmt ticket backend all match
the current repository (verified against `internal/cli`, `internal/config`, `internal/proxy`,
`internal/sysdep`, and the testscript/golden test layout). No `/tce:refresh` recommended.
