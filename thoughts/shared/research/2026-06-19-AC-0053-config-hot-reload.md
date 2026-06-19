---
date: 2026-06-19
ticket: AC-0053
title: "Research — hot-reload the source config during a run session"
branch: main
commit: 7df2d9a7f1cdebe45f6ede0eb932c524d8d5b00c
status: complete
---

# AC-0053 Research: hot-reload the source config on change during a run session

## Research question

While a cage is running, how should an edit to the project config
(`.agent-creance.yaml`) **or any file in its resolved include graph** (included
fragments + the global baseline) automatically recompile `policy.json` so the
enforcer's existing mtime poll applies it to the live proxy — without a restart,
keeping the last-good policy on an invalid edit, with the watcher active only
during an active run session and torn down cleanly?

## TL;DR / key findings

1. **The delivery half already exists end to end; AC-0053 only adds a new
   *trigger*.** `allow`/`deny`/`import` already "hot-reload" today: they
   recompile `policy.json` and the enforcer's 1-second mtime poll re-reads it.
   AC-0053's job is to drive the *same* `compiler.Compile(ctx, dir)` call from a
   file-watch trigger during a run, instead of only from explicit mutation
   commands. No change to the enforcer or to the compiler's write path is needed.

2. **"Last-good preserved on invalid edit" is already guaranteed at two layers,
   for free.** The compiler only ever writes `policy.json` via temp-file +
   atomic rename, and only as the *last* step after parse/validation and
   generators succeed (`internal/policy/compile/compile.go` `write`/`build`). Any
   resolve/validation/generator error returns before the write, leaving the old
   file untouched. Independently, the Python enforcer keeps its in-memory ruleset
   if a load fails (`enforcer.py` `_load`). A watcher only needs to *report* the
   failure, not protect the artifact.

3. **The one real gap: the config loader discards the resolved include-graph file
   list.** `Loader.Load` / `ResolveLayer` return a flattened `*Config` with
   `Include` cleared (`internal/config/load.go:69,101`); the canonical paths read
   during resolution exist only as a cycle-detection `stack` and are never
   returned. There is **no existing API** to enumerate "every file that
   contributes to this project's policy." The watcher needs that set, so the main
   new building block is a loader method that returns the resolved file list.
   **→ central implementation work, not a user decision.**

4. **Watch mechanism is the one genuine design fork.** Two viable approaches:
   (a) `fsnotify` event-driven watch behind a new `internal/sysdep` seam — the
   profile lists fsnotify as *the* file-watching library and `go.mod` already has
   it as a direct (currently unused) dependency, staged for exactly this; or
   (b) an mtime-polling loop consistent with the enforcer's own approach, reusing
   the existing `FileSystem.Stat().ModTime()` + `Clock` seams with no new
   dependency. **→ checkpoint question.**

5. **Lifecycle insertion points are clean and unambiguous.** `runRun`
   (`internal/cli/run.go:52-187`) is straight-line orchestration. Start the
   watcher after the proxy is attached (after `run.go:156`) and tear it down via a
   `defer` so it stops when `runRun` returns — which is *after* the blocking
   `cage.NewRunner(...).Run(ctx, inv)` (`run.go:183`) unblocks. Caveat: that `ctx`
   is **not** wired to OS signals (signals are handled inside `cage.Runner`), so
   the watcher's stop must be driven by the function returning / an explicit stop
   channel, not by `ctx.Done()` alone.

6. **Feedback should go through plain `fmt.Fprintf(app.Stderr, …)`, not the
   progress printer.** `progress.Printer` is documented "must be called from a
   single goroutine" and is owned by `runRun`'s main goroutine; a watcher
   goroutine writing to it would violate that. The established advisory-line
   pattern (warnings at `run.go:154,199,216`, `lifecycle.go:387`) is plain
   `fmt.Fprintf` to `app.Stderr` — that satisfies the AC ("distinguishable from
   the agent's foreground output"; run's stdout belongs to the agent, stderr to us).

7. **fsnotify best practice (if chosen): watch parent directories, filter by
   name, debounce with a reset timer.** Editors save atomically (write temp +
   rename), which destroys a direct file watch's inode. The maintainer-blessed
   pattern watches `filepath.Dir(p)` and matches `Event.Name`, debounced ~100ms
   per the official `dedup` example, filtering to `Create|Write` (ignore `Chmod` —
   Spotlight/backup noise on macOS). See "fsnotify guidance" below.

## Background / what already works

The proxy enforcer hot-reloads the **compiled** `policy.json` by polling its
mtime — this is why `allow`/`deny` take effect in a live cage. The gap AC-0053
closes is that a **hand-edit of the source config** never recompiles
`policy.json`, so a running cage keeps the stale policy until restart.

