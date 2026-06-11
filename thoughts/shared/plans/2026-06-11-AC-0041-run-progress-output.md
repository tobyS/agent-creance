---
date: 2026-06-11
ticket: AC-0041
research: thoughts/shared/research/2026-06-11-AC-0041-run-progress-output.md
git_commit: 0567a05
branch: main
status: approved
---

# Plan: AC-0041 — Progress and status output for the `run` command

## Overview

`run` currently prints nothing on the happy path. On a first run the policy
compiler performs one sequential HTTP registry lookup per direct dependency per
manifest, leaving the terminal frozen for minutes. This plan adds live
progress/status output to stderr: step announcements with durations, an
expectation message before network-heavy work, and an in-place per-dependency
counter per manifest (with an append-only fallback when stderr is not a
terminal).

**Decisions from the checkpoint (user-approved):**
- Counter style: in-place `\r` updates when stderr is a TTY; append-only
  milestone lines (25/50/75%) when piped/CI.
- Stream: all progress goes to **stderr** (run's real product is the agent
  session; matches git/curl convention).

## Current state

- `runRun` (`internal/cli/run.go:49-160`): 11 sequential steps, zero happy-path
  output. All steps finish before the agent subprocess takes the terminal.
- Compiler: `compile.New(fs, paths, clock, getter)` (`compile.go:118-140`);
  cache gate returns `Result{Skipped: true}` on input-hash hit
  (`compile.go:238-258`); sequential `runGenerators` loop (`compile.go:481-502`)
  through the unexported `generatorRunner` seam (`compile.go:74-80`).
- Generator: `Generate` checks the output cache (zero lookups on hit,
  `generator.go:98-114`); on miss `generate` parses deps (count known at
  `generator.go:165`) then loops `Lookup` per dependency (`generator.go:170-186`).
- No observer/callback pattern exists anywhere in compile/generator/registry.
  Precedent for an injected output sink: `proxy.NewManager(..., warn io.Writer)`
  (`lifecycle.go:66`, wired with `app.Stderr` at `run.go:119`).
- TTY probe: `sysdep.Terminal.IsInteractive()` checks **stdin only**
  (`terminal.go:20-37`).
- Output style: glyphs `✓ ⚠ ✗`, two-space indent, Unicode `…`/`—`, no ANSI
  colors, no `\r` anywhere yet.

## Desired end state

First run (interactive terminal), monorepo with 3 manifests:

```
Compiling egress policy…
  inputs changed (first run or updated config/manifest) — fetching package
  metadata from packagist/npm; results are cached for future runs
  backend/composer.json: looking up 42/87 packages…      ← single line, \r-updated
  ✓ backend/composer.json: 87 packages (41.2s)
  ✓ backoffice/composer.json: 64 packages (30.1s)
  ✓ frontend/package.json: 132 packages (1m3s)
✓ Egress policy compiled: 412 allow, 3 deny (2m14s)
✓ Sandbox profile compiled (0s)
Starting egress proxy…
✓ Egress proxy ready on port 8081 (0.4s)
Launching agent…
```

Cached run (compact):

```
Compiling egress policy…
✓ Egress policy up to date (cached) (0s)
✓ Sandbox profile compiled (0s)
Starting egress proxy…
✓ Egress proxy ready on port 8081 (0.2s)
Launching agent…
```

Non-TTY stderr: identical except the counter line becomes milestone lines
(`backend/composer.json: looking up 87 packages…` then `backend/composer.json:
22/87…`, `44/87…`, `65/87…` at ~25/50/75%), and in-place step completion is
plain appended lines. Generator output-cache hits render as
`✓ <path>: rules cached (0s)`. When a step fails, its announcement line
precedes the `error: …` line, giving phase context (AC bullet 7).

## What we're NOT doing (ticket out-of-scope)

- No parallelization/perf changes to lookups (follow-up ticket).
- No progress for `policy refresh/show/explain`, `setup`, `doctor`, or the
  allow/deny mutate commands — they pass a nil reporter and stay as today.
- No `--quiet`/`--verbose` flags, no colors, no spinner.
- No behavioral change to compilation, caching, or generator logic.

## Implementation approach

A new leaf package `internal/progress` (imports only stdlib + `sysdep`):

1. **`Reporter` interface** — events emitted by compile/generator:

```go
type ManifestRef struct {
    Type string // generator name, e.g. "composer_json"
    Path string // project-relative manifest path
}

type Reporter interface {
    BuildStart(manifests []ManifestRef) // input-hash miss; rules will be generated
    ManifestStart(m ManifestRef)        // before a generator runs
    LookupsStart(n int)                 // generator output-cache miss; n lookups ahead
    LookupDone(i, n int)                // after each registry lookup (1-based)
    ManifestCached()                    // generator output-cache hit (zero lookups)
    ManifestDone()                      // generator returned
}
```

2. **`Nop`** implementation; consumers normalize nil → Nop in constructors.

