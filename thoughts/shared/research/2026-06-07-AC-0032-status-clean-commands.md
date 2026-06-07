---
date: 2026-06-07
ticket: AC-0032
title: "Research: `status` / `clean` commands (WP-6.3)"
status: complete
commit: 99514a9506b8cb19379c107bf8b3b50a845c0824
researcher: Claude (tce:work)
---

# Research: `status` / `clean` commands (AC-0032 / WP-6.3)

## Research question

How should `agent-creance status` (list running cages across all projects) and
`agent-creance clean` (tear down this project's proxy + lock + overlay,
idempotently and orphan-safe) be implemented, reusing the existing proxy
lifecycle (AC-0020) and doctor diagnosis (AC-0031) machinery?

## Summary / verdict

Both commands are **thin orchestration over already-built, hermetically-tested
machinery** — almost no new security-critical logic.

- `clean` is **`CleanOrphan` without the orphan guard**: SIGTERM the recorded
  proxy if alive, purge the session overlay, clear the lock state — but
  unconditionally and idempotently, not only when the proxy is a true orphan.
  All the primitives already exist in `internal/proxy/lifecycle.go`.
- `status` is **`Inspect` (AC-0031) run across every project**: enumerate
  `~/.cache/agent-creance/projects/*`, call the existing read-only
  `Manager.Inspect` per project, and render the results. The only genuinely new
  infrastructure it needs is a directory-listing seam (`FileSystem.ReadDir`) and
  a `Resolver.ProjectsRoot()` path accessor — the rest is a pure, golden-tested
  renderer mirroring `internal/doctor/report.go`.

