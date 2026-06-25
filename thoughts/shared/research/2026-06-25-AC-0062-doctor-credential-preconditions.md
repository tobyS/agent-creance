---
date: 2026-06-25
ticket: AC-0062
topic: "doctor surfaces host credential preconditions (locked keychain, file-fallback, missing)"
status: complete
commit: 16b5fce97e2956145af0f6989719186c931c7f71
branch: main
---

# Research: AC-0062 — doctor surfaces host credential preconditions

## Research question

The 2026-06-23 design-conformance review (Gap 2) found that `agent-creance doctor`
reports **no** credential precondition, even though the design (`docs/design.md:20`,
`:530`) and `cred`'s own package doc assign that job to both `run` and `doctor`. Only
`run` calls `cred.Detect`. The ticket asks doctor to grow a credential finding that
surfaces every non-OK `cred.Detect` outcome (locked keychain, file fallback, missing)
using the cred package's existing messages, with a healthy state when the Keychain item
is present.

Goal: pin the exact current semantics of `cred.Detect`, the doctor section/golden
pattern to mirror, the seams to wire, and — critically — the **testscript hermeticity
consequence** of having doctor query the real host keychain.

## Summary / headline findings

1. **The detection logic already exists and is side-effect-free.** `cred.Detect` is a
   pure read through three `sysdep` seams; `run` already calls it and sources its refusal
   text from `cred.Result.Message()`. Mirroring it in doctor is a small, well-trodden
   change.

2. **Doctor has an exact template to copy: `checkCA`.** Both call a `Detect`/`Verify`
   helper returning a value with `OK()`/`Message()`, map a non-nil error to a `StatusWarn`
   "could not check" finding, and otherwise produce an OK/Problem finding. The section
   data model (`{ State Status; Detail string }`, like `CASection`), the render
   (`line` + a glyph helper), the golden fixtures (`goldenCases()`), and the
   verdict (`Actionable()`) are all established patterns.

3. **Two seams must be threaded onto `doctor.Checker`.** `cred.Detect` needs
   `sysdep.Keychain`, `sysdep.FileSystem`, `sysdep.PathResolver`. `Checker` has only
   `Paths` today. `Keychain` and `FS` already exist on `App` (and already flow into
   doctor's `Installer`), so `cli/doctor.go` only needs two more field assignments.

4. **The one real decision — and it is load-bearing for hermeticity:** doctor's
   testscripts run the **real binary**, which wires `OSKeychain`. `OSKeychain` shells out
   to `/usr/bin/security` by **absolute path**, so it runs even under a restricted
   testscript PATH. Adding a credential check makes all four doctor testscripts query the
   real host keychain, and **if a missing/locked credential is an actionable `StatusProblem`,
   doctor's exit code becomes host-dependent** — breaking `doctor_healthy.txtar` (which
   expects exit 0) on any machine without Claude credentials (CI, a fresh checkout). The
   ticket explicitly leaves "`StatusMissing` → Problem or Warn?" open; that choice is what
   keeps the testscripts hermetic. **This needs a user decision.**

## Detailed findings

### 1. `cred.Detect` — the function to call (`internal/cred/cred.go`)

Signature (`cred.go:103`):

```go
func Detect(kc sysdep.Keychain, fsys sysdep.FileSystem, paths sysdep.PathResolver) (Result, error)
```

- Reads `paths.Getenv("USER")` as the Keychain item account, then
  `kc.FindGenericPassword("Claude Code-credentials", account)`.
- `err == nil` → `StatusOK`; `ErrKeychainLocked` → `StatusLocked`; `ErrItemNotFound` →
  delegate to a file-fallback stat of `~/.claude/.credentials.json` (present →
  `StatusFileFallback`, absent → `StatusMissing`); any other keychain error → returns a
  wrapped non-nil `error` (genuinely-unexpected failure).
