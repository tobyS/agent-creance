---
date: 2026-06-19
ticket: AC-0053
title: "Plan — hot-reload the source config during a run session"
branch: main
commit: 1b3320a
research: thoughts/shared/research/2026-06-19-AC-0053-config-hot-reload.md
status: draft
---

# AC-0053 Implementation Plan: hot-reload the source config during a run session

## Overview

While a cage is running, an edit to the project config (`.agent-creance.yaml`) or
any file in its resolved include graph (included fragments + the global baseline)
must automatically recompile `policy.json`. The enforcer's existing 1-second
mtime poll then applies it to the live proxy — no restart. An invalid edit leaves
the enforced policy unchanged (last-good), prints a warning, and a later valid
save recompiles and reloads. The watcher runs **only during an active run
session** and is torn down cleanly.

The delivery half already works end to end (the compiler's atomic write + the
enforcer's mtime poll — used today by `allow`/`deny`/`import`). This ticket adds
the missing **trigger**: a file-watch loop that drives the same recompile from a
hand-edit. Per the research checkpoint, the watch mechanism is **fsnotify**,
introduced behind a new `internal/sysdep` seam (with a fake), following the
project's "never call the OS directly from logic packages" convention.

## Current state

- `runRun` (`internal/cli/run.go:52-187`) is straight-line orchestration: it
  compiles the policy (steps 5), attaches the refcounted proxy with `defer
  mgr.Detach` (step 9, ends `run.go:156`), then blocks on
  `cage.NewRunner(...).Run(ctx, inv)` (step 11, `run.go:182-185`). No background
  goroutine exists today.
- `recompile()` (`internal/cli/mutate.go:121-128`) wraps
  `compile.New(app.FS, app.Paths, app.Clock, app.HTTP, nil).Compile(ctx, dir)`.
  The compiler writes `policy.json` only via temp-file + atomic rename, only as
  the last step after parse/validation and generators succeed — so a failed
  compile leaves the previous `policy.json` untouched (last-good).
- The config `Loader` (`internal/config/load.go`) resolves the include graph but
  returns a flattened `*Config` with `Include` **cleared** — the list of files
  read is discarded. There is no API to enumerate the watch set.
- `go.mod:6` has `github.com/fsnotify/fsnotify v1.10.1` as a direct but
  **unused** dependency. No `internal/sysdep` file-watch seam exists.
- `App` (`internal/cli/cli.go:20-66`) holds injected seams; `Main()`
  (`cli.go:105-126`) wires the real OS-backed impls.

## Desired end state

- A new `internal/configwatch` package owns the watch logic: enumerate the watch
  set, watch parent directories via the fsnotify seam, filter by filename,
  debounce bursts, recompile on a settled change, re-derive the watch set, and
  emit concise stderr feedback — all driven by injected seams and unit-tested
  hermetically with a fake watcher.
- A new `sysdep.FileWatcher` interface + `FileWatcherFactory` (real fsnotify
  impl + `sysdeptest` fake) is the only place fsnotify is imported.
- `Loader.ResolveFiles` returns the resolved include-graph file set (global +
  project + transitive includes).
- `runRun` starts the watcher after the proxy is attached and stops it via
  `defer`; a watcher start failure warns but never blocks the session.
- `make test` and `make lint` pass; `make build` produces the final binary; a
  `//go:build integration` test exercises the real fsnotify-backed watcher.

## What we're NOT doing (per ticket "Out of Scope")

- No standalone long-running `watch` command — session-only.
- No fail-closed / cage-teardown on invalid config — keep last-good + warn.
- No recompile on changes to referenced manifests (`package.json`, etc.).
- No change to the enforcer's mtime poll (the delivery mechanism we feed).
- The session overlay (`--once`, machine-written out-of-tree) is not watched.

## Implementation approach

Five phases, each independently verifiable. Phases 1–3 are pure/hermetic
(table- and fake-driven). Phase 4 wires into the run command. Phase 5 adds the
real-FS integration test and docs.

Key design decisions (from research):

- **Watch parent directories, filter by `Event.Name`.** Editors save atomically
  (write temp + rename), which destroys a direct-file watch; watching the dir and
  matching the filename is the maintainer-blessed pattern and handles add/remove
  of includes within the same dir for free.
- **Debounce with a re-armed timer handled in the event loop's `select`** (not
  `time.AfterFunc`), so recompile + watch-set reconcile run on the single loop
  goroutine — no mutex needed for the watched-name set. Mirrors the
  `time.After`-in-select pattern in `internal/cage/run.go:70-84`. Debounce
  duration is a functional-option (default ~100ms; tests use a few ms +
  `require.Eventually`, exactly like `cage` `WithGrace`).
