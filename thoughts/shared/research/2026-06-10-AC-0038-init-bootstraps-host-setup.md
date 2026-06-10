---
date: 2026-06-10
ticket: AC-0038
title: "init bootstraps host setup when it hasn't been done — research"
status: complete
branch: main
last_commit: 6bb77cf
---

# AC-0038 Research: `init` bootstraps host setup when it hasn't been done

## Research question

How should `agent-creance init` detect (cheaply, no sudo) whether one-time host
setup has run and, when it hasn't, drive `setup` as part of onboarding — given
the project's testability rules and the existing `init` / `setup` / `setupcheck`
code? Resolve the ticket's six "Questions for Research/Planning".

## Summary / answer

Everything `init` needs is already on the `App` composition root; no new
*business* dependency is required. The change is:

1. **Guard cheaply.** `runInit` calls `setupcheck.Verify(app.Keychain, app.FS,
   app.Paths)` first — the same no-sudo gate `run` uses. `StatusOK` →
   short-circuit (today's behavior, no prompt, no sudo).
2. **Bootstrap on the missing path.** On `StatusCANotTrusted` /
   `StatusSkillMissing`, and when stdin is interactive, explain + prompt; on yes
   call the **existing** `runSetup(ctx, app, false, false)` (shared orchestration,
   same messages), then write the config. On `StatusKeychainLocked`, surface the
   unlock instruction and abort. On decline or setup failure, abort **without**
   writing the config.
3. **Two new seams on `App`:** `Stdin io.Reader` (the CLI's first interactive
   input) and a small `sysdep.Terminal` TTY-detection seam — both wired in `Main`,
   both faked for tests.
4. **A `--no-setup` opt-out flag** skips the gate entirely (config-only; for
   CI/scripted use). In a non-interactive invocation with setup missing and no
   `--no-setup`, `init` prints the "run `agent-creance setup`" instruction and
   aborts (matching `run`'s refusal style).

**Critical test-strategy finding:** `OSKeychain` shells out to the **absolute
path** `/usr/bin/security` (`internal/sysdep/keychain.go:128,156`), *not* a
`$PATH` lookup. Therefore a testscript running the real `cli.Main()` **cannot**
stub keychain state via `$PATH` — exactly as `run_missing_prereq.txtar` documents
("the later steps depend on the real login Keychain (cli.Main wires OSKeychain),
so the happy path and the setup/credential refusals are covered by run_test.go's
*App+fakes unit tests instead"). The keychain-dependent paths
(already-set-up / confirm / decline / setup-failure / keychain-locked /
non-interactive-abort) therefore live in **`*App`+fakes unit tests** in
`init_test.go` (mirroring `setup_test.go` / `run_test.go`); the **testscript**
(`init.txtar`) hermetically covers only `--no-setup` (config-only, no keychain
touch) plus the existing arg/help/scaffold assertions. This is a deliberate
deviation from the ticket's AC wording ("hermetic testscript … exercising the
confirm/decline, already-set-up, and non-interactive paths with stubbed tools"),
forced by the absolute-path `security` call — see Open Questions.

## Current state of `init`

`internal/cli/init.go` — `runInit(ctx, app, dir, force)` (`init.go:44-68`) is
today pure filesystem work over `app.FS`:

- Clobber guard: `app.FS.Stat(dest)`; exists + no `--force` → error
  `"%s already exists (use --force to overwrite)"` (`init.go:48-57`).
- `scanGenerators` → `renderConfigTemplate` → `writeFileAtomic` (`init.go:59-63`).
- Success lines (`init.go:65-66`): `"✓ Wrote %s %s\n"` + **`"Next: run
  \`agent-creance setup\`, then \`agent-creance run\`."`** ← this stale pointer
  changes (init now handles setup on the bootstrap/already-set-up path).

Constructor `newInitCmd` (`init.go:25-38`) has one flag, `--force`, and calls
`runInit(cmd.Context(), app, ".", force)`. `configFile` (`.agent-creance.yaml`)
is defined in `run.go:23`.

Unit tests in `init_test.go` drive `runInit(ctx, app, initDir, false)` directly
against the sysdep fakes via `newInitFixture` (`init_test.go:89-102`), which today
wires only `Stdout/Stderr/FS/Paths`. **All existing `runInit` call sites and the
fixture must be updated** when the signature gains parameters and the setup gate
activates (see Implementation Pointers).

## The setup gate: `setupcheck.Verify`

`internal/setupcheck/setupcheck.go:100-125`. Signature:
`Verify(kc sysdep.Keychain, fsys sysdep.FileSystem, paths sysdep.PathResolver)
(Result, error)` — all three args already on `App`. It is the **cheap, no-sudo**
check: `kc.FindCertificate("mitmproxy")` then `fsys.Stat(~/.claude/skills/
agent-creance/SKILL.md)`.

`Status` (`setupcheck.go:44-56`): `StatusOK`, `StatusCANotTrusted`,
`StatusSkillMissing`, `StatusKeychainLocked`. `Result.OK()` and
`Result.Message()` (`setupcheck.go:64-90`) give the bool + deterministic
human-facing strings (each pointing at `agent-creance setup`, except the locked
one which says "Unlock your login keychain and retry").

`run` consumes it (`run.go:60-67`) as the style to match: `Verify(...)`; real
error → `fmt.Errorf("verify setup: %w", err)`; `!res.OK()` → print
`res.Message()` to `app.Stdout` and return a short sentinel error (`"setup
incomplete"`). `SilenceUsage/SilenceErrors` on root → `Main` prints `error:
<msg>` and returns exit 1 (`cli.go:115-120`).

## The orchestration to reuse: `runSetup`

`internal/cli/setup.go:39-82` — `runSetup(ctx, app, noSkill, noCAInstall)`:
builds `setup.NewInstaller(app.FS, app.Keychain, app.ProcessManager,
app.PortAllocator, app.TLSProber, app.Sleeper, app.Paths)` and drives
`inst.Bootstrap(ctx, beforeInstall)` (verify-first CA, the AC-0037 idempotent
flow) + `inst.InstallSkill()`. It owns all the user-facing CA strings
(`msgPrePrompt` `setup.go:101-105`, `keychainNote()` `setup.go:111-116`).

**Reuse decision:** `init`'s bootstrap calls `runSetup(ctx, app, false, false)`
directly. This *is* the "shared helper both commands call" the ticket asks for —
no extracted helper needed beyond `runSetup`; it guarantees `init` and `setup`
can't drift in CA/skill messaging. (AC-0037 already made `setup` idempotent and
"always safe to call", so re-driving it is safe; the gate means `init` only calls
it when CA/skill are actually absent.)

`setup.Installer.Bootstrap` (`internal/setup/setup.go:268-295`) returns
`BootstrapResult{AlreadyTrusted bool}` and fires the `beforeInstall func()` hook
only on the install path. **AC-0037 introduced no interactive prompting / no
`Stdin`** — the hook is a one-way `fmt.Fprintln`, not a confirm gate. So AC-0038's
y/n prompt is genuinely the CLI's first interactive input.

## The two new seams

### `App.Stdin io.Reader`
`App` (`cli.go:20-57`) has `Stdout`/`Stderr io.Writer` but **no reader**. Add
`Stdin io.Reader`, wired to `os.Stdin` in `Main` (`cli.go:95-114`), and to a
`*bytes.Reader`/`strings.Reader` in tests. A small `confirm(app, prompt)` helper
reads one line from `app.Stdin` (via `bufio`) and returns yes/no.

### TTY detection — `sysdep.Terminal` seam
No TTY detection exists anywhere in `internal/`. Per the project rule ("never
call the OS directly from logic packages; new side-effecting deps get a new
`sysdep` interface + a fake"), add a minimal seam:

```go
// internal/sysdep/terminal.go
type Terminal interface { IsInteractive() bool }
type OSTerminal struct{}
func (OSTerminal) IsInteractive() bool { /* stdin fd is a tty */ }
```

**Dependency note:** `golang.org/x/term` is **not** a dependency, but
`golang.org/x/sys v0.45.0` already is (used in `flock.go`, `fstype.go`). Implement
the tty check via `golang.org/x/sys/unix` (e.g. `unix.IoctlGetTermios(fd,
unix.TIOCGETA)` returning nil error) so **no new module** is added. Fake:
`sysdeptest.FakeTerminal{Interactive bool}`.

**Why a seam and not a `Main`-time bool:** testscript always runs the CLI with a
non-TTY pipe stdin, and the keychain-dependent prompt paths are unit-tested
anyway; an injectable `Terminal` lets the `*App`+fakes tests force
interactive/non-interactive deterministically.

## Test strategy (shaped by the absolute-path `security` finding)

- **`init_test.go` (`*App`+fakes, direct `runInit`):** the real coverage for the
  new behavior. Extend the fixture to add `Keychain` (FakeKeychain),
  `ProcessManager`/`PortAllocator`/`TLSProber`/`Sleeper` (so a driven `runSetup`
  succeeds), `Terminal` (FakeTerminal), and `Stdin`. Cases: already-set-up
  short-circuit (cert+skill seeded → `StatusOK` → no setup run, no prompt, config
  written, success line says run not setup); interactive confirm → setup driven →
  config written; interactive decline → abort, no config, non-zero, distinct
  message; setup failure (prober untrusted) → abort, no config, setup's actionable
  error surfaced; keychain locked → abort with unlock instruction, no config;
  non-interactive + missing → "run `agent-creance setup`" + abort, no config;
  `--no-setup` → skip gate, config written even with setup missing.
  - The fakes that express the cases: `FakeKeychain.WithCertificate("mitmproxy",
    pem)` → CA present; empty `FakeKeychain` → `FindCertificate` returns
    `ErrItemNotFound` → `StatusCANotTrusted`; `FakeKeychain{Locked:true}` →
    `StatusKeychainLocked`. `FakeTLSProber.Outcomes =
    {ProbeUntrusted, ProbeTrusted}` scripts the verify-first install path
    (precedent: `setup_test.go:104`, AC-0037 commit `a4925b1`).
- **`init.txtar` (testscript, real `Main`):** keep hermetic. Update the existing
  scaffold scenarios to `agent-creance init --no-setup` (config-only, no keychain
  touch) — preserves the manifest-scan/template assertions and proves the opt-out
  flag. Keep the `--help` (advertises `--force` **and** `--no-setup`) and
  `! agent-creance init bogus` arg-validation assertions. Do **not** assert the
  keychain-dependent paths here (non-hermetic: depends on the dev machine's real
  login keychain).
- **Golden:** the init-template golden files (`testdata/init/*.golden`) are
  unaffected — `renderConfigTemplate` doesn't change. Only the success **stdout**
  line changes, which is asserted via `strings.Contains`, not golden.

## Detailed answers to the ticket's planning questions

1. **Interactive-input seam** → `App.Stdin io.Reader` + `sysdep.Terminal`
   (`IsInteractive() bool`) seam using `golang.org/x/sys/unix` (no new dep). A
   `confirm(app, prompt)` helper reads one line. Resolved.
2. **Shared orchestration** → call existing `runSetup(ctx, app, false, false)`;
   no new helper. Resolved.
3. **Config-only opt-out flag** → new `init --no-setup` (skip the gate, write
   config only). Cleaner than overloading setup's `--no-ca-install`/`--no-skill`
   (those mean *partial* setup, not *no* host setup). **User-facing — confirm at
   checkpoint.**
4. **Exit-code semantics** → all abort paths return a non-nil error → exit 1, with
   distinct messages: decline ("host setup declined; .agent-creance.yaml not
   written…"), setup failure (setup's own actionable error verbatim), keychain
   locked (`setupcheck` unlock message), non-interactive missing (`setupcheck`
   "Run `agent-creance setup` first" + a `--no-setup` hint). Resolved.
5. **Ordering vs AC-0037** → AC-0037 is **Done** (`6bb77cf`); the question is moot.
   Land independently. Resolved.
6. **Test surface** → see Test strategy above. The ticket's "hermetic testscript
   exercising confirm/decline/already-set-up/non-interactive" is **not achievable**
   (absolute-path `security`); those move to `*App`+fakes unit tests, testscript
   covers `--no-setup` + arg/help. **Flag the AC deviation at checkpoint.**

## Open questions for the user

1. **Confirm the `--no-setup` flag** as the config-only escape hatch (vs reusing
   `setup`'s opt-out flags). Recommendation: `--no-setup`.
2. **Confirm the test-strategy deviation** from the AC: keychain-dependent paths
   in `*App`+fakes unit tests (testscript can't hermetically control the real
   `/usr/bin/security`), testscript covers `--no-setup` + arg validation.
3. **Clobber guard vs setup gate ordering:** recommendation — keep the
   "config already exists" refusal *first* (cheap, no prompt/sudo) so a user who
   already ran `init` is refused immediately rather than being prompted for setup
   and then told the config exists. Confirm.

## Key files & references

- `internal/cli/init.go:25-68` — `newInitCmd` / `runInit` (the change site).
- `internal/cli/setup.go:39-82` — `runSetup` (orchestration to reuse).
- `internal/setupcheck/setupcheck.go:100-125` — `Verify` (the gate); status/messages `:44-90`.
- `internal/cli/run.go:58-67` — refusal-message style to match.
- `internal/cli/cli.go:20-57` (App), `:95-114` (Main wiring) — where `Stdin` + `Terminal` are added.
- `internal/sysdep/keychain.go:128,156` — **absolute `/usr/bin/security`** (the non-stubbable finding).
- `internal/cli/setup_test.go:21-65` — `setupFixture` (direct-drive pattern to mirror).
- `internal/cli/init_test.go:89-102` — `newInitFixture` (to extend).
- `internal/cli/testdata/script/init.txtar` — testscript to update; `run_missing_prereq.txtar` — documents the keychain-not-hermetic constraint.
- `internal/sysdep/sysdeptest/{keychain,tlsprober}.go` — fakes (`WithCertificate`, `Outcomes`).
- `setup.Installer.Bootstrap` `internal/setup/setup.go:268-295`; `BootstrapResult` `:251`.
</content>
</invoke>
