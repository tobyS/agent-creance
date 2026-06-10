---
date: 2026-06-10
ticket: AC-0038
title: "init bootstraps host setup when it hasn't been done — implementation plan"
status: ready
research: thoughts/shared/research/2026-06-10-AC-0038-init-bootstraps-host-setup.md
---

# AC-0038 Plan: `init` bootstraps host setup when it hasn't been done

## Overview

Make `agent-creance init` detect — cheaply, via `setupcheck.Verify` (no sudo) —
whether one-time host setup has run, and when it hasn't, drive the existing
`setup` flow as part of onboarding before writing `.agent-creance.yaml`. Add the
CLI's first interactive input (`App.Stdin`) plus a `sysdep.Terminal` TTY seam, and
a `--no-setup` config-only opt-out. All-or-nothing: a declined prompt or a setup
failure aborts without writing the config.

## Current state

`runInit` (`internal/cli/init.go:44-68`) is pure FS scaffolding and ends by
printing a stale `"Next: run \`agent-creance setup\`…"` pointer. It never checks
host-setup state. `App` (`internal/cli/cli.go:20-57`) has `Stdout`/`Stderr` but no
`Stdin` and no TTY detection. The setup gate (`setupcheck.Verify`,
`internal/setupcheck/setupcheck.go:100`) and the orchestration to reuse
(`runSetup`, `internal/cli/setup.go:39`) already exist and take only seams already
on `App`. AC-0037 (`6bb77cf`) made `setup` idempotent / "always safe to call".

## Desired end state

- `init` on an already-set-up host: `setupcheck.Verify` → `StatusOK` → no setup
  work, no prompt, no sudo; config written; success line points only to `run`.
- `init` on a host missing setup, interactive: explanation + y/n prompt; on yes,
  `runSetup(ctx, app, false, false)` runs (CA bootstrap + skill, with setup's own
  messages), then the config is written; success line points to `run`.
- Decline / setup failure / keychain-locked / non-interactive-missing: abort with
  a distinct, non-zero message; **no config written**.
- `init --no-setup`: gate skipped entirely (no keychain touch); config written;
  success line still points to `setup` then `run`.

### Key decisions (from the question checkpoint)

1. **Config-only opt-out = new `init --no-setup` flag** (not a passthrough of
   `setup`'s `--no-ca-install`/`--no-skill`).
2. **Test split:** keychain-dependent paths → `*App`+fakes unit tests in
   `init_test.go`; testscript (`init.txtar`) covers `--no-setup` + arg/help only.
   (Forced by `OSKeychain` shelling to the absolute path `/usr/bin/security`,
   which a real-`Main` testscript cannot stub — see research.)
3. **Ordering: host-setup gate runs FIRST, then the clobber guard.** Matches the
   ticket's "setup runs before the config is written" framing; accepted trade-off:
   a pre-existing-config refusal can come after a completed (idempotent) setup.

## What we are NOT doing

- No change to `setup`'s internal behavior, flags, or output (AC-0037 territory).
- No change to `run`'s gate, doctor, CA rotation, or the config template /
  generator scanning (golden files unchanged).
- No new business dependency or Go module (`golang.org/x/sys` already present;
  not adding `golang.org/x/term`).

---

## Phase 1 — Seams: `sysdep.Terminal`, `App.Stdin`, and a `confirm` helper

Foundation only; no behavior change to any command yet, so all existing tests stay
green.

### 1a. New `sysdep.Terminal` seam
- Add `internal/sysdep/terminal.go`:
  - `type Terminal interface { IsInteractive() bool }` with a doc comment in the
    house style (why the seam: TTY detection is an OS query; logic packages must
    not call it directly).
  - `type OSTerminal struct{}` with `var _ Terminal = (*OSTerminal)(nil)`.
    `IsInteractive()` checks whether stdin is a terminal via
    `golang.org/x/sys/unix` — e.g. `_, err := unix.IoctlGetTermios(int(os.Stdin.Fd()),
    unix.TIOCGETA); return err == nil`. (Mirror the `unix` usage already in
    `flock.go` / `fstype.go`; **no** `golang.org/x/term` dependency.)
- Add `internal/sysdep/sysdeptest/terminal.go`:
  - `type FakeTerminal struct{ Interactive bool }` implementing
    `IsInteractive() bool { return f.Interactive }`, plus a `NewFakeTerminal()` if
    the package's convention wants one (match the other fakes' shape).
