# AC-0053 Implementation Status

Plan: `thoughts/shared/plans/2026-06-19-AC-0053-config-hot-reload.md`

- [x] Phase 1: Loader include-graph enumeration (`ResolveFiles`)
- [x] Phase 2: `FileWatcher` sysdep seam + fake
- [x] Phase 3: `internal/configwatch` package
- [x] Phase 4: wire into run session
- [x] Phase 5: integration test + docs + build
- [x] Phase 6: config read-only in cage (security fix from the checkpoint)

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

### Phase 5 (done)

- Added the real-fsnotify integration test (`internal/sysdep/
  filewatcher_integration_test.go`, `//go:build integration`): watches a temp dir,
  asserts write + atomic-rename events arrive, and clean Close. Updated
  `docs/design.md` (config-compilation threat-model paragraph + new "In-session
  config hot-reload" subsection). `make build` rebuilds `bin/agent-creance`.

### Phase 6 — config read-only in cage (checkpoint security fix)

- **Why:** the live watcher would otherwise let a prompt-injected agent widen its
  own egress by editing the read-write-mounted `.agent-creance.yaml` (the design's
  line 341 invariant). Checkpoint decision: make the config read-only in the cage.
- Added `profile.RenderConfigReadOnlyFragment` (emits `(deny file-write* (literal
  …))` per resolved config file; golden `config-ro.golden`), `state.Layout.
  ConfigProfileSB` (`config-ro.sb`), `cage.Inputs.ConfigFiles` + Prepare writes /
  Build appends the fragment last (last-match-wins over the RW mount), and `runRun`
  resolves the include graph and fails closed if it can't. Read stays allowed.
- Tests: profile renderer (golden + write-only/dedupe/errors), cage Prepare +
  regenerated `invocation.golden.json`, and an adversarial integration test
  (`TestLiveSafehouseConfigReadOnly`) proving the config is readable but
  unwritable under real Seatbelt. Note: the verify battery's `kc-read`/`kc-write`
  vectors fail in this environment (no usable login Keychain in-cage) — verified
  identical on the pre-change commit, so pre-existing/environmental, not a
  regression.
