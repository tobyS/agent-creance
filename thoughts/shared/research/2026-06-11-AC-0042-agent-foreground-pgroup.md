---
date: 2026-06-11
researcher: Claude (quickfix pipeline)
git_commit: 6b893a4
branch: main
repository: git@github.com:tobyS/agent-creance.git
topic: "AC-0042: run hangs launching the agent — child pgroup never made foreground"
tags: [research, codebase, AC-0042, sysdep, processgroup, cage, signals, tty]
status: complete
last_updated: 2026-06-11
---

# Research: AC-0042 — agent child process group never made foreground

**Ticket:** `thoughts/shared/tickets/AC-0042-agent-foreground-pgroup.md`

## Problem recap (pre-confirmed)

`run` hangs at `Launching agent…`. `OSProcessGroup.Start`
(`internal/sysdep/processgroup.go:57-74`) starts the safehouse/agent child with
`SysProcAttr{Setpgid: true}` — a new process group that is never made the
terminal's **foreground** group (no `tcsetpgrp`/`Foreground`/`SIGTTOU`
handling anywhere; grep-confirmed). A TUI agent immediately calls `tcsetattr`
and reads stdin from a background group → kernel stops it with
`SIGTTOU`/`SIGTTIN` (state `T`) → `cage.Runner` blocks in `Wait()` forever.

## Findings

### Go `Foreground`/`Ctty` semantics on darwin (web research, source-verified)

