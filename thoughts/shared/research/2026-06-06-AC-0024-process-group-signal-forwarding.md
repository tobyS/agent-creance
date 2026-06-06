---
date: 2026-06-06
ticket: AC-0024
title: "Process group & signal forwarding (WP-4.3)"
status: complete
last_commit: 007a4c3627425bc106bfa4f19d9ac6b85dd5d7d0
branch: main
repo: git@github.com:tobyS/agent-creance.git
tags: [research, cage, sysdep, process-group, signals, lifecycle, WP-4.3]
---

# Research: AC-0024 — Process group & signal forwarding (WP-4.3)

## Research Question

How should `internal/cage` start the Safehouse → agent → children process tree in
its own process group and forward `SIGINT`/`SIGTERM` to the whole group, waiting for
the group to exit before the lock-file decrement — and what already exists in the
codebase (the `ProcessGroup` seam, the cage builder, the proxy decrement) to build
on? Specifically: does agent-safehouse create its own process group, which would
break a single-group teardown?

## Summary

The work is a **focused fill-in of an already-designed seam**, plus a thin
signal-forwarding wrapper, plus DI wiring. Everything around it is built and tested:

1. **The seam exists, dormant.** `sysdep.ProcessGroup` / `sysdep.Process`
   (`internal/sysdep/processgroup.go`) were seeded by AC-0009 (WP-1.4) with full
   interfaces, doc comments that spell out the exact intended syscalls, a working
   `Notify` (stdlib `signal.Notify`), a stub `Start` returning `ErrNotImplemented`,
   and complete fakes (`FakeProcessGroup`/`FakeProcess`) in `sysdeptest/`. **No
   production `osProcess` type exists yet** — only the fake implements `Process`.

2. **The cage builder exists, exec-free by design.** AC-0023 (`internal/cage`,
   just landed) builds a pure `Invocation{Path, Args, Env}` and prepares two
   on-disk artifacts (`Builder.Prepare`). Its package doc explicitly states it
   "never execs, forwards signals, or manages lifecycle (that is AC-0024 /
   AC-0025)" (`internal/cage/cage.go:13-14`). Nothing in production execs the
   `Invocation` today — only the integration test does, via raw `exec.CommandContext`.

3. **The decrement exists.** `proxy.Manager.Detach` (`internal/proxy/lifecycle.go:152`)
   is the lock-file refcount decrement AC-0024 must sequence *after* the group wait.
   AC-0024 only *orders* it; it does not own the refcount logic (AC-0020).

4. **The one open question is resolved (empirically).** agent-safehouse 0.10.1 does
   **not** create its own process group/session and does **not** `exec`-replace
   itself. It is a bash wrapper that runs `sandbox-exec -f policy -- /usr/bin/env -i
   <cmd>` as a *foreground subprocess* (no `setsid`, no `setpgid`, no `exec`). So a
   single `Setpgid: true` group rooted at the Safehouse child covers the entire tree,
   and `kill(-pgid, sig)` reaches everything. See "The Safehouse process model" below.

5. **No `run` orchestrator exists yet.** Nothing wires `proxy.Attach` → cage build/
   prepare → `ProcessGroup.Start` → forward → `Wait` → `proxy.Detach`. That
   orchestration is AC-0025's job; AC-0024 owns the start-group / forward / wait
   slice and the production `OSProcessGroup.Start` + `osProcess` implementation.

The net code surface for AC-0024: (a) implement `OSProcessGroup.Start` and a concrete
`osProcess` (`Signal`/`Wait`/`Pgid`) in `internal/sysdep/processgroup.go`; (b) add a
signal-forwarding wrapper (likely a small function/type in `internal/cage` that takes
a `sysdep.ProcessGroup`, an `Invocation`, and forwards `Notify`'d signals to
`Process.Signal` then `Wait`s); (c) wire `ProcessGroup` into `cli.App` + `cli.Main`.

## Detailed Findings

### The ProcessGroup seam (the thing AC-0024 implements)

`internal/sysdep/processgroup.go` — interfaces + stub:

- `ProcessGroup` interface (`processgroup.go:20-28`):
  - `Start(ctx, name, args...) (Process, error)` — "runs name with args in a NEW
    process group (Setpgid: true)" (`:21-24`). **Stubbed**: `OSProcessGroup.Start`
    returns `nil, ErrNotImplemented` (`:52-54`).
  - `Notify(ch, sigs...)` — wraps `signal.Notify` (`:25-27`, real impl `:56-58`).
    Already works and is tested live (`processgroup_test.go:27-47`).
