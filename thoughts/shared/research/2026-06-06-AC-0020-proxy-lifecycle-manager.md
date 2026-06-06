---
date: 2026-06-06
ticket: AC-0020
title: "Research: Proxy lifecycle manager (WP-3.4)"
status: complete
tags: [research, proxy, lifecycle, flock, refcount, WP-3.4]
git_commit: 9489e43
branch: main
repository: github.com/tobyS/agent-creance
---

# Research: AC-0020 — Proxy lifecycle manager (WP-3.4)

## Research question

What is required to build `internal/proxy`'s lifecycle manager — the `flock`-guarded
`proxy.lock` refcount that lets multiple `agent-creance` invocations share one
mitmproxy per project: start-or-attach, prune dead agents, allocate an ephemeral
port (best-effort reclaim on restart), decrement on exit and kill the proxy only on
last-out, purge the session-overlay on last-out, and warn (never kill) when a
restart lands on a different port with agents still attached? Which seams already
exist, which must be created, and what design decisions are open?

## Summary

- **The two consuming seams already exist as interface + fake, with stubbed real
  impls.** `Flock` (`internal/sysdep/flock.go`) and `ProcessGroup` /`Process`
  (`internal/sysdep/processgroup.go`) were seeded by AC-0009 (WP-1.4). Both real
  impls return `ErrNotImplemented` today; **`OSFlock`'s own doc comment defers its
  real `golang.org/x/sys/unix.Flock(fd, LOCK_EX)` implementation to "WP-3.4 (the
  proxy lifecycle)" — i.e. this ticket.** `OSProcessGroup.Start` is instead deferred
  to WP-4.3 (`internal/cage`), and exec-ing Safehouse / signal forwarding is
  explicitly **out of scope** for AC-0020 (AC-0023/AC-0024).
- **Two primitives this ticket needs have no seam at all and must be created:**
  1. **Process-liveness probe (`kill(pid, 0)`)** — to prune dead agent PIDs and
     verify the recorded proxy PID is alive. Nothing in the tree probes an arbitrary
     PID's liveness today.
  2. **Ephemeral-port allocation + best-effort reclaim** (`net.Listen` on `:0`, and
     a best-effort bind to a recorded port number). No port/socket seam exists.
  Both are macOS/syscall-adjacent and untestable inline, so both follow the
  established sysdep pattern: narrow interface + `OS*` real impl + fake in
  `sysdeptest` + `ErrNotImplemented` where a real impl is risky to unit-test.
- **The state package already owns the lock path.** `state.Layout.ProxyLock()` →
  `<cache>/agent-creance/projects/<hash>/proxy.lock`, and `SessionOverlay()` →
  `<root>/session-overlay.yaml` (the file purged on last-out). The `state` package
  does no I/O; `internal/proxy` owns reading/writing the lock and purging the
  overlay (cross-cutting C4: out-of-tree).
- **The package already exists** (`internal/proxy`, from AC-0019) but holds only the
  enforcer embed/extract half (`extract.go`). The lifecycle manager is **new code
  in the same package**, and `extract.go` is the local style template (constructor
  taking sysdep seams, `state.New(paths)`, atomic temp+rename writes, self-healing
  reads).
- **The lock-file format is unspecified beyond its fields** (PID, port, policy hash,
  attached-agent PIDs). The established serialization idiom in this codebase is a
  small JSON struct written atomically via temp+rename (`compile.go`,
  `generator/cache.go`), read with corrupt-as-absent tolerance.
- **`App` does not yet carry `Flock`/`ProcessGroup`/the new seams**, and there is
  **no `run` command** (`internal/cli/run.go` — that is AC-0025, downstream). For
  AC-0020 the manager is a logic package with unit tests; whether to wire it into
  `App`/`Main()` now (with no caller) is an open question (the AC-0009 convention is
  "don't wire unused deps into `cli.App`").
- The two ticket "Questions for Research/Planning" resolve to: (1) `flock`
  reliability detection is **AC-0031 doctor's** job (out of scope here; design names
  iCloud Drive + SMB as the unreliable cases); (2) "proxy is alive" needs **more
  than PID liveness** — a recycled PID is a real hazard — so a **port probe** (TCP
  connect to the recorded port, or a liveness check via the proxy) is the robust
  signal. This is a design decision (see Open Questions).