- `SysProcAttr{Foreground: true, Ctty: fd}`: **Foreground implies Setpgid**.
  Go's darwin fork path (`syscall/exec_libc2.go`, go1.24) performs `setpgid`
  then `ioctl(Ctty, TIOCSPGRP, &pgid)` **in the forked child before execve,
  while the fork-time signal mask still blocks everything** — the comment in
  the Go source explicitly names SIGTTOU as the reason. No parent-side
  `signal.Ignore` is needed to make Foreground itself work (golang/go#37217
  documents the historical hazard; modern Go's ordering is the mitigation).
- **For Foreground, `Ctty` is a parent fd number** (`int(os.Stdin.Fd())`),
  *not* a child-fd index — that index interpretation applies only to `Setctty`
  (rejected in combination with Foreground since Go 1.15; we don't set it).
  No O_RDWR requirement; a read-only fd on the controlling tty is fine
  (macOS `tcsetpgrp(3)`).
- **Non-tty Ctty fails loudly**: the child's ioctl errors (`ENOTTY`,
  "inappropriate ioctl for device") and `cmd.Start()` returns it. So
  Foreground **must be guarded by an isatty check on stdin** — otherwise every
  `go test` / testscript / CI run of the real `OSProcessGroup` breaks.
- `kill(-cmd.Process.Pid, sig)` teardown is unchanged: with `Pgid: 0` the
  child still leads a new group whose pgid == its pid.
- **Behavioral shift to document**: once the child group is foreground, the
  kernel delivers keyboard Ctrl-C/Ctrl-Z **directly to the child's group**;
  the wrapper (now background) no longer receives tty-generated SIGINT. The
  existing forwarding loop (`internal/cage/run.go:71-84`) remains correct for
  signals sent to the wrapper's PID (`kill <pid>`, ctx cancel).

### Parent side after the child exits

- Background **writes** to stderr stop the process only if the terminal's
  `TOSTOP` flag is set (off by default) or proceed if SIGTTOU is ignored.
  Background **reads** always raise SIGTTIN — the wrapper never reads stdin
  post-agent, so only writes matter here.
- Restoring the wrapper as foreground (`tcsetpgrp` back to own pgrp) itself
  raises SIGTTOU from a background group **unless SIGTTOU is ignored** — the
  canonical shell pattern is: `signal.Ignore(SIGTTOU)` → `tcsetpgrp(tty,
  getpgrp())`. Crucial ordering: do this **after** `Wait()` returns, never
  before `Start()` — `SIG_IGN` survives execve and would leak into the agent
  (golang/go#37217 calls out exactly this side effect).
- Restoration is cheap insurance for the wrapper's post-exit stderr writes
  (proxy-teardown warning `internal/cli/run.go:150-154`, progress `Close`,
  the final `error:` line `internal/cli/cli.go:126-129`) and for a crashed
  TUI leaving `TOSTOP` set; the shell reclaims the terminal when the wrapper
  exits regardless.

### x/sys/unix API on darwin

`unix.Tcsetpgrp`/`Tcgetpgrp` **do not exist on darwin**. With the repo's
pinned `golang.org/x/sys v0.45.0` (go.mod:10) the darwin pattern is:
- set: `unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, pgid)`
- own pgrp: `unix.Getpgrp()`
This matches the package's existing typed-wrapper ioctl style
(`unix.IoctlGetTermios` in `terminal.go:48`, `unix.Flock` in `flock.go`).

### Codebase map — what the change touches

- **Single production path**: `cli.Main()` wires `OSProcessGroup{}`
  (`cli.go:117`) → `runRun` step 11 `cage.NewRunner(app.ProcessGroup).Run`
  (`run.go:177`) → `Runner.Run` is the only interface caller of `Start`
  (`cage/run.go:54`).
- **isatty helper already exists in the same package**:
  `isTerminal(f *os.File)` at `internal/sysdep/terminal.go:47-50` — directly
  reusable from `processgroup.go`, no import changes.
- **`internal/verify` battery bypasses the seam entirely** — `runCaged` uses
  plain `exec.CommandContext` + `CombinedOutput()`
  (`verification_integration_test.go:234-252`). Unaffected.
- **Real-`Start` tests all run with non-tty stdin** and will take the guard's
  skip branch: `processgroup_test.go:18` (failure path, unit),
  `processgroup_integration_test.go:41` and `cage_integration_test.go:141`
  (group-teardown, integration tag).
- **FakeProcessGroup records only `{Name, Args, Env}`**
  (`sysdeptest/processgroup.go:34-38`) — no test can observe SysProcAttr;
  nothing breaks in unit tests.
- **Do not add SIGTTOU to `Runner`'s Notify set** — `cage/run_test.go:50`
  asserts the set is exactly `{SIGINT, SIGTERM}`. The ignore belongs inside
  `sysdep` (production-only code), not threaded through the seam.
- The only `run` testscript (`run_missing_prereq.txtar`) fails at the prereq
  gate, never reaching `Start`.

### Docs/comments to keep consistent

- `docs/design.md:433` ("Process group handling"): says Ctrl-C is forwarded by
  the wrapper via `kill(-pgid)`; after this fix the kernel delivers keyboard
  signals directly to the foreground child group and the wrapper's forwarding
  remains as a fallback for signals sent to its PID. Update the paragraph.
- `processgroup.go:25-29` (interface doc) and `:59-63` (the "keep the
  controlling terminal so the agent is interactive" comment — the very
  assumption this bug disproves; rewrite it to describe the foreground
  handover and the non-tty guard).
- `docs/design.md:414` already says "before the agent takes the terminal" —
  the fix makes that literally true.

## Impact analysis

`OSProcessGroup.Start` has exactly one production consumer (the agent run) and
two integration tests, both non-tty (skip branch). The proxy daemon uses the
separate `ProcessManager` (Setsid) — untouched. No unit test observes
SysProcAttr. Risk is concentrated in the tty-interactive path, which is only
verifiable manually (no pty harness in the repo).

## Recommended fix shape

```go
// in OSProcessGroup.Start
cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
foreground := isTerminal(os.Stdin)
if foreground {
    cmd.SysProcAttr.Foreground = true        // implies Setpgid; child does setpgid+TIOCSPGRP pre-exec
    cmd.SysProcAttr.Ctty = int(os.Stdin.Fd()) // parent fd of the controlling tty
}
// record foreground on osProcess

// in osProcess.Wait, after cmd.Wait() returns, only if foreground:
signal.Ignore(syscall.SIGTTOU)                                        // we are background now
_ = unix.IoctlSetPointerInt(int(os.Stdin.Fd()), unix.TIOCSPGRP, unix.Getpgrp()) // best-effort
```

## Code references

- `internal/sysdep/processgroup.go:57-74` — change site (`Start`), `:104` (`Wait`)
- `internal/sysdep/terminal.go:47-50` — reusable `isTerminal` helper
- `internal/cage/run.go:53-85` — Runner loop (unchanged; comment context)
- `internal/cage/run_test.go:50` — exact-match Notify assertion (don't touch the set)
- `internal/cli/run.go:150-154`, `internal/cli/cli.go:126-129` — post-exit stderr writes the restore protects
- `internal/sysdep/processgroup_integration_test.go:41`, `internal/cage/cage_integration_test.go:141` — non-tty skip-branch coverage
- `docs/design.md:433` — process-group/Ctrl-C paragraph to update

## Open questions

None — all resolved by research. Manual tty verification is inherently
required (acceptance criterion 1); no pty test harness exists and adding one
is out of scope for the quickfix.