- `Process` interface (`processgroup.go:30-41`):
  - `Signal(sig) error` — "forwards sig to the entire process group via
    kill(-pgid, sig)" (`:32-34`). **No production impl exists.**
  - `Wait() error` — "blocks until the group's leader has exited … The caller waits
    for the whole group before the lock-file decrement, so cleanup ordering is
    deterministic" (`:35-38`). **No production impl.**
  - `Pgid() int` (`:39-40`). **No production impl.**
- `OSProcessGroup struct{}` with compile-time assertion `var _ ProcessGroup` (`:48-50`).
- The doc comment at `:43-47` is a near-spec: *"the real impl sets
  `SysProcAttr{Setpgid: true}` and returns an osProcess whose Signal does
  `syscall.Kill(-pgid, sig)` and whose Wait reaps the group."*

`ErrNotImplemented` lives at `internal/sysdep/errors.go:11` and its doc names WP-4.3
(`internal/cage`) as the place `ProcessGroup.Start` lands.

**Existing test contract to keep green or update:**
`internal/sysdep/processgroup_test.go:16-25` (`TestOSProcessGroupStartNotImplemented`)
currently asserts `Start` returns `ErrNotImplemented`. **This test must be replaced**
when `Start` is implemented (it will start a real process). The `Notify` test
(`:27-47`) stays.

### The fakes (what unit tests will drive)

`internal/sysdep/sysdeptest/processgroup.go`:

- `FakeProcessGroup` (`:14-24`): knobs `StartErr`, recorders `Started []StartedCommand`,
  `Notified [][]os.Signal`, and `Proc *FakeProcess` (lazily created; pre-set to script
  `Pgid`/`WaitErr`).
- `FakeProcess` (`:34-41`): `PgidVal int`, `WaitErr error`, `Signals []os.Signal`
  (records forwarded signals in order).
- `Start` records the command, returns `StartErr` or `Proc` (`:51-60`); `Notify`
  records the signal set without delivering (`:62-64`); `Signal` records (`:66-69`);
  `Wait` returns `WaitErr` (`:71`); `Pgid` returns `PgidVal` (`:73`).

This is exactly what AC-0024's unit test needs: assert that on a forwarded signal the
wrapper calls `Process.Signal` (recorded in `Signals`), and that `Detach`/decrement is
called only after `Wait` returns. The fake `Notify` does *not* deliver, so the wrapper
must be structured so the test can inject a signal into the channel directly (the
wrapper should accept the channel/notifier, not own an un-injectable `signal.Notify`).

### The sibling seam to mirror: ProcessManager

`internal/sysdep/processmanager.go` already has a *real* implementation that shows the
exact syscall idioms AC-0024 mirrors (but for a single PID + Setsid, not a group +
Setpgid):

- `Spawn` uses `exec.CommandContext` + `cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}`
  then `cmd.Start()` + `cmd.Process.Release()` (`:45-60`) — **detached daemon** pattern
  (the proxy). AC-0024 wants the *opposite*: `Setpgid: true` and *retain* the handle to
  `Wait`.
- `Alive` uses `syscall.Kill(pid, 0)`, treating `EPERM` as alive (`:62-72`).
- `Signal` type-asserts `os.Signal` → `syscall.Signal`, calls `syscall.Kill(pid, s)`,
  swallows `ESRCH` (already gone) (`:74-89`).

For AC-0024, `osProcess.Signal` mirrors this but targets `-pgid`:
`syscall.Kill(-p.pgid, s)`, swallowing `ESRCH`. The type-assertion + ESRCH handling
is the established house style; copy it.

### The cage builder it plugs into (AC-0023)

`internal/cage/cage.go`:

- `Build(in Inputs) (Invocation, error)` (`:69-113`) — pure, no I/O. Produces
  `Invocation{Path: "safehouse", Args: [...], Env: [...]}`.
- `Builder` (`:117-120`) holds `sysdep.FileSystem` + `sysdep.PathResolver`;
  `New(fsys, paths)` (`:123`); `Resolve(cfg, layout, port) (Inputs, error)` (`:130`);
  `Prepare(in) error` (`:163-186`) seeds `CLAUDE_CONFIG_DIR/settings.json` and writes
  the proxy-port `.sb` fragment.
- `const Binary = "safehouse"` (`:38`) — "resolved on PATH by the caller that execs
  it; this package only constructs the invocation."