3. **`Printer`** — the single stateful renderer implementing `Reporter` plus a
   step API used directly by `runRun`:

```go
func NewPrinter(w io.Writer, clock sysdep.Clock, interactive bool) *Printer

func (p *Printer) StepStart(text string)  // "text…"
func (p *Printer) StepDone(text string)   // "✓ text (dur)"; in place if nothing intervened
func (p *Printer) Line(text string)       // plain announce line (e.g. "Launching agent…")
func (p *Printer) Close()                 // idempotent; terminates any open \r line
```

   Mechanics: track an "open line" + its rendered width. Interactive: open
   lines are rewritten via `\r` + space-padding to the previous width (no ANSI
   escapes — codebase has none). If other output intervenes between
   `StepStart`/`StepDone`, the open line is first terminated with `\n` and the
   `✓` completion is appended instead. Non-interactive: every write is an
   appended full line; `LookupDone` emits milestone lines at ~25/50/75% only
   (skip when n < 8, dedupe thresholds, never at i == n). Durations measured
   with `clock.Now()`/`Since` between Start/Done events; formatted via a
   `formatDuration` helper (`d.Round(time.Second)` for d ≥ 10s, else
   `d.Round(100*time.Millisecond)`, rendered with `Duration.String()`).
   `BuildStart` prints the indented expectation message, deriving registry
   names from manifest types (composer_json → packagist, package_json → npm).

4. **Threading** (mirrors the `proxy.NewManager` injected-sink precedent):
   - `compile.New` gains a `rep progress.Reporter` parameter (nil → Nop),
     stored on `Compiler` and forwarded by `realGenerators` to `generator.New`.
   - `Compile` emits `BuildStart` on cache miss (no events on `Skipped` hit);
     `runGenerators` brackets each `runner.Run` with `ManifestStart`/`ManifestDone`.
   - `generator.New`/`newGenerator` gain the reporter; `Generate` emits
     `ManifestCached` on output-cache hit; `generate` emits `LookupsStart(n)`
     after `eco.deps` and `LookupDone(i, n)` after each `Lookup` (including
     `ErrNotFound` skips — the lookup completed).
   - Registry layer: **no changes** (per-package cache-hit/network distinction
     not needed; cached lookups just tick fast).

5. **`runRun` wiring**: build the Printer from `app.Stderr`, `app.Clock`,
   `app.Terminal.IsStderrTerminal()`; `defer prog.Close()`. Announce steps 5
   (policy, with `Skipped`/counts in the done line), 7 (profile), 9 (proxy,
   port in done line), and 11 (`Launching agent…` plain line). Steps 1-4, 6,
   8, 10 stay silent (cheap checks; AC permits). Pass the printer to
   `compile.New` as the reporter.

6. **Terminal seam extension**: add `IsStderrTerminal() bool` to
   `sysdep.Terminal` (`OSTerminal`: same `unix.IoctlGetTermios` ioctl on
   `os.Stderr.Fd()`; `sysdeptest.FakeTerminal` gains a `StderrTerminal bool`
   field). Run fixtures must set `Terminal` (nil interface would panic).

---

## Phase 1: `internal/progress` package + Terminal seam extension

### Changes

1. `internal/sysdep/terminal.go` — add `IsStderrTerminal() bool` to the
   `Terminal` interface + `OSTerminal` impl (ioctl on `os.Stderr.Fd()`,
   mirroring the stdin probe and its doc comment).
2. `internal/sysdep/sysdeptest/terminal.go` — `FakeTerminal` gains
   `StderrTerminal bool` + method (zero value keeps existing tests valid).
3. New `internal/progress/progress.go` — `ManifestRef`, `Reporter`, `Nop`,
   `OrNop(Reporter) Reporter`.
4. New `internal/progress/printer.go` — `Printer` as specified above
   (`✓` glyph constant per the report.go convention comment).
5. New `internal/progress/printer_test.go` — table-driven byte-exact tests
   over `bytes.Buffer` + `sysdeptest.FakeClock` (advance between events for
   non-zero durations): interactive step rewrite, interrupted-step append,
   counter `\r` sequence + padding, non-interactive milestones (incl. n < 8 →
   none), cached manifest line, `BuildStart` expectation message (both
   registries / single registry), `Close` idempotence, Nop does nothing.

### Success criteria

- [x] Automated: `make test` green; `go build ./...`; `make lint` clean.
- [x] Manual: none (pure new package; behavior asserted byte-exact in tests).

---

## Phase 2: Thread `Reporter` through compile & generator

### Changes

1. `internal/generator/generator.go` — `New(...)` and `newGenerator(...)` gain
   `rep progress.Reporter` (normalized via `OrNop`); `Generate` emits
   `ManifestCached` on cache hit; `generate` emits `LookupsStart`/`LookupDone`.
