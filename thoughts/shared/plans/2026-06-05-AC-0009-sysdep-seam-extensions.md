---
date: 2026-06-05
ticket: AC-0009
title: "Plan: sysdep seam extensions (WP-1.4)"
status: ready
research: thoughts/shared/research/2026-06-05-AC-0009-sysdep-seam-extensions.md
tags: [plan, sysdep, testability, WP-1.4]
git_commit: 17c798e
branch: main
---

# Plan: AC-0009 — sysdep seam extensions (WP-1.4)

## Overview

Grow `internal/sysdep` with the OS-abstraction seams later phases need —
`Clock`, the `FileSystem` write methods, `Keychain`, `Flock`, and
`ProcessGroup`/signals — each with a configurable fake in
`internal/sysdep/sysdeptest` and a smoke test, following the existing
`Commander`/`FileSystem` pattern. WP-1.4 ships **interfaces + fakes**; real
impls land with their consumers, except where the production behaviour is a
trivial, portable stdlib one-liner.

## Decisions (resolved at the question checkpoint)

1. **Real + stub split.** Real stdlib implementations now for `Clock` and the
   `FileSystem` write methods (trivial, fully testable, matches the existing
   "delegate to stdlib" convention). `Keychain`, `Flock`, and `ProcessGroup.Start`
   are stubbed — the production type exists (so the `var _ Iface = (*Impl)(nil)`
   assertion compiles) but its method(s) return `ErrNotImplemented`; the real,
   macOS-specific behaviour lands with the consumer ticket. The one exception
   inside a stubbed seam is `ProcessGroup.Notify`, which is portable stdlib
   (`os/signal.Notify`) and is implemented for real.
2. **`sysdep.ErrNotImplemented` sentinel** is introduced (new file `errors.go`),
   so consumer tickets and tests can `errors.Is` against deferred behaviour.
3. **Grow `FileSystem` in place** with `WriteFile`/`Stat`/`MkdirAll`/`Remove`/
   `Rename`. **`Flock` is its own thin seam** wrapping (later) `unix.Flock`; any
   higher-level lock abstraction lives in the proxy package (WP-3.4), not sysdep.
4. **ProcessGroup minimal per design**: `Start` (new pgid) returning a `Process`
   handle (`Signal`/`Wait`/`Pgid`) plus `Notify` (signal subscription). Accept
   likely churn when WP-4.3 lands.

## Current state

- `internal/sysdep` has `Commander`, `PathResolver`, and `FileSystem`
  (`ReadFile` only). `Clock`/`Keychain`/`Flock`/`ProcessGroup` do not exist.
- `internal/sysdep/sysdeptest` has `FakeCommander`, `FakePathResolver`,
  `FakeFileSystem` (`ReadFile` only). No `_test.go` files exist there yet.
- No `ErrNotImplemented` sentinel; no call-recording fakes.

## Desired end state

- Five seams present per the table below; every new interface has a production
  type with a compile-time `var _ Iface = (*Impl)(nil)` assertion, a configurable
  fake in `sysdeptest` (with its own `var _ sysdep.Iface = (*FakeIface)(nil)`
  assertion), and a smoke test.
- `go build ./...`, `go vet ./...`, `go test -race ./internal/sysdep/...`, and
  `make test` are green; `make lint` clean.
- No new external module dependency is added (the stubbed `Flock`/`Keychain`
  impls avoid `golang.org/x/sys` and Security.framework until their consumers).