- **Filter to `Create|Write`**, ignore `Chmod` (Spotlight/backup noise on macOS).
- **Feedback via plain `fmt.Fprintf(app.Stderr, …)`** (the established ⚠/advisory
  style), never the single-goroutine `progress.Printer`.
- **Last-good needs no new protection** — the compiler + enforcer already
  guarantee it; the watcher only reports failures.

---

## Phase 1: Loader include-graph enumeration

Add `Loader.ResolveFiles` so the watcher can learn which files to watch.

### Changes

`internal/config/load.go`:

- Add `func (l *Loader) ResolveFiles(projectPath string) ([]string, error)`:
  - Resolve the home dir; collect the implicit global baseline
    (`~/.config/agent-creance.yaml`) as **optional** (absent → contributes
    nothing, no error — mirrors `Load`).
  - Collect the project file as **required**.
  - Return the deduplicated set of **canonical** absolute paths (symlink-resolved
    via `EvalSymlinks`, the on-disk identity fsnotify reports), order unspecified.
- Add an unexported recursive collector `collectFiles(path, home string, optional
  bool, seen map[string]struct{}, out *[]string, stack []string, depth int)
  error` that mirrors `resolve`'s walk (same `resolveIncludePath` rules, cycle
  detection via canonical `stack`, `maxIncludeDepth`) but accumulates canonical
  paths into `out`/`seen` instead of merging values. A missing optional file
  returns nil (no append); a missing required file/include is an error
  (`config: file not found: …`). Parse errors propagate with the path prefix
  (so an unparseable file is reported, consistent with `resolve`).
- Doc-comment `ResolveFiles` explaining it is the watch-set counterpart to
  `Load`: same resolution rules, paths instead of merged values.

### Why a separate collector (not threading through `resolve`)

Keeps the merge/precedence path untouched (lower risk); the small re-parse cost
is irrelevant for a handful of config files.

### Tests — `internal/config/load_test.go` (table-driven, sysdeptest fakes)

- Single project file, no includes → `[projectAbs]`.
- Project + one relative include → both, include resolved against the project
  file's dir.
- Nested includes (A→B→C) → all three.
- Diamond / repeated include → deduplicated (one entry per file).
- Global baseline present → included; absent → omitted, no error.
- `~/`-prefixed and absolute includes → resolved correctly.
- Missing required include → error; cycle → `ErrIncludeCycle`; depth >10 →
  `ErrMaxIncludeDepth` (reuse the existing fixtures' style).

### Success criteria

#### Automated
- [ ] `go test ./internal/config/...` passes
- [ ] `make lint` clean

#### Manual
- [ ] `ResolveFiles` returns the same files `Load` reads for a representative
      multi-include fixture (cross-check by inspection)

---

## Phase 2: `FileWatcher` sysdep seam + fake

Introduce the fsnotify abstraction. This is the only place fsnotify is imported.

### Changes

`internal/sysdep/filewatcher.go` (new):

- `type FileOp uint32` bitmask with `FileCreate`, `FileWrite`, `FileRemove`,
  `FileRename`, `FileChmod` and a `Has(FileOp) bool` helper (mirrors `fsnotify.Op`
  without leaking the dependency).
- `type FileEvent struct { Name string; Op FileOp }`.
- `type FileWatcher interface { Add(path string) error; Remove(path string)
  error; WatchList() []string; Events() <-chan FileEvent; Errors() <-chan error;
  Close() error }`.
- `type FileWatcherFactory interface { NewWatcher() (FileWatcher, error) }`.
- Real impl: `type OSFileWatcherFactory struct{}` with
  `var _ FileWatcherFactory = OSFileWatcherFactory{}`; `NewWatcher()` constructs
  `fsnotify.NewWatcher()` wrapped in an `osFileWatcher` that runs a small
  translation goroutine forwarding `fsnotify.Event`→`FileEvent` and errors onto
  its own channels, closing them when fsnotify's channels close (so `Close()` →
  fsnotify close → translation goroutine exits → our channels close). Include the
  `var _ FileWatcher = (*osFileWatcher)(nil)` assertion. A small `translateOp`
  maps the fsnotify bits to `FileOp`.

`internal/sysdep/sysdeptest/filewatcher.go` (new):