- Optional: a tiny `terminal_test.go` (table or single) only if the OS impl has
  pure logic worth pinning; the ioctl call itself is integration-only — follow the
  existing `keychain_test.go` vs `keychain_integration_test.go` split (don't invoke
  the real fd in a unit test).

### 1b. `App.Stdin` + `App.Terminal`
- `internal/cli/cli.go`: add `Stdin io.Reader` and `Terminal sysdep.Terminal` to
  the `App` struct (with field doc comments in the existing style).
- `Main()` wiring (`cli.go:95-114`): set `Stdin: os.Stdin` and `Terminal:
  sysdep.OSTerminal{}`.

### 1c. `confirm` prompt helper
- Add a small helper in `internal/cli` (e.g. in `init.go` near `runInit`, or a new
  `prompt.go` if cleaner):
  ```go
  // confirm prints prompt to app.Stdout and reads one line from app.Stdin,
  // returning true only for an explicit yes (y/yes, case-insensitive).
  func confirm(app *App, prompt string) (bool, error)
  ```
  Use `bufio.NewReader(app.Stdin).ReadString('\n')`; treat `io.EOF` as "no";
  trim + lowercase; `y`/`yes` → true. Return a wrapped error only on a genuine
  read failure.

### Verification (Phase 1)
- [ ] `go build ./...` passes.
- [ ] `make test` green (no behavior change; existing `App` literals in tests
      compile with the new optional fields left nil).
- [ ] `make lint` green.

---

## Phase 2 — `init` bootstrap behavior

### 2a. `--no-setup` flag
- `newInitCmd` (`init.go:25-38`): add `var noSetup bool`; register
  `cmd.Flags().BoolVar(&noSetup, "no-setup", false, "scaffold the config only; skip
  the one-time host-setup check (CI / config-only use)")`. Update the `RunE` call to
  `runInit(cmd.Context(), app, ".", force, noSetup)`.

### 2b. `runInit` reorder + gate
- Change the signature to `runInit(ctx context.Context, app *App, dir string,
  force, noSetup bool) error` (note: `ctx` is now used — drop the `_`).
- **Host-setup gate runs first** (per decision 3), then the existing clobber
  guard + scaffold:
  ```go
  func runInit(ctx, app, dir, force, noSetup) error {
      if !noSetup {
          if err := ensureHostSetup(ctx, app); err != nil {
              return err // abort before writing the config
          }
      }
      // ... existing clobber guard (Stat dest) ...
      // ... existing scan / render / writeFileAtomic ...
      // success lines (see 2d)
  }
  ```

### 2c. `ensureHostSetup`
- New unexported function in `init.go`:
  ```go
  func ensureHostSetup(ctx context.Context, app *App) error {
      res, err := setupcheck.Verify(app.Keychain, app.FS, app.Paths)
      if err != nil {
          return fmt.Errorf("verify setup: %w", err) // match run.go style
      }
      switch {
      case res.OK():
          return nil // already set up — fast path, no prompt, no sudo
      case res.Status == setupcheck.StatusKeychainLocked:
          fmt.Fprintln(app.Stdout, res.Message()) // unlock instruction
          return fmt.Errorf("setup incomplete")
      }
      // CA not trusted or skill missing.
      if !app.Terminal.IsInteractive() {
          fmt.Fprintln(app.Stdout, res.Message())          // "Run `agent-creance setup` first."
          fmt.Fprintln(app.Stdout, msgNoSetupHint)         // "...or `init --no-setup` to scaffold the config only."
          return fmt.Errorf("setup incomplete")
      }
      // Interactive: explain + prompt, then reuse runSetup.
      fmt.Fprintln(app.Stdout, msgInitNeedsSetup)          // high-level explanation
      ok, err := confirm(app, "Run host setup now?")
      if err != nil {
          return fmt.Errorf("read confirmation: %w", err)
      }
      if !ok {
          return fmt.Errorf("host setup declined; %s not written (re-run `agent-creance init` when ready)", configFile)
      }
      if err := runSetup(ctx, app, false, false); err != nil {
          return err // setup's actionable error verbatim; no config written
      }
      return nil
  }
  ```
