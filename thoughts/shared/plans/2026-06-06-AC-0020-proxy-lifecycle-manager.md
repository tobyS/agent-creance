---
date: 2026-06-06
ticket: AC-0020
title: "Plan: Proxy lifecycle manager (WP-3.4)"
status: ready
branch: main
research: thoughts/shared/research/2026-06-06-AC-0020-proxy-lifecycle-manager.md
---

# Plan: Proxy lifecycle manager (AC-0020, WP-3.4)

## Overview

Build `internal/proxy`'s lifecycle manager: the `flock`-guarded `proxy.lock`
refcount that lets multiple `agent-creance` invocations in one project share a
single mitmproxy. It does start-or-attach, prunes dead agent PIDs, allocates an
ephemeral port (best-effort reclaim of the recorded one on a crash restart),
decrements on exit and kills the proxy only on last-out, purges the
session-overlay on last-out, and — when a restart cannot reclaim the port while
agents are still attached — emits a loud warning naming those PIDs and **never**
signals them.

This is the riskiest non-security concurrency logic in the system, so it is built
on hermetically-testable seams. Three seam changes precede the manager:

1. **`Flock` redesign** — the lock and the read-modify-write must share one file
   descriptor (an advisory `flock` lives on a specific fd; the repo's temp+rename
   idiom would swap the inode out from under the lock). `Acquire` returns a
   `LockedFile` handle for in-place read/write. This ticket also implements the
   real `OSFlock` (`golang.org/x/sys/unix.Flock(fd, LOCK_EX)`) its doc comment
   defers here.
2. **`ProcessManager` seam** (new) — spawn the detached proxy daemon and learn its
   PID, probe an arbitrary PID's liveness (`kill -0`), and kill a recorded PID
   (the last agent out holds no live `Process` handle, only the recorded PID).
3. **`PortAllocator` seam** (new) — `:0` ephemeral allocation, best-effort reclaim
   of a recorded port, and a TCP probe used (with PID-liveness) to decide whether
   the proxy is genuinely alive.

Per the checkpoint, the manager + all three seams + their real `OS*` impls + full
fake-driven unit tests land here; **wiring into `cli.App`/`Main()` is deferred to
AC-0025** (the `run` command — the first caller), following the AC-0009 "no unused
deps in `cli.App`" convention.

## Current State

- `internal/proxy/extract.go` holds the AC-0019 enforcer extractor (the local style
  template: constructor takes sysdep seams + builds `state.New(paths)`; atomic
  writes; self-healing reads). No lifecycle/refcount Go code exists.
- `internal/sysdep/flock.go` — `Flock.Acquire(path) (release func() error, err error)`;
  `OSFlock.Acquire` is stubbed (`ErrNotImplemented`); its doc defers the real
  `unix.Flock(LOCK_EX)` impl to **this ticket**. Fake: `sysdeptest/FakeFlock`
  (records `Acquired`/`Released`, `Held`, `AcquireErr`/`ReleaseErr`). No production
  caller exists — blast radius of the redesign is the seam + fake + their tests.
- `internal/sysdep/processgroup.go` — `ProcessGroup`/`Process` (group-targeted
  `Signal` via `kill(-pgid, …)`, `Start` stubbed → WP-4.3). **Not changed here;**
  it is the *agent group's* seam, distinct from the proxy daemon and PID-targeted
  ops. No PID-liveness, PID-kill, daemon-spawn, or port primitive exists anywhere.
- `internal/state/state.go` — `Layout.ProxyLock()` → `<root>/proxy.lock`,
  `SessionOverlay()` → `<root>/session-overlay.yaml`, `PolicyJSON()`,
  `EgressJSONL()`; identity = SHA-256(realpath)[:8] hex. No I/O (this package only
  computes paths).
- `internal/sysdep/filesystem.go` — `FileSystem` (WriteFile/MkdirAll/Remove/Rename/
  Stat) + `RemoveIfPresent` helper (for the overlay purge).
- Atomic-JSON read/write idiom to mirror for the lock struct's marshalling:
  `internal/policy/compile/compile.go:402-412,515-537`; `internal/generator/cache.go`.
