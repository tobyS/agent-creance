# AC-0031: `doctor` extension (WP-6.2) Implementation Plan

## Overview

Turn `agent-creance doctor` from a version-only check into the full "tell me everything that's wrong" command. It adds five diagnostics on top of the existing version report — CA live-verify, orphan-proxy scan, exposed-host-service (0.0.0.0) scan, flock-unreliable-filesystem warning (iCloud/SMB), and a stranded-agents / port-change condition — plus a `--fix` flag that safely cleans orphan proxies and reports what it changed. Exit code is non-zero when an actionable problem remains.

## Current State Analysis

`internal/cli/doctor.go:14-38` is a thin slice: `prereq.Check` → `prereq.Report` → if any tool missing, append `prereq.MissingInstructions` and return a non-nil error (exit 1). Its header comment already pre-declares the five additions. The version report already surfaces *every* skew including patch (`prereq.Report`; `Skew.Loud()` is only consulted by run/setup), so **the version section needs no change** and its golden (`internal/prereq/testdata/doctor_report.golden`) must stay byte-identical.

Two subsystems this reuses are fully built and wired:
- **CA live-verify** — `setup.Installer.Verify(ctx) (setup.Result, error)` (`internal/setup/setup.go:203-226`). Status-as-data: untrusted is `Result.Status`/`Message()`, a non-nil error is an environment failure. All seven seams it needs are already `App` fields wired in `Main()`.
- **Proxy lifecycle / locks** — `proxy.Manager` + the `proxy.lock` JSON schema (`internal/proxy/lifecycle.go:36-45`) and the liveness composite `proxyUp := ProxyPID!=0 && proc.Alive(ProxyPID) && ports.Probe(Port)` (`lifecycle.go:116`). Last-out teardown (`Detach`, `lifecycle.go:152-182`) is the cleanup `--fix` mirrors.

Two checks need **new `sysdep` seams** (nothing exists today): filesystem-type detection and network-listener enumeration. `sysdep.FileSystem` has no `ReadDir` and `state.Resolver` has no cross-project enumerator — but per the decisions below the **orphan scan is scoped to the current project only**, so no cross-project enumeration is built here (left to AC-0032).

### Key Discoveries:
- `prereq.Report` returns one string; new sections compose *after* it in a new `internal/doctor` renderer — leave `prereq` untouched (`internal/prereq/report.go:20-48`).
- The full doctor cannot be exercised hermetically through `cli.Main`/testscript: a live CA verify spawns real `mitmdump`, the listener scan shells `lsof`. So **full-report correctness is tested via Go unit + golden tests with `sysdeptest` fakes** (the `setup`/`run` precedent, e.g. `internal/cli/setup_test.go`), and each check **degrades gracefully** (an unavailable tool → a non-fatal "could not check" finding, not a crash) so the existing hermetic `doctor_*.txtar` testscripts keep passing. The `--fix` orphan cleanup is proven in a `//go:build integration` test (ticket test step 4).
- `Verify` spawning a bare `mitmdump` **materialises the CA as a side effect** (first run writes `~/.mitmproxy`). To keep doctor read-only when no CA exists, doctor must check CA presence *before* calling `Verify` — hence a new read-only `Installer.CAGenerated()`.
- cwd → project state is `state.New(app.Paths).Resolve(".")` (`internal/cli/logs.go:31`).

## Desired End State

`agent-creance doctor` prints, in order: Version compatibility (unchanged), CA trust, Proxy health (current project), Exposed host services, Filesystem reliability — then the existing missing-prereq install block when applicable. `doctor --fix` additionally tears down an orphan proxy and prints what it cleaned. Exit is non-zero iff an actionable problem remains: **untrusted CA, an orphan proxy not cleaned (i.e. `--fix` not passed), or a missing prerequisite**. All other findings (patch skew, exposed services, iCloud/SMB filesystem, stranded agents, CA-not-generated, any "could not check") are warnings with exit 0. Existing `prereq` golden + `doctor_healthy`/`doctor_missing` testscripts stay green.

## Resolved Decisions (from the question checkpoint)