- New message consts in `init.go`:
  - `msgInitNeedsSetup` — a 2–3 line explanation that one-time host setup
    (trusting the mitmproxy CA + installing the skill) hasn't run and will run now;
    honest/concrete tone matching `setup.go`'s `msgPrePrompt`.
  - `msgNoSetupHint` — one line naming `agent-creance init --no-setup` as the
    config-only escape hatch.

### 2d. Conditional success line
- Replace the unconditional `"Next: run \`agent-creance setup\`, then
  \`agent-creance run\`."` (`init.go:66`) with:
  - `noSetup` → keep `"Next: run \`agent-creance setup\`, then \`agent-creance
    run\`."` (setup still pending).
  - otherwise → `"Next: run \`agent-creance run\`."` (setup is done/just-run).

### Verification (Phase 2)
- [ ] `go build ./...` passes.
- [ ] `make lint` green.
- [ ] (Unit tests updated in Phase 3; `make test` is expected red between 2 and 3
      because the gate now fires — land 2+3 together in one commit.)

---

## Phase 3 — Tests (unit + testscript) and final verification

### 3a. Extend `init_test.go` fixture
- Extend `newInitFixture` (`init_test.go:89-102`) to wire the setup seams so the
  gate and a driven `runSetup` work, defaulting to **already-set-up** (so the
  existing scaffold/clobber tests pass the gate unchanged):
  - `Keychain`: `sysdeptest.NewFakeKeychain().WithCertificate(setupcheck.CACommonName, "<pem>")`.
  - Seed the skill file at `<HomeDir>/.claude/skills/agent-creance/SKILL.md` in the
    FakeFileSystem (use `sysdeptest.NewFakePathResolver()` with `HomeDir` set, as
    `setup_test.go` does via `runHome`), so `setupcheck.Verify` → `StatusOK`.
  - `ProcessManager`/`PortAllocator`/`TLSProber`/`Sleeper` seeded like
    `newSetupFixture` (`setup_test.go:33-56`) so an actually-driven `runSetup`
    succeeds, plus the CA PEM at the installer's `setupCAPath`.
  - `Terminal`: `&sysdeptest.FakeTerminal{}` (non-interactive by default).
  - `Stdin`: an empty `strings.Reader` by default; helper to set it per test.
  - Expose `kc`, `prober`, `term`, and a way to set stdin on the fixture for the
    bootstrap cases.
- Update existing call sites to the new `runInit(ctx, app, dir, force, noSetup)`
  signature (add the `noSetup` arg, `false` for the gate-active tests).
- Update `TestInitEmptyDir` (`init_test.go:112-125`): the success line is now
  `"Next: run \`agent-creance run\`."` on the (default) already-set-up path —
  change the `Contains` assertion accordingly.

### 3b. New unit-test cases (the real coverage for AC-0038)
Add to `init_test.go` (direct `runInit` / `ensureHostSetup` drives):
- **Already set up → no setup run, no prompt:** default fixture; `runInit(...,
  false, false)`; assert config written, `kc.AddedCerts` empty, `prober.Calls`
  empty (no Bootstrap), stdout has the run-only "Next" line and **no** prompt text.
- **Interactive confirm → setup driven → config written:** empty keychain (CA not
  trusted), `Terminal.Interactive = true`, stdin `"y\n"`; assert `runSetup` ran
  (`kc.AddedCerts` == [setupCAPath], prober called), config written, success line
  points to `run`.
- **Interactive decline → abort, no config:** as above but stdin `"n\n"`; assert
  error contains "declined", config **not** written, no cert added.
- **Setup failure → abort, no config:** empty keychain, interactive, stdin
  `"y\n"`, `prober.Outcome = ProbeUntrusted`; assert error contains "CA
  verification failed" (setup's message), config **not** written.
- **Keychain locked → abort, unlock message, no config:** `kc.Locked = true`;
  assert stdout has the unlock instruction, error non-nil, config not written, no
  prompt shown.
