---
date: 2026-06-19
ticket: AC-0052
title: "Polish CLI output — semantic color and visual hierarchy, TTY-aware"
status: complete
type: research
git_commit: e25597a62a10a12e29a23ee3a20b2ed638a66f32
branch: main
repository: github.com/tobyS/agent-creance
---

# Research: AC-0052 — Semantic color & visual hierarchy for CLI output

## Research question

How should `agent-creance` add a restrained, semantic color / visual-hierarchy
layer (green=ok, yellow=warn, red=problem; bold headers; dimmed secondary
detail) across **every** command, while keeping output **byte-for-byte
identical to today** when stdout/stderr is not a TTY, `NO_COLOR` is set, or
`--color=never` — and without corrupting the progress printer's in-place `\r`
width math? This resolves the five "Questions for Research/Planning" in the
ticket: where TTY detection lives, hand-rolled vs. dependency, the two-mode
golden strategy, how `--color` wires into cobra, and the per-command token
audit.

## Summary / recommendation

The codebase is unusually well-shaped for this change, but the change is **wide,
not deep**:

1. **All four report renderers are pure `string`-builders** (`Render`/`Report`/
   `Show`) that take a data struct and return a string; the command layer writes
   that string to `app.Stdout`. They take **no writer and no color flag** today.
   Color must therefore be introduced as a **value threaded into each renderer**
   (a small "styler"), not as writer post-processing — only the renderer knows
   which token is a glyph vs. a header vs. secondary detail.

2. **Glyphs (`✓ ⚠ ✗`) live in two places**, not one: the renderer packages
   (`doctor`, `prereq`, `policy/render`) *and* inline in several command files
   (`init.go`, `setup.go`, `run.go`, `import.go`, `mutate.go`). `status` uses
   **words, not glyphs**. So the styler has to be reachable at the command layer
   (via `app`) as well as inside renderers.

3. **The progress printer counts runes** for its `\r` pad math and explicitly
   forbids ANSI ("the codebase uses none"). The robust fix is **compute width
   from the plain string, inject escapes only after width is known** — escapes
   never enter the width path.

4. **TTY detection already exists** as `sysdep.Terminal` (stdin + stderr). It
   needs **one new method, `IsStdoutTerminal()`**, mirroring the existing
   termios probe — **no new dependency** (it reuses `golang.org/x/sys/unix`,
   already imported).

5. **No persistent/global cobra flags exist yet.** A global `--color` flag is
   registered once on the root command and resolved (auto/always/never + env +
   isatty) in a root `PersistentPreRunE` into a new `App` field.

6. **Golden tests use a `-update` flag with a map-of-variants loop** (doctor).
   The two-mode strategy is the same loop with the color mode folded into the
   variant key (`render_<name>_plain.golden` / `render_<name>_color.golden`),
   color forced on/off explicitly (tests are never a TTY).

**Recommended approach** (subject to the checkpoint questions): a tiny
hand-rolled SGR styler in a new `internal/style` (or `internal/ui`) package —
zero new `go.mod` entries, the only option that *guarantees* the disabled path
emits today's exact bytes, and fully table-testable in keeping with the
project's "inject deps / pure-logic tests" ethos. Add `IsStdoutTerminal()` to
`sysdep.Terminal`, a `--color` persistent flag resolved into `App`, thread the
styler into every renderer + the `Printer` + the inline-glyph command sites, and
pin both plain and colored output with `-update` goldens.

## Detailed findings

### 1. Report renderers are pure string-builders (no writer, no color seam)

All four renderers return a `string` built into a `strings.Builder` (or
`tabwriter` for `status`); the command layer is the only place an `io.Writer`
is touched.

| Renderer | Entry point | Returns | Written by |
|---|---|---|---|
| doctor | `Render(r Report) string` — `internal/doctor/report.go:105` | string | `internal/cli/doctor.go:59` |
| status | `Render(r Report) string` — `internal/status/report.go:31` | string (via `tabwriter`) | `internal/cli/status.go:40` |
| prereq | `Report(results []Result) string` — `internal/prereq/report.go:20` | string | embedded in doctor at `internal/doctor/report.go:107` |
| policy/render | `Show`/`Explain`/`Refresh` — `internal/policy/render/render.go:39,122,193` | string | `internal/cli/policy.go:55,85,122` |

