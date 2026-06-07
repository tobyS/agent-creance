---
date: 2026-06-07
ticket: AC-0032
title: "Plan: `status` / `clean` commands (WP-6.3)"
status: ready
research: thoughts/shared/research/2026-06-07-AC-0032-status-clean-commands.md
commit: 99514a9506b8cb19379c107bf8b3b50a845c0824
---

# Plan: `status` / `clean` commands (AC-0032 / WP-6.3)

## Overview

Add two operator commands:

- `agent-creance status` — list running cages across **all** projects, one row per
  project, showing the project directory, state (running/orphan/stranded/down),
  port, and attached-agent count.
- `agent-creance clean` — tear down **this** project's proxy + lock + session
  overlay; idempotent and orphan-safe. Refuses when live agents are still attached
  unless `--force` is given.

Both are thin orchestration over existing machinery (`proxy.Manager.Inspect`,
`CleanOrphan`/`Detach`, `state.Resolver`). New work is small and additive: a
directory-listing seam, a `proxy.lock` `canonical_path` field, one new
`Manager.Clean` method, a small `internal/status` package mirroring
`internal/doctor`, and the two cobra commands.

## Decisions (from the checkpoint)

1. **status identity** — store the project's canonical path in `proxy.lock`
   (written by `Attach`) so `status` shows the real directory; fall back to the
   16-hex state-dir hash when the field is absent (older locks).
2. **status format** — aligned table `PROJECT | STATE | PORT | AGENTS`, no
   policy-hash column.
3. **clean + live agents** — refuse by default (warn-never-kill), `--force`
   overrides to force-stop.

## Current state

- `proxy.lock` payload is `lockState{ProxyPID, Port, PolicyHash, Agents}`
  (`internal/proxy/lifecycle.go:36`). No project path is recorded.
- `Manager.Inspect` (lifecycle.go:214) already returns a `Diagnosis`
  (running/orphan/stranded + live agents) per project — read-only, flock-guarded.
- `Manager.CleanOrphan` (lifecycle.go:256) tears down **only** true orphans.
- `Manager.Detach` last-out (lifecycle.go:165) is the SIGTERM + overlay-purge +
  clear-lock sequence `clean` mirrors.
- `state.Resolver` (state.go) computes paths only (no I/O); `Layout` exposes
  `ProxyLock()`/`SessionOverlay()` from `Root`. No `ProjectsRoot()` accessor; no
  way to build a `Layout` from a bare project root.
- `sysdep.FileSystem` (filesystem.go) has no `ReadDir`.
- `internal/doctor` is the precedent: side-effecting `Checker` (doctor.go) + pure
  golden-tested `Render` (report.go); `internal/cli/doctor.go` is the
  cobra-registration + testable-body template.

## Desired end state

- `agent-creance status` prints a deterministic table of every project whose
  `proxy.lock` records a live/recorded proxy or agents; "No active cages." when
  none. Read-only; exits 0.
- `agent-creance clean` stops this project's proxy, purges the overlay, clears the
  lock; idempotent; refuses with a non-zero exit + actionable message when live
  agents are attached and `--force` is absent; `--force` tears down anyway.
- Neither command touches another project's state beyond its own per-project
  flock-guarded read (`status`) or this-project teardown (`clean`).
- All new logic hermetically tested; rendering golden-tested; one integration test
  proves a real proxy is stopped.

---

## Phase 1 — Seams & state plumbing

Additive, no behaviour change to existing commands.

**`internal/sysdep/filesystem.go`**
- Add `ReadDir(name string) ([]fs.DirEntry, error)` to the `FileSystem` interface
  (doc: mirrors `os.ReadDir`; a non-existent dir yields `errors.Is(err, fs.ErrNotExist)`).
- Implement on `OSFileSystem` delegating to `os.ReadDir`.

