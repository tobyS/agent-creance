---
date: 2026-06-11
researcher: Claude (work pipeline)
git_commit: 6a1e6d4b23a2499075d2f79ef7ccc033b86759e5
branch: main
repository: git@github.com:tobyS/agent-creance.git
topic: "AC-0041: progress and status output for the run command"
tags: [research, codebase, AC-0041, cli, run, compile, generator, registry, output, ux]
status: complete
last_updated: 2026-06-11
---

# Research: AC-0041 — Progress and status output for the `run` command

**Ticket:** `thoughts/shared/tickets/AC-0041-run-progress-output.md`

## Research question

Where and how can `run` announce its major steps, show live per-dependency
progress during registry lookups, set expectations before slow work, and report
per-step durations — without violating the project's sysdep-injection testing
conventions and output style?

## Summary of findings

- `runRun` (`internal/cli/run.go:49-160`) is 11 sequential steps with **zero
  happy-path output**. All steps complete *before* the agent subprocess starts,
  so progress output cannot collide with the agent owning the terminal.
- The slow step is policy compilation (step 5, `run.go:89-96`): on an
  input-hash cache miss, one sequential HTTP lookup per direct dependency per
  manifest. The compiler, generator, and registry layers have **no callback or
  observer mechanism** — all reporting today is post-hoc via returned structs.
- The codebase's one precedent for injecting an output sink into a logic layer
  is `proxy.NewManager(..., warn io.Writer)` (`internal/proxy/lifecycle.go:66`,
  wired with `app.Stderr` at `run.go:119`). A progress reporter seam should
  mirror this shape.
- Per-manifest dependency counts are known **inside** `generator.generate`
  (after `eco.deps(manifest)` at `internal/generator/generator.go:165`), not in
  the compile layer. Accurate counts are therefore per-manifest as each
  generator starts, not one upfront total.
- Output conventions: pure `func(...) string` renderers + one `fmt.Fprint` in
  the command; glyphs `✓ ⚠ ✗`, Unicode `…`, two-space indents; **no colors, no
  spinners, no `\r` in-place updates anywhere today**. `setup` is the only
  "Doing X… / ✓ done" precedent (`internal/cli/setup.go:54-81`).
- Durations: `sysdep.Clock` (`Now`/`Since`) is already on `App` and already
  passed to the compiler (`run.go:89`); `sysdeptest.FakeClock.Advance` makes
  durations deterministic in tests.

## Detailed findings

### 1. The `run` flow and its current output (`internal/cli/run.go`)

`newRunCmd` delegates to `runRun(ctx, app, ".")` (`run.go:35-44`). The 11 steps:

| # | Lines | Step | Output today |
|---|-------|------|--------------|
| 1 | 52-57 | Prereq check (`prereq.Check`) + skew warning | Missing-tool block → Stdout (:54); skew `⚠ …` lines → Stderr (`warnVersionSkew`, :165-172) |
| 2 | 61-68 | Setup verification (CA + skill) | Refusal message → Stdout (:66) |
| 3 | 72-79 | Credential check (Keychain) | Refusal message → Stdout (:77) |
| 4 | 82-85 | State layout resolution | none |
| 5 | 89-96 | **Policy compile** (`compile.New(app.FS, app.Paths, app.Clock, app.HTTP)` :89; `Compile(ctx, dir)` :93) | none — the slow step |
| 6 | 99-102 | Config load | none |
| 7 | 106-108 | Profile (`network.sb`) compile | none |
| 8 | 111-114 | Enforcer extraction | none |
| 9 | 119-134 | Proxy attach (`proxy.NewManager(..., app.Stderr)` :119) | warn-only lines → Stderr (`lifecycle.go:383-390`; teardown warning :132) |
| 10 | 138-152 | Cage build/prepare | none |
| 11 | 156-159 | Agent run (blocking) | child owns the terminal |

Any returned error is printed once as `error: …` to Stderr by `cli.Main()`
(`internal/cli/cli.go:126-130`); cobra's own output is suppressed
(`cli.go:79-84`).

**Agent handoff is a subprocess, not exec(2):** `cage.NewRunner(app.ProcessGroup).Run`
(`internal/cage/run.go:53-85`) starts the child with `Setpgid` and wires it
directly to the parent's terminal (`internal/sysdep/processgroup.go:57-74`,
`cmd.Stdin/Stdout/Stderr = os.Stdin/...` at :66). The CLI stays alive,
forwarding signals. **All progress output happens in steps 1-10, strictly
before the child starts** — no interleaving risk. The only post-agent CLI
activity is the deferred proxy `Detach` (`run.go:130-134`).