Key composition fact: **doctor embeds prereq** — `doctor.Render` calls
`prereq.Report(r.Version)` (`internal/doctor/report.go:107`) and prepends it. The
"Version compatibility" block in `doctor` output is produced by the `prereq`
package, so styling it once covers both.

`app.Stdout`/`app.Stderr` are `io.Writer` fields on the `App` struct
(`internal/cli/cli.go:22-23`), production-wired to `os.Stdout`/`os.Stderr`
(`cli.go:113-...`), test-wired to buffers. **No renderer receives the writer or
any flag** — the seam stops at the command boundary. Consequence: color can't be
bolted on by post-processing the assembled string (you'd have to re-parse
semantics); it must be a value the renderer uses while building. There is **no
shared formatting helper** across the four packages — each formats
independently, and the glyph constants are *duplicated* (see §3).

### 2. Per-command token audit (what gets green/yellow/red/bold/dim)

**doctor** (`internal/doctor/report.go`): every status line goes through one
primitive `line(b, glyph, text)` at `report.go:128-130` (`"  %s %s\n"`) — the
`glyph` is the color token. Glyph constants `glyphOK/glyphWarn/glyphMiss = ✓/⚠/✗`
at `report.go:20-24`. Section headers (bold candidates) are bare writes at
`report.go:109,112,115,118` (`"\nCA trust:\n"`, `"\nProxy (this project):\n"`,
`"\nExposed host services:\n"`, `"\nFilesystem reliability:\n"`). Glyph
selection: `caGlyph(Status)` at `report.go:132-141`; `renderProxy`/
`renderExposed`/`renderFS` pick glyph literals inline (`report.go:143-182`).
Secondary detail (dim candidates) is **interpolated mid-message** —
`"orphan proxy (pid %d, port %d) — …"` (`report.go:151`), `"… (pid %d) listening
on %s"` (`report.go:169`), etc. Dimming just the number requires restructuring
those format strings.

**prereq** (`internal/prereq/report.go`): header `"Version compatibility:\n"`
(`report.go:22`). Per-tool row `"  %-*s  %-*s   %s\n"` (`report.go:36-40`): col 1
`name` (label), col 2 `installedField` — the **"installed X, tested against Y"**
string (`report.go:65`, the dim "tested" candidate), col 3 `statusField`
(`report.go:69-82`) — `glyphMiss + " missing"`, `glyphOK`, or `glyphWarn + " " +
Skew`. **Alignment caveat:** widths use `len()` (bytes) and `%-*s`; the glyphs
sit only in the **unpadded trailing column**, so escapes must stay there (or be
discounted) or column alignment breaks. Trailing explanatory paragraph
(`report.go:42-46`) is a dim candidate.

**status** (`internal/status/report.go`): uses **`text/tabwriter`**
(`report.go:36`). Header row `"PROJECT\tSTATE\tPORT\tAGENTS"` (`report.go:37`,
bold candidate). No glyphs — `stateLabel` (`report.go:57-68`) yields the
colorable words `orphan`/`stranded`/`running`/`down`; `portLabel`
(`report.go:72-77`) is dim secondary. **tabwriter counts in-cell bytes** —
escapes inside a cell corrupt alignment; this is the renderer most sensitive to
in-cell ANSI.

**policy/render** (`internal/policy/render/render.go`): block headers `"\nALLOW\n"`
/ `"\nDENY\n"` (`render.go:43,51`, bold). Marker constants `⚠ passthrough` /
`⚠ lower-trust` at `render.go:27-34` (warn color). `renderAllow`/`renderDeny`
use manual `%-*s` width alignment (`render.go:72-117`) — same byte-`len` caveat
as prereq. `Explain` decision word `allow`/`hard-deny`/`soft-deny`
(`render.go:128-130`) is the colorable verdict token; `"Note:      %s"`
(`render.go:150`) is dim.

**Inline glyphs in command files** (NOT in renderer packages — found via grep):
- `internal/cli/run.go`: `"⚠ config hot-reload unavailable: …"` (`run.go:214`),
  `"⚠ plugin marketplace: …"` (`run.go:238`), `"⚠ %s %s differs from tested …"`
  (`run.go:277`) — all to `app.Stderr`.