- The module has **no `golang.org/x/sys` dependency yet**; Phase 1 adds it.
- `internal/cli/cli.go` `App` has no `Flock`/process/port fields and there is **no
  `run` command** (`internal/cli/run.go` — AC-0025). Nothing calls the manager yet.

## Desired End State

- `internal/proxy` exposes a `Manager` that, given a project `state.Layout`, the
  current policy hash, the extracted enforcer path, and the caller's PID:
  - `Attach` — under `flock`, prunes dead agents, verifies the proxy alive
    (PID-liveness **and** a TCP port probe), starts mitmproxy if none/crashed (with
    best-effort port reclaim on restart), records its own PID, writes the lock, and
    reports the live port + whether a warn-never-kill condition occurred.
  - `Detach` — under `flock`, removes its own PID, and on last-out kills the proxy
    PID and purges the session-overlay (a non-final exit does neither).
- The lock file (`proxy.lock`) records proxy PID, port, policy hash, and the
  attached-agent PID array as JSON, written in-place on the locked fd; it lives
  out-of-tree under the project state dir (cross-cutting C4).
- New seams `Flock` (redesigned), `ProcessManager`, `PortAllocator` each have a
  real `OS*` impl, a `sysdeptest` fake, a compile-time `var _ Iface` assertion, and
  a smoke test, matching the AC-0009 seam conventions.
- All five required unit scenarios pass with fakes; a concurrent attach/detach
  simulation is clean under `-race`; an `//go:build integration` test (gated S3)
  drives a real mitmproxy start/attach/teardown across two invocations.
- Ticket AC boxes ticked, Status → Done. `make test`, `go build ./...`, `make lint`
  green.

## Key Decisions (from research + checkpoint)

1. **Write-in-place on the locked fd** (not temp+rename). `Flock.Acquire` returns a
   `LockedFile{ ReadAll, Write, Release }` so the read-modify-write happens on the
   same descriptor the lock is held on. Redesigns the AC-0009 `Flock` seam (no
   production callers to break).
2. **Proxy-alive = PID-liveness AND TCP port probe.** A recycled PID alone is
   unsafe; the port probe disambiguates. Reuses the `PortAllocator` seam.
3. **One new PID-oriented seam `ProcessManager`** (`Spawn` detached daemon →
   returns PID; `Alive(pid)`; `Signal(pid, sig)`), distinct from `ProcessGroup`
   (the agent group, WP-4.3). The proxy is a standalone host daemon spawned with
   `Setsid`, killed by PID from a later invocation.
4. **Manager + seams + `OS*` impls + tests now; defer `cli.App`/`Main()` wiring to
   AC-0025.** The manager takes a `StartConfig` (Layout, enforcer path, policy
   hash, self PID, proxy binary) so AC-0025 orchestrates extract→compile→attach.
5. **The manager does not own extraction or policy compilation** — those are inputs
   (AC-0019/AC-0013). It owns the lock/refcount/port/proxy-process lifecycle only.
6. **`flock`-reliability detection (iCloud/SMB) is AC-0031 doctor's job** — out of
   scope; AC-0020 assumes a reliable filesystem.

---

## Phase 1 — Redesign the `Flock` seam + real `OSFlock`

### Interface change — `internal/sysdep/flock.go`

Replace the `release`-closure shape with a handle that owns the locked fd:

```go
// Flock abstracts an exclusive advisory file lock used for the atomic
// read-modify-write of proxy.lock (proxy PID, port, policy hash, attached-agent
// PIDs). Because an advisory flock lives on a specific descriptor, the SAME
// descriptor must carry the read and the write — so Acquire returns a LockedFile
// for in-place I/O rather than just an unlock closure. (Temp-file + rename would
// swap the inode out from under the lock and silently break exclusion.)
type Flock interface {
    // Acquire opens path (creating it if absent), takes an exclusive advisory lock,
    // blocking until held, and returns a LockedFile for reading/replacing the
    // contents on the locked descriptor. A non-nil error means the lock was not
    // taken; LockedFile is then nil. The parent directory must already exist.
    Acquire(path string) (LockedFile, error)
}

// LockedFile is a held lock plus in-place content access on the locked descriptor.
type LockedFile interface {
    // ReadAll returns the full current contents (empty for a freshly created file).
    ReadAll() ([]byte, error)
    // Write replaces the contents (truncate to zero, then write from offset 0) on
    // the same locked descriptor.
    Write(data []byte) error
    // Release unlocks and closes the descriptor. Always call it (defer).
    Release() error
}
```