The package imports **no** `os/exec`, `os`, `syscall`, or `os/signal` today
(`cage.go:21-34`). AC-0024 adds the exec/forward layer. Open design choice (for the
plan): does the wrapper live *in* `internal/cage` (the WP-4.3 home named by
`errors.go`), keeping cage as the owner of the full launch, or does
`OSProcessGroup.Start` alone suffice and the forwarding loop lives in the AC-0025 `run`
command? The `errors.go:9` comment and the ticket both point the *seam impl* at
`internal/cage`, but the *forwarding loop* (Notify → Signal → Wait → Detach) is shared
with the `run` orchestration. Recommended split (see Open Questions): `Start`/`osProcess`
in `sysdep`; a reusable forwarding helper in `internal/cage` that AC-0025 calls.

### The decrement it sequences (AC-0020)

`internal/proxy/lifecycle.go`:

- `Manager.Detach(layout, selfPID) error` (`:152-182`): under the flock, removes
  `selfPID` from `Agents`; **on last-out** SIGTERMs the proxy by PID
  (`m.proc.Signal(cur.ProxyPID, syscall.SIGTERM)`, `:168`), purges the session overlay
  (`:172`), clears proxy state (`:177`); non-final exit just drops the PID (`:181`).
- The file header (`:22-23`) confirms the split: lifecycle "does NOT … exec Safehouse /
  forward signals to the agent group (AC-0023/0024) — those are its callers."

The deterministic ordering the design mandates: **`Process.Wait()` returns (whole caged
group reaped) → THEN `Manager.Detach`.** AC-0024 owns the "wait first" half; the actual
`Detach` call site is the `run` command (AC-0025). For AC-0024's *unit* test the
sequencing is demonstrated against the fake: assert `Wait` is observed before the
decrement callback fires.

### The DI root (where ProcessGroup must be wired)

`internal/cli/cli.go`:

- `App` struct (`:20-36`) holds `Commander`, `FS`, `Paths`, `Clock`, `HTTP`,
  `Keychain` (+ `Stdout`/`Stderr`/`Tested`). **No `ProcessGroup` field.**
- `cli.Main()` (`:67-85`) constructs the production seams; **`OSProcessGroup{}` is not
  constructed anywhere in the codebase** (grep confirms only the seam's own files
  reference it).

AC-0024 adds `ProcessGroup sysdep.ProcessGroup` to `App` and `sysdep.OSProcessGroup{}`
to `Main`. (Whether it's added now or with AC-0025's `run` command is a sequencing
choice — adding the field + wiring now keeps AC-0024 self-contained and lets the seam
be exercised, even if no command consumes it until AC-0025.)

### The Safehouse process model (resolves the open question)

The ticket's open question (`AC-0024-…md:47`): *"Interaction between our process group
and Safehouse's own child handling — does Safehouse already create a group?"* The
design doc asserts the *model* (Safehouse `exec`s through to the agent) but never
verified it empirically. **Resolved by inspecting the installed `safehouse` 0.10.1
(`/opt/homebrew/bin/safehouse`, a bash script — the exact tested version):**

- `runtime_launch_command` (`safehouse:7001-7010`) runs:
  `sandbox-exec -f "$policy_path" -- /usr/bin/env -i [env...] "$@"`.
- Its caller `cmd_execute_run` (`:7372-7398`) runs it as a **foreground subprocess**
  (`set +e; runtime_launch_command …; status=$?; set -e; cmd_cleanup_rendered_policy`)
  — **no `exec` prefix**, because the wrapper must clean up the rendered policy temp
  file after the command exits.
- A targeted grep for `setsid|setpgid|disown|^exec |nohup` (`safehouse` whole file)
  finds: **no `setsid`, no `setpgid`, no `disown`**; the only `exec` uses are fd
  redirections (`exec 9>…`); the only `nohup … &` is in `launch_code_detached`
  (`:6842`), used solely by the VS Code special case (`:6906`/`:6921`), **not** the
  normal agent-command path.

So the runtime tree under our wrapper is:

```
our-wrapper            (Setpgid leader; pgid P = Safehouse child PID)
└── bash (safehouse)   our direct child — stays alive to clean up; does NOT exec/setsid
    └── sandbox-exec -f policy --   (execs through; no new group)
        └── /usr/bin/env -i …       (execs through; no new group)
            └── agent (claude / sh / …)
                └── grandchildren (npm, php artisan, …)
```

Every node inherits process group **P**. Therefore `syscall.Kill(-P, sig)` reaches the
whole tree and Ctrl-C teardown works with a **single** `Setpgid: true` group — no
special handling for a Safehouse-created group is needed. (If a future Safehouse
version added `setsid`, the AC-0024 integration test — spawn `sleep 300 &`, SIGINT the
wrapper, assert `pgrep -g <pgid>` empty — would catch the regression.)