- `Result` is `{ Status Status }` with `OK() bool` (= `StatusOK`) and `Message() string`
  returning deterministic, golden-pinnable strings: `""` for OK, else the locked /
  file-fallback / missing message constants (`cred.go:87-96`). The `default` case of
  `Message()` also returns `""`.

The four statuses (`cred.go:42-59`): `StatusOK`, `StatusLocked`, `StatusFileFallback`,
`StatusMissing`. The `error` return is reserved for unexpected failures only — the
package doc and `Detect`'s doc comment state this explicitly.

Seams used: `sysdep.Keychain.FindGenericPassword`, `sysdep.FileSystem.Stat`,
`sysdep.PathResolver.Getenv`/`UserHomeDir`.

### 2. The `run` precedent to mirror (`internal/cli/run.go:76-85`)

```go
credRes, err := cred.Detect(app.Keychain, app.FS, app.Paths)
if err != nil {
    return fmt.Errorf("detect credential: %w", err)
}
if !credRes.OK() {
    fmt.Fprintln(app.Stdout, credRes.Message())
    return fmt.Errorf("credential unavailable")
}
```

- Passes the three `App` seams `Keychain` / `FS` / `Paths` (cli.go:38-45; wired to
  `OSKeychain{}` / `OSFileSystem{}` / `OSPathResolver{}` in `cli.Main`, cli.go:147-151).
- Sources its message from `cred.Result.Message()` — does **not** re-word it. doctor must
  do the same so the two paths can't drift (an explicit AC).
- Note ordering in `run`: credential detection is **step 3**, after the prereq gate (step
  1) and setup check (step 2). `run_missing_prereq.txtar` bails at step 1, so `run` never
  reaches `cred.Detect` in any testscript — by design (see §6).

### 3. The doctor structure to extend (`internal/doctor/`)

**`Checker` struct** (`doctor.go:22-32`) — has `Paths sysdep.PathResolver` but **no**
`Keychain` and **no** `FS` field. Both must be added.

**`Run`** (`doctor.go:39-48`) assembles the report by calling one `checkX` method per
section into a `Report` field; no method returns an error to `Run` ("status-as-data",
doctor.go:34-38 / report.go:1-6). Add `r.Cred = c.checkCred()`.

**`checkCA` is the template** (`doctor.go:50-69`):

```go
func (c *Checker) checkCA(ctx context.Context) CASection {
    gen, err := c.Installer.CAGenerated()
    if err != nil {
        return CASection{State: StatusWarn, Detail: "could not check CA: " + err.Error()}
    }
    ...
    res, err := c.Installer.Verify(ctx)
    if err != nil {
        return CASection{State: StatusWarn, Detail: "could not verify (mitmproxy unavailable)"}
    }
    if !res.OK() {
        return CASection{State: StatusProblem, Detail: res.Message()}
    }
    return CASection{State: StatusOK, Detail: "trusted"}
}
```

A `checkCred` mirrors this: `cred.Detect(c.Keychain, c.FS, c.Paths)`; non-nil error →
`StatusWarn` "could not check credential: …"; otherwise map the `Result` to a finding
carrying `res.Message()`.

**Section data model** (`report.go`): each section is its own struct; the
`{ State Status; Detail string }` shape of `CASection` (report.go:51-55) is the exact
template for a `CredSection`. `Report` (report.go:40-49) gains a `Cred CredSection`
field.