- `internal/cli/init.go`: `"✓ Wrote %s …"` (`init.go:134`).
- `internal/cli/setup.go`: several `"✓ …"` lines (`setup.go:74,76,88,142,144`).
- `internal/cli/import.go`: `"✓ Imported config into %s …"` (`import.go:101`).
- `internal/cli/mutate.go`: `"✓ %s %s in %s; policy recompiled\n"` (`mutate.go:114`).
- `internal/cli/version.go`: `"agent-creance %s (commit …)"` + dim "tested
  against:" list (`version.go:19-24`).
- `internal/cli/clean.go`, `logs.go`, `init_imports.go`: plain status lines.

So the styler must be available to **both** the renderer packages and the
`internal/cli/*.go` command bodies (the latter already hold `app`).

### 3. The progress printer's `\r` width math (and the no-ANSI rule)

`internal/progress/printer.go` (three files: `progress.go`, `printer.go`,
`printer_test.go`). It renders in **runes**, never bytes/columns. The rule lives
in the `Printer` doc comment (`printer.go:21-26`): *"rewritten in place with `\r`
and space-padding — no ANSI escapes, the codebase uses none."*

The math:
- `openLine(s)` records `openWidth = utf8.RuneCountInString(s)` (`printer.go:188-191`).
- `rewrite(s)` computes `pad = openWidth - utf8.RuneCountInString(s)`, clamps `>=0`,
  emits `"\r" + s + strings.Repeat(" ", pad)`, raises `openWidth` to the new
  width if larger — a high-water mark to fully erase a longer prior render
  (`printer.go:195-205`). No `\x1b[K`; clearing is by overwriting with spaces.
- `replaceLine`/`endOpenLine` finalize and reset (`printer.go:208-221`).

ANSI SGR escapes are multi-rune, zero-column → `utf8.RuneCountInString` would
over-count, inflating `pad` and desyncing the erase math. **Fix: keep the width
computation on the plain text and inject escapes after the width is known** (or
strip escapes before counting). The test helper `pad` (`printer_test.go:22-29`)
re-implements the same rune-count, so it must use the same width measure.

Construction: `NewPrinter(w io.Writer, clock sysdep.Clock, interactive bool)`
(`printer.go:58`). The writer + interactivity are **injected** — the printer
does no TTY probing. Call site: `internal/cli/run.go:95`
`progress.NewPrinter(app.Stderr, app.Clock, app.Terminal.IsStderrTerminal())`.
A color flag would be a 4th constructor arg (or a styler value).

Tests are **exact-string** `require.Equal` against a `bytes.Buffer`
(`printer_test.go`), no golden files. Adding escapes means escapes appear in the
expected strings; the `pad` helper and production must agree on a plain-width
measure or pad expectations diverge. Public surface a refactor touches:
`Printer` struct + `NewPrinter` + the methods `StepStart/StepDone/Line/Close` +
`Reporter` methods + the width helpers `openLine/rewrite/replaceLine/
endOpenLine/writeCounter/interrupt` + constants `glyphOK = "✓"`, `indent`.

### 4. TTY detection: `sysdep.Terminal` needs one new method

`internal/sysdep/terminal.go`: interface has exactly `IsInteractive()` (stdin)
and `IsStderrTerminal()` (`terminal.go:22-29`). `OSTerminal` delegates to a
shared `isTerminal(f *os.File)` doing the termios get ioctl
`unix.IoctlGetTermios(fd, unix.TIOCGETA)` via `golang.org/x/sys/unix`
(`terminal.go:39-50`) — **the same check `x/term` does, deliberately reusing the
existing `x/sys/unix` dep** (doc at `terminal.go:31-34`). There is **no stdout
method**. Reports go to stdout, progress to stderr, so the color decision is
**per-stream**: add `IsStdoutTerminal()` (delegating to `isTerminal(os.Stdout)`)
— **zero new dependency**.

Fake (`internal/sysdep/sysdeptest/terminal.go:9-22`): `FakeTerminal` with one
bool per method (`Interactive`, `StderrTerminal`); add `StdoutTerminal bool`.
Pattern to mirror (interface + `OS<Name>` + `var _ Iface = (*Impl)(nil)` + `Fake<Name>`)
is uniform across `sysdep` — see `Clock` (`internal/sysdep/clock.go:12-31`,
`sysdeptest/clock.go:13-28`) and `PathResolver`.

### 5. Cobra wiring: no persistent flags today; add `--color` on root