- `type FakeFileWatcher struct{…}` implementing `FileWatcher`, mutex-guarded
  (driven from a goroutine while a test asserts — same discipline as
  `FakeProcessManager`/`FakeFileSystem`):
  - `EventsCh chan sysdep.FileEvent`, `ErrorsCh chan error` (buffered) so tests
    push synthetic events; `Events()/Errors()` return them.
  - Call-recording: `Added []string`, `Removed []string`; an `AddErrs
    map[string]error` knob; a `Closed bool`.
  - `WatchList()` returns the current added-minus-removed set.
  - `Close()` records and closes the channels.
- `type FakeFileWatcherFactory struct{ Watcher *FakeFileWatcher; NewErr error }`
  implementing `FileWatcherFactory`; `NewWatcher()` returns the embedded fake (or
  `NewErr`). `var _` assertions for both.
- Constructor `NewFakeFileWatcher()` initializing channels/maps.

`go.mod` / `go.sum`: fsnotify becomes actually used (already a direct require;
`go mod tidy` should be a no-op or just confirm).

### Tests — `internal/sysdep/sysdeptest/filewatcher_test.go` (light)

- The fake records `Add`/`Remove`, `WatchList` reflects them, `AddErrs` surfaces,
  `Close` closes channels. (Real `osFileWatcher` behavior is covered in Phase 5
  integration tests — no external-tool unit tests.)

### Success criteria

#### Automated
- [ ] `go build ./...` (compiles; fsnotify wired)
- [ ] `go test ./internal/sysdep/...` passes
- [ ] `make lint` clean

---

## Phase 3: `internal/configwatch` package (the watch logic)

The heart of the feature: hermetic, fake-driven.

### Changes

`internal/configwatch/configwatch.go` (new):

- `type ReloadFunc func(ctx context.Context) (changed bool, summary string, err
  error)` — recompiles; returns `changed=false` when the compile was a cache
  skip (policy unchanged), a `summary` like `"5 allow, 2 deny"` on a real
  recompile, or an error (leaving last-good in force). Decouples the package from
  `policy/compile`.
- `type Watcher struct{…}` holding: `loader *config.Loader`, `factory
  sysdep.FileWatcherFactory`, `reload ReloadFunc`, `out io.Writer`, `debounce
  time.Duration`, plus runtime state (`fw sysdep.FileWatcher`, watched-name set
  `map[string]struct{}`, `stop chan struct{}`, `done chan struct{}`).
- `func New(loader *config.Loader, factory sysdep.FileWatcherFactory, reload
  ReloadFunc, out io.Writer, opts ...Option) *Watcher`; `Option` =
  `WithDebounce(d time.Duration)` (default 100ms).
- `func (w *Watcher) Start(ctx context.Context, projectConfigPath string) error`:
  1. `files, err := w.loader.ResolveFiles(projectConfigPath)` — fail → return
     err (caller decides; run treats it as advisory).
  2. `w.fw, err = w.factory.NewWatcher()` — fail → return err.
  3. Build the watched-name set (the files) and the deduped parent-dir set;
     `w.fw.Add(dir)` for each dir (ignore "already watching"). An `Add` failure
     for a single dir warns but does not abort.
  4. Launch the event-loop goroutine; return nil.
- `func (w *Watcher) loop(ctx)` — single goroutine, `select` over:
  - `<-ctx.Done()` / `<-w.stop` → return.
  - `e, ok := <-w.fw.Events()`: `!ok` → return; skip unless
    `e.Op.Has(FileCreate|FileWrite)`; skip unless `e.Name` ∈ watched set; arm/reset
    the debounce timer (a `*time.Timer` whose `C` feeds a `debounceCh` select
    case).
  - `err, ok := <-w.fw.Errors()`: warn (`⚠ config watch: <err>`), keep going.
  - `<-debounceCh`: call `w.doReload(ctx)`; clear the timer.