## Detailed findings

### The package and where the new code lands

`internal/proxy` already exists (AC-0019, WP-3.3). Its current contents:

- `internal/proxy/extract.go` — `Extractor` (embed + extract the Python enforcer
  addon). **This is the local style template** for the new lifecycle manager:
  - `NewExtractor(fsys sysdep.FileSystem, paths sysdep.PathResolver)` builds
    `state.New(paths)` internally (`extract.go:57-59`) — the same shape the manager
    should take.
  - `writeIfChanged` (`extract.go:93-114`) is the canonical atomic write: read
    current → `bytes.Equal` short-circuit → `WriteFile(tmp)` → `Rename(tmp, dest)`
    → best-effort `Remove(tmp)` on failure; `errors.Is(err, fs.ErrNotExist)`
    distinguishes first-run from a real read error.
  - `dirPerm = 0o755`, `filePerm = 0o644`, `tmpSuffix = ".tmp"` (`extract.go:40-42`).
- `internal/proxy/enforcer/*.py` — the addon the manager will hand to mitmproxy as
  `mitmdump -s <enforcer.py> --set ...`. `extract.go`'s `Extract()` returns the path
  to `enforcer.py` (`extract.go:68-86`). The manager starts mitmproxy *with* that.

The lifecycle manager is new code (e.g. `internal/proxy/lifecycle.go` + the lock
struct), living alongside `extract.go` in `package proxy`.

### Seam 1 — `Flock` (exists; real impl is this ticket's job)

`internal/sysdep/flock.go`:

```go
type Flock interface {
    // Acquire takes an exclusive advisory lock on the file at path (creating it
    // if absent), blocking until the lock is held, and returns a release func
    // that unlocks and closes the underlying descriptor. A non-nil error means
    // the lock could not be acquired; in that case release is nil.
    Acquire(path string) (release func() error, err error)
}
```

- `OSFlock.Acquire` is **stubbed** (`return nil, ErrNotImplemented`,
  `flock.go:30-32`). Its doc (`flock.go:21-25`) prescribes the real impl: open
  `path`, `golang.org/x/sys/unix.Flock(fd, LOCK_EX)` (blocking, exclusive); release
  does `Flock(LOCK_UN)` then `Close`. **Implementing this is part of AC-0020** —
  and it adds the first `golang.org/x/sys` dependency to the module.
- Fake: `sysdeptest/FakeFlock` (`sysdeptest/flock.go`) records `Acquired`/`Released`
  paths in order, exposes `Held(path) bool`, and has `AcquireErr`/`ReleaseErr`
  knobs. Tests assert the lock was taken *and* released around the read-modify-write.
- Note the contract: `Flock` only **serialises**; the holder still reads/writes the
  lock-file *contents* through `FileSystem`. So the manager pairs `Flock.Acquire`
  with `FS.ReadFile`/`WriteFile`/`Rename` on the same path.

### Seam 2 — `ProcessGroup`/`Process` (exists; partially relevant)

`internal/sysdep/processgroup.go`:

```go
type ProcessGroup interface {
    Start(ctx context.Context, name string, args ...string) (Process, error) // Setpgid:true
    Notify(ch chan<- os.Signal, sigs ...os.Signal)                           // os/signal.Notify
}
type Process interface {
    Signal(sig os.Signal) error // kill(-pgid, sig) — whole group
    Wait() error                // blocks until leader exits; nil on clean exit
    Pgid() int
}
```

- `OSProcessGroup.Start` is **stubbed** (`ErrNotImplemented`), deferred to **WP-4.3
  (`internal/cage`)** — *not* this ticket. `Notify` is already real.
- Fake: `sysdeptest/FakeProcessGroup`/`FakeProcess` records `Started` commands,
  `Notified` signal sets, forwarded `Signals`, and returns scripted `Pgid`/`WaitErr`.