Note: because Safehouse does **not** `exec`, our direct child is bash, not Safehouse's
sandbox-exec. `Process.Wait()` therefore waits on the bash wrapper, which exits after
its foreground `sandbox-exec` child does. On a group kill, every descendant gets the
signal directly (they're all in group P), so we don't depend on bash forwarding it.

### Web research: the canonical Go/Darwin pattern (validated for macOS)

From the web-research agent (sources: go.dev `exec_bsd.go`, pkg.go.dev `os/exec` &
`os/signal`, setpgid(2)/POSIX kill, sigmoid.at, DoltHub, golang/go#20285 & #50436):

- Use `SysProcAttr{Setpgid: true, Pgid: 0}` (**not** `Setsid` — we want to stay in the
  session/terminal, just in a distinct group). Go performs `setpgid` **in the forked
  child before `execve`**, so the new pgid is **deterministically `cmd.Process.Pid`** —
  no `syscall.Getpgid` call, and **no fork/setpgid race** to guard. `osProcess` can
  store `pgid = cmd.Process.Pid`.
- Forward with `syscall.Kill(-pgid, sig)`; **ignore `ESRCH`** (group already gone) —
  same as `ProcessManager.Signal`.
- Pattern: `Start` → read `pgid` → `signal.Notify(buffered ch≥1, SIGINT, SIGTERM)`
  (`Notify` is already the seam's job) → on signal, `Kill(-pgid, sig)` → `cmd.Wait()`
  → `signal.Stop`/cleanup. Cleanup strictly *after* `Wait` returns.
- **Double-delivery is avoided precisely by `Setpgid`:** once the child is in its own
  (non-foreground) group, the tty no longer delivers terminal Ctrl-C to it; only our
  wrapper receives the terminal SIGINT and forwards it once. Deterministic, no double
  kill. (Had we left the child in our group, Ctrl-C would hit both directly *and* via
  forwarding.)
- Optional polish (not required by the ticket): `Cmd.Cancel` (send group signal) +
  `Cmd.WaitDelay` (grace then SIGKILL) — the stdlib's built-in escalation, Go 1.20+.
  Consider a grace-then-`SIGKILL(-pgid)` escalation so a hung child can't wedge teardown.

## Code References

- `internal/sysdep/processgroup.go:20-58` — `ProcessGroup`/`Process` interfaces;
  stub `Start`; real `Notify`. **The file AC-0024 implements.**
- `internal/sysdep/processgroup_test.go:16-25` — `Start`-not-implemented test to
  replace; `:27-47` — `Notify` test to keep.
- `internal/sysdep/sysdeptest/processgroup.go:14-73` — `FakeProcessGroup`/`FakeProcess`
  for the unit tests.
- `internal/sysdep/processmanager.go:45-89` — the `Setsid`/`Kill`/`ESRCH` idioms to
  mirror (for a group + `Setpgid`).
- `internal/sysdep/errors.go:5-11` — `ErrNotImplemented`, names WP-4.3.
- `internal/cage/cage.go:13-14,38,69-113,163-186` — the `Invocation` builder + the
  "never execs … (AC-0024/0025)" boundary.
- `internal/cage/cage_integration_test.go:103-124` — `runCaged` (raw exec today); the
  shape AC-0024's integration test extends to spawn a long-lived child and assert
  `pgrep -g` empty.
- `internal/proxy/lifecycle.go:152-182` — `Detach`, the decrement to sequence after
  `Wait`; `:22-23` — the exec/signal boundary comment.
- `internal/cli/cli.go:20-36,67-85` — `App` + `Main`, where `ProcessGroup` is wired.
- `/opt/homebrew/bin/safehouse:7001-7010,7372-7398` — Safehouse launches the command
  as a non-exec, non-setsid foreground subprocess (resolves the open question).
- `docs/design.md:389-412` — "Multi-agent lifecycle" / "Process group handling" (the
  authoritative contract).

## Architecture Insights

- **Seam-first, consumer-later** (cross-cutting C2): AC-0009 deliberately shipped the
  `ProcessGroup` interface + fakes ahead of the consumer, accepting churn. AC-0024 is
  the consumer that supplies the real `Start`. The interface doc comments are effectively
  the spec — implement to them.
- **The "wait-then-decrement" ordering is the whole point.** The deterministic teardown
  the design cares about (`design.md:406`) is purely a *sequencing* contract:
  `Wait()` (group reaped) must precede `Detach()` (refcount drop + maybe proxy
  SIGTERM + overlay purge). AC-0024 guarantees the first half; AC-0025 places the
  `Detach` call.
- **Single group suffices** because Safehouse is a transparent (non-exec but
  non-detaching) wrapper. The cage = "every process descended from the wrapper's
  invocation" (`design.md:54`), and they all share one pgid.
- **`Setpgid`, not `Setsid`** is the correct knob here, in deliberate contrast to
  `ProcessManager.Spawn` (which uses `Setsid` for the *detached* daemon proxy). Same
  file family, opposite intent: detach the proxy daemon vs. group-but-attach the agent.
- **Testability hinge:** the forwarding wrapper must take the signal channel / notifier
  as an injected dependency (via `ProcessGroup.Notify`), not call `signal.Notify`
  inline, so the unit test can drive a signal into the fake's channel and assert the
  `Process.Signal` → `Wait` → decrement order.

## Historical Context (from thoughts/)

- `thoughts/shared/research/2026-06-05-AC-0009-sysdep-seam-extensions.md:161-167` —
  designed this seam for WP-4.3: new pgid (`Setpgid`/`setsid`), forward via
  `kill(-pgid)`, wait-before-decrement; flagged it "the least consumer-determined of
  the five" seams (expect churn when WP-4.3 lands).
- `thoughts/shared/plans/2026-06-05-AC-0009-sysdep-seam-extensions.md:39-41,70-75` —
  shipped `Start` as an `ErrNotImplemented` stub, `Notify` real; "no
  `Setpgid`/`kill(-pgid)` wiring — those land with WP-3.4/4.1/4.3."
- `thoughts/shared/research/2026-06-06-AC-0020-proxy-lifecycle-manager.md:31-33,123-147,
  213-215,247-248` — `OSProcessGroup.Start` deferred to WP-4.3; the `Signal`-targets-
  the-whole-group (`kill(-pgid)`) distinction vs the proxy's PID-targeted kill; the
  lock-file decrement protocol AC-0024 sequences after.
- `thoughts/shared/research/2026-06-06-AC-0023-safehouse-invocation.md:61-63,172` —
  scoped out process-group/signal forwarding (AC-0024) and noted `cli.App` has no
  `ProcessGroup`/`ProcessManager` field yet ("those land with AC-0024/0025").
- `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md:274-279` —
  WP-4.3 definition; `:280-286` WP-4.4 (`run`, the orchestrator); `:355-367`
  critical path `WP-4.2/4.3 → WP-4.4 → M3`.
- `thoughts/shared/tickets/AC-0025-run-command.md` — the consumer; depends on AC-0024;
  order of ops: "… proxy start/attach → cage exec → trapped teardown."

## Open Questions

1. **Where does the forwarding loop live — `internal/cage` or `internal/cli` (the `run`
   command)?** The seam impl (`OSProcessGroup.Start` + `osProcess`) clearly belongs in
   `internal/sysdep`. The Notify→Signal→Wait loop is shared with AC-0025's `run`
   orchestration. *Recommended for the plan:* put a small reusable runner/helper in
   `internal/cage` (the WP-4.3 home named by `errors.go`) that takes a
   `sysdep.ProcessGroup` + an `Invocation` + a "post-wait" hook, so AC-0025 composes it
   with `proxy.Detach`. This keeps AC-0024 self-contained and unit-testable against the
   fake without needing the full `run` command. **Decision needed.**

2. **Add `ProcessGroup` to `App`/`Main` now, or with AC-0025?** Wiring it now makes
   AC-0024 deliver a usable, wired seam (and lets an integration test reach it via a
   minimal harness); deferring keeps the diff smaller but leaves the seam unwired.
   *Recommended:* wire it now (field + `OSProcessGroup{}` in `Main`).

3. **Grace-then-SIGKILL escalation — in scope?** The ticket only requires forwarding
   SIGINT/SIGTERM and waiting. A hung child would wedge teardown indefinitely.
   stdlib `Cmd.WaitDelay` + a `SIGKILL(-pgid)` fallback is cheap insurance. *Suggest*
   a bounded grace period with SIGKILL escalation, but confirm it's wanted (it adds a
   timeout knob the ticket doesn't mention).

4. **Integration-test host constraint.** The cage integration tests already skip when
   the host "cannot apply a nested sandbox-exec policy" (this dev box, being itself
   sandboxed, hits exactly that — verified: `sandbox_apply: Operation not permitted`).
   The AC-0024 teardown integration test (`sleep 300 &` → SIGINT → `pgrep -g` empty)
   must follow the same skip-guard so it doesn't false-fail on a sandboxed host. Not a
   blocker — just a test-design constraint to carry into the plan.