**`internal/sysdep/sysdeptest/filesystem.go`**
- Implement `FakeFileSystem.ReadDir(name)`: return the immediate children of `name`
  derived from the `Dirs` and `Files` maps (entries whose `filepath.Dir == name`),
  as `fs.DirEntry` values; honour a new `ReadDirErrs` knob; return `fs.ErrNotExist`
  when `name` is neither a known dir nor has children.
- Add a minimal `fakeDirEntry` implementing `fs.DirEntry` (Name/IsDir/Type/Info).

**`internal/state/state.go`**
- Add `Resolver.ProjectsRoot() (string, error)` → `<cache>/agent-creance/projects`
  (path-only, mirrors `RegistriesRoot`/`CacheDir`).
- Add `LayoutForRoot(root string) Layout` → `Layout{Hash: filepath.Base(root), Root: root}`
  (Canonical unknown). Doc: for enumeration where only `Root`-derived paths are
  needed (e.g. `Inspect`); `Canonical` is not recoverable from the hash.

**Tests**
- `internal/sysdep/sysdeptest/filesystem_test.go` (new or existing): `ReadDir`
  returns immediate children only, error knob, `fs.ErrNotExist` for unknown dir.
- `internal/state/state_test.go`: table cases for `ProjectsRoot()` (XDG + HOME
  fallback) and `LayoutForRoot()` (Hash = base, ProxyLock/SessionOverlay derive
  from Root).

**Success criteria**
- [x] `go build ./...`
- [x] `go test -race ./internal/sysdep/... ./internal/state/...`

---

## Phase 2 — Record the project path in `proxy.lock`

**`internal/proxy/lifecycle.go`**
- Add `CanonicalPath string `json:"canonical_path"`` to `lockState` (doc: the
  project directory, recorded so `status` can show a readable path; the state-dir
  hash is one-way).
- `Attach` (line 142): set `CanonicalPath: cfg.Layout.Canonical` in the `next`
  lock write (both the start and attach paths write `next`).
- `Detach` "others remain" write (line 181): preserve `CanonicalPath: cur.CanonicalPath`.
  (Last-out clear and `CleanOrphan` clear may drop it — those locks read back as
  `LockPresent: false`, so it is irrelevant.)
- Add `CanonicalPath string` to `Diagnosis` (line 186) and populate it in
  `Inspect` (line 231) from `cur.CanonicalPath`.

**Test-helper updates** (the wire struct is mirrored in two places)
- `internal/proxy/lifecycle_test.go` `lockJSON` and
  `internal/cli/doctor_test.go` `doctorLockJSON`: add
  `CanonicalPath string `json:"canonical_path"``.
- Update any existing assertion that compares full lock contents after `Attach`
  to expect `CanonicalPath == testLayout().Canonical`.

**Tests**
- `internal/proxy/lifecycle_test.go`: assert `Attach` records `CanonicalPath`; a
  second agent attaching and one detaching (non-final) preserves it; `Inspect`
  surfaces it.

**Success criteria**
- [x] `go build ./...`
- [x] `go test -race ./internal/proxy/...`
- [x] existing doctor tests still green (`go test -race ./internal/doctor/... ./internal/cli/...`)

---

## Phase 3 — `proxy.Manager.Clean`

**`internal/proxy/lifecycle.go`**
- Extend `CleanResult` with `Refused bool` and `LiveAgents []int` (zero-valued for
  existing `CleanOrphan` callers — doctor unaffected).