### The recompile + delivery chain (existing)

- **`recompile()`** — `internal/cli/mutate.go:121-128`: thin wrapper that builds a
  `compile.Compiler` from the `FS`/`Paths`/`Clock`/`HTTP` seams (with a **nil,
  silent** progress reporter) and calls `compiler.Compile(ctx, dir)`.
- **`mutateAndRecompile`** — `internal/cli/mutate.go:85-116`: writes the edited
  source file atomically then calls `recompile`. Used by `allow.go`, `deny.go`,
  and `import.go` (`import.go:98-101`).
- **The change reaches the enforcer purely via `policy.json`'s mtime** — no IPC,
  no signal to mitmproxy. mutate.go's own comment: the rewrite "advanc[es] its
  mtime … and the enforcer's mtime poll reloads it within ~1s."
- **Enforcer poll** — `internal/proxy/enforcer/enforcer.py`:
  `_POLL_INTERVAL_SECONDS = 1.0`; `_maybe_reload` compares `os.path.getmtime`;
  `_load` reassigns `self._ruleset` only on full success, so a malformed
  `policy.json` leaves the previous ruleset enforcing (and retries each second
  because `_mtime` is not advanced on failure).

### The compiler's last-good guarantee (existing)

`internal/policy/compile/compile.go`:
- `resolve` (≈210-256) loads three layers via `Loader.ResolveLayer`: global
  baseline (`GlobalPath()`, optional), project file (`.agent-creance.yaml`,
  required), session overlay (`--once`, optional). Parse/validation errors
  surface here, **before** any write.
- `Compile` (≈261) computes an `inputHash`; if it matches the existing artifact
  it returns `Skipped:true` **without rewriting the file** (so the enforcer's
  poll does not fire). A recompile must change the hash to force a reload.
- `write` (≈600-620): `WriteFile(dest+".tmp")` then `Rename(tmp, dest)` — atomic;
  called only from `build`, the last step after generators succeed.

Net: **any failure path returns an error without writing; the only write is
atomic.** Last-good is preserved by construction.

## The include graph and the watch set

### What contributes to the policy

For AC-0053 the watch set is: **global baseline** (`~/.config/agent-creance.yaml`,
may be absent) + **project file** (`.agent-creance.yaml`) + **every transitively
`include:`d fragment**. Explicitly *not* in scope:

- **Referenced manifests** (`package.json`, `composer.json`, …) — generator-driven,
  out of scope per the ticket.
- **The session overlay** (`--once`) — machine-written out-of-tree, not
  hand-edited; not part of the include graph the ticket names.

### Include resolution (`internal/config/load.go`)

- `Config.Include` is `[]string` (`config.go:34`). `resolve` (`load.go:109-165`)
  reads one file, `Parse`s it, recurses over `cfg.Include`, merging low→high.
- **Include path rules** (`resolveIncludePath`, `load.go:170-179`): `~/` → home;
  absolute → verbatim; relative → resolved against the **declaring file's
  directory** (not the project root).
- A declared include that is missing **is an error** (`load.go:154`,
  `optional=false`). A missing global/overlay is *not* (optional).
- **Cycle detection + depth limit**: canonical paths via `EvalSymlinks` form a
  `stack` for cycle detection (`load.go:133-141`); `maxIncludeDepth = 10`.

### The gap (key finding 3)

`Load` (`load.go:48`) and `ResolveLayer` (`load.go:92`) both return `(*Config,
error)` with `Include` **cleared** (`load.go:69,101-102`). The `stack` of
canonical paths is used only for cycle detection and is **never returned**. So
after a load, the list of files actually read is gone.

**Implication for the plan:** add a loader capability that returns the resolved
file set — e.g. a new method that walks the same recursion as `resolve` but
accumulates the canonical paths (project + transitive includes), plus the
caller adds the global baseline path (`GlobalPath()`) when it exists. Because an
edit can *add or remove* an include, the watch set must be **re-derived after
each successful recompile** and the watched paths/dirs reconciled.

## Lifecycle: where the watcher lives

`runRun` (`internal/cli/run.go:52-187`) — verified straight-line, no existing
background goroutine, no signal handling in the body (signals live in
`cage.Runner`):

1-8. prereq / setup / cred / state layout / progress printer / **policy compile**
   (`run.go:101-114`) / load merged config (`run.go:117`) / sandbox profile /
   extract enforcer.