- `cli.App` is **not** changed — unused deps stay unwired until a consumer
  arrives (per the ticket's Out of Scope).

| Seam | Production method behaviour |
|---|---|
| `Clock` | real (`time.Now`, `time.Since`) |
| `FileSystem` writes | real (`os.WriteFile`/`Stat`/`MkdirAll`/`Remove`/`Rename`) |
| `Keychain` | stub → `ErrNotImplemented` |
| `Flock` | stub → `ErrNotImplemented` |
| `ProcessGroup` | `Start` stub → `ErrNotImplemented`; `Notify` real |

## What we're NOT doing

- No real `unix.Flock`, Security.framework/`security`-CLI Keychain access, or
  `Setpgid`/`kill(-pgid)` wiring — those land with WP-3.4 / WP-4.1 / WP-4.3.
- No `go.mod` changes; no `golang.org/x/sys` import yet.
- No wiring of the new deps into `cli.App`.
- No change to `Commander`/`PathResolver` or their fakes.
- No `RemoveAll` on `FileSystem` (its consumer — the `clean` command — adds it).

---

## Phase 1 — Clock seam (real impl)

### Files
- `internal/sysdep/clock.go` (new)
- `internal/sysdep/clock_test.go` (new)
- `internal/sysdep/sysdeptest/clock.go` (new)
- `internal/sysdep/sysdeptest/clock_test.go` (new)

### `clock.go`
```go
package sysdep

import "time"

// Clock abstracts reading the current time, so logic with time-dependent
// behaviour (the 30-day registry-cache expiry, audit-log timestamps) can be
// unit-tested deterministically instead of racing the real wall clock.
//
// Why route time through the seam (for someone coming from PHP/TS): calling
// time.Now() directly makes a test depend on when it runs. Packages take a Clock
// and call *that*; production wires OSClock, tests wire the fake in sysdeptest.
type Clock interface {
	// Now returns the current local time, mirroring time.Now.
	Now() time.Time
	// Since returns the time elapsed since t, mirroring time.Since (i.e.
	// Now().Sub(t)). Provided as a convenience so callers need not hold a Now().
	Since(t time.Time) time.Duration
}

// OSClock is the production Clock backed by the time package. The compile-time
// assertion mirrors the Commander idiom.
type OSClock struct{}

var _ Clock = (*OSClock)(nil)

func (OSClock) Now() time.Time                  { return time.Now() }
func (OSClock) Since(t time.Time) time.Duration { return time.Since(t) }
```

### `sysdeptest/clock.go`
```go
package sysdeptest

import (
	"time"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// FakeClock is a Clock frozen at Current. Now returns Current verbatim and Since
// is computed against it, so cache-expiry/timestamp logic is fully deterministic.
// Advance moves the clock forward to exercise "time has passed" branches.
type FakeClock struct {
	Current time.Time
}

var _ sysdep.Clock = (*FakeClock)(nil)

// NewFakeClock returns a clock frozen at t.
func NewFakeClock(t time.Time) *FakeClock { return &FakeClock{Current: t} }

func (f *FakeClock) Now() time.Time                  { return f.Current }
func (f *FakeClock) Since(t time.Time) time.Duration { return f.Current.Sub(t) }

// Advance moves the fake clock forward by d.
func (f *FakeClock) Advance(d time.Duration) { f.Current = f.Current.Add(d) }
```

### Tests
- `clock_test.go` (`package sysdep`): assert `OSClock{}.Now()` is within a small
  window of `time.Now()` and `Since(now)` is `>= 0` and small.
- `sysdeptest/clock_test.go`: a fixed `Current`; assert `Now()` equals it,
  `Since(Current.Add(-time.Hour)) == time.Hour`, and `Advance` shifts `Now`.

### Success criteria
- Automated: `go build ./...`; `go vet ./...`; `go test -race ./internal/sysdep/...` pass.
- Manual: `clock.go` doc/style matches `filesystem.go`.

---

## Phase 2 — FileSystem write methods (real impl, grow in place)

### Files
- `internal/sysdep/filesystem.go` (extend)
- `internal/sysdep/filesystem_test.go` (extend)
- `internal/sysdep/sysdeptest/filesystem.go` (extend)
- `internal/sysdep/sysdeptest/filesystem_test.go` (new)

### Interface additions (`filesystem.go`)
Update the interface doc to drop the "(AC-0009) grows it" note (now done) and add
the methods. Use `io/fs` types (`fs.FileMode`, `fs.FileInfo`):
```go
	// WriteFile writes data to name with perm, mirroring os.WriteFile (truncating
	// or creating). A non-nil error means the write failed.
	WriteFile(name string, data []byte, perm fs.FileMode) error
	// Stat returns file info for name, mirroring os.Stat. A non-existent path
	// yields an error satisfying errors.Is(err, fs.ErrNotExist).
	Stat(name string) (fs.FileInfo, error)
	// MkdirAll creates name and any missing parents with perm, mirroring
	// os.MkdirAll; it is a no-op (nil) if the directory already exists.
	MkdirAll(name string, perm fs.FileMode) error
	// Remove removes the named file or empty directory, mirroring os.Remove.
	Remove(name string) error
	// Rename renames (moves) oldpath to newpath, mirroring os.Rename — the
	// primitive behind atomic "write temp then rename" updates.
	Rename(oldpath, newpath string) error
```
`OSFileSystem` gains five value-receiver one-liners delegating to `os.WriteFile`,
`os.Stat`, `os.MkdirAll`, `os.Remove`, `os.Rename`. (`filesystem.go` imports `os`
already; `io/fs` is referenced only in the interface signatures — add the import.)

### Fake additions (`sysdeptest/filesystem.go`)
Keep `Files`/`Errs` (read). Add the write surface and an unexported `fakeFileInfo`:
```go
type FakeFileSystem struct {
	Files map[string][]byte      // path -> contents (ReadFile/WriteFile)
	Dirs  map[string]bool        // paths created via MkdirAll
	Perms map[string]fs.FileMode // path -> perm recorded by WriteFile/MkdirAll
	// Per-operation error knobs (checked before the op). Errs is ReadFile's.
	Errs       map[string]error
	WriteErrs  map[string]error
	StatErrs   map[string]error
	MkdirErrs  map[string]error
	RemoveErrs map[string]error
	RenameErrs map[string]error
}

var _ sysdep.FileSystem = (*FakeFileSystem)(nil)
```
- `NewFakeFileSystem` initializes every map to non-nil.
- `WriteFile`: check `WriteErrs[name]`; else copy data into `Files[name]`, record
  `Perms[name]`.
- `Stat`: check `StatErrs`; if in `Files` → `fakeFileInfo{size, mode}`; if in
  `Dirs` → dir info; else `fs.ErrNotExist`.
- `MkdirAll`: check `MkdirErrs`; mark `Dirs[name]=true`, record perm.
- `Remove`: check `RemoveErrs`; delete from `Files`/`Dirs`/`Perms`.
- `Rename`: check `RenameErrs`; move the `Files` (or `Dirs`) entry; `fs.ErrNotExist`
  if neither present.
- `fakeFileInfo` implements `fs.FileInfo` (`Name` via `filepath.Base`, `Size`,
  `Mode`, `ModTime`→zero, `IsDir`, `Sys`→nil).
- Adding the `var _` assertion makes `sysdeptest` import `sysdep` (no cycle —
  sysdep never imports sysdeptest).

### Tests
- `filesystem_test.go` (extend, real impl, `t.TempDir()`): WriteFile→ReadFile
  roundtrip; MkdirAll then Stat `IsDir()`; Remove then ReadFile is
  `fs.ErrNotExist`; Rename moves content; Stat(missing) is `fs.ErrNotExist`.
- `sysdeptest/filesystem_test.go` (new): write then read back; Stat reports size
  & dir; a scripted `WriteErrs` entry surfaces; Rename moves the in-memory entry;
  absent path → `fs.ErrNotExist`.

### Success criteria
- Automated: `go build ./...`; `go vet ./...`; `go test -race ./internal/sysdep/...`;
  `make test` (confirms `internal/config`, which uses `FakeFileSystem`, still
  compiles against the grown fake) pass.
- Manual: existing `internal/config`/`internal/state` fake usages unaffected
  (only additive fields/methods).

---

## Phase 3 — ErrNotImplemented sentinel + Keychain seam (stub)

### Files
- `internal/sysdep/errors.go` (new)
- `internal/sysdep/keychain.go` (new)
- `internal/sysdep/keychain_test.go` (new)
- `internal/sysdep/sysdeptest/keychain.go` (new)
- `internal/sysdep/sysdeptest/keychain_test.go` (new)

### `errors.go`
```go
package sysdep

import "errors"

// ErrNotImplemented is returned by production sysdep implementations whose real,
// platform-specific behaviour is deferred to the ticket that introduces their
// first consumer (WP-1.4 seeds the interface + fake now; the macOS impls for
// Keychain, Flock, and ProcessGroup land with internal/cred, the proxy
// lifecycle, and internal/cage). Callers can errors.Is against it.
var ErrNotImplemented = errors.New("sysdep: not implemented")
```

### `keychain.go`
```go
package sysdep

import "errors"

// Keychain abstracts reading a generic-password item from the macOS login
// Keychain — specifically the Anthropic OAuth credential (item "Claude
// Code-credentials"). v0.1's host-side job is detection: present, absent, or
// locked (the one non-interactive doctor failure). Refresh happens inside the
// cage, not here.
type Keychain interface {
	// FindGenericPassword returns the secret bytes of the login-keychain
	// generic-password item identified by service and account. A missing item
	// yields ErrItemNotFound; a locked keychain yields ErrKeychainLocked; callers
	// distinguish these via errors.Is.
	FindGenericPassword(service, account string) ([]byte, error)
}

// Contract sentinels (distinct from ErrNotImplemented — these are the real
// outcomes the seam models and the fake returns):
var (
	ErrItemNotFound   = errors.New("sysdep: keychain item not found")
	ErrKeychainLocked = errors.New("sysdep: keychain is locked")
)

// OSKeychain is the production Keychain. Deferred to WP-4.1 (internal/cred): the
// real impl reads the item via Security.framework and maps absent→ErrItemNotFound,
// locked→ErrKeychainLocked.
type OSKeychain struct{}

var _ Keychain = (*OSKeychain)(nil)

func (OSKeychain) FindGenericPassword(service, account string) ([]byte, error) {
	return nil, ErrNotImplemented
}
```

### `sysdeptest/keychain.go`
```go
type FakeKeychain struct {
	Items   map[string][]byte // "service\x00account" -> secret
	Errs    map[string]error  // per-key error override
	Locked  bool              // simulate a locked keychain (wins over Items)
	Lookups []KeychainQuery   // records each FindGenericPassword call
}
type KeychainQuery struct{ Service, Account string }

var _ sysdep.Keychain = (*FakeKeychain)(nil)
```
- `NewFakeKeychain` inits maps. `WithItem(service, account, secret)` builder.
- `FindGenericPassword`: append to `Lookups`; if `Locked` → `ErrKeychainLocked`;
  if `Errs[key]` → that error; if `Items[key]` → copy of bytes; else
  `ErrItemNotFound`.

### Tests
- `keychain_test.go`: `OSKeychain{}.FindGenericPassword(...)` returns
  `errors.Is(err, sysdep.ErrNotImplemented)`.
- `sysdeptest/keychain_test.go`: `WithItem` then found; absent → `ErrItemNotFound`;
  `Locked` → `ErrKeychainLocked`; `Lookups` recorded.

### Success criteria
- Automated: `go build ./...`; `go vet ./...`; `go test -race ./internal/sysdep/...` pass.
- Manual: sentinels documented; stub clearly references WP-4.1.

---

## Phase 4 — Flock seam (stub)

### Files
- `internal/sysdep/flock.go` (new)
- `internal/sysdep/flock_test.go` (new)
- `internal/sysdep/sysdeptest/flock.go` (new)
- `internal/sysdep/sysdeptest/flock_test.go` (new)

### `flock.go`
```go
package sysdep

// Flock abstracts an exclusive advisory file lock — the primitive behind the
// atomic read-modify-write of the proxy.lock file in the multi-agent lifecycle.
// It is a separate concern from FileSystem (content I/O): Flock only locks.
type Flock interface {
	// Acquire takes an exclusive advisory lock on the file at path (creating it
	// if absent), blocking until the lock is held, and returns a release func
	// that unlocks and closes the underlying descriptor. A non-nil error means
	// the lock could not be acquired.
	Acquire(path string) (release func() error, err error)
}

// OSFlock is the production Flock. Deferred to WP-3.4 (proxy lifecycle): the real
// impl opens path and calls golang.org/x/sys/unix.Flock(fd, LOCK_EX); release
// does Flock(LOCK_UN) + Close. Kept stubbed here so this ticket adds no
// golang.org/x/sys dependency.
type OSFlock struct{}

var _ Flock = (*OSFlock)(nil)

func (OSFlock) Acquire(path string) (func() error, error) {
	return nil, ErrNotImplemented
}
```

### `sysdeptest/flock.go`
```go
type FakeFlock struct {
	AcquireErr error    // forces Acquire to fail
	ReleaseErr error    // returned by the release func
	Acquired   []string // paths Acquire was called with, in order
	Released   []string // paths whose release func was invoked, in order
	held       map[string]bool
}

var _ sysdep.Flock = (*FakeFlock)(nil)
```
- `NewFakeFlock` inits `held`.
- `Acquire`: append to `Acquired`; if `AcquireErr` → it; mark held; return a
  release closure that appends to `Released`, clears held, returns `ReleaseErr`.
- `Held(path) bool` helper for assertions.

### Tests
- `flock_test.go`: `OSFlock{}.Acquire(...)` returns
  `errors.Is(err, sysdep.ErrNotImplemented)`.
- `sysdeptest/flock_test.go`: Acquire records path & marks Held; release records
  & clears Held; `AcquireErr` surfaces; `ReleaseErr` surfaces from the closure.

### Success criteria
- Automated: `go build ./...`; `go vet ./...`; `go test -race ./internal/sysdep/...` pass.
- Manual: confirm `go.mod` unchanged (no `golang.org/x/sys`).

---

## Phase 5 — ProcessGroup / signals seam (Start stub, Notify real)

### Files
- `internal/sysdep/processgroup.go` (new)
- `internal/sysdep/processgroup_test.go` (new)
- `internal/sysdep/sysdeptest/processgroup.go` (new)
- `internal/sysdep/sysdeptest/processgroup_test.go` (new)

### `processgroup.go`
```go
package sysdep

import (
	"context"
	"os"
	"os/signal"
)

// ProcessGroup abstracts running a child in its own process group and tearing the
// whole group down deterministically — the basis for Ctrl-C handling, where a
// SIGINT must reach everything the agent spawned, not just the leader. It also
// wraps signal subscription (os/signal.Notify) so the wrapper can catch the
// signals it forwards.
type ProcessGroup interface {
	// Start runs name with args in a NEW process group (Setpgid: true) and returns
	// a handle for signalling and waiting on the whole group.
	Start(ctx context.Context, name string, args ...string) (Process, error)
	// Notify relays the given OS signals into ch, mirroring os/signal.Notify.
	Notify(ch chan<- os.Signal, sigs ...os.Signal)
}

// Process is a handle to a started process group.
type Process interface {
	// Signal forwards sig to the entire process group via kill(-pgid, sig).
	Signal(sig os.Signal) error
	// Wait blocks until the group's leader has exited and returns its exit error.
	Wait() error
	// Pgid returns the process-group id.
	Pgid() int
}

// OSProcessGroup is the production ProcessGroup. Start is deferred to WP-4.3
// (internal/cage): the real impl sets SysProcAttr{Setpgid: true} and returns an
// osProcess whose Signal does syscall.Kill(-pgid, sig). Notify is portable
// stdlib and implemented now.
type OSProcessGroup struct{}

var _ ProcessGroup = (*OSProcessGroup)(nil)

func (OSProcessGroup) Start(ctx context.Context, name string, args ...string) (Process, error) {
	return nil, ErrNotImplemented
}

func (OSProcessGroup) Notify(ch chan<- os.Signal, sigs ...os.Signal) {
	signal.Notify(ch, sigs...)
}
```
Note: no real `osProcess` type yet (Start returns `nil, ErrNotImplemented`), so
no `var _ Process` assertion on a production type — that lands with WP-4.3. The
fake provides the only `Process` implementation for now.

### `sysdeptest/processgroup.go`
```go
type FakeProcessGroup struct {
	StartErr error            // forces Start to fail
	Started  []StartedCommand // records Start calls
	Notified [][]os.Signal    // records each Notify subscription
	Proc     *FakeProcess     // handle Start returns when StartErr is nil
}
type StartedCommand struct {
	Name string
	Args []string
}
type FakeProcess struct {
	PgidVal int
	WaitErr error
	Signals []os.Signal // records forwarded signals, in order
}

var (
	_ sysdep.ProcessGroup = (*FakeProcessGroup)(nil)
	_ sysdep.Process      = (*FakeProcess)(nil)
)
```
- `NewFakeProcessGroup` inits slices.
- `Start`: append to `Started`; if `StartErr` → it; lazily create `Proc`; return it.
- `Notify`: append `sigs` to `Notified` (no real delivery in the fake).
- `FakeProcess.Signal`: append to `Signals`. `Wait`: return `WaitErr`. `Pgid`:
  return `PgidVal`.

### Tests
- `processgroup_test.go`:
  - `OSProcessGroup{}.Start(...)` → `errors.Is(err, sysdep.ErrNotImplemented)`.
  - `Notify` (real) delivers: register `ch` for `syscall.SIGUSR1`, send it to the
    test process (`syscall.Kill(syscall.Getpid(), SIGUSR1)`), assert receipt
    within a 1s `select` timeout; `defer signal.Stop(ch)`. (SIGUSR1 is caught
    because Notify registered a handler, so it does not terminate the test.)
- `sysdeptest/processgroup_test.go`: Start records the command and returns a
  handle; `StartErr` surfaces; `Notify` records the signal set; `FakeProcess`
  records forwarded signals and returns scripted `Pgid`/`WaitErr`.

### Success criteria
- Automated: `go build ./...`; `go vet ./...`; `go test -race ./internal/sysdep/...`;
  `make test` pass.
- Manual: `processgroup.go` doc references WP-4.3; Notify is the only real method.

---

## Phase 6 — Final verification & ticket close

### Steps
1. Run the full automated suite (commands from `.claude/tce/profile.md`):
   - `go build ./...`
   - `go vet ./...`
   - `go test -race ./internal/sysdep/...`
   - `make test`
   - `make lint`
2. Grep guard (ticket verification step 4): confirm each new interface has a
   matching fake — `grep -l 'Clock\|FileSystem\|Keychain\|Flock\|ProcessGroup'
   internal/sysdep/sysdeptest/*.go` lists the fake files.
3. Confirm `go.mod`/`go.sum` are unchanged (no new dependency).
4. Tick the ticket's acceptance criteria and verification boxes; set
   `Status: Done`; add a `Notes & Updates` entry dated 2026-06-05.

### Success criteria
- All five acceptance criteria in the ticket satisfied.
- All five ticket verification steps pass.
- No regression in `cli`/`prereq`/`config`/`state`.

---

## Testing strategy

- **Real impls** (`Clock`, `FileSystem` writes, `ProcessGroup.Notify`):
  white-box smoke tests in `package sysdep` against `t.TempDir()` and real
  signals, mirroring `filesystem_test.go`/`pathresolver_test.go`.
- **Stubbed impls** (`Keychain`, `Flock`, `ProcessGroup.Start`): assert
  `errors.Is(err, sysdep.ErrNotImplemented)`.
- **Fakes**: each fake gets a smoke test in `sysdeptest` (a new pattern this
  ticket introduces, required by the ticket's verification step 3) exercising
  scripted results, error knobs, and call recording.
- **Compile-time assertions**: every production type and every new fake carries a
  `var _ Iface = (*Impl)(nil)` assertion, so signature drift breaks the build.

## Risks & mitigations

- **Fake import of `sysdep`** for the `var _` assertions: one-way dependency, no
  cycle (sysdep never imports sysdeptest). Confirmed by `go build`.
- **`ProcessGroup.Notify` smoke test flakiness**: bounded by a 1s `select`
  timeout and `signal.Stop` cleanup; SIGUSR1 to self is caught (not terminating)
  because Notify has registered a handler.
- **ProcessGroup interface churn** when WP-4.3 lands: accepted per the checkpoint
  decision; the shape covers exactly the design's named operations.

## References

- Research: `thoughts/shared/research/2026-06-05-AC-0009-sysdep-seam-extensions.md`
- Ticket: `thoughts/shared/tickets/AC-0009-sysdep-seam-extensions.md`
- Pattern source: `internal/sysdep/{commander,filesystem,pathresolver}.go`,
  `internal/sysdep/sysdeptest/{fake,filesystem,pathresolver}.go`,
  `internal/sysdep/{filesystem,pathresolver}_test.go`
</content>