- Add:
  ```go
  // Clean is the `agent-creance clean` primitive: an unconditional, idempotent
  // teardown of this project's proxy. Under the flock it prunes dead agents and,
  // unless force is set, REFUSES when live agents remain (warn-never-kill),
  // returning Refused with their PIDs and mutating nothing. Otherwise it SIGTERMs
  // the recorded proxy if alive, purges the session overlay, and clears the lock
  // state. Safe to run repeatedly and when nothing is running (Cleaned=false).
  func (m *Manager) Clean(layout state.Layout, force bool) (CleanResult, error)
  ```
  Body: acquire flock (defer release); `readLock`; `live := m.pruneDead(cur.Agents)`;
  if `len(live) > 0 && !force` → return `CleanResult{Refused: true, LiveAgents: live}`;
  else: if `cur.ProxyPID != 0 && m.proc.Alive(cur.ProxyPID)` → `Signal(SIGTERM)` and
  set `res.Cleaned, res.ProxyPID`; `sysdep.RemoveIfPresent(overlay)`;
  `writeLock(lf, lockState{})`; return res.

**Tests** — `internal/proxy/clean_test.go` (reuse `newHarness`/`seedLock`):
- tears down a running-with-no-live-agents proxy: SIGTERM, overlay purged, lock
  cleared, `Cleaned=true`.
- idempotent no-op on an absent/zeroed lock: `Cleaned=false`, no signal, no error.
- refuses with live agents and `force=false`: `Refused=true`, `LiveAgents` set, no
  signal, lock untouched.
- `force=true` with live agents: tears down anyway, `Cleaned=true`.
- overlay-only purge when proxy already dead but overlay present.

**Success criteria**
- [x] `go build ./...`
- [x] `go test -race ./internal/proxy/...`

---

## Phase 4 — `internal/status` package

Mirror `internal/doctor`: side-effecting `Scanner`, pure golden-tested `Render`.

**`internal/status/status.go`**
- `Scanner{ Manager *proxy.Manager; Resolver *state.Resolver; FS sysdep.FileSystem }`.
- `Scan() (Report, error)`: `root, err := Resolver.ProjectsRoot()`;
  `entries, err := FS.ReadDir(root)` — treat `fs.ErrNotExist` as empty; for each
  dir entry build `state.LayoutForRoot(filepath.Join(root, name))`, call
  `Manager.Inspect`; append `ProjectStatus{Hash, Diag}` when `diag.LockPresent`.
  Sort projects by `Hash` for determinism.

**`internal/status/report.go`** (pure)
- `Report{ Projects []ProjectStatus }`, `ProjectStatus{ Hash string; Diag proxy.Diagnosis }`.
- `Render(Report) string`: "No active cages.\n" when empty; otherwise a
  `text/tabwriter` table with header `PROJECT  STATE  PORT  AGENTS`. Per row:
  PROJECT = `Diag.CanonicalPath` or, when empty, `Hash`; STATE derived from the
  Diagnosis (`orphan`/`stranded`/`running`/`down`); PORT = port or `-` when not
  applicable; AGENTS = `len(Diag.LiveAgents)`.
- Keep a single `state(d proxy.Diagnosis) string` helper so the verdict has one
  source of truth.

**Tests**
- `internal/status/report_test.go`: golden cases (`-update` flag, `testdata/render_*.golden`):
  `empty`, `running`, `orphan`, `stranded`, `mixed` (several projects, exercise the
  hash-fallback row), matching the doctor golden pattern.
- `internal/status/status_test.go`: `Scan` over a `FakeFileSystem` seeded with
  several `projects/<hash>` dirs + a `FakeFlock` seeded with per-project locks +
  `FakeProcessManager`/`FakePortAllocator` liveness; assert the right rows, that
  zeroed/absent locks are skipped, and that a missing `projects/` root yields an
  empty report.

**Success criteria**
- [x] `go build ./...`
- [x] `go test -race ./internal/status/...`
- [x] `make golden` produces only the intended new golden files (review diff)

---

## Phase 5 — CLI commands

**`internal/cli/status.go`**
- `newStatusCmd(app)` (`Use: "status"`, `Args: cobra.NoArgs`) → `runStatus(app)`.
- `runStatus`: build `status.Scanner{ Manager: proxy.NewManager(app.FS, app.Flock,
  app.ProcessManager, app.PortAllocator, app.Stderr), Resolver: state.New(app.Paths),
  FS: app.FS }`; `Scan()`; `fmt.Fprint(app.Stdout, status.Render(rep))`. Exit 0.