- **Non-interactive + missing → abort with instruction:** empty keychain,
  `Terminal.Interactive = false`; assert stdout has "Run `agent-creance setup`"
  and the `--no-setup` hint, error non-nil, config not written.
- **`--no-setup` → skip gate, config written:** empty keychain (setup missing),
  `runInit(..., false, true)`; assert config written, `kc.AddedCerts`/`prober.Calls`
  empty, **no** `FindCertificate` gate effect on writing, success line points to
  `setup` then `run`.

### 3c. Update `init.txtar`
- `internal/cli/testdata/script/init.txtar`: change the scaffold scenarios
  (empty / pkg / both / mono) to `agent-creance init --no-setup` so they stay
  hermetic (no real-keychain dependency). Keep `exists`/`grep` assertions.
- `--help` block: assert it advertises both `--force` **and** `--no-setup`.
- Keep `! agent-creance init bogus` → `stderr 'unknown command'`.
- On the `--no-setup` path the success line still says `Next: run` (setup pointer),
  so existing `stdout 'Next: run'` assertions hold; add a `stdout 'agent-creance
  setup'` check on one `--no-setup` scenario to pin the pointer.
- Update the header comment to explain why these use `--no-setup` (real `Main`
  wires `OSKeychain` → `/usr/bin/security`, so the gate paths are unit-tested).

### 3d. Final verification
- [ ] `make test` green (race) — all new + updated unit tests and the testscript.
- [ ] `make lint` green (`go vet` + `golangci-lint`).
- [ ] `go build ./...` passes.
- [ ] `make golden` produces **no** diff (template unchanged) — confirm.
- [ ] Manual read-through of `init.txtar` and the new stdout assertions for the
      exact user-facing strings.

---

## Success criteria

### Automated
- [ ] `make test` (race) green.
- [ ] `make lint` green.
- [ ] `go build ./...` clean.
- [ ] `make golden` → no diff.

### Manual / behavioral (maps to ticket Acceptance Criteria)
- [ ] `StatusOK` path: no setup work, no sudo/keychain dialog, config written as
      today (output differs only in the final "Next" line, now `run`-only).
- [ ] Missing + interactive: explanation + prompt; on yes runs full setup then
      writes config.
- [ ] Decline: clear message, no config, non-zero exit.
- [ ] Setup failure: setup's actionable error surfaced, no config, non-zero exit.
- [ ] `StatusKeychainLocked`: unlock instruction, abort, no config.
- [ ] Non-interactive + missing: prints "run `agent-creance setup`" + `--no-setup`
      hint, aborts; `--no-setup` writes config and skips setup.
- [ ] New/changed user-facing strings covered by unit tests; CLI arg/flag/`--no-setup`
      behavior covered by `init.txtar`; golden diff reviewed (none expected).

## Testing strategy

- **Unit (`init_test.go`, `*App`+fakes):** every keychain/setup-dependent path
  (the table in 3b), mirroring `setup_test.go`/`run_test.go`. This is where the
  confirm/decline/already-set-up/locked/non-interactive/setup-failure behavior is
  proven, because the real `cli.Main` wires `OSKeychain` (absolute
  `/usr/bin/security`) and cannot be stubbed hermetically.
- **Testscript (`init.txtar`, real `Main`):** hermetic-only — `--no-setup`
  config-only scaffolding (+ manifest scan assertions) and arg/help validation.
- **Seam tests:** `FakeTerminal` drives interactivity; `OSTerminal`'s ioctl is
  integration-only (don't invoke a real fd in a unit test).

## References
- Research: thoughts/shared/research/2026-06-10-AC-0038-init-bootstraps-host-setup.md
- `internal/cli/init.go:25-68`, `internal/cli/setup.go:39-82`,
  `internal/setupcheck/setupcheck.go:100-125`, `internal/cli/run.go:58-67`,
  `internal/cli/cli.go:20-57,95-114`, `internal/cli/setup_test.go:21-65`,
  `internal/cli/init_test.go:89-102`, `internal/sysdep/keychain.go:128,156`,
  `internal/cli/testdata/script/init.txtar`,
  `internal/sysdep/sysdeptest/{keychain,tlsprober}.go`.
</content>
