---
date: 2026-06-19
ticket: AC-0052
title: "Polish CLI output — semantic color and visual hierarchy, TTY-aware"
status: ready
type: plan
git_commit: 8fa47e9
branch: main
repository: github.com/tobyS/agent-creance
research: thoughts/shared/research/2026-06-19-AC-0052-cli-output-styling.md
---

# AC-0052 — Semantic color & visual hierarchy for CLI output: implementation plan

## Overview

Add a restrained, semantic color / visual-hierarchy layer (green=ok,
yellow=warn, red=problem; **bold** section headers; dim secondary detail) to
every `agent-creance` command, gated on a `--color=auto|always|never` flag plus
`NO_COLOR` and per-stream TTY detection. When color is off the output is
**byte-for-byte identical to today** (existing plain golden files stay
unchanged). Color is always additive — every status remains identifiable by its
glyph/word with escapes stripped.

The change is **wide, not deep**: the OS-touching part is one new TTY method and
a flag; the bulk is threading a small styler into the four pure renderers, the
progress printer, and the inline-glyph command sites, then pinning both plain and
colored output with `-update` goldens.

## Decisions locked at the checkpoint

1. **Library:** use **`github.com/fatih/color`** (not hand-rolled). Wrapped in a
   thin project `Styler` so renderers stay decoupled from the lib and we control
   the enabled/plain decision per stream via `*color.Color.EnableColor()` /
   `.DisableColor()` (per-instance, overriding fatih's global `color.NoColor`).
   Adds transitive deps `mattn/go-isatty`, `mattn/go-colorable` — accepted.
2. **Dim granularity:** **isolate every token** — restructure format strings so
   each secondary token (including mid-sentence pids/ports/durations) is split
   out and dimmed, not just cleanly-trailing segments.
3. **Colored goldens:** commit **raw escape bytes** via the existing `-update`
   flow; one plain + one colored golden per case.

## Current state (from research)

- Four renderers are pure `string`-builders taking only a data struct:
  `doctor.Render` (`internal/doctor/report.go:105`, embeds `prereq.Report` at
  `:107`), `status.Render` (`internal/status/report.go:31`, uses `tabwriter`),
  `prereq.Report` (`internal/prereq/report.go:20`), `policy/render`
  `Show`/`Explain`/`Refresh` (`internal/policy/render/render.go:39,122,193`).
  Commands write the returned string to `app.Stdout`.
- Glyphs `✓ ⚠ ✗` live in the renderer packages **and** inline in
  `internal/cli/{run,init,setup,import,mutate,version}.go`. `status` uses words.
- Progress printer counts **runes** for `\r` pad math and forbids ANSI
  (`internal/progress/printer.go:21-26,188-205`).
- `sysdep.Terminal` has `IsInteractive`/`IsStderrTerminal` only — **no stdout**
  (`internal/sysdep/terminal.go:22-29`). Env via `PathResolver.Getenv`
  (`internal/sysdep/pathresolver.go:30-32`).
- No persistent cobra flags exist; no subcommand defines a `PreRunE`
  (`internal/cli/cli.go:80-104`). `App` holds `Stdout`/`Stderr`/`Terminal`/`Paths`
  (`cli.go:20-71`); `Main()` wires reals (`:110-139`).
- Golden harnesses use a `-update` flag + map-of-variants loop
  (`internal/doctor/report_test.go:17-97`). Manual `%-*s`+`len()` alignment in
  prereq/policy; `tabwriter` in status — both count bytes, so escapes in a padded
  column misalign.

## Desired end state

- A `--color` persistent flag (default `auto`) resolves once at the cobra root
  into two `App` stylers (stdout + stderr), honoring `NO_COLOR` and per-stream
  isatty; `--color=always` overrides `NO_COLOR` (per no-color.org).
- Every renderer + the progress printer + inline command glyphs colorize through
  the styler. Disabled styler = identity ⇒ existing plain goldens unchanged.
- Alignment preserved under color via **width-before-inject**: padding computed
  from the plain token, color injected into the content only.
- Both plain and colored renderings pinned by `-update` goldens; testscript
  covers `NO_COLOR`, `--color=never|always`, non-TTY, and the invalid-value error.
- `make test`, `make lint` green; `make build` at the end.

## What we're NOT doing

- No live/interactive TUI, spinners, alt-screen, or redraw dashboards.
- No user-configurable themes/palettes; single built-in palette.
- No change to *what* each command reports (fields/ordering/sections) beyond the
  minimal format-string restructuring needed to isolate secondary tokens.
- No `FORCE_COLOR`/`CLICOLOR*` support (out of the ticket's stated scope).
- No styling of the `tmt`/`tce` plugin scripts.

## Key design

**`Styler`** (new `internal/style` package): wraps fatih/color.

```go
type Styler struct { ok, warn, bad, header, dim *color.Color; enabled bool }
func New(enabled bool) *Styler   // EnableColor/DisableColor each *color.Color per `enabled`
func Plain() *Styler             // = New(false)
// nil-safe methods (nil or disabled => return s unchanged):
func (s *Styler) OK(string) string     // green
func (s *Styler) Warn(string) string   // yellow
func (s *Styler) Bad(string) string    // red
func (s *Styler) Header(string) string // bold
func (s *Styler) Dim(string) string    // faint
func (s *Styler) Enabled() bool
```

Nil-safety means every existing test that constructs `App` without stylers (or
calls a renderer with `nil`) keeps producing plain bytes — no churn there.

**Resolution** (pure, table-tested):

```go
func Resolve(mode string, noColor bool, isTTY bool) (bool, error)
// "always"->true; "never"->false; "auto"-> !noColor && isTTY; else error
```

**Width-before-inject**: `style.VisibleWidth(s)` strips SGR escapes
(`\x1b\[[0-9;]*m`) then counts runes; equals `utf8.RuneCountInString` for
plain strings (so plain math is unchanged). Renderers compute column pad from the
plain token and wrap content in color; the progress printer uses `VisibleWidth`
in place of `utf8.RuneCountInString`.

---

## Phase 0 — Foundation (no visible output change)

Adds the dependency, the styler, the TTY method, the flag, and the wiring.
Nothing colorizes yet, so all existing output/goldens stay identical.

### Changes
1. `go get github.com/fatih/color@latest` then `go mod tidy` — review the
   `go.mod`/`go.sum` additions (`fatih/color`, `mattn/go-isatty`,
   `mattn/go-colorable`).
2. New `internal/style/style.go`: `Styler`, `New`, `Plain`, nil-safe
   `OK/Warn/Bad/Header/Dim/Enabled`, `Resolve`, `VisibleWidth`, `stripANSI`.
   Palette: green=ok, yellow=warn, red=problem, bold=header, faint=dim.
3. New `internal/style/style_test.go` (table-driven): `Resolve` matrix
   (always/never/auto × NO_COLOR × isTTY × invalid); `New(false)` is identity;
   `New(true)` wraps with `\x1b[...m`; `VisibleWidth` strips escapes; nil receiver
   returns input.
4. `internal/sysdep/terminal.go`: add `IsStdoutTerminal()` to the interface and
   `OSTerminal` (`isTerminal(os.Stdout)`); update the doc comment.
5. `internal/sysdep/sysdeptest/terminal.go`: add `StdoutTerminal bool` field +
   method.
6. `internal/cli/cli.go`: add `App.OutStyle *style.Styler`, `App.ErrStyle
   *style.Styler`. In `Main()` initialize both to `style.Plain()` (safe default
   before PreRun). In `newRootCmd`: register
   `root.PersistentFlags().String("color", "auto", "color output: auto|always|never")`
   and a `PersistentPreRunE` that reads the flag, resolves per stream via
   `style.Resolve(mode, app.Paths.Getenv("NO_COLOR") != "", app.Terminal.IsStdoutTerminal()/IsStderrTerminal())`,
   and sets `app.OutStyle`/`app.ErrStyle` (returning the validation error for a
   bad value).
7. New testscript `internal/cli/testdata/script/color_flag.txtar`:
   `! agent-creance --color=bogus doctor` asserts the invalid-value error;
   `agent-creance --color=never version` unchanged output.

### Verify
- [ ] `make test` green (existing output unchanged; `style` tests pass).
- [ ] `make lint` clean.
- [ ] `go build ./...` ok.

---

## Phase 1 — Progress printer color (run / stderr)

### Changes
1. `internal/progress/printer.go`: add a `sty *style.Styler` to `Printer`; extend
   `NewPrinter(w, clock, interactive, sty)`. Replace `utf8.RuneCountInString` in
   `openLine`/`rewrite` (and any width site) with `style.VisibleWidth`. Colorize
   the `glyphOK` (`✓`) green via `sty.OK`; keep step/counter text plain (dim a
   trailing duration if present). Width is computed on the visible string, so
   colored and plain renders pad identically.
2. `internal/cli/run.go:95`: pass `app.ErrStyle` into `NewPrinter`. Colorize the
   inline `⚠` warnings (`run.go:214,238,277`) via `app.ErrStyle.Warn` and dim the
   "differs from tested" detail.
3. `internal/progress/printer_test.go`: update the `pad` helper to use
   `style.VisibleWidth`; add colored-mode cases (styler enabled) asserting the
   escape bytes appear and the `\r`/pad math stays correct; keep all existing
   plain assertions unchanged.

### Verify
- [ ] `make test` green; plain printer assertions byte-identical.
- [ ] `make lint`, `go build ./...` ok.

---

## Phase 2 — doctor + prereq renderers (stdout)

### Changes
1. `internal/prereq/report.go`: `Report(results, sty)`. Bold "Version
   compatibility:" header; color glyphs in `statusField` (ok green / warn yellow
   / miss red); dim the "installed X, **tested against Y**" detail and the
   trailing paragraph. Apply width-before-inject: compute `nameW`/`instW` from
   plain fields, then emit colorized content + manual pad (keep color out of the
   padded measurement).
2. `internal/doctor/report.go`: `Render(r, sty)`; pass `sty` into
   `prereq.Report`. Bold the four section headers; route every glyph through
   `caGlyph`/`renderProxy`/`renderExposed`/`renderFS` + `sty`. **Isolate every
   token**: restructure the mid-message format strings (`report.go:149-180`) so
   pids/ports/counts are split out and dimmed.
3. `internal/cli/doctor.go:59`: pass `app.OutStyle` into `doctor.Render`.
4. Goldens: extend `internal/doctor/report_test.go` and
   `internal/prereq/report_test.go` to loop plain+color variants
   (`render_<name>_plain.golden` already-existing bytes + new
   `render_<name>_color.golden`); colored produced with `style.New(true)`, plain
   with `style.Plain()`. Run `make golden`, **confirm the existing plain goldens
   are unchanged**.

### Verify
- [ ] `make golden` then `git diff` shows plain goldens unchanged, only new color
      goldens added.
- [ ] `make test`, `make lint`, `go build ./...` ok.

---

## Phase 3 — status renderer (stdout, tabwriter)

### Changes
1. `internal/status/report.go`: `Render(r, sty)`. **Two-path** to protect the
   byte-identical plain output:
   - styler disabled ⇒ existing `tabwriter` code verbatim (unchanged bytes);
   - styler enabled ⇒ manual visible-width column layout (compute widths from
     plain cells, then colorize): bold `PROJECT/STATE/PORT/AGENTS` header;
     `stateLabel` words colored (running=green, orphan=red, stranded=yellow,
     down=dim); dim `portLabel`.
2. `internal/cli/status.go:40`: pass `app.OutStyle`.
3. `internal/status/report_test.go`: loop plain+color; new color goldens; plain
   goldens unchanged.

### Verify
- [ ] `make golden` diff: plain status goldens unchanged, color goldens added.
- [ ] `make test`, `make lint`, `go build ./...` ok.

---

## Phase 4 — policy/render renderer (stdout)

### Changes
1. `internal/policy/render/render.go`: `Show(c, sty)`, `Explain(c, req, sty)`,
   `Refresh(r, sty)` (JSON variants unchanged — never colored). Bold
   `ALLOW`/`DENY` and the `Refresh` header; color `⚠` markers yellow; color the
   `Explain` decision word (allow=green, hard-deny=red, soft-deny=yellow); dim
   notes/paths/methods. Width-before-inject for the `%-*s` columns in
   `renderAllow`/`renderDeny`/`Refresh`.
2. `internal/cli/policy.go:55,85,122`: pass `app.OutStyle`.
3. `internal/policy/render/render_test.go`: loop plain+color across `show`,
   `explain_*`, `refresh*`; JSON cases stay single. New color goldens; plain
   unchanged.

### Verify
- [ ] `make golden` diff: plain + JSON goldens unchanged, color goldens added.
- [ ] `make test`, `make lint`, `go build ./...` ok.

---

## Phase 5 — remaining inline command glyphs (stdout/stderr)

### Changes
Route the inline glyphs/key tokens through `app.OutStyle`/`app.ErrStyle`:
- `internal/cli/init.go:134` (+ other `✓`/notes), `setup.go:74,76,88,142,144`,
  `import.go:101`, `mutate.go:114` — `✓` green.
- `internal/cli/version.go:19-24` — dim the "tested against:" list values.
- `internal/cli/clean.go`, `logs.go`, `init_imports.go` — apply bold/dim where a
  header or secondary detail exists; otherwise leave plain.
Existing testscripts assert via substrings and run non-TTY (plain), so they keep
passing unchanged.

### Verify
- [ ] `make test` (testscripts) green, `make lint`, `go build ./...` ok.

---

## Phase 6 — testscript coverage + final verification

### Changes
1. Add/extend `.txtar` under `internal/cli/testdata/script/`:
   - `NO_COLOR=1` ⇒ output equals the plain case (parity);
   - `--color=never` ⇒ no escape bytes; `--color=always` ⇒ an escape byte present
     (e.g. assert `stdout '\x1b\['` on `doctor`/`version`);
   - non-TTY default ⇒ plain (already the testscript default).
2. Tick the plan checkboxes; tick the ticket Acceptance Criteria.

### Final verification
- [ ] `make test` and `make lint` green.
- [ ] `make build` so `bin/agent-creance` reflects the final commit.
- [ ] Manual smoke (TTY): `./bin/agent-creance doctor` shows color; piped
      (`| cat`) and `NO_COLOR=1` and `--color=never` are byte-identical to a
      pre-change build; `--color=always | cat` shows escapes.
- [ ] Review every regenerated/added golden diff.

## Success criteria

**Automated:** `make test`, `make lint`, `go build ./...` all green; `make
golden` leaves every pre-existing plain/JSON golden unchanged and adds only
colored goldens.

**Manual / acceptance (maps to ticket):** color auto-detects TTY and honors
`NO_COLOR`; `--color=auto|always|never` works, default `auto`; non-TTY /
`NO_COLOR` / `--color=never` is byte-for-byte identical to today; colored output
is golden-pinned; every status stays identifiable with color stripped; the
progress `\r` math stays aligned with color active.

## Risks / notes

- **fatih/color global state:** we never rely on `color.NoColor`; each `*color.Color`
  is forced via `EnableColor`/`DisableColor`, making tests deterministic
  regardless of the test process's TTY/env.
- **Plain parity is the gate:** the disabled styler must be the exact identity.
  The Phase 2-4 golden diffs are the proof — any change to a plain golden is a
  regression to fix, not to accept.
- **status two-path** trades a small duplicate layout routine for a guaranteed
  unchanged plain path (tabwriter byte-output is hard to reproduce manually).
- **Mid-sentence token isolation** (doctor pids/ports) is the most format-string
  churn; keep the plain rendering identical while restructuring.
