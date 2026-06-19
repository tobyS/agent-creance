# AC-0053 Implementation Status

Plan: `thoughts/shared/plans/2026-06-19-AC-0053-config-hot-reload.md`

- [x] Phase 1: Loader include-graph enumeration (`ResolveFiles`)
- [x] Phase 2: `FileWatcher` sysdep seam + fake
- [x] Phase 3: `internal/configwatch` package
- [x] Phase 4: wire into run session
- [ ] Phase 5: integration test + docs + build

## Notes

### Phase 1 (done)

- Added `Loader.ResolveFiles` + `collectFiles` in `internal/config/load.go`:
  returns the canonical, deduped file set (global + project + transitive
  includes), mirroring `resolve`'s walk (cycle detection, depth limit, include
  path rules). 10 table-driven tests in `load_test.go`. `make test`/`make lint`
  green.

### Phase 2 (done)

- Added `sysdep.FileWatcher`/`FileWatcherFactory` (+ `FileOp`/`FileEvent`) in
  `internal/sysdep/filewatcher.go`, fsnotify-backed `OSFileWatcherFactory` with a
  translation goroutine. Fake `FakeFileWatcher`/`FakeFileWatcherFactory` in
  `sysdeptest/`. Light fake tests; real impl deferred to the Phase 5 integration
  test. `-race`/lint green.

### Phase 3 (done)

- Added `internal/configwatch` package: `Watcher` with `Start`/`Stop`, a single
  event-loop goroutine doing op/name filtering, re-armed-timer debounce,
  `doReload` (✓ reloaded / "policy unchanged" / ⚠ keep last-good), and
  `rederive`/`reconcileDirs` to track added/removed includes. `ReloadFunc` keeps
  it decoupled from `policy/compile`. 12 fake-driven tests (small debounce +
  `require.Eventually`/`require.Never`); `-race -count=5` and lint green.

### Phase 4 (done)

- Added `App.WatcherFactory` (wired `OSFileWatcherFactory{}` in `Main`). `runRun`
  now builds a `configwatch.ReloadFunc` over the compiler and starts the watcher
  after proxy attach, stopping it via `defer` (before the proxy Detach). Watcher
  start failure warns but never blocks the run. Extended `run_test.go` (fake
  factory) with start/stop and advisory-failure tests. The one run testscript
  refuses at the prereq gate, so the real watcher is never hit there. `make
  test`/`make lint` green.