- **Relevance to AC-0020:** the manager *starts* mitmproxy and must be able to
  *signal/kill* it on last-out. But note the seam's `Signal` targets the **whole
  process group** (`kill(-pgid, sig)`), which is the *agent's* teardown semantics
  (AC-0024), not "kill this one proxy PID". Two open design points: (a) is mitmproxy
  started via `ProcessGroup.Start` (giving a `Process` handle to `Wait`/`Signal`),
  and (b) "kill the proxy" on last-out happens in a **different invocation** than
  the one that started it (the last agent out usually didn't start the proxy) — so
  the killer only has the recorded **PID**, not a live `Process` handle. That argues
  for a **PID-targeted signal/kill** primitive (likely folded into the liveness seam
  below: `kill(pid, sig)` with `sig==0` for liveness, `sig==SIGTERM` for kill),
  distinct from the group-targeted `Process.Signal`. **Decision needed.**

### Gap 1 — process liveness (`kill -0`), and PID-targeted kill

design.md "Multi-agent lifecycle" step 2 (`design.md:400`): *"prunes dead agent
PIDs (via `kill -0`), verifies the proxy is alive… If the proxy is dead but agents
are listed, it's been a crash and we start fresh."* The ticket AC #2 echoes this
(`kill -0`).

**No seam probes an arbitrary PID's liveness today.** Grep across `internal/`: the
only `syscall.Kill`/`Getpid` is in `processgroup_test.go` (test-only, the SIGUSR1
Notify test). `Process.Signal` forwards to a *group* (`kill(-pgid, …)`), not a
liveness probe of a recorded PID. So AC-0020 introduces a new seam — shape (proposal):

```go
// e.g. internal/sysdep/process.go  (name TBD: "ProcessProber" / "Signaller")
type ... interface {
    // Alive reports whether pid is a live process (syscall.Kill(pid, 0): nil ⇒ alive;
    // ESRCH ⇒ dead; EPERM ⇒ alive-but-not-ours, treated as alive).
    Alive(pid int) bool
    // Signal sends sig to a single pid (syscall.Kill(pid, sig)) — used to kill the
    // recorded proxy PID on last-out, by an invocation that holds no Process handle.
}
```

This is real-impl-able now (pure `syscall.Kill`), unlike `OSFlock`/`OSProcessGroup`,
but it's the kind of thing that's awkward to unit-test against real PIDs, so the fake
is what the manager's tests use. **Whether liveness + PID-kill is one seam or two,
and whether it reuses/extends `ProcessGroup`, is an open decision.**

### Gap 2 — ephemeral-port allocation + best-effort reclaim

design.md "Multi-agent lifecycle" (`design.md:408`) and "Crash recovery and the
port" (`design.md:410-412`): bind mitmproxy to `:0`, let the OS assign a free
ephemeral port, record it in the lock; subsequent runs read it back. On a **restart**
after a crash, **attempt to reclaim the recorded port** (best-effort `bind` to that
number); reclaim can fail (another process holds it) and `SO_REUSEADDR` doesn't help
against a live holder. Ticket AC #3: "bind `:0`, record the port; on restart attempt
best-effort reclaim."

**No port/socket seam exists.** This is new. Two sub-operations:

1. **Allocate** a free ephemeral port — `net.Listen("tcp", "127.0.0.1:0")`, read
   `.Addr().(*net.TCPAddr).Port`, then the port is handed to mitmproxy. (Subtlety:
   the classic `:0`-then-close-then-rebind has a TOCTOU window; how mitmproxy is
   actually told which port — `--listen-port N` vs. inheriting a socket — affects
   the design. mitmproxy takes `--listen-port`/`-p`, so the manager allocates,
   closes, and passes `N`, accepting the small race.)
2. **Reclaim** — try to `bind` the *recorded* port number; success ⇒ reuse it
   (attached agents recover transparently, since their frozen `network.sb` only
   allows the old port); failure ⇒ allocate a fresh `:0` port and enter the
   **warn-never-kill** path (AC #5).

Seam shape is genuinely consumer-determined here; options range from a thin
`PortAllocator{ Allocate() (int, error); TryBind(port int) (ok bool, err error) }`
to passing a `net`-backed interface. **Decision needed**, and this is the seam most
worth pinning down in planning because it drives the crash-recovery logic.

### The lock file: path, fields, format

- Path: `state.Layout.ProxyLock()` → `<cache>/agent-creance/projects/<hash>/proxy.lock`
  (`state.go:176-177`). Resolve a `Layout` via `state.New(paths).Resolve(projectDir)`.
- Project identity = SHA-256(canonical realpath)[:8] as 16 hex chars
  (`state.go:97-100`); symlinks collapse to one identity, moved dirs are distinct —
  the same scheme the design ties the lock to (`state.go:9-10`).
- Fields the lock must record (ticket AC #1 + design.md:393): **proxy PID, port,
  policy hash, attached-agent PIDs**. (Policy hash is what step 2 compares against
  the on-disk `policy.json` to decide whether to touch it for a hot-reload — though
  *touching policy.json to hot-reload* is arguably the run command's job, AC-0025;
  for AC-0020 the lock just **records** the hash.)
- **Format is unspecified.** The codebase's established small-state serialization is
  JSON via `json.MarshalIndent(v, "", "  ")` + trailing `'\n'`, written atomically
  temp→rename, read with corrupt-as-absent tolerance:
  - `internal/policy/compile/compile.go:515-537` (write), `:402-412` (read).
  - `internal/generator/cache.go:23-26,44-81` (envelope struct + read/write).
  Recommend a `lockFile` struct with `json` tags written this way. **Confirm format.**

Important ordering nuance vs. the atomic-rename idiom: under `flock`, the
read-modify-write happens **while the lock is held on `proxy.lock` itself**. A
temp+rename *replaces the inode*, which can interact badly with an advisory lock
held on the original fd. Two viable patterns: (a) write-in-place (truncate + write)
under the held flock, or (b) flock a **separate** lock file and rename the data file.
The design says the flock is *on the lock file* (`design.md:399`), implying the
lock file and the data file are the same — which points to **write-in-place under
flock**, not temp+rename, for this one file. **This is a real decision** (it diverges
from the repo's temp+rename default) and should be settled in planning. It may need a
`FileSystem` method the seam lacks (truncate/write-at-offset) — current `FileSystem`
has `WriteFile` (which truncates) but the holder must write through the *same fd it
locked* for the lock to mean anything, and `Flock.Acquire` returns only a release
closure, not the fd. **Flag: the `Flock` seam may need to expose the fd, or the
manager may flock a sentinel and `WriteFile` the data file.** See Open Questions.

### Teardown / last-out semantics

design.md:404 + ticket AC #4: on any exit (clean, SIGINT, crash) the trap handler
**reacquires the flock**, removes its own PID, and **kills the proxy iff the agents
array is now empty**, and **purges the session-overlay on last-out**
(`design.md:305` — the overlay is `state.Layout.SessionOverlay()`).

- "Reacquire on teardown" means the manager exposes a **detach/teardown** operation
  symmetric to attach: lock → read → drop own PID → if empty {kill proxy PID; remove
  overlay; optionally remove lock} else {write back} → unlock.
- Overlay purge = `FS.Remove(layout.SessionOverlay())` (use the existing
  `sysdep.RemoveIfPresent(fs, name)` helper, `filesystem.go:82-93`, so a missing
  overlay isn't an error).
- "Kill the proxy" is PID-targeted (the last agent out generally didn't start it) →
  argues again for the PID-kill primitive in Gap 1, not `Process.Signal`.

### Warn-never-kill (AC #5)

design.md:410-412 + ticket AC #5: when a restart **could not reclaim** the recorded
port **and** agents remain attached, emit the documented warning naming affected
PIDs — *"proxy restarted on port N (was M); attached agents <pids> will see egress
failures and should be relaunched"* — and **never signal those agents**. Test asserts
the message names the PIDs and that no signal was sent. This is a pure-logic branch
(no new seam) once the port-reclaim result is known; the warning goes to `app.Stderr`
(the manager should take an `io.Writer`, mirroring how commands get `app.Stderr`).

### Composition / wiring

- `App` (`internal/cli/cli.go:20-32`) holds `Commander, Stdout, Stderr, Tested, FS,
  Paths, Clock, HTTP`. It does **not** yet hold `Flock`, `ProcessGroup`, or the new
  liveness/port seams. `Main()` (`cli.go:62-79`) wires the `OS*` impls.
- The canonical constructor to mirror is `compile.New(fsys, paths, clock, getter)
  (*Compiler, error)` (`compile.go:108-130`) — takes bare sysdep interfaces, builds
  `state.New(paths)` internally, returns `(*T, error)`.
- **There is no `run` command** (`internal/cli/run.go` does not exist — that's
  AC-0025). So AC-0020 has **no in-repo caller** yet. Per the AC-0009 convention
  ("do not wire unused deps into `cli.App`"), the safe path is: build the manager +
  its seams + unit tests now, and **defer the `App`/`Main()` wiring to AC-0025**
  (the run command). But the seams' `OS*` impls (real `OSFlock`, new port/liveness
  impls) should still be implemented now since this ticket owns them. **Confirm the
  wiring boundary.**

### Testing approach (matches profile + ticket Verification)

The ticket's required unit cases (`AC-0020.md` Verification §2) map cleanly onto
fake-driven table/scenario tests in `internal/proxy`:

- **start-then-attach** — second `Run`/attach doesn't `Start` a second mitmproxy
  (`FakeProcessGroup.Started` length 1); agents array length 2 in the written lock.
- **last-out teardown** — removing the final PID kills the proxy (PID-kill primitive
  records the signal) and purges the overlay (`FakeFileSystem` shows overlay
  removed); a non-final exit does neither.
- **dead-PID prune** — a recorded PID the liveness fake reports dead is dropped on
  next run.
- **crash restart, reclaim success** — port preserved; attached agents untouched
  (no signal).
- **crash restart, reclaim fail with agents attached** — warning emitted (assert it
  names the PIDs), agents not signaled.

Conventions to follow (from profile.md + the pattern catalog):
- **Blackbox `package proxy_test`** for behavior, injecting `sysdeptest` fakes
  (model: `prereq_test.go:28-48`). White-box only for unexported pure helpers
  (model: `version_test.go`).
- **Race detector** (`go test -race ./internal/proxy/...`) — ticket §3 wants a
  concurrent attach/detach simulation clean under `-race`.
- The **integration** path (real mitmproxy across two invocations, ticket §4) is
  `//go:build integration`, gated on spike **S3** — out of scope for the unit work
  but the manager must be structured so an integration test can drive it.
- A **golden** lock-file render is optional but available (`report_test.go` pattern)
  if the lock JSON is worth pinning.

## Code references

- `internal/proxy/extract.go:51-114` — local style template (constructor +
  `state.New` + atomic write/self-heal).
- `internal/sysdep/flock.go:13-32` — `Flock` interface; `OSFlock` stub whose doc
  defers the real `unix.Flock(LOCK_EX)` impl to **this ticket**.
- `internal/sysdep/sysdeptest/flock.go` — `FakeFlock` (Acquired/Released/Held).
- `internal/sysdep/processgroup.go:20-54` — `ProcessGroup`/`Process`; group-targeted
  `Signal`, `Start` stubbed (deferred to WP-4.3, not here).
- `internal/sysdep/sysdeptest/processgroup.go` — `FakeProcessGroup`/`FakeProcess`.
- `internal/sysdep/filesystem.go:19-39,82-93` — `FileSystem` (WriteFile/Rename/Remove)
  + `RemoveIfPresent` helper (for overlay purge).
- `internal/sysdep/clock.go` + `sysdeptest/clock.go` — `Clock` (lock timestamps, if any).
- `internal/sysdep/errors.go:11` — `ErrNotImplemented` sentinel.
- `internal/state/state.go:97-100,176-177,185-187` — identity hash; `ProxyLock()`;
  `SessionOverlay()`.
- `internal/policy/compile/compile.go:108-130,402-412,515-537` — constructor +
  atomic-JSON read/write idiom to mirror.
- `internal/generator/cache.go:23-26,44-81` — second JSON-state read/write example.
- `internal/cli/cli.go:20-32,62-79` — `App` + `Main()` (where wiring would land; no
  `Flock`/`ProcessGroup` today).
- `internal/cli/testdata/script/doctor_healthy.txtar` — PATH-stub pattern for any
  future CLI-level test (downstream `run`, not this ticket).
- `docs/design.md:389-412` — "Multi-agent lifecycle" + "Crash recovery and the port".
- `docs/design.md:301-305` — session-overlay purge-on-last-out contract.
- `docs/design.md:447` — tech stack: `golang.org/x/sys/unix.Flock`.
- `thoughts/shared/research/2026-06-05-AC-0009-sysdep-seam-extensions.md` — seam
  conventions; design rationale per seam (Flock/ProcessGroup shapes, §"Design
  rationale per seam").

## Open questions / decisions for planning

1. **Liveness + PID-kill seam.** A new seam is required (none exists). One seam
   (`Alive(pid) bool` + `Signal(pid, sig)`) or two? Reuse/extend `ProcessGroup`, or
   a fresh `internal/sysdep/process.go`? The PID-kill is needed because the last
   agent out (the proxy's killer) holds no live `Process` handle — only the recorded
   PID. **Recommend** a small new seam with `Alive(pid int) bool` and
   `Signal(pid int, sig os.Signal) error`, real-impl now (`syscall.Kill`), fake in
   `sysdeptest`. **Decide.**

2. **Port seam shape.** New seam required. Thin `PortAllocator{ Allocate() (int,
   error); TryReclaim(port int) (ok bool, err error) }` vs. exposing a `net`-style
   interface. How is mitmproxy told the port (`--listen-port N`, accepting the
   allocate-close-pass TOCTOU race)? **Recommend** the thin allocator + `--listen-port`.
   **Decide.**

3. **Lock-file write strategy under flock.** The repo default is temp+rename, but an
   advisory `flock` is held on the *lock file's fd*; temp+rename swaps the inode out
   from under the lock. Either (a) write-in-place (truncate+write) on the locked fd,
   or (b) flock a sentinel and temp+rename a separate data file. This may force a
   change to the `Flock` seam (expose the fd) or a new `FileSystem` capability.
   **This is the riskiest correctness decision in the ticket — settle it explicitly.**

4. **"Proxy is alive" verification (ticket Question 2).** PID-liveness alone is
   unsafe — PIDs are recycled, so a dead proxy's PID may now be some unrelated
   process. The robust signal is a **port probe** (TCP-connect the recorded port,
   and/or check the process identity). **Recommend** combine PID-liveness *and* a
   port probe before deciding "alive vs crashed". Decide how deep (connect-only vs.
   HTTP ping the proxy). This likely reuses the port seam from Q2.

5. **`flock`-reliability detection (ticket Question 1).** design.md:395 assigns the
   "warn on iCloud Drive / SMB" check to `doctor` — that is **AC-0031**, out of scope
   here. **Recommend** note it and move on; AC-0020 assumes a reliable FS.

6. **Wiring boundary.** No `run` command exists (AC-0025). Wire the manager into
   `App`/`Main()` now (unused) or defer to AC-0025 per the AC-0009 "no unused deps"
   rule? **Recommend** implement the manager + seams + `OS*` impls now, and **defer
   the `App` field/`Main()` wiring to AC-0025**. **Confirm.**

7. **Lock-file JSON format.** Confirm a `json`-tagged struct (proxy PID, port, policy
   hash, `[]int` agent PIDs, maybe a `Clock`-stamped `started_at`) written the
   repo-standard way. **Confirm fields + format.**

## Verification (from the ticket)

1. `go build ./...` compiles.
2. `go test -race ./internal/proxy/...` with fakes (Flock, process, clock) — the five
   required cases above.
3. Race detector clean on a concurrent attach/detach simulation.
4. Integration (`make test-integration`, gated S3): real mitmproxy
   start/attach/teardown across two invocations.
5. C4 guard: lock path resolves under the out-of-tree state dir
   (`state.Layout.ProxyLock()`), never in the project tree.
</content>
</invoke>