**`internal/cli/clean.go`**
- `newCleanCmd(app)` with a `--force` bool flag (`Use: "clean"`, `Args: cobra.NoArgs`)
  → `runClean(app, force)`.
- `runClean`: resolve `.` via `state.New(app.Paths).Resolve(".")`; build the
  `proxy.Manager`; `res, err := mgr.Clean(layout, force)`; render outcome to stdout:
  - `Refused` → print "N agent(s) still attached (PIDs …); stop them or re-run with --force"
    and return an error (non-zero exit).
  - `Cleaned` → "stopped proxy (pid X); cleared lock and session overlay".
  - otherwise → "nothing to clean (no proxy running)".

**`internal/cli/cli.go`**
- Register `newStatusCmd(app)` and `newCleanCmd(app)` in `newRootCmd`.

**Tests**
- `internal/cli/status_test.go`: fixture mirroring `doctorFixture`; seed multiple
  project dirs (`FakeFileSystem.Dirs`) + locks (`FakeFlock.Contents`) + liveness;
  assert the rendered rows + "No active cages." path.
- `internal/cli/clean_test.go`: clean stops a seeded (dead-agent) proxy and clears
  the lock; idempotent second run; refuses with live agents (non-zero, message);
  `--force` overrides.
- testscript scenarios under `internal/cli/testdata/script/` with a new `seedlock`
  custom command (modelled on `seedAudit` in `script_test.go`) that writes a
  fixture `proxy.lock` at the CLI's resolved hash path:
  - `status_lists.txtar`: seed a lock with a dead proxy PID → `status` lists it as
    `down`; assert the project path/columns render.
  - `status_empty.txtar`: no locks → "No active cages.".
  - `clean_idempotent.txtar`: seed a dead-proxy lock → `clean` succeeds; second
    `clean` is a no-op; `clean` with no lock at all succeeds.

**Success criteria**
- [x] `go build ./...`
- [x] `go test -race ./internal/cli/...`
- [x] `make lint`

---

## Phase 6 — Integration test

Mirror `internal/cli/doctor_fix_integration_test.go` (`//go:build integration`):
spawn a real `mitmdump` listening on a real port, write a real `proxy.lock`
(no live agents) at a temp state dir, run the real `Manager.Clean` over the OS
seams, and assert the proxy stops listening, the lock is cleared, and the session
overlay is purged. (Driving the full `run` → `clean` path needs the entire cage
stack; the established precedent spawns the proxy directly, which proves the same
teardown contract.)

**Success criteria**
- [ ] `make test-integration` passes locally (documents real-tool behaviour;
      requires `mitmdump` on PATH).

---

## Final verification

- [ ] `make test` green (race, hermetic).
- [ ] `make lint` clean.
- [ ] `go build ./...`.
- [ ] `make test-integration` (where tools are available).
- [ ] C4 guard: `status`/`clean` only read/write under
      `~/.cache/agent-creance/projects/<hash>/` (no in-tree state).
- [ ] Update `thoughts/shared/tickets/AC-0032-status-clean-commands.md` —
      check the acceptance boxes and mark the ticket Done.

## Notes / risks

- **`proxy.lock` schema change is additive & backward-compatible**: an older lock
  without `canonical_path` unmarshals with the field empty; `status` falls back to
  the hash. No migration needed.
- **"removes the lock"**: implemented as *clearing* the lock state (write zero
  `lockState`), consistent with the architecture's "the lock file is the flock
  target, never unlinked" invariant (lifecycle.go:175). A cleared lock reads back
  as `LockPresent: false`, so `status` treats the project as gone — behaviourally
  equivalent to removal.
- **No cross-project mutation in `status`**: it only takes each project's own
  flock for a read; `clean` only touches the current project.
</content>