`App` struct (`internal/cli/cli.go:20-71`) holds every dependency including
`Stdout`/`Stderr` (`io.Writer`) and `Terminal` (`sysdep.Terminal`). Production
wiring in `Main()` (`cli.go:110-139`); `cmd/agent-creance/main.go:15-17` is just
`os.Exit(cli.Main())`.

Root command built in `newRootCmd(app *App)` (`cli.go:80-104`) — sets
`Use/Short/Silence*`, routes `root.SetOut/SetErr` to `app.Stdout/Stderr`, adds
subcommands. **No `PersistentFlags()` call anywhere.** Every existing flag is a
per-command local bound with `cmd.Flags().BoolVar(&x, …)` and read in `RunE`,
which passes it as an explicit param to a testable `runXxx(ctx, app, …)` body
(e.g. `doctor.go:22-33`, `import.go:28-41`). Commands close over `*App`
(`newXxxCmd(app *App) *cobra.Command`).

Idiomatic placement for `--color`: register
`root.PersistentFlags().StringVar(&colorMode, "color", "auto", …)` in
`newRootCmd`, and resolve it **once** in a root `PersistentPreRunE` (runs before
any subcommand `RunE`): `auto|always|never` + `NO_COLOR` + per-stream isatty →
write a resolved value onto a new `App` field (e.g. `App.Stdout`/`App.Stderr`
wrapped, or `App.Style` / `App.Color` enum). `newRootCmd` already closes over
`app`, so the PreRun can mutate it. This introduces the first persistent-flag /
PreRun mechanism in the codebase.

### 6. Two-mode golden strategy

Golden harness pattern (`-update` flag + map-of-variants loop) — the
plain-vs-color template is **doctor** (`internal/doctor/report_test.go:17-97`):
`goldenCases() map[string]Report` → `TestRender` loops, builds
`filepath.Join("testdata", "render_"+name+".golden")`, update-or-compare:

```go
got := Render(rep)
golden := filepath.Join("testdata", "render_"+name+".golden")
if *update { os.WriteFile(golden, []byte(got), 0o644); return }
want, _ := os.ReadFile(golden)
require.Equal(t, string(want), got)
```

To pin both modes: same loop with the mode in the key/path (e.g.
`render_<name>_plain.golden` + `render_<name>_color.golden`), running the
renderer with color **forced** on/off (tests are never a TTY, so colored mode
must be forced, not auto-detected). The **existing plain goldens stay
byte-identical** — that is the acceptance check for plain parity.

testscript (`.txtar`) coverage for the flag/env path: `env NO_COLOR=1`,
`env PATH=$CREANCE_BIN`, and substring `stdout '…'` assertions already exist
(`internal/cli/script_test.go:26-57`, `testdata/script/doctor_*.txtar`,
`version.txtar`). testscript stdout is **not** a TTY, so default output there is
already plain — a `NO_COLOR` / `--color=never` scenario fits naturally; a
`--color=always` scenario can assert that an escape byte appears.

**No existing ANSI-strip/normalize helper** — the repo achieves determinism by
construction (fixed PIDs/ports/timestamps in fixtures). So colored goldens are
produced by **forcing color on**, not by stripping after the fact.

### 7. Library vs. hand-rolled (web research)

- **NO_COLOR** (no-color.org): suppress when the var is present and non-empty,
  any value. The spec explicitly says **CLI args / config override `NO_COLOR`**
  — so `--color=always` beating `NO_COLOR` is spec-sanctioned. Precedence to
  implement (the ticket's stated scope, no `FORCE_COLOR`):
  `--color=always` → on; `--color=never` → off; `--color=auto` (default) → on
  iff `NO_COLOR` unset **and** the target stream isatty.
- **TTY detection:** `golang.org/x/term.IsTerminal(int(fd))` is the std-adjacent
  choice, **but the project already has its own termios probe** in
  `sysdep/terminal.go` — reuse it (no new dep at all).
- **Color lib vs hand-rolled:** for a *fixed* palette (green/yellow/red + bold +
  dim), a hand-rolled SGR helper is ~20 lines, **zero deps**, and the **only**
  option that guarantees the disabled path emits today's exact bytes (byte-for-
  byte plain). `fatih/color` works and honors `NO_COLOR`/isatty but adds
  `go-isatty` + `go-colorable`; `termenv` can leave bold/dim attributes at its
  Ascii profile (risking non-zero plain bytes); `lipgloss` is TUI-grade /
  over-scoped. SGR codes: `1`=bold, `2`=dim, `31`=red, `32`=green, `33`=yellow,
  `0`=reset.