Three design decisions need human input before planning (see "Open questions"):
the `status` output format (the ticket's own open question), whether to make the
project column show a readable path (requires a small `proxy.lock` schema
addition) or just the opaque hash, and `clean`'s behaviour when **live agents**
are still attached (force-stop vs. warn-never-kill refuse).

## The ticket

`thoughts/shared/tickets/AC-0032-status-clean-commands.md`. Acceptance criteria:

- `status` enumerates `~/.cache/agent-creance/projects/*/proxy.lock`, showing per
  project: proxy alive?, port, attached agent count.
- `clean` stops this project's proxy (if any), removes the lock, purges the
  session overlay; idempotent (safe twice; safe when nothing is running).
- Neither command corrupts another project's state.
- Out of scope: diagnostics/`--fix` (AC-0031), cross-user/system-wide cleanup.
- Open question already flagged in the ticket: `status` output format (table) and
  whether to show policy hash / staleness.

## Spec (WP-6.3) and design context

From `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`
(lines 342-345) and `docs/design.md`:

- `status`: "running cages across all projects (read every lock)." Done when
  `status` lists fixtures.
- `clean`: "tear down this project's proxy + lock + overlay; orphan-safe." Done
  when `clean` is idempotent and purges the overlay.
- `docs/design.md` line 305: the session overlay "is purged on last-agent-exit
  teardown … Explicit `agent-creance clean` also removes it, for the
  orphan-cleanup path where no live teardown ran." — this is the canonical
  mandate for `clean` purging the overlay.
- `docs/design.md` line 412: the "port changed under attached agents" condition is
  surfaced by both `doctor` **and** `status` — i.e. `status` should render the
  *stranded* state, which `Inspect` already computes.
- The output **format/columns are unspecified** in the docs — a deliberate design
  choice for this ticket.

## How the machinery already works (key findings)

### Proxy lifecycle — `internal/proxy/lifecycle.go`

The lock file (`proxy.lock`) payload is `lockState` (unexported, lines 36-45):

```go
type lockState struct {
    ProxyPID   int    `json:"proxy_pid"`
    Port       int    `json:"port"`
    PolicyHash string `json:"policy_hash"`
    Agents     []int  `json:"agents"`
}
```

There is **no project path / canonical path** recorded — only the hash (the
project state-dir name) ties a lock back to a project. This is central to the
"status identity" open question.

`Manager` (line 50) holds seams `fs`, `lock`, `proc`, `ports`, `warn`.

- **`Inspect(layout) (Diagnosis, error)`** (line 214) — read-only, AC-0031. Reads
  the lock under the flock, prunes dead agent PIDs, computes
  `ProxyUp = ProxyPID alive AND port probe`, and returns
  `Diagnosis{LockPresent, ProxyPID, Port, ProxyUp, LiveAgents, Orphan, Stranded}`
  (lines 186-208). This is **exactly the per-project datum `status` needs**, and
  it already encodes orphan/stranded/healthy. `status` calls this once per
  project.
- **`CleanOrphan(layout) (CleanResult, error)`** (line 256) — AC-0031's `--fix`
  primitive. Under the flock: if (and only if) the proxy is a true orphan (up,
  zero live agents) it SIGTERMs the proxy by PID, purges the overlay via
  `sysdep.RemoveIfPresent`, and clears the lock to `lockState{PolicyHash: cur.PolicyHash}`.
  Non-orphan → safe no-op (`Cleaned=false`). **`clean` is this minus the
  `if !up || len(live) > 0 { return no-op }` guard** (lifecycle.go:269) and made
  idempotent on an absent/empty lock.
- **`Detach`** (line 152) — the last-out teardown the design references: same
  SIGTERM + overlay purge + clear-lock sequence. Confirms `clean`'s shape.
- Note the architecture **never deletes `proxy.lock`** — it is the flock target,
  written in place; teardown *clears* it (line 175-177: "Keep the lock file (it
  is the flock target) but clear the proxy state"). So the ticket's "removes the
  lock" should mean *clear the recorded state*, not unlink the file. (Unlinking
  is also awkward to test: the fake lock contents live in `FakeFlock.Contents`,
  not `FakeFileSystem.Files`, so `FS.Remove` wouldn't clear them.)

### State layout — `internal/state/state.go`

- `Resolver.Resolve(dir)` (line 84) canonicalises → hash → `Layout{Canonical,
  Hash, Root}` where `Root = <cache>/agent-creance/projects/<hash>` (line 114).
  `clean` resolves `.` exactly like `run`/`doctor`.
- `Layout` accessors: `ProxyLock()` (line 201), `SessionOverlay()` (line 218),
  `PolicyJSON()`, `EgressJSONL()`, etc. — all derive from `Root`.
- The hash is **one-way** (SHA-256 of the canonical path, line 103): you cannot
  recover the project directory from the state-dir name. → `status` can show the
  hash for free, but a readable project path needs it stored somewhere.
- The package does **no I/O** by design (doc comment lines 12-16) — only path
  computation. So directory *enumeration* for `status` must go through a
  `FileSystem` seam in the caller, not into `Resolver`. There is currently no
  `ProjectsRoot()` accessor and no listing method.

### Doctor — the precedent to mirror

- `internal/doctor/doctor.go`: `Checker` bundles seams; `checkProxy(fix)` (line 73)
  does `Resolver.Resolve(".")` → `Manager.Inspect` → optional `CleanOrphan`. This
  is the exact pattern for both new commands.
- `internal/doctor/report.go`: pure `Render(Report)` (line 105) with status glyphs
  (`✓`/`⚠`/`✗`), section structs, and `renderProxy` (line 143) which already
  formats running/orphan/stranded lines from a `Diagnosis`. **The `status`
  renderer should mirror this** (pure function, golden-tested).
- `internal/cli/doctor.go`: `newDoctorCmd` + `runDoctor` — the cobra-registration
  and testable-body template (`RunE` → `run<Cmd>(ctx, app, …)`).

### sysdep filesystem seam — `internal/sysdep/filesystem.go`

The `FileSystem` interface has `ReadFile/WriteFile/Stat/MkdirAll/Remove/Rename`
but **no `ReadDir`**. A grep confirms no `ReadDir` exists in any seam (only two
tests call `os.ReadDir` directly). `status` needs to list project dirs, so this is
the one new seam method required:
- add `ReadDir(name string) ([]fs.DirEntry, error)` to the interface,
- implement on `OSFileSystem` (delegates to `os.ReadDir`),
- add to `FakeFileSystem` (`internal/sysdep/sysdeptest/filesystem.go`) backed by
  the existing `Dirs`/`Files` maps.
`sysdep.RemoveIfPresent` (filesystem.go:82) already exists for idempotent overlay
purge — `clean` reuses it.

### Composition root — `internal/cli/cli.go`

`App` already injects every seam both commands need (`FS`, `Paths`, `Flock`,
`ProcessManager`, `PortAllocator`, `Stderr`). `newRootCmd` (line 66) wires
commands via `root.AddCommand(newXxxCmd(app))` — add `newStatusCmd` and
`newCleanCmd` here.

## Test infrastructure (what to mirror)

- **proxy unit/golden**: `internal/proxy/lifecycle_test.go` (`newHarness`,
  `seedLock`, `lockJSON`) and `diagnose_test.go` are the templates for any new
  `clean` Manager method test — seed a lock, set `proc.AlivePIDs`/`ports.Listening`,
  assert `proc.Signaled`, overlay purge, and resulting lock contents.
- **renderer golden**: `internal/doctor/report_test.go` — `goldenCases()` map +
  `-update` flag + `testdata/render_*.golden`. The `status` renderer gets the same
  treatment (a new `internal/status` package, mirroring `internal/doctor`).
- **CLI unit**: `internal/cli/doctor_test.go` — `doctorFixture` wires an `App`
  with fakes, seeds the lock via `FakeFlock.Contents` (`seedLock`), and asserts on
  captured stdout. `status`/`clean` get analogous fixtures.
- **CLI behaviour (testscript)**: `internal/cli/testdata/script/doctor_healthy.txtar`
  + `script_test.go`. Note `seedAudit` (script_test.go:63) shows the pattern for
  pre-seeding out-of-tree state at the CLI's resolved hash path — a `clean`/`status`
  txtar would need a similar custom command (`seedlock`) to plant fixture locks,
  since the path is hashed from `$WORK`'s realpath.
- **Integration**: `internal/proxy/lifecycle_integration_test.go` is the model for
  the ticket's integration step (real `run` → `clean` → assert proxy gone).

## Proposed shape (for the plan)

1. **seam**: add `FileSystem.ReadDir` (+ OS impl + fake).
2. **state**: add `Resolver.ProjectsRoot()` (path-only) and a
   `state.LayoutForRoot(root)`/inline `Layout{Hash, Root}` constructor so `status`
   can build a `Layout` per enumerated dir (Canonical unknown). `Inspect` only
   needs `Root`.
3. **proxy**: add `Manager.Clean(layout) (CleanResult, error)` — unconditional,
   idempotent teardown (the `CleanOrphan` body minus the orphan guard; decision
   pending on live-agent handling).
4. **status package** (`internal/status/`): `Scanner` (seams: `Manager`,
   `Resolver`, `FS`) → `Report` ([]project + `Diagnosis`); pure `Render` →
   golden. Mirrors `internal/doctor`.
5. **cli**: `internal/cli/status.go` + `clean.go` (+ register in `cli.go`).
6. **tests**: proxy `clean` unit tests; `status` renderer golden; cli fixtures for
   both; testscript scenarios; integration test.

## Open questions (for the checkpoint)

1. **`status` output format** (ticket's own open question). A simple aligned text
   table (PROJECT, STATE, PORT, AGENTS)? Whether to add a POLICY-HASH column.
   "Staleness" (lock hash vs current input hash) would require re-running the
   compiler per project — likely too heavy / out of scope; recommend excluding.
2. **Project identity in `status`** — the hash is one-way, so a readable project
   path requires storing the canonical path in `proxy.lock` (a small additive
   schema change to `lockState`, written by `Attach`; backward compatible). Show
   the opaque hash only, or add the path?
3. **`clean` with live attached agents** — force-stop the proxy regardless
   (explicit operator command), refuse with a warn-never-kill message, or refuse
   by default with a `--force` override?

## References

- Ticket: `thoughts/shared/tickets/AC-0032-status-clean-commands.md`
- Spec: `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md` (WP-6.3, lines 342-345)
- Sibling work: AC-0031 (doctor) research/plan/status under `thoughts/shared/`, AC-0020 (lifecycle), AC-0006 (state dir)
- Code: `internal/proxy/lifecycle.go`, `internal/state/state.go`, `internal/doctor/{doctor,report}.go`, `internal/cli/{cli,doctor,run}.go`, `internal/sysdep/filesystem.go`
</content>
</invoke>