9. **Proxy attach** (`run.go:139-156`): `mgr.Attach(...)`; `defer mgr.Detach(...)`.
10. Build cage invocation (`run.go:160-178`).
11. **Blocking foreground run** (`run.go:182-185`):
   `cage.NewRunner(app.ProcessGroup).Run(ctx, inv)` — blocks until the agent's
   whole process group is reaped, so the deferred `Detach` runs strictly after.

- **Start the watcher**: between `run.go:156` (after `defer mgr.Detach`) and
  `run.go:182`. At that point `policy.json` exists and the proxy is live, so a
  recompile is meaningful.
- **Stop the watcher**: a `defer` registered right after starting it → fires when
  `runRun` returns (after the blocking `Run` unblocks, including on Ctrl-C, since
  the cage runner returns once the group is reaped). Do **not** rely on
  `ctx.Done()` — production `ctx` is `context.Background()` (`cli.go:127`) and is
  not signal-wired.

## Recommended watcher shape (per chosen mechanism)

Either mechanism shares the same skeleton, modeled on the existing goroutine +
`select` + `defer`-teardown pattern in `internal/cage/run.go:53-85`:

- A small watcher type constructed with injected seams (mirroring
  `proxy.NewManager(fs, lock, proc, ports, warn)` / `cage.NewRunner(pg, opts...)`).
- A background goroutine running a `select` loop over events + a stop signal.
- A debounce/coalesce so one save → one recompile.
- On a (debounced) change: call the existing `Compile(ctx, dir)`; on success
  print `✓`-style reload line + re-derive and reconcile the watch set; on error
  print a warning naming the problem and keep watching (last-good stays in force).
- Clean teardown: stop loop + close watcher + join the goroutine (no FD/goroutine
  leak — an explicit AC).

### Option A — fsnotify (event-driven, new seam)