1. **Exit codes** — actionable (non-zero): untrusted CA, un-fixed orphan proxy, missing prereqs. Everything else: warning, exit 0.
2. **Orphan scope** — current project only. No `ReadDir` seam / `ProjectsRoot()` built here.
3. **CA not generated** — report "run `agent-creance setup`", do **not** generate (doctor stays read-only). Only a definitive *untrusted* verdict is actionable; not-generated and "could not verify" are warnings.
4. **Filesystem warning** — check both the agent-creance cache dir (`<cache>/agent-creance`, where `proxy.lock` lives) and the current working dir.

## What We're NOT Doing

- No cross-project orphan scan / no `status` / `clean` commands (AC-0032). No `ReadDir` on `sysdep.FileSystem`, no `Resolver.ProjectsRoot()`.
- No CA generation or trust mutation in doctor (no `EnsureCA`/`InstallCA`/`Bootstrap`).
- No killing of live attached agents (warn-never-kill). `--fix` only cleans true orphans (proxy up, zero live agents).
- No change to `prereq.Report` / the version golden, and no new distinct exit codes (the codebase has only binary exit-1).
- No `libproc`/cgo for the listener scan — shell out to `lsof`.
- No persisted old-vs-new port history; the "port change" requirement is satisfied by detecting the persistent symptom (stranded agents — see Phase 2).

## Implementation Approach

Follow the project seam discipline: every new OS touch is a `sysdep` interface with a real impl + a `sysdeptest` fake; pure logic (lsof parsing, fs-type classification, report rendering, exit aggregation) is table/golden tested. Orchestration lives in a new `internal/doctor` package producing a `Report` data value (status-as-data); a pure `Render` turns it into text (golden); `cli/doctor.go` stays thin (build checker from `App`, run, optionally fix, render, decide exit). Proxy orphan/stranded detection + cleanup are added to `proxy.Manager` (where the lock schema and liveness primitives already live) rather than reimplemented.

---

## Phase 1: New sysdep seams — filesystem type + listener scan

### Overview
Add the two missing OS seams, their fakes, pure-helper unit tests, and wire them into `App`/`Main`.

### Changes Required:

#### 1. Filesystem-type seam
**File**: `internal/sysdep/fstype.go` (new)
```go
package sysdep

// FSInfo describes the filesystem a path resides on, for the doctor
// flock-reliability warning. Name is the statfs f_fstypename (e.g. "apfs",
// "smbfs"); Local is the MNT_LOCAL flag (false ⇒ a network mount).
type FSInfo struct {
    Name  string
    Local bool
}

// FilesystemTyper reports the filesystem type of a path. Advisory locks
// (flock/fcntl) are unreliable on network mounts (smbfs/afpfs/nfs/webdav) and on
// iCloud Drive; doctor uses this to warn when state/working dirs land there.
type FilesystemTyper interface {
    // FSType returns the filesystem info for path. A non-existent path yields an
    // error satisfying errors.Is(err, fs.ErrNotExist).
    FSType(path string) (FSInfo, error)
}

type OSFilesystemTyper struct{}
var _ FilesystemTyper = (*OSFilesystemTyper)(nil)

func (OSFilesystemTyper) FSType(path string) (FSInfo, error) {
    var st unix.Statfs_t
    if err := unix.Statfs(path, &st); err != nil {
        return FSInfo{}, err // ENOENT surfaces as fs.ErrNotExist-compatible via os mapping if wrapped; see note
    }
    name := string(st.Fstypename[:])
    if i := strings.IndexByte(name, 0); i >= 0 {
        name = name[:i]
    }
    return FSInfo{Name: name, Local: st.Flags&unix.MNT_LOCAL != 0}, nil
}
```
Notes: `unix.Statfs` returns a raw `syscall.Errno` (e.g. `ENOENT`), which already satisfies `errors.Is(err, fs.ErrNotExist)` (Go maps `ENOENT`). `golang.org/x/sys/unix` is already a dependency (`flock.go:8`). The iCloud case is **path-based**, handled in the doctor logic (Phase 3), not here — modern iCloud reports `apfs` + `MNT_LOCAL`.

