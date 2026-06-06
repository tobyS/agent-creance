---
date: 2026-06-06
ticket: AC-0024
title: "Process group & signal forwarding (WP-4.3)"
status: ready
research: thoughts/shared/research/2026-06-06-AC-0024-process-group-signal-forwarding.md
branch: main
tags: [plan, cage, sysdep, process-group, signals, lifecycle, WP-4.3]
---

# Implementation Plan: AC-0024 — Process group & signal forwarding (WP-4.3)

## Overview

Fill in the dormant `sysdep.ProcessGroup` seam (seeded by AC-0009) with a real
`OSProcessGroup.Start` + `osProcess` that runs the Safehouse → agent → children tree
in its own process group (`Setpgid: true`) and forwards `SIGINT`/`SIGTERM` to the
whole group via `kill(-pgid, sig)`. Add a small reusable forwarding **`Runner`** in
`internal/cage` that subscribes to the signals, relays them to the group, escalates to
`SIGKILL` after a grace period, and returns only once the group has been reaped — so a
caller (AC-0025's `run`) can perform the lock-file decrement strictly *after* teardown.
Wire `ProcessGroup` into the `App`/`cli.Main` composition root now.

This is scoped exactly to the start-group / forward / wait-before-return slice. It does
**not** add the `run` command, call `proxy.Detach`, or own the refcount (AC-0025 /
AC-0020).

## Current State

- `internal/sysdep/processgroup.go`: `ProcessGroup`/`Process` interfaces fully declared;
  `OSProcessGroup.Start` returns `ErrNotImplemented` (`:52-54`); `Notify` is real
  (`:56-58`). **No production `osProcess` type exists.**
- `internal/sysdep/processgroup_test.go:16-25`: `TestOSProcessGroupStartNotImplemented`
  asserts the stub; `:27-47`: live `Notify` test.
- `internal/sysdep/sysdeptest/processgroup.go`: `FakeProcessGroup`/`FakeProcess` record
  `Started`, `Notified`, forwarded `Signals`; `FakeProcess.Wait` returns immediately;
  `Notify` records the signal set but **not the channel** and delivers nothing.
- `internal/cage/cage.go`: builds `Invocation{Path, Args, Env}`; package doc says it
  "never execs, forwards signals, or manages lifecycle (AC-0024/AC-0025)"; imports no
  `os`/`exec`/`syscall`/`os/signal`.
- `internal/cli/cli.go:20-36,67-85`: `App` has no `ProcessGroup` field; `Main` does not
  construct `OSProcessGroup{}`.
- `internal/sysdep/processmanager.go:45-89`: the established `SysProcAttr` /
  `syscall.Kill` / `ESRCH`-swallow idioms to mirror (for `Setsid`/single-PID; we adapt
  to `Setpgid`/`-pgid`).

Empirically resolved (research): safehouse 0.10.1 runs `sandbox-exec`/`env` as a
non-exec, non-setsid foreground subprocess, so one `Setpgid` group covers the whole
tree and `kill(-pgid, sig)` reaches everything.

## Desired End State

- `OSProcessGroup.Start` starts a child in a new process group with terminal stdio and
  the merged environment, returning an `*osProcess` whose `Signal` does
  `kill(-pgid, sig)` (ESRCH-tolerant), `Wait` reaps via `cmd.Wait()`, and `Pgid`
  returns the group id (= child PID).
- `cage.Runner` starts a prepared `Invocation` via an injected `sysdep.ProcessGroup`,
  forwards `SIGINT`/`SIGTERM` to the group, escalates to `SIGKILL(-pgid)` after a
  bounded grace, and returns the child's exit error only after the group has exited.
- `App.ProcessGroup` is populated with `sysdep.OSProcessGroup{}` in `cli.Main`.
- Unit tests (fake) prove: the invocation (name/args/env) is started; a forwarded
  signal is relayed to the group; `Run` blocks until the group exits (so any later
  decrement is necessarily after teardown); a `Start` failure surfaces as an error;
  grace-elapsed escalates to `SIGKILL`.
- Integration tests prove real teardown of a spawned child tree (a plain-`sh` group
  test that runs on any macOS host, plus a through-safehouse composition test that
  skips on hosts that can't nest sandbox-exec).
- `go build ./...`, `make test` (race), `make lint` clean; `make test-integration`
  passes (or skips with the nested-sandbox guard) on a capable macOS host.

## What We're NOT Doing

- No `run` command (AC-0025) and no `proxy.Detach` call site here — `Runner` only
  guarantees wait-before-return; the caller sequences the decrement.
- No change to the refcount/lock logic (AC-0020).
- No `Setctty`/foreground-tty job-control handling (the caged agent does not own the
  controlling terminal's foreground group; `Setpgid` without `Setsid` is sufficient).
- No `Stop` method added to the `ProcessGroup` seam (the Runner uses `signal.Stop`
  directly; safe no-op against the fake).

## Implementation Approach

`Setpgid: true` (not `Setsid`) keeps the child in our session but in a distinct group;
Go performs the `setpgid` in the forked child before `execve`, so `pgid ==
cmd.Process.Pid` deterministically (no `Getpgid`, no fork/setpgid race). The forwarding
loop lives in `cage.Runner` and takes the channel from `ProcessGroup.Notify` so it is
unit-testable against the fake. To make forwarding observable in tests, the fake gains
a captured-channel recorder and a `Wait` gate so a test can inject a signal, assert it
was relayed, then release `Wait`.

---

## Phase 1 — Implement the `OSProcessGroup.Start` seam + extend the fake

### Changes

**`internal/sysdep/processgroup.go`**

1. Extend `ProcessGroup.Start` to carry the child environment (the cage `Invocation`
   needs `Env`; the seam doc/AC-0009 plan explicitly sanctioned churn here):
   ```go
   // Start runs name with args in a NEW process group (Setpgid: true), with the
   // given extra environment appended to the parent's, wired to the controlling
   // terminal's stdio, and returns a handle for signalling and waiting on the whole
   // group. A non-nil error means the child could not be started; Process is nil.
   Start(ctx context.Context, env []string, name string, args ...string) (Process, error)
   ```
2. Implement it on `OSProcessGroup` (mirror `OSProcessManager.Spawn`'s idioms but with
   `Setpgid` and retained handle):
   ```go
   func (OSProcessGroup) Start(ctx context.Context, env []string, name string, args ...string) (Process, error) {
       cmd := exec.CommandContext(ctx, name, args...)
       cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // Pgid:0 ⇒ new group == child PID
       cmd.Env = append(os.Environ(), env...)
       cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
       // On ctx cancellation, tear down the whole group, not just the leader.
       cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGINT) }
       if err := cmd.Start(); err != nil {
           return nil, fmt.Errorf("sysdep: start %q: %w", name, err)
       }
       return &osProcess{cmd: cmd, pgid: cmd.Process.Pid}, nil
   }
   ```
3. Add the production `osProcess`:
   ```go
   type osProcess struct {
       cmd  *exec.Cmd
       pgid int
   }
   var _ Process = (*osProcess)(nil)
   func (p *osProcess) Signal(sig os.Signal) error {
       s, ok := sig.(syscall.Signal)
       if !ok { return fmt.Errorf("sysdep: signal pgid %d: unsupported signal %v", p.pgid, sig) }
       if err := syscall.Kill(-p.pgid, s); err != nil {
           if errors.Is(err, syscall.ESRCH) { return nil } // group already gone
           return fmt.Errorf("sysdep: signal pgid %d: %w", p.pgid, err)
       }
       return nil
   }
   func (p *osProcess) Wait() error { return p.cmd.Wait() }
   func (p *osProcess) Pgid() int  { return p.pgid }
   ```
4. Update imports (`errors`, `fmt`, `os`, `os/exec`, `syscall` join `context`,
   `os/signal`). Drop the "Start is deferred to WP-4.3" paragraph from the
   `OSProcessGroup` doc; keep the `Notify` note.

**`internal/sysdep/sysdeptest/processgroup.go`** (add test knobs)

5. `StartedCommand` gains `Env []string`; `Start` records it and matches the new
   signature `Start(ctx, env, name, args...)`.
6. `FakeProcessGroup` gains `NotifyChans []chan<- os.Signal`; `Notify` appends the
   channel (so a test can inject a signal). Keep `Notified` for the signal-set
   assertions.
7. `FakeProcess` gains:
   - `WaitGate chan struct{}` — if non-nil, `Wait` blocks until it is closed (lets a
     test assert forwarding *before* the group exits).
   - a `sync.Mutex` guarding `Signals`, plus a `SignalsSnapshot() []os.Signal`
     accessor — `Signal` is called from the Runner's loop goroutine while the test
     reads concurrently, so `-race` requires synchronization.
   - `Wait` becomes: `if p.WaitGate != nil { <-p.WaitGate }; return p.WaitErr`.

**`internal/sysdep/processgroup_test.go`**

8. Replace `TestOSProcessGroupStartNotImplemented` with a fast unit test that a failed
   start surfaces an error (no successful spawn): `Start(ctx, nil, "/nonexistent/xyz")`
   returns a non-nil error and a nil `Process`. Keep `TestOSProcessGroupNotifyDeliversSignal`.

**`internal/sysdep/sysdeptest/processgroup_test.go`**

9. Update existing fake tests to the new `Start` signature (pass `env`); add coverage
   for `NotifyChans` capture and the `WaitGate` blocking behavior if not already
   implied.

### Success Criteria

#### Automated
- [ ] `go build ./...` compiles.
- [ ] `go test -race ./internal/sysdep/...` green (fake + seam unit tests).
- [ ] `make lint` clean.

#### Manual
- [ ] `OSProcessGroup.Start` sets `Setpgid: true`, merges env over `os.Environ()`, wires
      terminal stdio; `osProcess.Signal` targets `-pgid` and swallows `ESRCH`.
- [ ] No remaining reference to `ErrNotImplemented` from `processgroup.go` (the sentinel
      stays defined in `errors.go` for `Keychain`/other future use — verify it is still
      referenced elsewhere or update its doc comment to drop the ProcessGroup mention).

---

## Phase 2 — The `cage.Runner` forwarding loop (Notify → forward → grace/SIGKILL → wait)

### Changes

**`internal/cage/run.go`** (new file)

```go
// Runner executes a prepared Invocation in its own process group and forwards
// SIGINT/SIGTERM to the whole group, escalating to SIGKILL after a grace period.
// Run returns only once the group has exited, so a caller (the run command) can do
// the lock-file decrement strictly after teardown — the deterministic ordering the
// design requires. This package builds the invocation and now drives it; it still
// does not touch the proxy lock (that is the caller's job, AC-0025/AC-0020).
type Runner struct {
    pg    sysdep.ProcessGroup
    grace time.Duration
}

const defaultGrace = 5 * time.Second

func NewRunner(pg sysdep.ProcessGroup) *Runner { return &Runner{pg: pg, grace: defaultGrace} }

// Run starts inv in a new process group, forwards SIGINT/SIGTERM to the group, and
// returns the child's exit error once the whole group has been reaped.
func (r *Runner) Run(ctx context.Context, inv Invocation) error {
    proc, err := r.pg.Start(ctx, inv.Env, inv.Path, inv.Args...)
    if err != nil {
        return fmt.Errorf("cage: start %s: %w", inv.Path, err)
    }

    sigCh := make(chan os.Signal, 1)
    r.pg.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
    defer signal.Stop(sigCh) // safe no-op against the fake

    waitCh := make(chan error, 1)
    go func() { waitCh <- proc.Wait() }()

    var killTimer <-chan time.Time
    for {
        select {
        case sig := <-sigCh:
            _ = proc.Signal(sig) // best effort; ESRCH already tolerated
            if killTimer == nil { // start the grace clock on the first signal
                killTimer = time.After(r.grace)
            }
        case <-killTimer:
            _ = proc.Signal(syscall.SIGKILL) // escalate: the group ignored the term
        case werr := <-waitCh:
            return werr
        }
    }
}
```

Notes:
- `signal.Stop(sigCh)` keeps handler registration tidy; it is a stdlib call on the
  channel the seam registered, and a harmless no-op when the fake's `Notify` never
  touched stdlib.
- The grace timer is armed once (on the first received signal) and fires at most one
  `SIGKILL`; after that we keep selecting until `Wait` returns.
- `Run` is synchronous: the run command (AC-0025) calls `proxy.Detach` *after* `Run`
  returns — the wait-before-decrement contract falls out of the synchronous return.

### Tests — `internal/cage/run_test.go`

Use `sysdeptest.NewFakeProcessGroup`:

1. **Starts the invocation**: `Run` with a gated `Wait`; assert `fake.Started[0]` has
   `Name == inv.Path`, `Args == inv.Args`, `Env == inv.Env`; release gate; assert nil
   error.
2. **Forwards a signal to the group**: pre-set `fake.Proc = &FakeProcess{PgidVal: 4242,
   WaitGate: make(chan struct{})}`; run `Run` in a goroutine; `require.Eventually`
   until `len(fake.NotifyChans) > 0`; send `fake.NotifyChans[0] <- syscall.SIGINT`;
   `require.Eventually` until `fake.Proc.SignalsSnapshot()` contains `SIGINT`; close
   the gate; assert `Run` returns. (Proves Notify→Signal relay.)
3. **Wait-before-return ordering**: assert `Run` is still blocked while the gate is
   open (e.g. select on a `done` channel with a short timeout shows not-yet-returned),
   then returns after the gate closes — demonstrating any later decrement is after
   teardown.
4. **Start failure**: `fake.StartErr = errors.New("boom")`; `Run` returns a wrapped
   error and never calls `Notify`/`Wait`.
5. **Grace escalation**: construct a `Runner` with a tiny grace (inject via an
   unexported field/option or a test helper `newRunnerWithGrace`), gated `Wait`; inject
   a `SIGTERM`; `require.Eventually` until `SignalsSnapshot()` contains `SIGKILL`;
   close gate; assert return. (Proves the grace→SIGKILL path.)
   - Add an unexported `withGrace(d)` option or a test-only constructor so the grace is
     injectable without exporting it.

### Success Criteria

#### Automated
- [ ] `go test -race ./internal/cage/...` green.
- [ ] `go build ./...`, `make lint` clean.

#### Manual
- [ ] `Run` forwards exactly `SIGINT`/`SIGTERM`, escalates to `SIGKILL` after grace, and
      returns the child's wait error only after the group exits.
- [ ] `cage` package still does not import `internal/proxy` (no decrement coupling).

---

## Phase 3 — Wire `ProcessGroup` into the composition root

### Changes

**`internal/cli/cli.go`**

1. Add `ProcessGroup sysdep.ProcessGroup` to `App` (with a doc comment noting the
   `run` command, AC-0025, consumes it to start + tear down the caged agent group).
2. In `cli.Main`, construct `ProcessGroup: sysdep.OSProcessGroup{}`.

No command consumes it yet; this delivers a fully wired seam ready for AC-0025.

### Tests

3. If any test constructs `App` literally and would need the field, leave it zero
   (`nil`) — no current command path dereferences it. Confirm `internal/cli` tests and
   the testscript harness still pass (the field is additive). If there is a shared
   `App`-builder test helper, add `ProcessGroup: sysdeptest.NewFakeProcessGroup()` there
   for forward-compatibility.

### Success Criteria

#### Automated
- [ ] `go build ./...` compiles; `go test -race ./internal/cli/...` green.
- [ ] `make lint` clean.

#### Manual
- [ ] `App` exposes `ProcessGroup`; `cli.Main` injects `sysdep.OSProcessGroup{}`.

---

## Phase 4 — Integration tests (real teardown) + final verification

### Changes

**`internal/sysdep/processgroup_integration_test.go`** (new, `//go:build integration`)

Proves the syscall layer tears down a child tree on any macOS host (no safehouse, so it
actually runs on this dev box):
1. `Start(ctx, nil, "/bin/sh", "-c", "sleep 300 & echo started; wait")` (or write a
   marker file). Skip on `runtime.GOOS != "darwin"`.
2. `pgid := proc.Pgid()`; sanity-assert `pgrep -g <pgid>` is **non-empty** within a
   short timeout (the group is alive).
3. `proc.Signal(syscall.SIGTERM)` (relays `kill(-pgid, SIGTERM)`); `proc.Wait()`.
4. `require.Eventually` that `pgrep -g <pgid>` is **empty** (exit status 1) within a
   few seconds — the backgrounded `sleep` (same group, non-interactive sh) is gone.

**`internal/cage/cage_integration_test.go`** (extend, `//go:build integration`)

Proves the through-safehouse composition. Reuse `requireSafehouse`, `setupLayout`, and
the nested-sandbox skip guard:
5. Build/prepare an `Invocation` whose agent command spawns a long-lived child
   (`sh -c 'sleep 300 & echo ok; wait'`). Start it via `sysdep.OSProcessGroup{}.Start`
   (or a `cage.Runner` driven by a directly-injected signal); capture `Pgid`.
6. Forward `SIGTERM` to the group; `Wait`.
7. `require.Eventually` that `pgrep -g <pgid>` is empty — confirming the bash wrapper +
   sandbox-exec + env + agent + `sleep` all died (validates the research's single-group
   finding end-to-end). Skip with the existing `sandbox_apply: Operation not permitted`
   guard so a sandboxed host neither false-passes nor false-fails.

### Final Verification

- [ ] `go build ./...` — compiles.
- [ ] `make test` — green (race; full unit suite).
- [ ] `make lint` — clean.
- [ ] `make test-integration` — on a capable macOS host the sysdep teardown test passes
      and the cage composition test passes (or skips on a nested-sandbox host); on this
      dev box the cage one skips and the sysdep one runs.
- [ ] Manual: from a real terminal, a `cage.Runner`-driven caged command that spawns a
      `sleep 300 &` is fully gone after Ctrl-C (`pgrep -g <pgid>` empty) — the headline
      acceptance behavior. (Optional; covered by integration tests where the host allows.)

---

## Acceptance-Criteria Mapping (ticket)

- *Child started in a new process group* → Phase 1 (`Setpgid: true`), Phase 4 (group
  is live before signal).
- *SIGINT/SIGTERM forwarded to the entire group (`kill(-pgid, sig)`)* → Phase 1
  (`osProcess.Signal`), Phase 2 (Runner forwarding), unit test #2, integration #3–7.
- *Wait for the whole group before the lock-file decrement* → Phase 2 (synchronous
  `Run` returns only after `Wait`), unit test #3 (wait-before-return ordering). The
  actual `Detach` call is AC-0025; this guarantees the ordering it relies on.

## Testing Strategy

- **Pure/loop logic** (`Runner`) → fake-driven table/scenario tests with `require.Eventually`
  for the concurrent forwarding assertions; `-race` clean via the `FakeProcess` mutex.
- **Syscall layer** (`OSProcessGroup`/`osProcess`) → `integration`-tagged real-process
  tests (no external tool needed for the sysdep one; safehouse for the cage one).
- **No golden files** — nothing new is rendered to disk.
- External tools (`safehouse`) only under `//go:build integration`, with the established
  nested-sandbox skip guard.

## References

- Research: `thoughts/shared/research/2026-06-06-AC-0024-process-group-signal-forwarding.md`
- Ticket: `thoughts/shared/tickets/AC-0024-process-group-signal-forwarding.md`
- Seam: `internal/sysdep/processgroup.go`; fake `internal/sysdep/sysdeptest/processgroup.go`
- Mirror idioms: `internal/sysdep/processmanager.go:45-89`
- Builder: `internal/cage/cage.go`; integration patterns `internal/cage/cage_integration_test.go`
- Decrement (sequenced by the caller): `internal/proxy/lifecycle.go:152-182`
- DI root: `internal/cli/cli.go:20-36,67-85`
- Design contract: `docs/design.md:389-412`