2. `internal/policy/compile/compile.go` — `New(...)` gains
   `rep progress.Reporter`; `Compiler` + `realGenerators` carry it;
   `Compile` emits `BuildStart(in.manifests as []ManifestRef)` before
   `c.build` on cache miss only; `runGenerators` emits
   `ManifestStart`/`ManifestDone` around each `runner.Run`.
   (`Refresh` reuses `c.build`, so a non-nop reporter would also report there —
   harmless; all current `Refresh` callers pass nil.)
3. Update every `compile.New` / `generator.New` call site to pass `nil`
   for now (grep; expected: `internal/cli/run.go:89`, `internal/cli/policy.go`
   ×2 incl. `resolvePolicy`, `internal/cli/mutate.go` if it recompiles, plus
   any tests using `New` e.g. `TestRefresh_RealStackRefetchesRegistry`).
4. Tests: recording fake reporter (ordered event log, same idiom as
   `fakeRunner.log`) in both `compile_test.go` and `generator_test.go`:
   - compile cache hit → zero events; miss → `BuildStart` + per-manifest
     Start/Done in config order (monorepo case: one pair per manifest).
   - generator output-cache hit → `ManifestCached` only; miss → `LookupsStart(n)`
     + n `LookupDone` ticks (ErrNotFound still ticks).

### Success criteria

- [x] Automated: `make test` green; `go build ./...`; `make lint` clean.
- [x] Manual: `agent-creance policy refresh` output unchanged (nil reporter; covered by the untouched policy_refresh.txtar and render tests).

---

## Phase 3: Wire the printer into `runRun`

### Changes

1. `internal/cli/run.go`:
   - Construct `prog := progress.NewPrinter(app.Stderr, app.Clock,
     app.Terminal.IsStderrTerminal())` after the precondition gates (steps
     1-3 stay silent); `defer prog.Close()`.
   - Step 5: `prog.StepStart("Compiling egress policy")`; pass `prog` to
     `compile.New`; done line from `Result`: `Egress policy up to date
     (cached)` when `Skipped`, else `Egress policy compiled: %d allow, %d
     deny`.
   - Step 7: `StepStart("Compiling sandbox profile")` / `StepDone("Sandbox
     profile compiled")`.
   - Step 9: `StepStart("Starting egress proxy")` / `StepDone(fmt.Sprintf(
     "Egress proxy ready on port %d", att.Port))`.
   - Step 11: `prog.Line("Launching agent…")` immediately before
     `runner.Run` (after `prog.Close()` ordering is irrelevant — Line is a
     plain `\n`-terminated write; Close stays deferred for error paths).
2. `internal/cli/run_test.go` — fixture: set `Terminal:
   sysdeptest.FakeTerminal{}` (non-interactive default so assertions are
   plain lines); happy-path test asserts the step sequence on the stderr
   buffer (cached + compile variants as the fixture allows); confirm existing
   `strings.Contains` assertions still pass.
3. Check testscript impact: `run_missing_prereq.txtar` fails at step 1 —
   before any progress output — so `stdout`/exit assertions hold; `version`/
   `policy_*` scripts untouched (nil reporters).
4. `docs/design.md` — add a short paragraph in the `run` section describing
   the progress output (steps + durations to stderr, in-place counter on TTY,
   milestone fallback), consistent with the verbosity-tiering prose at
   design.md:346-372.
5. Ticket: tick acceptance-criteria boxes, set status to Done, note final
   decisions under Notes & Updates.

### Success criteria

- [x] Automated: `make test` green; `go build ./...`; `make lint` clean.
  Integration tests for generator/compile pass against live registries; the
  verify cage battery could not run in this session ($HOME writes denied by
  the environment — unrelated to this change).
- [ ] Manual: in a real monorepo, first `agent-creance run` shows the
  expectation message + live counters; second run shows the compact cached
  sequence; `2>/dev/null` hides progress; piping stderr yields milestone
  lines, no `\r`. (Left for the user — needs a real project + keychain.)

---

## Testing strategy

- **Unit (byte-exact)**: `internal/progress` printer tests over buffers +
  `FakeClock` — the rendering contract lives here.
- **Event-sequence**: recording reporters in compile/generator tests (ordered
  log idiom) — the "when do events fire" contract, incl. cache-hit silence.
- **CLI**: `run_test.go` fixture asserts the assembled stderr step sequence
  with fakes (FakeClock frozen → durations render as `0s`, asserted as such).
- **Hermetic**: no new external-tool or network use anywhere; testscript files
  unaffected (HTTP seam unstubable from txtar — registry path stays
  unit-tested, per `policy_refresh.txtar:4-6`).

## References

- Ticket: `thoughts/shared/tickets/AC-0041-run-progress-output.md`
- Research: `thoughts/shared/research/2026-06-11-AC-0041-run-progress-output.md`
- Injected-sink precedent: `internal/proxy/lifecycle.go:64-68`
- Step-output precedent: `internal/cli/setup.go:54-81`
- Glyph/format conventions: `internal/prereq/report.go:8-14`,
  `internal/policy/render/render.go:69-71`