**File**: `internal/sysdep/sysdeptest/fstype.go` (new)
```go
// FakeFilesystemTyper: FSType(path) returns Types[path]; absent key ⇒ Err or a
// default local apfs. Records Calls.
type FakeFilesystemTyper struct {
    Types   map[string]sysdep.FSInfo
    Default sysdep.FSInfo // returned for unknown paths (default: {"apfs", true})
    Err     error
    Calls   []string
}
```

#### 2. Listener-scan seam
**File**: `internal/sysdep/listener.go` (new)
```go
// Listener is one listening TCP socket discovered on the host.
type Listener struct {
    Command string // process name (lsof "c")
    PID     int    // owning pid (lsof "p")
    Address string // bind address as lsof renders it: "*:8080", "127.0.0.1:7000", "[::1]:631"
}

// ListenerScanner enumerates listening TCP sockets, for doctor's exposed-service
// scan. Wildcard binds ("*:port" ⇒ 0.0.0.0 / ::) are "exposed".
type ListenerScanner interface {
    Listeners(ctx context.Context) ([]Listener, error)
}

type OSListenerScanner struct{}
var _ ListenerScanner = (*OSListenerScanner)(nil)

// Real impl shells: lsof -nP -iTCP -sTCP:LISTEN -F pcn
// -n/-P keep addresses numeric so 0.0.0.0 & :: both render as "*:port".
// lsof exits 1 with no output when there are no matches → treat as empty, not error.
func (OSListenerScanner) Listeners(ctx context.Context) ([]Listener, error) { /* exec + ParseLsof */ }

// ParseLsof parses lsof -F field output into Listeners. Pure; table-tested.
func ParseLsof(out []byte) []Listener { /* p=pid, c=command, f starts an fd, n=name */ }

// IsExposed reports whether a bind address is on all interfaces (wildcard).
// True for "*:…", "0.0.0.0:…", "[::]:…", ":::…"; false for loopback/specific IPs.
func IsExposed(addr string) bool { /* pure */ }
```
`OSListenerScanner` calls `exec.CommandContext` directly (the sysdep impl layer, like `OSKeychain`/`OSTLSProber`). Distinguish "no matches" (exit 1, empty stdout → `nil, nil`) from a real failure (lsof missing / other error → return error so doctor can render "could not scan").

**File**: `internal/sysdep/sysdeptest/listener.go` (new) — `FakeListenerScanner{ List []sysdep.Listener; Err error }`.

#### 3. Wire into App
**File**: `internal/cli/cli.go` — add fields `FSType sysdep.FilesystemTyper` and `Listeners sysdep.ListenerScanner` to `App` (`:20-52`) with a doc comment; wire `sysdep.OSFilesystemTyper{}` / `sysdep.OSListenerScanner{}` in `Main()` (`:88-105`).

### Success Criteria:

#### Automated Verification:
- [x] `go build ./...` compiles
- [x] `make test` passes (new `ParseLsof`/`IsExposed` table tests; `FakeFilesystemTyper`/`FakeListenerScanner` compile-time interface assertions hold)
- [x] `make lint` clean

#### Manual Verification:
- [ ] On a real Mac, a throwaway program calling `OSListenerScanner.Listeners` lists known listeners and flags a `0.0.0.0`-bound one as `*:port`; `OSFilesystemTyper.FSType` returns `apfs`/local for a normal path (covered by Phase 5 integration tests).

---

## Phase 2: Proxy lifecycle diagnostics — Inspect + CleanOrphan

### Overview
Add read-only orphan/stranded detection and orphan cleanup to `proxy.Manager`, reusing the existing lock schema and liveness primitives. Also add `state.Resolver.CacheDir()` for the filesystem check.

### Changes Required:

#### 1. Diagnosis + Inspect (read-only)
**File**: `internal/proxy/lifecycle.go`
```go
// Diagnosis is doctor's read-only view of a project's proxy lifecycle, computed
// from proxy.lock plus live PID/port probes. No mutation.
type Diagnosis struct {
    LockPresent bool
    ProxyPID    int
    Port        int
    ProxyUp     bool  // ProxyPID alive AND Port listening (the lifecycle.go:116 composite)
    LiveAgents  []int // attached agent PIDs still alive (pruneDead of Agents)
    Orphan      bool  // ProxyUp && len(LiveAgents) == 0
    Stranded    bool  // !ProxyUp && len(LiveAgents) > 0  (proxy gone/moved; agents point at a dead port)
}

// Inspect reads the project's proxy.lock under the flock and reports its health
// without mutating anything. A missing/empty/corrupt lock ⇒ Diagnosis{LockPresent:false}.
func (m *Manager) Inspect(layout state.Layout) (Diagnosis, error) {
    lf, err := m.lock.Acquire(layout.ProxyLock())
    if err != nil { return Diagnosis{}, fmt.Errorf("proxy: acquire lock: %w", err) }
    defer func() { _ = lf.Release() }()
    cur, err := readLock(lf)
    if err != nil { return Diagnosis{}, err }
    if cur.ProxyPID == 0 && len(cur.Agents) == 0 { return Diagnosis{LockPresent: false}, nil }
    live := m.pruneDead(cur.Agents)
    up := cur.ProxyPID != 0 && m.proc.Alive(cur.ProxyPID) && m.ports.Probe(cur.Port)
    return Diagnosis{
        LockPresent: true, ProxyPID: cur.ProxyPID, Port: cur.Port, ProxyUp: up,
        LiveAgents: live, Orphan: up && len(live) == 0, Stranded: !up && len(live) > 0,
    }, nil
}
```
**Port-change rationale (document in code comment):** AC-0020 surfaces "port changed under attached agents" only transiently (`warnPortChanged`, `lifecycle.go:219`) and persists no old port. The persistent, detectable manifestation is `Stranded` — live attached agents while the proxy is not reachable on the recorded port (design.md "Multi-agent lifecycle": the stranded agent's next request gets connection-refused). That is what doctor reports for this condition.

#### 2. CleanOrphan (the `--fix` primitive)
**File**: `internal/proxy/lifecycle.go`
```go
// CleanResult reports what CleanOrphan changed.
type CleanResult struct {
    Cleaned   bool // true iff an orphan was found and torn down
    ProxyPID  int  // the proxy that was signalled (0 if none alive)
}

// CleanOrphan re-checks under the flock and, only if the project's proxy is a true
// orphan (up, zero live agents), tears it down exactly like last-out Detach: SIGTERM
// the proxy by PID, purge the session overlay, clear the lock's proxy state. It never
// touches a proxy with live agents (warn-never-kill).
func (m *Manager) CleanOrphan(layout state.Layout) (CleanResult, error) {
    lf, err := m.lock.Acquire(layout.ProxyLock())
    if err != nil { return CleanResult{}, fmt.Errorf("proxy: acquire lock: %w", err) }
    defer func() { _ = lf.Release() }()
    cur, err := readLock(lf)
    if err != nil { return CleanResult{}, err }
    live := m.pruneDead(cur.Agents)
    up := cur.ProxyPID != 0 && m.proc.Alive(cur.ProxyPID) && m.ports.Probe(cur.Port)
    if !up || len(live) > 0 { return CleanResult{}, nil } // not an orphan
    if err := m.proc.Signal(cur.ProxyPID, syscall.SIGTERM); err != nil {
        return CleanResult{}, fmt.Errorf("proxy: stop orphan mitmproxy: %w", err)
    }
    if _, err := sysdep.RemoveIfPresent(m.fs, layout.SessionOverlay()); err != nil {
        return CleanResult{}, fmt.Errorf("proxy: purge session overlay: %w", err)
    }
    if err := writeLock(lf, lockState{PolicyHash: cur.PolicyHash}); err != nil {
        return CleanResult{}, err
    }
    return CleanResult{Cleaned: true, ProxyPID: cur.ProxyPID}, nil
}
```
(The teardown body mirrors `Detach`'s last-out branch `lifecycle.go:165-178`; factor a small private `tearDownProxy(lf, cur)` helper if it reads cleanly, but a duplicate three-liner is acceptable.)

#### 3. CacheDir accessor
**File**: `internal/state/state.go` — add `func (r *Resolver) CacheDir() (string, error)` returning `filepath.Join(cache, appCacheSubdir)` (mirrors `RegistriesRoot` etc., `state.go:124-160`). Doctor probes this path's filesystem.

### Success Criteria:

#### Automated Verification:
- [x] `make test` passes — new table/unit tests in `internal/proxy` for `Inspect` (orphan, stranded, healthy-with-agents, no-lock) and `CleanOrphan` (cleans orphan; no-op when live agents present; no-op when proxy down) using `FakeFlock` seeded with lock JSON + `FakeProcessManager.AlivePIDs` + `FakePortAllocator.Listening`
- [x] `internal/state` test for `CacheDir()`
- [x] `make lint` clean

#### Manual Verification:
- [ ] Covered by the Phase 5 integration test (real proxy + lock).

---

## Phase 3: CA presence helper + `internal/doctor` orchestration & rendering

### Overview
Add a read-only `Installer.CAGenerated()`, then build the `internal/doctor` package: the `Report` data model, the `Checker` that runs every diagnostic (status-as-data, graceful degradation), the pure `Render`, the `Fix`, and the exit-code aggregation. Golden + unit tested.

### Changes Required:

#### 1. CA presence (read-only, no generation)
**File**: `internal/setup/setup.go`
```go
// CAGenerated reports whether the mitmproxy CA cert already exists, WITHOUT
// generating it. doctor uses this to stay read-only: Verify() spawns mitmdump,
// which would materialise the CA as a side effect, so doctor only calls Verify
// when a CA is already present.
func (i *Installer) CAGenerated() (bool, error) {
    certPath, err := i.caCertPath()
    if err != nil { return false, err }
    switch _, err := i.fs.Stat(certPath); {
    case err == nil: return true, nil
    case errors.Is(err, fs.ErrNotExist): return false, nil
    default: return false, fmt.Errorf("setup: stat CA %s: %w", certPath, err)
    }
}
```
Unit test in `internal/setup` (present / absent / stat-error) via `FakeFileSystem` + `FakePathResolver`.

#### 2. Doctor report model + renderer
**File**: `internal/doctor/report.go` (new)
```go
package doctor

// Status is a per-section verdict.
type Status int
const ( StatusOK Status = iota; StatusWarn; StatusProblem; StatusSkipped )

// Report is the full doctor result as data (status-as-data); Render turns it into text.
type Report struct {
    Version  []prereq.Result   // existing version section (rendered via prereq.Report)
    CA       CASection
    Proxy    ProxySection
    Exposed  ExposedSection
    FS       FSSection
    Missing  string            // prereq.MissingInstructions(Version), may be ""
}

type CASection struct { State Status; Detail string } // Problem=untrusted, Warn=not-generated/could-not-verify
type ProxySection struct { Diag proxy.Diagnosis; Cleaned *proxy.CleanResult } // Cleaned set when --fix ran
type ExposedSection struct { State Status; Listeners []sysdep.Listener; Detail string } // Warn if any exposed; Skipped if scan failed
type FSSection struct { State Status; Warnings []FSWarning }
type FSWarning struct { Label, Path, FSType, Reason string }

// Render produces the full deterministic report (golden-pinned). Order: Version,
// CA, Proxy, Exposed, FS, then Missing block when non-empty. Reuses prereq.Report
// and the glyphs (✓/⚠/✗).
func Render(r Report) string { /* compose sections */ }

// Actionable returns the labels of remaining actionable problems: untrusted CA,
// an orphan proxy that was NOT cleaned, and missing prerequisites. Empty ⇒ exit 0.
func (r Report) Actionable() []string { /* aggregate */ }
```
Rendering rules (each line warned/ok with a glyph; iCloud detection is path-based):
- **CA**: trusted → `CA trust: ✓ trusted`; untrusted → `✗` + `setup.Result.Message()` (actionable); not generated → `⚠ CA not generated — run \`agent-creance setup\``; could-not-verify (env error) → `⚠ CA trust: could not verify (mitmdump unavailable)`.
- **Proxy (current project)**: no lock → `Proxy: ✓ no proxy state`; healthy → `✓ proxy running (pid N, port P), M agent(s) attached`; orphan & not fixed → `✗ orphan proxy (pid N, port P) — no live agents; run \`doctor --fix\`` (actionable); orphan & fixed → `✓ cleaned orphan proxy (pid N)`; stranded → `⚠ N attached agent(s) but proxy not reachable on recorded port P (port may have changed; relaunch them)`.
- **Exposed**: none → `Exposed host services: ✓ none on 0.0.0.0`; some → one `⚠ <cmd> (pid N) listening on <addr>` per exposed listener; scan failed → `⚠ Exposed host services: could not scan (lsof unavailable)`.
- **FS**: ok → `Filesystem reliability: ✓ ok`; per warned path → `⚠ <label> is on <fstype> (<reason>); file locks may be unreliable`.

#### 3. Doctor checker (orchestration with graceful degradation)
**File**: `internal/doctor/doctor.go` (new)
```go
// Checker bundles the seams doctor needs (built from *App in cli/doctor.go).
type Checker struct {
    Commander sysdep.Commander; Tested map[string]string
    Installer *setup.Installer
    Manager   *proxy.Manager; Resolver *state.Resolver
    Listeners sysdep.ListenerScanner; FSType sysdep.FilesystemTyper
    Paths     sysdep.PathResolver
}

// Run executes every diagnostic and returns the Report. fix=true cleans a found
// orphan. No check aborts the others: an environment error becomes a Skipped/Warn
// finding (status-as-data), so doctor never crashes on a missing tool.
func (c *Checker) Run(ctx context.Context, fix bool) (Report, error) {
    var r Report
    r.Version = prereq.Check(ctx, c.Commander, prereq.DefaultTools(c.Tested))
    r.Missing = prereq.MissingInstructions(r.Version)
    r.CA = c.checkCA(ctx)          // CAGenerated → (Verify | not-generated); env error → Warn
    r.Proxy = c.checkProxy(fix)    // Resolve(".") → Inspect; if fix && Orphan → CleanOrphan
    r.Exposed = c.checkExposed(ctx)// Listeners → filter IsExposed; scan err → Skipped
    r.FS = c.checkFS()             // FSType(cwd) + FSType(cacheDir) → classify
    return r, nil
}
```
- `checkProxy`: `layout, err := c.Resolver.Resolve(".")`; on err → no-lock section. `Inspect`; if `fix && diag.Orphan` → `CleanOrphan` and record `Cleaned`.
- `checkFS`: classify each of cwd (`Paths.Abs(".")`) and cache dir (`Resolver.CacheDir()`); warn when `!FSInfo.Local` **or** the `EvalSymlinks`-resolved path contains `/Library/Mobile Documents/` (iCloud). If the exact path doesn't exist (`fs.ErrNotExist`, e.g. cache dir not yet created), ascend to the nearest existing ancestor before statfs (pure helper `nearestExisting(path, FSType)`); if none, skip that path. Pure classifier `classifyFS(info FSInfo, resolvedPath, home string) (warn bool, reason string)` is table-tested.

### Success Criteria:

#### Automated Verification:
- [x] `make test` passes — `internal/doctor` golden tests (`Render` over representative `Report`s: healthy, problems, fixed, stranded) with a `-update` flag and `testdata/*.golden`; table tests for `classifyFS` and `Report.Actionable()`; `Checker.Run` tests with all fakes covering each branch (CA trusted/untrusted/not-generated/env-error; proxy healthy/orphan/orphan+fix/stranded/no-lock; exposed some/none/scan-failed; fs ok/network/icloud)
- [x] `internal/setup` `CAGenerated` test passes
- [x] `go build ./...`; `make lint` clean
- [x] `make golden` produces no unexpected drift (review diff)

#### Manual Verification:
- [x] Rendered report reads clearly and the glyphs/wording are accurate for each condition (golden fixtures reviewed).

---

## Phase 4: Wire into the `doctor` command + testscripts

### Overview
Make `cli/doctor.go` build the `Checker` from `App`, add `--fix`, render, and set the exit code. Keep existing testscripts green; add hermetic fixtures where feasible.

### Changes Required:

#### 1. The command
**File**: `internal/cli/doctor.go`
```go
func newDoctorCmd(app *App) *cobra.Command {
    var fix bool
    cmd := &cobra.Command{
        Use: "doctor", Short: "Diagnose prerequisites, CA trust, proxies and environment", Args: cobra.NoArgs,
        RunE: func(cmd *cobra.Command, _ []string) error { return runDoctor(cmd.Context(), app, fix) },
    }
    cmd.Flags().BoolVar(&fix, "fix", false, "remediate what can be safely fixed (e.g. clean orphan proxies)")
    return cmd
}

func runDoctor(ctx context.Context, app *App, fix bool) error {
    chk := &doctor.Checker{
        Commander: app.Commander, Tested: app.Tested,
        Installer: setup.NewInstaller(app.FS, app.Keychain, app.ProcessManager, app.PortAllocator, app.TLSProber, app.Sleeper, app.Paths),
        Manager:   proxy.NewManager(app.FS, app.Flock, app.ProcessManager, app.PortAllocator, app.Stderr),
        Resolver:  state.New(app.Paths),
        Listeners: app.Listeners, FSType: app.FSType, Paths: app.Paths,
    }
    rep, err := chk.Run(ctx, fix)
    if err != nil { return err } // only a truly fatal orchestration error
    fmt.Fprint(app.Stdout, doctor.Render(rep))
    if probs := rep.Actionable(); len(probs) > 0 {
        return fmt.Errorf("%d actionable problem(s) remain: %s", len(probs), strings.Join(probs, ", "))
    }
    return nil
}
```
Exit semantics: `Render` already printed the human report (incl. the missing-prereq block); the returned error is the one-line summary `Main` prints to stderr → exit 1. Matches today's "print report then error" shape (`doctor.go:30-33`).

#### 2. Testscripts
**File**: `internal/cli/testdata/script/doctor_healthy.txtar`, `doctor_missing.txtar` — verify still green. The new sections degrade gracefully (no CA file in the test HOME → "CA not generated" warning; `lsof` absent → "could not scan" warning; no lock → "no proxy state"; temp-dir statfs → local apfs → ok), so neither forbidden substring (`not installed`) nor exit code changes. Adjust assertions only if output genuinely conflicts.
**File**: `internal/cli/testdata/script/doctor_fix_noop.txtar` (new) — `doctor --fix` in a project with no proxy state exits 0 and prints `no proxy state` (hermetic; no orphan to clean).

#### 3. Go-level command test
**File**: `internal/cli/doctor_test.go` (new) — drive `runDoctor` via `*App` with all fakes (mirror `setup_test.go`): assert exit/error for untrusted CA and un-fixed orphan (actionable) vs warnings (exit 0), and that `--fix` flips an orphan to cleaned + exit 0.

### Success Criteria:

#### Automated Verification:
- [ ] `make test` passes including the existing `doctor_healthy`/`doctor_missing` testscripts and the new `doctor_fix_noop.txtar` and `doctor_test.go`
- [ ] `go build ./...`; `make lint` clean
- [ ] `make golden` no unexpected drift

#### Manual Verification:
- [ ] `make run ARGS="doctor"` on a configured Mac prints all sections; `make run ARGS="doctor --fix"` reports no-op when nothing to clean.

---

## Phase 5: Integration test — `--fix` cleans a real orphan + real seam impls

### Overview
Prove the end-to-end `--fix` orphan cleanup and the two new real seam impls against real tools (ticket Verification step 4), behind the `integration` build tag.

### Changes Required:

#### 1. Orphan `--fix` integration test
**File**: `internal/cli/doctor_fix_integration_test.go` (new, `//go:build integration`)
- Arrange a real orphan in a temp project: allocate a real port, spawn a real `mitmdump` (or a stand-in listener) on `127.0.0.1`, write a real `proxy.lock` (via `proxy.Manager.Attach` or seeded JSON) recording that proxy PID/port with a **dead** agent PID so live agents == 0.
- Run `runDoctor(ctx, app, true)` with real OS seams.
- Assert: the orphan proxy PID is no longer `Alive`, the lock's proxy state is cleared, the session overlay is gone, and the command exited 0 (orphan was cleaned, nothing else actionable).

#### 2. Real seam integration tests
**File**: `internal/sysdep/listener_integration_test.go` (new, `//go:build integration`) — start a real loopback listener and assert `OSListenerScanner.Listeners` includes it; optionally bind `0.0.0.0` and assert `IsExposed`. **File**: `internal/sysdep/fstype_integration_test.go` (new, `//go:build integration`) — `OSFilesystemTyper.FSType(t.TempDir())` returns a non-empty `Name` and `Local == true`.

### Success Criteria:

#### Automated Verification:
- [ ] `make test-integration` passes (orphan cleaned end-to-end; real listener enumerated; real fs-type read)
- [ ] `make test` (unit) and `make lint` still clean

#### Manual Verification:
- [ ] On a Mac with a service bound to `0.0.0.0`, `doctor` lists it under Exposed host services.
- [ ] With the agent-creance cache dir relocated onto an SMB/iCloud path, `doctor` emits the filesystem-reliability warning.

---

## Testing Strategy

### Unit Tests:
- Pure helpers (table): `sysdep.ParseLsof`, `sysdep.IsExposed`, `doctor.classifyFS`, `doctor.Report.Actionable`.
- `proxy.Inspect`/`CleanOrphan` with `FakeFlock` (seeded lock JSON) + `FakeProcessManager.AlivePIDs` + `FakePortAllocator.Listening`.
- `setup.CAGenerated` with `FakeFileSystem`/`FakePathResolver`.
- `doctor.Checker.Run` with all fakes, every branch.

### Golden Tests:
- `internal/doctor/testdata/*.golden` for `Render` over representative `Report` values (all-healthy, all-problems, mixed), with `-update`.
- Existing `internal/prereq/testdata/doctor_report.golden` stays byte-identical (regression).

### Integration Tests (`make test-integration`):
- `--fix` cleans a real orphan proxy + lock + overlay (ticket step 4).
- `OSListenerScanner` / `OSFilesystemTyper` against the real OS.

### Manual Testing Steps:
1. `make run ARGS="doctor"` on a set-up Mac → all five sections render; exit 0 when healthy.
2. Bind a service to `0.0.0.0`, re-run → it appears under Exposed host services (warning, exit 0).
3. Leave an orphan proxy (start a run, kill the agent leaving mitmproxy) → `doctor` flags it and exits non-zero; `doctor --fix` cleans it and exits 0.
4. Break CA trust (cancel the keychain dialog) → `doctor` reports untrusted CA and exits non-zero.

## Performance Considerations
`doctor` spawns a short-lived `mitmdump` for the live CA verify and shells `lsof` once — both bounded, run-once-per-invocation, and already the cost `setup` pays. No hot path.

## Migration Notes
Purely additive: new `App` fields/seams, a new flag, a new package. No on-disk format or command-contract change. The `proxy.lock` schema is read as-is and cleared (never deleted) on `--fix`, matching last-out `Detach`.

## References
- Ticket: `thoughts/shared/tickets/AC-0031-doctor-extension.md`
- Research: `thoughts/shared/research/2026-06-07-AC-0031-doctor-extension.md`
- CA reuse: `internal/setup/setup.go:203-226` (Verify), `internal/cli/setup.go:38-42` (construction)
- Lifecycle reuse: `internal/proxy/lifecycle.go:36-45,111-182` (schema, liveness, Detach)
- Render extension point: `internal/prereq/report.go:20-48`; golden mechanism `internal/prereq/report_test.go:21`
- App wiring: `internal/cli/cli.go:20-52,88-105`
- Detection techniques (lsof `*:port`, `unix.Statfs` `Fstypename`/`MNT_LOCAL`, iCloud is path-based): research "sysdep seams" + web-research sections
</content>