- **Pros:** immediate (no poll latency stacked on top of the enforcer's 1s);
  profile + `go.mod` already designate fsnotify; matches "native events" framing.
- **Cons:** new `internal/sysdep` seam (e.g. `FileWatcher`) + `sysdeptest` fake +
  `//go:build integration` test for the real backend (unit tests stay hermetic per
  convention). More surface.
- **fsnotify guidance** (v1.10.1, from godoc + canonical `cmd/fsnotify`
  examples): watch **parent directories**, filter by `Event.Name` (atomic saves
  destroy direct-file watches); **debounce with a per-key reset timer** (~100ms,
  the official `dedup` pattern), keyed on a single constant so a multi-file edit
  coalesces to one reload; filter to `Create|Write` (ignore `Chmod` — Spotlight /
  backup noise on macOS/kqueue); reconcile the watched-directory set against
  `WatchList()` after each reload (`Add` new, `Remove` stale, ignore
  `ErrNonExistentWatch`); shut down via context-cancel + `watcher.Close()` (closes
  `Events`) + join on a `done` channel, and `Stop()` pending debounce timers.
  macOS/kqueue uses one FD per watched path — watch the specific config dirs, not
  a broad tree.

### Option B — mtime polling (reuses existing seams)

- **Pros:** no new dependency; reuses `FileSystem.Stat().ModTime()` + `Clock`
  (both already in `App` and already faked in `sysdeptest`); trivially hermetic;
  conceptually consistent with the enforcer's own mtime poll.
- **Cons:** poll latency (interval) added on top of the enforcer's 1s; less
  idiomatic given fsnotify is already staged; must handle the "file briefly
  absent during atomic rename" window (treat transient not-exist as no-change).

## Patterns to model after (verified)

- **sysdep seam template** (if Option A): interface + `var _ Iface = (*OSImpl)(nil)`
  compile-time assertion + real impl (`internal/sysdep/clock.go`,
  `processmanager.go`, `filesystem.go`) + mutex-guarded scripted fake in
  `internal/sysdep/sysdeptest/` (`processmanager.go`, `filesystem.go` are the
  richest templates — per-op error knobs, call-recording slices, `sync.Mutex` for
  `-race`).
- **App injection**: add one field to the `App` struct (`internal/cli/cli.go:20-66`)
  + one `sysdep.OS…{}` line in `Main()` (`cli.go:105-126`).
- **Goroutine + timer + select + defer teardown**: `internal/cage/run.go:53-85`
  (buffered result channel, `for { select {…} }`, `defer signal.Stop`,
  functional-options constructor `NewRunner(pg, WithGrace(d))` for a
  test-tunable interval).
- **Testing a gated background goroutine**: `internal/cage/run_test.go`
  (`startedGroup` + `require.Eventually`; `FakeProcess.WaitGate`;
  `FakeProcessGroup.NotifyChans()` to inject events) — the model for injecting
  synthetic fs events / mtime ticks through a fake.
- **CLI/testscript**: `internal/cli/script_test.go` harness +
  `testdata/script/doctor_healthy.txtar` (stub external tools on PATH via
  `-- bin/foo --` blocks; minimal-PATH via `$CREANCE_BIN`).
- **No existing debounce helper** in the repo; only `time.After` in a select
  (`cage/run.go:70-84`) — re-arm on each event to coalesce.

## Error/feedback handling (verified constraints)

- **Distinguish invalid config from IO error**: `Parse` returns a typed
  `*config.ValidationError` (schema/unknown-key, strict decode); `resolve` prefixes
  it with the offending path (`load.go:143-148`). IO errors are wrapped distinctly
  (`config: file not found: %s`, `config: read %s`). Sentinels `ErrIncludeCycle`,
  `ErrMaxIncludeDepth`. A reload callback can type-assert to name the problem.
- **No enforcer ack channel**: a printed "reloaded" line is inferred from the
  *compile* succeeding, not from enforcer confirmation; the ~1s poll means the
  message slightly leads the in-proxy effect. Word the message accordingly.
- **Printer single-goroutine constraint**: emit watcher messages with plain
  `fmt.Fprintf(app.Stderr, …)` (the existing advisory-line style), not via the
  `progress.Printer` instance owned by the main goroutine.

## Code references

- `internal/cli/run.go:52-187` — `runRun`; insertion (after :156) / teardown points.
- `internal/cli/mutate.go:85-128` — `mutateAndRecompile` / `recompile` (reuse).
- `internal/cli/allow.go`, `internal/cli/deny.go`, `internal/cli/import.go:98-101`
  — existing recompile-then-mtime-reload callers.
- `internal/config/load.go:48-179` — loader, include resolution, the discarded
  file set (the gap), `resolveIncludePath`, `GlobalPath`.
- `internal/config/config.go` — `Parse`, `Include`, `*ValidationError`.
- `internal/policy/compile/compile.go` — `resolve`/`Compile`/`build`/`write`
  (atomic write, cache skip, last-good guarantee).
- `internal/proxy/enforcer/enforcer.py` — mtime poll + last-good-in-memory.
- `internal/progress/printer.go` — single-goroutine constraint.
- `internal/cli/cli.go:20-126` — `App` struct + `Main()` wiring.
- `internal/sysdep/filesystem.go`, `clock.go` — `Stat().ModTime()`, `Clock`
  (Option B seams; FileSystem has no notify primitive).
- `internal/sysdep/sysdeptest/` — fake templates.
- `internal/cage/run.go:53-85`, `internal/cage/run_test.go` — goroutine/timer/
  teardown + its test harness.
- `go.mod:6` — `github.com/fsnotify/fsnotify v1.10.1` (direct, unused).

## Related thoughts documents

- `thoughts/shared/research/2026-06-05-AC-0008-config-include-merge.md` +
  `…/plans/…AC-0008…` — include graph merge semantics (the graph to watch).
- `thoughts/shared/research/2026-06-07-AC-0030-allow-deny-commands.md` +
  plan — the allow/deny recompile path AC-0053 reuses.
- `thoughts/shared/research/2026-06-11-AC-0041-run-progress-output.md` + plan —
  the progress printer + stderr-feedback conventions.
- `thoughts/shared/research/2026-06-06-AC-0020-proxy-lifecycle-manager.md`,
  `…AC-0019-embed-extract-enforcer.md`, `…AC-0017-enforcer-decision-engine.md` —
  proxy/enforcer mtime-reload delivery side.
- `thoughts/shared/research/2026-06-05-AC-0009-sysdep-seam-extensions.md` — the
  seam-extension pattern (relevant if Option A).
- `thoughts/shared/research/2026-06-07-AC-0025-run-command.md`,
  `…AC-0024-process-group-signal-forwarding.md` — run lifecycle / signal handling.

## Open questions for the checkpoint

1. **Watch mechanism — fsnotify (new seam) vs. mtime polling (existing seams)?**
   See key finding 4 / Options A & B. Everything else (the new loader file-set
   API, directory-watch + name filter, ~100ms debounce, stderr feedback, defer
   teardown) is a settled implementation detail regardless of choice.

No UX surface → no design exploration needed. The ticket's other
"Questions for Research/Planning" are answered above (include-graph enumeration =
new loader API; lifecycle placement = after run.go:156, defer teardown;
debouncing = reset timer / re-armed `time.After`; last-good = already guaranteed;
feedback = plain stderr).
</content>
</invoke>