### 2. App seams relevant to this feature (`internal/cli/cli.go`)

- `Stdout io.Writer`, `Stderr io.Writer` (`cli.go:22-23`) — all command output
  goes through these, never `os.Stdout` directly.
- `Clock sysdep.Clock` (`cli.go:39`; interface `internal/sysdep/clock.go:12-18`
  with `Now()`/`Since()`; fake `sysdeptest.FakeClock` with `Advance(d)` at
  `internal/sysdep/sysdeptest/clock.go:13-28`). Already consumed by `run`
  (passed to the compiler).
- `Terminal sysdep.Terminal` (`cli.go:32`; `internal/sysdep/terminal.go:20-37`)
  — single method `IsInteractive() bool` probing **stdin** via
  `unix.IoctlGetTermios` (deliberate choice to avoid `golang.org/x/term`).
  Currently used only by `init`. There is **no stdout/stderr TTY probe**; if
  in-place `\r` counters are wanted, the seam needs a stdout/stderr variant
  (same ioctl, different fd) — a small, convention-conforming extension.

### 3. The compile layer (`internal/policy/compile/compile.go`)

- Construction: plain constructor, no options —
  `New(fsys sysdep.FileSystem, paths sysdep.PathResolver, clock sysdep.Clock, getter sysdep.HTTPGetter) (*Compiler, error)`
  (`compile.go:118-140`). `Compiler` fields are unexported
  (`compile.go:109-114`); compile tests build the struct via literal
  (`compile_test.go:67-72`), so a new field (progress sink) is directly
  settable in tests.
- `resolve(projectDir)` (`compile.go:187-233`) produces `compileInputs` with
  the **full manifest list** (`[]manifestInput{gen resolvedGenerator, data}`,
  `compile.go:65-68`) and input hash — known *before* the cache gate and before
  any generator runs. Absent manifests are silently skipped
  (`readManifests`, `compile.go:395-408`). Dependency counts are **not** known
  here (parsing happens inside the generator).