- **Width with escapes:** strip with `\x1b\[[0-9;]*m` before counting, **or**
  (more robust) compute pad from the plain string and inject escapes after —
  escapes never enter the width path.
- **Colored goldens:** commit **raw escape bytes** via `-update` (matches the
  repo), one plain + one colored golden per case. Trade-off: raw-escape diffs
  are noisy to review (the repo already mandates "review the golden diff").

## Code references

- `internal/doctor/report.go:105` — `Render`; `:107` embeds prereq; `:128-130`
  `line` primitive; `:20-24` glyph constants; `:109-118` section headers;
  `:132-182` glyph selection + mid-message pid/port detail.
- `internal/prereq/report.go:20` — `Report`; `:36-40` row format (byte-`len`
  alignment); `:65` "tested against"; `:69-82` `statusField`; `:42-46` paragraph.
- `internal/status/report.go:31` — `Render` (tabwriter); `:37` header row;
  `:57-77` `stateLabel`/`portLabel`.
- `internal/policy/render/render.go:39,122,193` — `Show`/`Explain`/`Refresh`;
  `:27-34` markers; `:43,51` block headers; `:128-130` decision word.
- `internal/progress/printer.go:21-26` no-ANSI rule; `:188-205` width math;
  `:58` `NewPrinter`; `internal/progress/printer_test.go:22-29` `pad` helper.
- `internal/sysdep/terminal.go:22-50` `Terminal` + termios probe;
  `internal/sysdep/sysdeptest/terminal.go:9-22` `FakeTerminal`.
- `internal/sysdep/clock.go:12-31` + `sysdeptest/clock.go:13-28` — seam template.
- `internal/cli/cli.go:20-71` `App`; `:80-104` `newRootCmd` (no persistent
  flags); `:110-139` `Main()`. `internal/cli/doctor.go:22-33` flag pattern.
- `internal/cli/run.go:95` printer construction; `:214,238,277` inline `⚠`.
  `init.go:134`, `setup.go:74-144`, `import.go:101`, `mutate.go:114`,
  `version.go:19-24` — inline glyphs / dim candidates.
- `internal/doctor/report_test.go:17-97` golden harness (map-of-variants);
  `internal/cli/script_test.go:26-57` testscript harness;
  `testdata/script/doctor_*.txtar`, `version.txtar` — `.txtar` env/substring.

## Architecture insight

The repo already isolates *what to print* (pure renderers + `Reporter` events)
from *where it goes* (the command layer owns `app.Stdout/Stderr`). AC-0052 adds a
third axis — *how it looks* — and the clean fit is a **pure styler value** that
renderers consult, with the single OS-touching decision (is-this-stream-a-tty)
behind the existing `sysdep.Terminal` seam and the policy decision (`--color` +
`NO_COLOR` + isatty) resolved once at the cobra root. Everything downstream of
that resolved boolean is pure logic → table/golden testable, and the disabled
styler is the identity function → byte-identical plain output for free. The
width-before-inject discipline in the progress printer is the one place where
"how it looks" and "how it's laid out" intersect and must be kept separate.

## Related tickets / docs

- **AC-0041** (`thoughts/shared/tickets/AC-0041-run-progress-output.md`,
  research + plan dated 2026-06-11) — built the progress printer and the
  interactive/non-interactive `\r` behavior this ticket must preserve.
- **AC-0009** (`AC-0009-sysdep-seam-extensions.md`) — precedent for extending the
  `sysdep` seam (the pattern `IsStdoutTerminal()` would follow).
- No prior thoughts document targets CLI color/styling specifically.

## Open questions for the checkpoint

1. **Dependency:** hand-rolled SGR styler (zero deps, guaranteed byte-identical
   plain, fits project ethos — recommended) vs. `fatih/color` (de-facto std,
   handles NO_COLOR/isatty, +2 transitive deps).
2. **Dim granularity:** dim only cleanly-separable trailing/parenthetical
   secondary segments (pids/ports/durations/"tested against X"), leaving
   mid-sentence prose uncolored (less format-string churn — recommended) vs.
   restructure messages to isolate and dim every secondary token.
3. **Colored golden files:** commit raw escape bytes via `-update` (matches the
   repo's `-update` convention — recommended) vs. a human-readable escaped
   representation decoded in the harness (cleaner diffs, more test machinery).