Real impl (replaces the stub; adds `golang.org/x/sys/unix` + `io`/`os`):

```go
type OSFlock struct{}

var _ Flock = (*OSFlock)(nil)

func (OSFlock) Acquire(path string) (LockedFile, error) {
    f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, lockPerm) // 0o600
    if err != nil {
        return nil, fmt.Errorf("sysdep: open lock %q: %w", path, err)
    }
    if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
        _ = f.Close()
        return nil, fmt.Errorf("sysdep: flock %q: %w", path, err)
    }
    return &osLockedFile{f: f}, nil
}

type osLockedFile struct{ f *os.File }

func (l *osLockedFile) ReadAll() ([]byte, error) {
    if _, err := l.f.Seek(0, io.SeekStart); err != nil {
        return nil, err
    }
    return io.ReadAll(l.f)
}

func (l *osLockedFile) Write(data []byte) error {
    if err := l.f.Truncate(0); err != nil {
        return err
    }
    if _, err := l.f.Seek(0, io.SeekStart); err != nil {
        return err
    }
    _, err := l.f.Write(data)
    return err
}

func (l *osLockedFile) Release() error {
    _ = unix.Flock(int(l.f.Fd()), unix.LOCK_UN) // close also releases; explicit for clarity
    return l.f.Close()
}
```