**Severity vocabulary** (`report.go:27-38`): `StatusOK`, `StatusWarn` (non-fatal, exit
0), `StatusProblem` (actionable, non-zero exit), `StatusSkipped` (couldn't run, non-fatal).

**Verdict** (`report.go:86-101`): `Actionable()` returns labels for problems that force a
non-zero exit; only `StatusProblem`-level findings contribute (`StatusWarn`/`Skipped` are
deliberately excluded — see the "warnings only are not actionable" test). A credential
clause would be `if r.Cred.State == StatusProblem { probs = append(probs, "...") }` —
generic on `State == Problem`, so whichever statuses we map to Problem fold in
automatically. Consumed in `cli/doctor.go:61-64` (non-empty → error → non-zero exit).

**Render** (`report.go:103-127`) defines section order literally; insert a
`sty.Header("Credential:")` block + a `line(&b, credGlyph(r.Cred.State, sty), r.Cred.Detail)`
call (the `caGlyph` helper at report.go:131-142 maps OK→✓, Problem→✗, else→⚠ and can be
reused or copied). Natural placement: right after `CA trust:` (both are setup/credential
preconditions), before `Proxy`.

**Wiring** (`cli/doctor.go:40-53`): add `Keychain: app.Keychain, FS: app.FS` to the
`&doctor.Checker{…}` literal. `app.Keychain`/`app.FS` are already present (already passed
to `setup.NewInstaller` here), so no `App` change is needed.

### 4. Unit-test harness (`internal/doctor/doctor_test.go`)

`newCheckerHarness()` (doctor_test.go:60-93) already constructs a
`sysdeptest.NewFakeKeychain()` (`kc`) and a `FakeFileSystem` (`fsys`) — currently only fed
to `setup.NewInstaller`. After adding the `Checker.Keychain`/`Checker.FS` fields, wire
`kc`/`fsys` into the `&Checker{…}` literal and the harness can drive each credential
status deterministically (empty fake keychain → `StatusMissing`; seed an item →
`StatusOK`; set the fake's locked/error behavior → `StatusLocked`/Warn). This is the same
shape as `TestRun_*` cases. **This is where each status is proven**, exactly as the
`run` happy/refusal paths are covered in `run_test.go` rather than testscript.

(Confirm the `FakeKeychain` API for "locked" and "item present" before writing — it lives
in `internal/sysdep/sysdeptest/`.)

### 5. Golden tests (`internal/doctor/report_test.go` + `testdata/`)

`goldenCases()` (report_test.go:31-82) returns a `map[string]Report` of four fully-
populated `Report` literals (`healthy`, `problems`, `fixed`, `stranded`), each rendered in
plain and `_color` modes by `TestRender` (report_test.go:84-103) against
`testdata/render_<name>{,_color}.golden` (8 files). Adding a `Cred` field means:
populate `Cred` in each of the four fixtures (pick statuses that exercise OK + a Problem +
a Warn across the set), then run `make golden` (= `go test ./... -update`) and review the
diff. `TestActionable` (report_test.go:128-163) is the table to extend with a
credential-Problem case (→ included) and a credential-Warn case (→ not actionable).

### 6. Testscript hermeticity — the load-bearing consequence

The four doctor testscripts (`doctor_healthy`, `doctor_brew_binary`, `doctor_fix_noop`,
`doctor_missing`) run the **real `agent-creance` binary**, which wires `OSKeychain`.
`OSKeychain.FindGenericPassword` execs `/usr/bin/security` by **absolute path**
(keychain.go:96-104, 156) — so a restricted testscript PATH does **not** prevent it. doctor
runs every check (status-as-data; it does not bail early), so adding `checkCred` makes all
four testscripts query the **real host keychain**.

Today **no** doctor testscript touches the keychain: `checkCA` only stats the CA cert file
(absent on the test host → `StatusWarn`, no keychain/mitmdump). This change introduces the
first keychain touch — consistent with why `run`'s credential path is deliberately kept
out of testscript and into `run_test.go` fakes (see the comment in
`run_missing_prereq.txtar`: "the later steps depend on the real login Keychain … so the
happy path and the … credential refusals are covered by run_test.go's *App+fakes unit
tests instead").

Consequences by host state, given the real keychain query:

- Dev machine, Claude logged in → `StatusOK`.
- CI / fresh checkout, no credential → `StatusMissing` (item absent, no fallback file).
- Locked login keychain → `security find-generic-password` blocks on an unlock prompt up
  to the 10s `securityFindTimeout`, then → `StatusLocked` (rare during an active dev
  session; pathological for `make test`).

Therefore the **severity mapping decides whether the testscripts stay hermetic**:

- If `StatusMissing` is a **Warn** (and only `StatusLocked`/`StatusFileFallback` are
  `Problem`): the common CI state (missing) keeps `doctor` at **exit 0**, and the existing
  exit-0 testscripts (`doctor_healthy`) stay green **without rework** (the extra
  `Credential:` line isn't asserted against). `StatusLocked`/`FileFallback` essentially
  never occur on CI. This preserves hermeticity. Recommended.
- If `StatusMissing` is a **Problem**: `doctor`'s exit code becomes host-dependent, and
  `doctor_healthy.txtar` (bare `agent-creance doctor`, which asserts exit 0) **fails on any
  machine without Claude creds**, including CI. This would force reworking the doctor
  testscripts (e.g. dropping the to-completion happy path to the unit-test harness, as
  `run` did), a larger and grain-crossing change.

The ticket already fixes `StatusLocked` and `StatusFileFallback` as `Problem` findings and
explicitly leaves `StatusMissing` open ("Missing → Problem or Warn?"). The hermeticity
analysis above is the decisive input to that open question.

## Code references

- `internal/cred/cred.go:42-59` — `Status` enum (OK/Locked/FileFallback/Missing)
- `internal/cred/cred.go:62-96` — `Result`, `OK()`, `Message()`, message constants
- `internal/cred/cred.go:103-117` — `Detect` (keychain switch + error return)
- `internal/cli/run.go:76-85` — the run-side `cred.Detect` precedent (message via `.Message()`)
- `internal/cli/cli.go:38-45,147-151` — `App.Keychain/FS/Paths` fields + real wiring
- `internal/doctor/doctor.go:22-32` — `Checker` (no Keychain/FS field today)
- `internal/doctor/doctor.go:39-48` — `Run` (add `r.Cred = c.checkCred()`)
- `internal/doctor/doctor.go:50-69` — `checkCA` (the template for `checkCred`)
- `internal/doctor/report.go:27-49` — `Status` enum + `Report` struct
- `internal/doctor/report.go:51-55` — `CASection` (template for `CredSection`)
- `internal/doctor/report.go:86-101` — `Actionable()` verdict
- `internal/doctor/report.go:103-142` — `Render`, `line`, `caGlyph`
- `internal/cli/doctor.go:40-65` — `Checker` construction from `App`
- `internal/doctor/doctor_test.go:60-93` — `newCheckerHarness` (already builds FakeKeychain/FS)
- `internal/doctor/report_test.go:31-103,128-163` — `goldenCases`, `TestRender`, `TestActionable`
- `internal/sysdep/keychain.go:87-104,151-178` — `OSKeychain` execs `/usr/bin/security` (abs path)
- `internal/cli/testdata/script/doctor_*.txtar` — the four doctor testscripts
- `internal/cli/testdata/script/run_missing_prereq.txtar` — the "credential paths go to unit tests" precedent

## Architecture insight

The doctor subsystem is built so that **adding a diagnostic is a four-touch, low-risk
change**: a section struct + a `checkX` method + a `Run` line + a `Render` block, with
verdict/golden/test patterns already in place. The credential check fits this mold exactly
by reusing `checkCA`'s `OK()`/`Message()` shape. The only non-mechanical question is the
severity-to-exit-code mapping, and it is non-mechanical *because* doctor's testscripts run
the real binary against the real keychain — the same hermeticity boundary the codebase
already respects by routing `run`'s credential path through unit tests rather than
testscript.

## Open questions for the checkpoint

1. **Severity of `StatusMissing` in doctor's verdict** (Problem vs Warn), given the
   testscript-hermeticity trade-off in §6. `StatusLocked`/`StatusFileFallback` are
   `Problem` per the ticket; this is about whether "not logged in" also flips doctor's
   exit code, and whether we keep the doctor testscripts as-is or rework them.