- Cache gate (`Compile`, `compile.go:238-258`): hit (:246-247: existing
  `policy.json` parses, version matches, `InputHash` matches) → returns
  `Result{Skipped: true}` with **zero generator work and the artifact
  untouched** (mtime preserved so the proxy doesn't hot-reload). Miss →
  `build` → `buildRuleSet` → `runGenerators`.
- `runGenerators` (`compile.go:481-502`): plain sequential loop over
  `in.manifests` (:483) calling `c.runner.Run(ctx, m.gen.Type, m.data)` (:484)
  through the unexported seam
  `generatorRunner { Run(ctx, name, manifest) ([]generator.Rule, error); Invalidate(...) }`
  (`compile.go:74-80`). Production impl `realGenerators` (`compile.go:84-106`)
  constructs `generator.New(...)` per call. Multi-manifest monorepos: one
  `Run` per manifest (`monorepo_test.go:47-73`).
- **The compiler cannot distinguish a generator-output cache hit from a full
  registry walk** — both are just `runner.Run` returning rules. That
  distinction lives inside `generator.Generate`.
- `Result{PolicyPath, InputHash string; Skipped bool; AllowCount, DenyCount int}`
  (`compile.go:144-150`) — `run` consumes only `InputHash` today (`run.go:124`).

### 4. The generator layer (`internal/generator/generator.go`)

- `Generate(ctx, manifest)` (`generator.go:98-114`): output-cache check first —
  `<generatorsRoot>/<eco>/<sha256(manifest)>.json`, content-addressed, no TTL
  (`cache.go:31-39`). Hit → cached rules, **zero lookups**. Miss →
  `generate(ctx, manifest)`.
- `generate` (`generator.go:164-187`): `deps, err := g.eco.deps(manifest)` at
  **:165** — **total dependency count is known here, before the loop**. Then
  `for _, pkg := range deps` (:170-186) with `g.lookup.Lookup(ctx, pkg)` at
  :171 (sequential; `ErrNotFound` skipped, other errors abort).
- Constructors: exported
  `New(name string, fs, clock, getter, registriesRoot, generatorsRoot string) (*Generator, error)`
  (`generator.go:79-88`); unexported `newGenerator(eco, lookup, fs, generatorsRoot)`
  (`generator.go:90-92`) used by tests with `fakeLookuper`
  (`generator_test.go:26-52`).
- The `lookuper` interface (`generator.go:17-21`) is unexported with two
  implementers (`registry.Client`, test fake) — easy to leave untouched: the
  generator itself can emit per-dependency progress events around the Lookup
  call without registry changes.

### 5. The registry layer (`internal/generator/registry/registry.go`)

- `Lookup` (`registry.go:118-138`): per-package disk cache
  (`<registriesRoot>/<npm|packagist>/<pkg>.json`), "fresh" = younger than
  30 days (`refreshInterval`, :35; age via injected Clock, not mtime). Fresh
  hit → no network; otherwise one GET via `sysdep.HTTPGetter`.
- **Cache-hit vs network is invisible to callers** — `Lookup` returns only
  `(Metadata, error)`. A per-package counter that ticks per *lookup* (fast on
  disk-cache hits, slow on network) needs no registry change; distinguishing
  hit/miss in output would require an API change and is not needed for the
  ticket's acceptance criteria.

### 6. Output conventions in this codebase

- **Rendering style:** pure `func(...) string` in a logic package; the command
  does one `fmt.Fprint(app.Stdout, ...)`. Pinned by golden files with
  `-update` (`internal/prereq/report_test.go:21-49`; multi-case variant
  `internal/doctor/report_test.go:84-93`; regenerate via `make golden`).
  Note: a *live* progress reporter inherently can't be a pure
  accumulate-then-render function; `internal/policy/render/render.go:6-11`
  documents that the render package is deliberately pure (no I/O), so live
  progress cannot live there.
- **Visual style:** glyph constants `✓ ⚠ ✗` ("kept as constants so the golden
  file and the code agree on exact bytes", `internal/prereq/report.go:8-14`),
  two-space indents, `Section:` headers, Unicode `…`/`—`, manual `%-*s` column
  alignment (`render.go:69-71`: "no tabwriter in this codebase", except
  `status`). **No ANSI colors, no spinners, no `\r` anywhere.**
- **Step-announcement precedent:** `setup` (`internal/cli/setup.go:54-81`)
  prints `"Checking whether the mitmproxy CA is already trusted…"` then
  `"✓ CA installed and verified."` — to **Stdout**. Mutating commands print
  single `✓ …` confirmations (`mutate.go:114`, `init.go:81`).
- **Stdout vs Stderr convention:** Stdout = primary output, reports, step
  confirmations, actionable refusal prose (`run.go:54,66,77`). Stderr =
  warnings accompanying otherwise-succeeding work (`warnVersionSkew`
  `run.go:168`, proxy teardown `run.go:132`, proxy restart
  `lifecycle.go:383-390`) and the final `error: …` line. Long-lived components
  receive Stderr as injected `warn io.Writer` (`proxy.NewManager`,
  `run.go:119`). Several testscripts assert `! stderr .` on clean runs
  (`version.txtar:9`, `policy_show.txtar:16`).
- **Toggles:** `--json` exists on policy subcommands (paired pure
  `...JSON` renderers); no `NO_COLOR`/`--quiet`/`--verbose` anywhere
  (ticket scopes these out).
- **docs/design.md guidance:** refusals actionable, no stack traces (:331-340);
  verbosity tiering for skew warnings (:346-357, "noise during normal work"
  rationale at :372). No dedicated UX section; nothing prohibiting progress
  output.

### 7. Testing landscape for this feature

- **Compile:** `fakeRunner` with ordered event `log` (`compile_test.go:37-54`)
  — exactly the idiom for asserting per-manifest progress-event sequences and
  "no events on cache hit". Compiler built by struct literal in tests, so a
  progress-sink field is trivially injectable.
- **Generator:** `fakeLookuper` (`generator_test.go:26-52`) + unexported
  `newGenerator` — natural home for "emits N-package start + per-dependency
  ticks" tests.
- **CLI:** `run_test.go` fixture (`newRunFixture`, :52-109) drives `runRun`
  against a fully faked `App` with `bytes.Buffer` writers and
  `FakeClock` (:97) — the place to assert the full rendered step sequence.
  testscript **cannot** exercise the registry path (HTTP seam not stubbable
  from txtar — noted in `policy_refresh.txtar:4-6`); the only run txtar is
  `run_missing_prereq.txtar`. Live-counter rendering (a small stateful
  printer) fits a table-driven or golden unit test in its own package.
- **External tools never invoked in unit tests**; durations must come from
  `app.Clock` (convention), with `FakeClock.Advance` between fake steps if
  per-step durations need to be non-zero in tests. Note `FakeClock` is frozen
  — durations in unit tests will render as `0s`-ish unless the fake is
  advanced by the code path under test (it isn't, today) or the test asserts
  on format rather than value.

### 8. Answers to the ticket's "Questions for Research/Planning"

1. **TTY degrade for live counters:** No `\r`/in-place updates exist today; the
   `sysdep.Terminal` seam probes stdin only. Two viable shapes: (a) extend the
   Terminal seam with a stdout/stderr interactivity probe and use `\r`
   in-place counters when interactive, falling back to coarse milestone lines;
   (b) skip `\r` entirely and print append-only per-manifest lines (start line
   with count + completion line with duration), possibly with periodic
   milestone ticks. (b) is materially simpler and closer to existing style;
   (a) matches the ticket's "live counter" wording most literally. **→ user
   decision at checkpoint.**
2. **Stdout or Stderr:** Codebase convention puts step announcements on Stdout
   (`setup` precedent) and warnings on Stderr. Counter-argument: `run`'s real
   output is the agent session itself; progress is meta-information, and
   `! stderr .` assertions exist only for *other* commands' scripts. Both are
   defensible. **→ user decision at checkpoint.**
3. **Where progress hooks fit:** A new seam is required (no existing
   observer/callback anywhere in compile/generator/registry). The established
   shape is constructor-injected sinks (`proxy.NewManager`'s `warn io.Writer`).
   Concretely: define a small progress-reporter interface (e.g. in a new
   `internal/policy/progress` package or alongside compile), add it as a
   parameter/field on `compile.New` and thread it through `realGenerators` →
   `generator.New`/`newGenerator`. The unexported `generatorRunner` and
   `lookuper` seams have exactly two production implementers each, so widening
   construction is cheap. Registry layer needs **no change**.
4. **Upfront accurate count:** Not possible before manifest parsing; dependency
   counts surface inside `generator.generate` after `eco.deps` (:165). The
   honest design: per-manifest "N packages" announcement as each generator
   starts (plus the compiler announcing the manifest list upfront from
   `compileInputs.manifests`, which *is* known before work begins).
5. **Interaction with version-skew warnings:** Skew warnings print in step 1,
   strictly before any progress output from step 5+; no interleaving risk
   regardless of stream choice. If progress goes to Stderr, both share the
   stream but at different times.

## Code references (key)

- `internal/cli/run.go:49-160` — `runRun` 11-step flow
- `internal/cli/run.go:165-172` — `warnVersionSkew` (Stderr)
- `internal/cli/cli.go:20-66` — `App` struct; `:104-132` — `Main` wiring
- `internal/policy/compile/compile.go:74-80` — `generatorRunner` seam
- `internal/policy/compile/compile.go:118-140` — `compile.New`
- `internal/policy/compile/compile.go:187-233` — `resolve` (manifest list known here)
- `internal/policy/compile/compile.go:238-258` — cache gate (`Skipped: true`)
- `internal/policy/compile/compile.go:481-502` — sequential `runGenerators`
- `internal/generator/generator.go:98-114` — `Generate` (output cache first)
- `internal/generator/generator.go:164-187` — per-dependency loop; count at :165
- `internal/generator/registry/registry.go:118-138` — `Lookup` (cache/network collapsed)
- `internal/proxy/lifecycle.go:64-68,383-390` — injected `warn io.Writer` precedent
- `internal/sysdep/clock.go:12-18`, `internal/sysdep/sysdeptest/clock.go:13-28` — Clock seam
- `internal/sysdep/terminal.go:20-37` — stdin-only TTY probe
- `internal/cli/setup.go:54-81` — "Doing X… / ✓ done" precedent
- `internal/policy/render/render.go:193-211` — post-hoc refresh summary (pure)
- `internal/cli/run_test.go:52-109` — run fixture (buffers + fakes)
- `internal/cli/testdata/script/run_missing_prereq.txtar` — only run txtar (and why)

## Related thoughts documents

- `thoughts/shared/research/2026-06-07-AC-0025-run-command.md` — run flow origin
- `thoughts/shared/research/2026-06-09-AC-0036-monorepo-multi-manifest-generators.md` — why many lookups per run
- `thoughts/shared/research/2026-06-05-AC-0011-registry-clients-cache.md` — registry cache design
- `thoughts/shared/research/2026-06-05-AC-0013-policy-compiler.md` — compile pipeline
- `thoughts/shared/research/2026-06-07-AC-0028-setup-command.md` — output convention note (progress to Stdout "as doctor/run do")
- `thoughts/shared/research/2026-06-11-AC-0040-npm-packument-too-large.md` — freshest registry-lookup behavior

## Open questions (for the checkpoint)

1. Live counter rendering: in-place `\r` updates when the output stream is a
   terminal (new stdout/stderr TTY probe on the Terminal seam) with
   append-only fallback — or append-only milestone lines everywhere
   (simpler, no new seam, no in-place precedent broken)?
2. Stream choice: progress/status to Stdout (matches `setup` precedent and
   "primary output" convention) or Stderr (keeps Stdout clean; progress as
   meta-information for a command whose real output is the agent session)?