Add `lockPerm = 0o600` (PIDs are not secret, but the lock is out-of-tree host-only,
matching the audit log's `0600`).

### Fake rework — `internal/sysdep/sysdeptest/flock.go`

`FakeFlock` must (a) back the lock content so manager tests can pre-seed and assert
it, and (b) provide **real mutual exclusion** so the concurrent `-race` test in
Phase 4 is meaningful:

```go
type FakeFlock struct {
    // Contents backs each path's lock-file bytes (pre-seed and inspect here).
    Contents map[string][]byte
    // AcquireErr, if set, makes Acquire fail (no LockedFile returned).
    AcquireErr error
    // Acquired/Released record path acquisition/release order.
    Acquired, Released []string

    guard sync.Mutex             // guards the maps + slices
    locks map[string]*sync.Mutex // per-path real lock, for true exclusion
}

func NewFakeFlock() *FakeFlock // inits maps

func (f *FakeFlock) Acquire(path string) (sysdep.LockedFile, error) {
    if f.AcquireErr != nil {
        return nil, f.AcquireErr
    }
    m := f.lockFor(path) // get-or-create per-path mutex under guard
    m.Lock()             // blocks concurrent acquirers — models flock exclusion
    f.guard.Lock()
    f.Acquired = append(f.Acquired, path)
    f.guard.Unlock()
    return &fakeLockedFile{flock: f, path: path, mu: m}, nil
}
```

`fakeLockedFile.ReadAll` returns a copy of `Contents[path]`; `Write` stores a copy;
`Release` records `Released` and `mu.Unlock()`. Keep `Held(path)` (try-lock based or
a held-set) for existing callers if any test still uses it; otherwise drop it.

### Update existing tests

- `internal/sysdep/flock_test.go` — rewrite the (currently stub-asserting) test into
  a real smoke test against `t.TempDir()`: Acquire a path, `Write` bytes, `Release`,
  re-`Acquire`, `ReadAll` returns the bytes; a second goroutine's `Acquire` blocks
  until the first `Release` (exclusion). Use `var _ Flock = (*OSFlock)(nil)`.
- `internal/sysdep/sysdeptest/flock_test.go` — update to the new fake API
  (Contents round-trips through ReadAll/Write; Acquired/Released recorded; a second
  Acquire blocks until Release).

### Dependency

Run `go get golang.org/x/sys/unix` then `go mod tidy`; review `go.mod`/`go.sum` diff
(first `x/sys` dependency).

### Success criteria

#### Automated
- [ ] `go build ./...` compiles (interface + impls + assertions).
- [ ] `go test -race ./internal/sysdep/... ./internal/sysdep/sysdeptest/...` passes.
- [ ] `make lint` clean.

#### Manual
- [ ] `go.mod` now lists `golang.org/x/sys`; `go.sum` updated.

---

## Phase 2 — New seams: `ProcessManager` + `PortAllocator`

Two narrow seams, one file each, mirroring the AC-0009 pattern (interface + `OS*`
real impl + compile-time assertion + `sysdeptest` fake + smoke test).

### `internal/sysdep/processmanager.go`

```go
// ProcessManager manages standalone host processes BY PID — distinct from
// ProcessGroup, which targets a whole agent group via a live Process handle.
// The shared mitmproxy is a daemon that outlives the invocation that spawned it
// and is killed later, by PID, from a different invocation that holds no handle.
type ProcessManager interface {
    // Spawn starts name+args as a detached background process (its own session,
    // Setsid) and returns its PID without waiting. stdout/stderr are discarded
    // (the proxy writes its own audit log). Used to launch mitmproxy.
    Spawn(ctx context.Context, name string, args ...string) (pid int, err error)
    // Alive reports whether pid is a live process: kill(pid, 0) — nil ⇒ alive,
    // ESRCH ⇒ dead, EPERM ⇒ alive-but-not-ours (treated as alive).
    Alive(pid int) bool
    // Signal sends sig to a single pid (kill(pid, sig)). ESRCH (already gone) is
    // not an error. Used to SIGTERM the proxy on last-out.
    Signal(pid int, sig os.Signal) error
}
```

`OSProcessManager` (real, implementable now):
- `Spawn` — `exec.CommandContext`, `SysProcAttr{Setsid: true}`, stdout/stderr to
  `nil` (discarded) or `os.DevNull`, `cmd.Start()`, return `cmd.Process.Pid`. Do
  **not** `Wait` (daemon). Detach so it survives this process.
- `Alive` — `syscall.Kill(pid, 0)`; map `nil`/`EPERM` → true, `ESRCH` → false.
- `Signal` — `syscall.Kill(pid, sig.(syscall.Signal))`; swallow `ESRCH`.

`FakeProcessManager` (`sysdeptest/processmanager.go`):
```go
type FakeProcessManager struct {
    SpawnPID   int            // PID returned by Spawn
    SpawnErr   error
    AlivePIDs  map[int]bool   // liveness oracle; absent key ⇒ dead
    SignalErr  error
    Spawned    []StartedCommand        // recorded Spawn calls (reuse the struct)
    Signaled   []SignaledPID           // {PID int; Sig os.Signal}
}
```
`Alive(pid)` → `AlivePIDs[pid]`; `Signal` records `{pid,sig}` and returns `SignalErr`.

### `internal/sysdep/portallocator.go`

```go
// PortAllocator allocates ephemeral loopback TCP ports and probes/reclaims a
// specific one — the basis for the proxy's :0 allocation, best-effort reclaim of
// the recorded port on a crash restart, and the "is the proxy listening?" check.
type PortAllocator interface {
    // Allocate binds 127.0.0.1:0, reads the OS-assigned port, closes the listener
    // and returns the port. (mitmproxy is then told the port via --listen-port,
    // accepting the small allocate-close-rebind race.)
    Allocate() (int, error)
    // TryReclaim attempts to bind 127.0.0.1:port; ok=true if the bind succeeded
    // (port free, reclaimable), ok=false if a live holder refused it. Closes the
    // listener before returning. A non-nil err is an unexpected failure, not "held".
    TryReclaim(port int) (ok bool, err error)
    // Probe reports whether something is accepting connections on 127.0.0.1:port
    // (a short-timeout TCP dial succeeds). Used with ProcessManager.Alive to decide
    // the proxy is genuinely up.
    Probe(port int) bool
}
```

`OSPortAllocator` (real): `Allocate`/`TryReclaim` via `net.Listen("tcp",
"127.0.0.1:N")` (distinguish "address in use" → `ok=false` from other errors);
`Probe` via `net.DialTimeout("tcp", addr, shortTimeout)` (e.g. 200ms).

`FakePortAllocator` (`sysdeptest/portallocator.go`): scripted —
`AllocPort int`/`AllocErr`; `ReclaimOK map[int]bool`/`ReclaimErr`;
`Listening map[int]bool` for `Probe`. Records `Allocations`/`Reclaims` for assertions.

### Smoke tests

- `internal/sysdep/processmanager_test.go` — `Alive(os.Getpid())` true;
  `Alive(<very-large-unused-pid>)` false; `Spawn` a trivial `sleep`/`true` and
  assert a non-zero PID + `Alive` of it; `Signal(pid, SIGTERM)` then it dies
  (poll `Alive`). Skippable bits guarded if a binary isn't present; keep hermetic
  (use `/bin/sleep`).
- `internal/sysdep/portallocator_test.go` — `Allocate` returns a port in range;
  `Probe` of it is false (nothing listening after close); open a real
  `net.Listener` on a port, `Probe` true and `TryReclaim` ok=false; on a free port
  `TryReclaim` ok=true.

### Success criteria

#### Automated
- [ ] `go build ./...` compiles (both seams + assertions + fakes).
- [ ] `go test -race ./internal/sysdep/...` passes (smoke tests green).
- [ ] Grep guard: `ProcessManager` and `PortAllocator` each have a fake in
      `sysdeptest/`.
- [ ] `make lint` clean.

---

## Phase 3 — The lifecycle `Manager`

### New file `internal/proxy/lifecycle.go`

The lock record + manager. Marshalling mirrors the repo's JSON idiom, but written
through `LockedFile.Write` (not temp+rename).

```go
// lockState is the on-disk proxy.lock contents (out-of-tree, C4). Written in-place
// under the held flock.
type lockState struct {
    ProxyPID   int    `json:"proxy_pid"`
    Port       int    `json:"port"`
    PolicyHash string `json:"policy_hash"`
    Agents     []int  `json:"agents"`
}

// Manager owns the flock-guarded proxy refcount/lifecycle for a project.
type Manager struct {
    fs    sysdep.FileSystem
    lock  sysdep.Flock
    proc  sysdep.ProcessManager
    ports sysdep.PortAllocator
    warn  io.Writer // warn-never-kill messages (wired to app.Stderr by AC-0025)
}

func NewManager(fs sysdep.FileSystem, lock sysdep.Flock, proc sysdep.ProcessManager,
    ports sysdep.PortAllocator, warn io.Writer) *Manager

// StartConfig is everything Attach needs to identify/launch the project's proxy.
type StartConfig struct {
    Layout     state.Layout // ProxyLock(), SessionOverlay(), PolicyJSON(), EgressJSONL(), Root
    EnforcerPy string       // from proxy.Extractor.Extract()
    PolicyHash string       // current compiled-policy hash
    SelfPID    int          // os.Getpid() of this invocation
    ProxyBin   string       // resolved mitmdump path (or "mitmdump")
}

// Attachment is what Attach reports back to the caller.
type Attachment struct {
    Port        int  // the live proxy port the caller must point the cage at
    ProxyPID    int
    PortChanged bool // true when a crash restart could not reclaim the old port
}
```

**`Attach(ctx, cfg) (Attachment, error)`** algorithm (design.md:397-410, ticket
AC #1-3,5):

1. `m.fs.MkdirAll(cfg.Layout.Root, dirPerm)` (parent must exist for Acquire).
2. `lf, err := m.lock.Acquire(cfg.Layout.ProxyLock())`; `defer lf.Release()`.
3. `cur := readLock(lf)` — `ReadAll` → empty ⇒ zero `lockState`; corrupt ⇒ treated
   as zero (self-heals, like the compiler's corrupt-as-absent read).
4. **Prune dead agents:** `alive := filter(cur.Agents, m.proc.Alive)`.
5. **Proxy liveness:** `proxyUp := cur.ProxyPID != 0 && m.proc.Alive(cur.ProxyPID)
   && m.ports.Probe(cur.Port)`.
6. **Branch:**
   - **Attach (proxyUp):** `port = cur.Port`, `proxyPID = cur.ProxyPID`. No spawn.
   - **Start fresh (dead/crashed):**
     - **Port:** if `cur.Port != 0` (crash restart) → `ok,_ := m.ports.TryReclaim(cur.Port)`;
       ok ⇒ `port = cur.Port`; else ⇒ `port = m.ports.Allocate()`, `portChanged = true`.
       If `cur.Port == 0` (cold) → `port = m.ports.Allocate()`.
     - **Warn-never-kill:** if `portChanged && len(alive) > 0` → write the documented
       warning to `m.warn` naming `alive` PIDs ("proxy restarted on port N (was M);
       attached agents <pids> will see egress failures and should be relaunched");
       **do not** signal them.
     - **Spawn:** `proxyPID = m.proc.Spawn(ctx, cfg.ProxyBin, mitmArgs(port, cfg))`.
7. **Add self:** append `cfg.SelfPID` to `alive` if absent.
8. **Persist:** `writeLock(lf, lockState{proxyPID, port, cfg.PolicyHash, agents})`
   (marshal indented + `'\n'`, `lf.Write`).
9. Return `Attachment{port, proxyPID, portChanged}` (release via defer).

`mitmArgs(port, cfg)` builds the mitmproxy command:
`["--listen-host", "127.0.0.1", "--listen-port", itoa(port), "-s", cfg.EnforcerPy,
"--set", "creance_policy="+cfg.Layout.PolicyJSON(), "--set",
"creance_audit="+cfg.Layout.EgressJSONL(), "-q"]` — exact `--set` keys cross-checked
against the enforcer addon (AC-0017/0018); refine against `enforcer.py`'s option
names. (Keep the arg-builder a small private func so the integration test and a unit
test can assert it.)

**`Detach(layout state.Layout, selfPID int) error`** (design.md:404, ticket AC #4):

1. `lf, err := m.lock.Acquire(layout.ProxyLock())`; `defer lf.Release()`.
2. `cur := readLock(lf)`.
3. `agents := remove(cur.Agents, selfPID)`.
4. **Last-out:** if `len(agents) == 0`:
   - if `cur.ProxyPID != 0 && m.proc.Alive(cur.ProxyPID)` →
     `m.proc.Signal(cur.ProxyPID, syscall.SIGTERM)`.
   - `sysdep.RemoveIfPresent(m.fs, layout.SessionOverlay())` (purge overlay).
   - `writeLock(lf, lockState{PolicyHash: cur.PolicyHash})` (cleared proxy + empty
     agents; keep the file so the flock target persists). Next `Attach` cold-starts.
   - **Not last-out:** `writeLock(lf, lockState{cur.ProxyPID, cur.Port,
     cur.PolicyHash, agents})` — proxy untouched.

Constants: reuse `dirPerm = 0o755`, `filePerm` n/a (write via LockedFile),
indent `"  "`. Helpers `readLock`/`writeLock`/`removeInt`/`containsInt` are pure and
unit-testable white-box.

### Success criteria

#### Automated
- [ ] `go build ./...` compiles.
- [ ] `go test -race ./internal/proxy/...` passes (Phase 4 tests).
- [ ] `make lint` clean.

---

## Phase 4 — Manager tests, race simulation, integration scaffold

### Unit tests — `internal/proxy/lifecycle_test.go` (blackbox `package proxy_test`)

Drive `Manager` with `sysdeptest.FakeFlock` + `FakeProcessManager` +
`FakePortAllocator` + `FakeFileSystem`, plus a `bytes.Buffer` for `warn`. Use a
deterministic `state.Layout` (build via `state.New(FakePathResolver{XDG:/cache})
.Resolve` or construct the `Layout` directly). The five ticket-mandated cases:

- `TestAttachStartsProxyWhenNone` — empty lock; `FakePortAllocator.AllocPort=8080`;
  `Spawn` returns PID 111. Asserts: one `Spawned` command with `--listen-port 8080`
  and `-s <EnforcerPy>`; lock now `{proxy_pid:111, port:8080, agents:[self]}`;
  returned `Attachment.Port==8080`.
- `TestSecondAttachDoesNotStartSecondProxy` — pre-seed lock `{proxy_pid:111,
  port:8080, agents:[111-agent]}`; mark proxy PID alive + `Listening[8080]=true`.
  Second `Attach(selfPID=222)`: **no** new `Spawned` entry; lock agents length 2
  (`[..,222]`); port unchanged.
- `TestLastOutTearsDownAndPurgesOverlay` — lock `{proxy_pid:111, port:8080,
  agents:[222]}`; seed `Contents` for `SessionOverlay()` in the FS. `Detach(222)`:
  `FakeProcessManager.Signaled` contains `{111, SIGTERM}`; overlay removed from FS;
  lock agents empty + proxy cleared.
- `TestNonFinalExitLeavesProxyAndOverlay` — lock `agents:[222,333]`. `Detach(222)`:
  **no** signal sent; overlay still present; lock agents `[333]`, proxy untouched.
- `TestDeadAgentPidPruned` — lock `agents:[999(dead),222(alive)]`, proxy alive.
  `Attach(333)`: resulting agents `[222,333]` (999 dropped); proxy not restarted.
- `TestCrashRestartReclaimsPort` — lock `{proxy_pid:111(dead), port:8080,
  agents:[222(alive)]}`; `Probe(8080)=false` (proxy dead) so proxyUp=false;
  `ReclaimOK[8080]=true`. `Attach(333)`: `Spawn` called with `--listen-port 8080`
  (port preserved); **no** signal to 222 (`Signaled` empty); `Attachment.PortChanged
  ==false`; warn buffer empty.
- `TestCrashRestartReclaimFailWarnsNeverKills` — same but `ReclaimOK[8080]=false`,
  `AllocPort=9090`. `Attach(333)`: `Spawn` with `--listen-port 9090`;
  `Attachment.PortChanged==true`; warn buffer **names PID 222** and the old/new
  ports; `FakeProcessManager.Signaled` is **empty** (222 never signaled).
- `TestAttachCorruptLockSelfHeals` — `Contents[ProxyLock]=[]byte("{bad")`; `Attach`
  treats it as empty and cold-starts without error.
- `TestAttachMkdirError` / `TestAttachAcquireError` / `TestPersistWriteError` —
  inject `FakeFileSystem.MkdirErrs`, `FakeFlock.AcquireErr`, and a `LockedFile`
  write error; `Attach` surfaces the wrapped error; on Acquire failure nothing is
  spawned.
- `TestLockPathIsOutOfTree` (C4) — assert the path passed to `lock.Acquire`
  (recorded in `FakeFlock.Acquired`) equals `layout.ProxyLock()` and is under
  `/cache/agent-creance/projects/…`, never under the project tree.

White-box `lifecycle_internal_test.go` (`package proxy`) for the pure helpers:
`readLock`/`writeLock` round-trip; `removeInt`/`containsInt`; `mitmArgs` shape.

### Race test — `internal/proxy/lifecycle_race_test.go`

Spin up N goroutines each doing `Attach(selfPID=i)` then `Detach(i)` against one
`Manager` sharing one `FakeFlock` (whose per-path real mutex models flock
exclusion). Assert: run clean under `-race`; the proxy is spawned at most once while
at least one agent overlaps; after all detach, agents array is empty and the proxy
was SIGTERM'd exactly once. (Liveness oracle marks the spawned PID alive; `Listening`
true for its port.)

### Integration scaffold — `internal/proxy/lifecycle_integration_test.go`

`//go:build integration` (ticket §4, gated S3). Uses the **real** `OSFlock`,
`OSProcessManager`, `OSPortAllocator`, `OSFileSystem`, and a real `mitmdump` +
the extracted enforcer (via `proxy.NewExtractor(...).Extract()`), against a
`t.TempDir()` cache root:
- Attach #1 starts mitmproxy (PID alive, port listening); Attach #2 (different
  selfPID) attaches without a second process; Detach #1 leaves it up; Detach #2
  (last out) SIGTERMs it and purges a seeded overlay. Skips with a clear message if
  `mitmdump` is absent. Keep it tagged so `make test` never runs it.

### Success criteria

#### Automated
- [ ] `go test -race ./internal/proxy/...` — all five mandated cases + error/C4
      cases green.
- [ ] Race simulation clean under `-race`.
- [ ] `make test` green (full suite; no regressions in sysdep/state/cli/etc.).
- [ ] `go build -tags=integration ./...` compiles the integration test.
- [ ] `make lint` clean.

#### Manual
- [ ] (If a real `mitmdump` is available) `make test-integration` exercises the
      two-invocation start/attach/teardown path and passes.

---

## Phase 5 — Reconcile docs & close the ticket

### Changes

- `docs/design.md` — verify "Multi-agent lifecycle" / "Crash recovery and the port"
  match the implementation; in particular note that proxy-alive is **PID-liveness +
  port probe** (the design says "verifies the proxy is alive" without specifying the
  probe — add the port-probe detail so the doc matches reality). No contradictions
  expected; adjust wording only if found.
- `thoughts/shared/tickets/AC-0020-proxy-lifecycle-manager.md` — tick the six
  acceptance criteria, set Status → Done, and answer the two "Questions for
  Research/Planning": (1) `flock` reliability detection is delegated to AC-0031
  doctor (iCloud/SMB); (2) proxy-alive verification is PID-liveness **plus** a TCP
  port probe (recycled-PID safety). Add a Notes entry recording the `Flock`-seam
  redesign (write-in-place on the locked fd) and the new `ProcessManager`/
  `PortAllocator` seams, and that `cli.App`/`Main()` wiring is deferred to AC-0025.

### Success criteria

#### Automated
- [ ] `make test` green.
- [ ] `go build ./...` green.
- [ ] `make lint` clean.

#### Manual
- [ ] Ticket marked Done; ACs ticked; design doc consistent.

---

## Testing Strategy

- **Unit (hermetic):** the whole manager via the three new fakes + `FakeFileSystem`
  — no real OS, no mitmproxy. Lock content round-trips through `FakeFlock.Contents`;
  liveness/port via scripted oracles; warnings captured in a `bytes.Buffer`.
- **Concurrency:** the `FakeFlock`'s per-path real mutex models advisory exclusion,
  so the N-goroutine attach/detach simulation is a genuine `-race` exercise of the
  read-modify-write ordering (ticket §3).
- **Seam smoke tests:** real `OSFlock`/`OSProcessManager`/`OSPortAllocator` verified
  once against `t.TempDir()`/`os.Getpid()`/real listeners (white-box `package
  sysdep`), so logic packages can trust the fakes.
- **Integration (`//go:build integration`, gated S3):** real mitmproxy
  start/attach/teardown across two invocations + C4 (lock under the state dir).
  Never runs under `make test`.
- **No golden files** — the lock is a tiny JSON struct; direct struct/round-trip
  assertions are stronger than a golden render. (A golden lock render is optional
  if a stable wire format is later worth pinning.)

## Automated verification (per profile.md)

- `make test` (= `go test -race ./...`)
- `go build ./...` and `go build -tags=integration ./...`
- `make lint` (= `go vet ./...` + `golangci-lint run`)
- (gated) `make test-integration` (= `go test -race -tags=integration ./...`)

## Manual verification

- Build the binary; in a scratch project, drive `Attach` twice (two PIDs) and
  confirm one `mitmdump` runs, `~/.cache/agent-creance/projects/<hash>/proxy.lock`
  records both PIDs + the port, and last-out `Detach` kills the proxy and removes a
  seeded `session-overlay.yaml`. (Covered by the integration test; full end-to-end
  through `run` lands with AC-0025.)

## Out of Scope

- Exec-ing Safehouse / the agent and forwarding signals to the agent group
  (AC-0023/AC-0024; `ProcessGroup` real impl stays WP-4.3).
- The `run` command and wiring the manager into `cli.App`/`Main()` (AC-0025).
- Building `policy.json`/`network.sb` (AC-0013/0014) and extracting the addon
  (AC-0019) — consumed as inputs.
- `doctor`'s `flock`-reliability + orphan-proxy detection (AC-0031).
- `policy.json` hot-reload on hash mismatch (the lock only **records** the hash;
  touching `policy.json` to trigger mitmproxy's reload is AC-0025's orchestration).
</content>