- `func (w *Watcher) doReload(ctx)`:
  - `changed, summary, err := w.reload(ctx)`.
  - On `err`: `fmt.Fprintf(out, "⚠ config reload failed, keeping last-good
    policy: %v\n", err)` — do NOT re-derive the watch set (stay on the last-good
    set).
  - On success + `changed`: `fmt.Fprintf(out, "✓ config reloaded (%s)\n",
    summary)`; then **re-derive** the watch set
    (`loader.ResolveFiles`) and **reconcile** watched dirs against
    `w.fw.WatchList()` (`Add` new dirs, `Remove` stale ones, ignoring a
    not-watched error), and swap in the new name set. A re-derive error here
    warns but keeps the previous set.
  - On success + `!changed` (cache skip): `fmt.Fprintf(out, "config changed;
    policy unchanged\n")` (concise; tells the user the edit was seen). Still
    re-derive/reconcile (includes may have changed even if the merged policy
    didn't).
- `func (w *Watcher) Stop() error`: `close(w.stop)`; `<-w.done`; then
  `w.fw.Close()`. Idempotent-safe (guard with `sync.Once`). No goroutine/FD leak.

### Tests — `internal/configwatch/configwatch_test.go` (fake watcher + fake fs + small debounce + `require.Eventually`)

- **Reload on a watched-file write**: seed a valid config via `FakeFileSystem`,
  start with a `FakeFileWatcherFactory`, push a `FileWrite` event for the project
  file, assert the `ReloadFunc` ran once and the `✓ config reloaded` line hit
  `out`.
- **Debounce/coalesce**: push a burst of N write events within the debounce
  window; assert `ReloadFunc` ran exactly once.
- **Filtering**: a `FileChmod` event, or a write to an unwatched filename, does
  not trigger reload.
- **Invalid edit**: `ReloadFunc` returns an error; assert the `⚠ … keeping
  last-good policy` line and that the watch set is NOT re-derived (no extra
  `ResolveFiles`-driven `Add`).
- **Recover after invalid**: error then success → second event reloads.
- **Include added**: after a successful reload whose new `ResolveFiles` returns an
  extra dir, assert `w.fw.Add` was called for the new dir (reconcile).
- **Clean stop**: `Start` then `Stop` → `FakeFileWatcher.Closed == true`, the
  goroutine exited (`done` closed), `Stop` is safe to call twice.
- **Start failure**: `FakeFileWatcherFactory.NewErr` → `Start` returns the error,
  no goroutine started.

### Success criteria

#### Automated
- [ ] `go test -race ./internal/configwatch/...` passes (race detector clean —
      goroutine lifecycle)
- [ ] `make lint` clean

#### Manual
- [ ] Tests use a small debounce + `require.Eventually` (no fixed sleeps),
      matching `internal/cage/run_test.go`

---

## Phase 4: Wire the watcher into the run session

### Changes

`internal/cli/cli.go`:

- Add field `WatcherFactory sysdep.FileWatcherFactory` to `App` (grouped with the
  run-related seams, with a comment).
- In `Main()`, wire `WatcherFactory: sysdep.OSFileWatcherFactory{}`.

`internal/cli/run.go` — in `runRun`, after the proxy is attached and its `defer
mgr.Detach` is registered (after `run.go:156`) and before the blocking step 11
(`run.go:182`):

- Build the project config path `filepath.Join(dir, configFile)`.
- Build a `configwatch.ReloadFunc` closure that recompiles exactly as
  `recompile` does (`compile.New(app.FS, app.Paths, app.Clock, app.HTTP,
  nil).Compile(ctx, dir)`), mapping the `compile.Result` to `(changed =
  !res.Skipped, summary = "%d allow, %d deny", err)`.
- `watcher := configwatch.New(config.NewLoader(app.FS, app.Paths),
  app.WatcherFactory, reload, app.Stderr)`.
- `if err := watcher.Start(ctx, cfgPath); err != nil { fmt.Fprintf(app.Stderr,
  "⚠ config hot-reload unavailable: %v\n", err) } else { defer func(){ _ =
  watcher.Stop() }() }` — advisory: a watcher failure never blocks the session.
  The `defer Stop` (registered after the `defer mgr.Detach`) runs first on
  return — stop the watcher, then detach the proxy.
- A one-line announce before launch is optional; keep the existing `prog.Line`.

`internal/cli/run_test.go`:

- Extend the App-from-fakes harness to inject a `FakeFileWatcherFactory`.
- Assert that on a normal run the watcher is started (`Added` non-empty) and
  stopped (`Closed == true`) after the (fake) agent exits.
- Assert a `FakeFileWatcherFactory.NewErr` produces the `⚠ config hot-reload
  unavailable` warning on stderr and does NOT fail the run.

### Why here

At this point `policy.json` exists and the proxy is live, so a recompile is
meaningful; the blocking `cage…Run` keeps `runRun` on its main goroutine until
the agent exits, and the deferred `Stop` then tears the watcher down before
`Detach`. `ctx` is not signal-wired (it is `context.Background()` in production),
so teardown is driven by the function returning — `Stop()` does not rely on
`ctx.Done()`.

### Success criteria

#### Automated
- [ ] `go test -race ./internal/cli/...` passes
- [ ] `go build ./...` passes
- [ ] `make lint` clean

---

## Phase 5: Integration test + docs + build

### Changes

- `internal/sysdep/filewatcher_integration_test.go` (`//go:build integration`):
  exercise the real `OSFileWatcherFactory`/`osFileWatcher` against a `t.TempDir()`
  — `Add(dir)`, write/rename a file, assert a `FileCreate`/`FileWrite` event with
  the right `Name` arrives; assert `Close()` releases cleanly (no hang). This is
  where real fsnotify (kqueue) is validated, keeping `make test` hermetic.
- Optionally a `//go:build integration` end-to-end `configwatch` test using the
  real factory + real `OSFileSystem` + a temp config that recompiles a real
  `policy.json`, asserting a hand-edit advances the file mtime. (Nice-to-have;
  the enforcer apply is already covered by AC-0030/AC-0020.)
- `docs/design.md`: document that during a run the source config + include graph
  are watched and recompiled on change (feeding the existing enforcer mtime
  reload), last-good-on-invalid behavior, and session-only scope. Place it with
  the run command / hot-reload narrative.
- Run `make build` so `bin/agent-creance` reflects the final commit.

### Success criteria

#### Automated
- [ ] `make test` passes (full hermetic suite, race)
- [ ] `make test-integration` passes (real fsnotify watcher test)
- [ ] `make lint` passes
- [ ] `make build` succeeds; `bin/agent-creance` rebuilt

#### Manual (optional, requires real tools on macOS)
- [ ] Start a real `agent-creance run`; hand-edit `.agent-creance.yaml` to add an
      allow rule; observe the `✓ config reloaded` line and that the new host is
      reachable within ~1–2s without restart.
- [ ] Edit an `include:`d fragment; observe the same reload.
- [ ] Save a syntactically broken config; observe the `⚠ … keeping last-good`
      warning and that egress is unchanged; fix it and observe recovery.
- [ ] Ctrl-C / agent exit leaves no stray watcher goroutine or FD (clean teardown).

---

## Testing strategy

- **Pure logic (loader enumeration)** → table-driven tests against `sysdeptest`
  fakes (`internal/config/load_test.go` style).
- **Watch logic (configwatch)** → hermetic fake-driven tests: a
  `FakeFileWatcherFactory` injects synthetic events; a small debounce +
  `require.Eventually` asserts coalescing and reload/feedback, with `-race` for
  the goroutine lifecycle. No real FS, no external tools.
- **Run wiring** → extend `run_test.go` (App-from-fakes) to assert start/stop and
  the advisory-failure path.
- **Real fsnotify** → `//go:build integration` only, so `make test` stays fast
  and deterministic (project convention: external/real-IO behavior behind the
  integration tag).

## Acceptance-criteria mapping (from the ticket)

- Project config edit recompiles + live proxy enforces + feedback line → Phases
  3 (`doReload`/feedback) + 4 (wiring) + 5 (manual).
- Include-graph edits also trigger → Phases 1 (`ResolveFiles`) + 3 (reconcile).
- Invalid edit keeps last-good + warns + later valid save recompiles → Phase 3
  (`doReload` error path; recover test) — backed by the compiler/enforcer
  guarantee documented in research.
- Watcher active only during a session, stops cleanly, no leaks → Phases 3
  (`Stop`, `-race`) + 4 (`defer` teardown).
- Feedback distinguishable from agent output → Phase 3/4 (plain `app.Stderr`,
  not the `progress.Printer`).
- File-watching via `internal/sysdep` seam with a fake; no external tool in unit
  tests → Phases 2 + 5 (integration-tagged real impl).
- `make test`, `make lint` pass; `make build` at the end → Phase 5.

## Risks / notes

- **fsnotify `Events()` channel returning a chan via an interface method** is
  slightly unusual but works; the OS impl's translation goroutine is the only
  untested-by-unit code (covered by the Phase 5 integration test).
- **macOS/kqueue**: one FD per watched path; we watch a small set of config dirs,
  so FD cost is negligible. Directory `Write` events (kqueue emits these) are
  dropped by the name filter.
- **Skip-on-unchanged-policy**: a comment-only edit yields `changed=false` (cache
  skip) — we still re-derive the watch set and print a concise "policy unchanged"
  line so the user knows the edit registered.
</content>
