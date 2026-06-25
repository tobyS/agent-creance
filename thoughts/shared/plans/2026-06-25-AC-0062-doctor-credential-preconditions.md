---
date: 2026-06-25
ticket: AC-0062
topic: "doctor surfaces host credential preconditions (locked keychain, file-fallback, missing)"
status: ready
research: thoughts/shared/research/2026-06-25-AC-0062-doctor-credential-preconditions.md
---

# Implementation Plan: AC-0062 — doctor surfaces host credential preconditions

## Overview

`agent-creance doctor` reports no credential precondition, even though the design and
`cred`'s package doc assign that job to both `run` and `doctor` (only `run` calls
`cred.Detect`). This plan adds a `Credential` diagnostic section to doctor that calls the
existing, side-effect-free `cred.Detect`, mirroring the established `checkCA` pattern, and
sources its wording from `cred.Result.Message()` so the `run` and `doctor` paths cannot
drift.

## Decision locked at the question checkpoint

**Severity mapping** (the one open question; the rest is fixed by the ticket):

| `cred` outcome        | doctor `Status` | Actionable? | Glyph |
|-----------------------|-----------------|-------------|-------|
| `StatusOK`            | `StatusOK`      | no          | ✓     |
| `StatusLocked`        | `StatusProblem` | yes         | ✗     |
| `StatusFileFallback`  | `StatusProblem` | yes         | ✗     |
| `StatusMissing`       | `StatusWarn`    | **no**      | ⚠     |
| unexpected `error`    | `StatusWarn`    | no          | ⚠     |

`StatusMissing` → **Warn** (not Problem) was the user's choice. Rationale: doctor is a
diagnostic, not a gate; "not logged in" is a precondition the user resolves by logging in,
not a broken environment. Decisively, it keeps doctor's exit code host-independent for the
common "no credential" state, so the four doctor testscripts — which run the real binary
and now query the real host keychain via `OSKeychain` → `/usr/bin/security` — stay
hermetic and green without rework. (See research §6.)

## Current state (from research)

- `doctor.Checker` (`internal/doctor/doctor.go:22-32`) has `Paths sysdep.PathResolver` but
  **no** `Keychain` and **no** `FS` field. `cred.Detect` needs all three.
- `Checker.Run` (`doctor.go:39-48`) assembles findings status-as-data (one `checkX` per
  `Report` field; no method errors out). `checkCA` (`doctor.go:50-69`) is the exact template:
  call a `Detect`/`Verify` helper, map a non-nil error to a `StatusWarn` "could not check"
  finding, map `!OK()` to a finding carrying `res.Message()`, else `StatusOK`.
- `Report` (`report.go:40-49`) has one field per section; `CASection` (`report.go:51-55`,
  `{ State Status; Detail string }`) is the shape for a new `CredSection`.
- `Render` (`report.go:103-127`) lists sections in literal order; `caGlyph`
  (`report.go:131-142`) maps OK→✓, Problem→✗, else→⚠ — identical to what a credential
  section needs. `line` (`report.go:129`) prints `  <glyph> <detail>`.
- `Actionable` (`report.go:86-101`) collects labels only for `StatusProblem`-level findings.
- `cli/doctor.go:40-53` builds the `Checker` from `App`; `app.Keychain`/`app.FS` already
  exist there (already passed to `setup.NewInstaller`), so no `App` change is needed.
- `run.go:76-85` is the precedent: `cred.Detect(app.Keychain, app.FS, app.Paths)`, refusal
  text from `credRes.Message()`.
- Test harness `newCheckerHarness` (`doctor_test.go:60-93`) already builds a
  `FakeKeychain` (`kc`) and `FakeFileSystem` (`fsys`) — currently only fed to the
  `Installer`. `FakeKeychain`: `WithItem(service, account, secret)` → present;
  empty → `ErrItemNotFound`; `Locked: true` → `ErrKeychainLocked`; `Errs[key]` → arbitrary
  error. `FakePathResolver.Env` is empty by default, so `Getenv("USER")` → `""` (account
  `""`); `HomeDir` = `/home/toby`, so the fallback path is
  `/home/toby/.claude/.credentials.json`.
- Golden fixtures `goldenCases()` (`report_test.go:31-82`): four `Report` literals
  (`healthy`/`problems`/`fixed`/`stranded`) × {plain, `_color`} → 8 `testdata/render_*.golden`.
- Four doctor testscripts: `doctor_healthy` (asserts exit 0 + section headers),
  `doctor_brew_binary`, `doctor_fix_noop`, `doctor_missing` (asserts non-zero from missing
  prereqs).

No import cycle: `cred` imports only `sysdep`; `doctor` importing `cred` is safe.

## Desired end state

`agent-creance doctor` prints a `Credential:` section: ✓ reachable when the Keychain item
is present; ✗ (actionable, non-zero exit) with the cred locked / file-fallback message when
the keychain is locked or only a file credential exists; ⚠ with the cred not-logged-in
message when missing; ⚠ "could not check credential: …" on an unexpected lookup failure.
`run` and `doctor` share the wording (both via `cred.Result.Message()`). `doctor --fix`
does not touch credentials (documented in `checkCred`). Each status is proven by a
fakes-based unit test; render output is pinned by goldens; the verdict is covered by
`TestActionable`. `make test`, `make lint`, `make build` all green.

---

## Phase 1 — Production code: credential check, section, render, verdict

### Changes

1. **`internal/doctor/doctor.go` — add seams to `Checker`** (`:22-32`):
   add two fields after `Paths`:
   ```go
   Keychain sysdep.Keychain
   FS       sysdep.FileSystem
   ```
   (`sysdep` is already imported.)

2. **`internal/doctor/doctor.go` — wire into `Run`** (`:39-48`): add after `r.CA`:
   ```go
   r.Cred = c.checkCred()
   ```

3. **`internal/doctor/doctor.go` — add `checkCred`** (after `checkCA`), importing
   `internal/cred`:
   ```go
   // checkCred reports whether the host Claude credential is reachable, mirroring run's
   // cred.Detect precondition so doctor answers "is my credential reachable, and if not,
   // why" without starting a caged session. The wording comes from cred.Result.Message()
   // so the run and doctor paths cannot drift. Severity (AC-0062): a locked keychain or an
   // unsupported file-based credential is an actionable Problem; "not logged in" is a Warn
   // (a precondition the user fixes by logging in, not a broken environment); an unexpected
   // lookup failure degrades to Warn (status-as-data). doctor --fix deliberately does NOT
   // act here — unlocking the keychain and running `claude` login are interactive user
   // actions, not automatable fixes.
   func (c *Checker) checkCred() CredSection {
       res, err := cred.Detect(c.Keychain, c.FS, c.Paths)
       if err != nil {
           return CredSection{State: StatusWarn, Detail: "could not check credential: " + err.Error()}
       }
       switch res.Status {
       case cred.StatusOK:
           return CredSection{State: StatusOK, Detail: "reachable"}
       case cred.StatusLocked, cred.StatusFileFallback:
           return CredSection{State: StatusProblem, Detail: res.Message()}
       case cred.StatusMissing:
           return CredSection{State: StatusWarn, Detail: res.Message()}
       default:
           return CredSection{State: StatusWarn, Detail: "credential state unknown"}
       }
   }
   ```
   The explicit `default` is defensive: a future `cred.Status` defaults to the
   hermeticity-safe Warn rather than silently becoming actionable.

4. **`internal/doctor/report.go` — add `Cred` field to `Report`** (`:40-49`), after `CA`:
   ```go
   Cred    CredSection
   ```
   and define `CredSection` next to `CASection`:
   ```go
   // CredSection is the host Claude-credential finding. Detail is the text after the glyph
   // (cred.Result.Message() for the non-OK cases, "reachable" for OK).
   type CredSection struct {
       State  Status // OK=reachable, Problem=locked/file-fallback (actionable), Warn=missing/could-not-check
       Detail string
   }
   ```

5. **`internal/doctor/report.go` — render the section** (`Render`, after the CA block):
   ```go
   b.WriteString("\n" + sty.Header("Credential:") + "\n")
   line(&b, stateGlyph(r.Cred.State, sty), r.Cred.Detail)
   ```
   Rename `caGlyph` → `stateGlyph` (it is generic OK→✓/Problem→✗/else→⚠ and now serves both
   CA and Credential); update the CA render line to call `stateGlyph`. Behavior-preserving,
   so CA goldens are unaffected.

6. **`internal/doctor/report.go` — extend the verdict** (`Actionable`, after the CA clause):
   ```go
   if r.Cred.State == StatusProblem {
       probs = append(probs, "credential unavailable")
   }
   ```
   Generic on `State == Problem`, so locked/file-fallback fold in and missing (Warn) does
   not.

7. **`internal/cli/doctor.go` — wire the seams** (`:41-52`): add to the `&doctor.Checker{…}`
   literal:
   ```go
   Keychain: app.Keychain,
   FS:       app.FS,
   ```

8. **`internal/doctor/report_test.go` — populate `Cred` in the four `goldenCases()`
   fixtures** so the rendered section is meaningful and all three glyphs are exercised
   across the set:
   - `healthy`: `Cred: CredSection{State: StatusOK, Detail: "reachable"}`
   - `stranded`: `Cred: CredSection{State: StatusOK, Detail: "reachable"}`
   - `problems`: `Cred: CredSection{State: StatusProblem, Detail: cred.Result{Status: cred.StatusLocked}.Message()}`
   - `fixed`: `Cred: CredSection{State: StatusWarn, Detail: cred.Result{Status: cred.StatusMissing}.Message()}`

   Sourcing the fixture detail from `cred.Result{…}.Message()` (rather than a hand-copied
   string) guarantees the goldens track the cred messages. (`report_test.go` gains a `cred`
   import.)

9. **Regenerate goldens:** `make golden`, then review the diff — every
   `render_*.golden` / `render_*_color.golden` should gain exactly a `Credential:` header
   plus one finding line in the position after `CA trust:`; nothing else should change.

### Verification

- [ ] `go build ./...` — compiles (typecheck).
- [ ] `make golden` diff shows only the added `Credential:` blocks; CA/Proxy/Exposed/FS
      blocks byte-identical.
- [ ] `make test` — green (existing doctor unit/golden tests pass with the regenerated
      goldens; `TestRun_*` still pass since `Checker` gained fields the harness will set in
      Phase 2, but the zero-value `FakeKeychain`/`nil` seams are only exercised once Phase 2
      wires them — confirm `newCheckerHarness` still compiles; if `Run` now calls
      `checkCred` with nil seams, Phase 2's harness wiring must land in the **same commit**,
      so treat Phases 1+2 as one commit — see "Commit boundary" below).
- [ ] `make lint` — clean.

> **Commit boundary.** `Run` now calls `checkCred`, which dereferences
> `c.Keychain`/`c.FS`. `newCheckerHarness` must set those fields (Phase 2 step 1) or the
> existing `TestRun_*` tests nil-panic. Therefore Phase 1 and Phase 2 step 1 land in **one
> commit**; the remaining Phase 2 steps (new per-status tests, `TestActionable` cases,
> testscript assertions) may share that commit or follow in a second. Plan to commit once
> the whole change is green.

---

## Phase 2 — Tests: per-status unit coverage, verdict, testscripts

### Changes

1. **`internal/doctor/doctor_test.go` — wire the seams into the harness**
   (`newCheckerHarness`, `:79-88`): add to the `&Checker{…}` literal:
   ```go
   Keychain: kc,
   FS:       fsys,
   ```
   (`kc`/`fsys` already exist in the function.) Optionally add a `kc`/`fsys` field to
   `checkerHarness` if a test needs to mutate them post-construction (the keychain item /
   fallback file are seeded via the existing `h.fs`/a new `h.kc` handle).

   Default harness state (empty keychain, no fallback file) yields `StatusMissing` → a
   `Credential` **Warn**, which is **non-actionable** — so the existing `TestRun_*`
   assertions (`assert.Empty(t, rep.Actionable())` in the healthy/warn cases) **still
   hold**. Confirm `TestRun_HealthyTrustedCA` and the orphan/exposed/fs cases still pass; if
   any asserts exact `Actionable()` contents, missing-cred adds nothing (Warn), so they’re
   unaffected.

2. **`internal/doctor/doctor_test.go` — add `TestRun_Credential*` cases** (mirroring
   `TestRun_*`), each asserting `rep.Cred.State`, `rep.Cred.Detail`, and `rep.Actionable()`:
   - **OK:** seed `h.kc.WithItem(cred.KeychainService, "", "secret")` (account `""` because
     `FakePathResolver.Env["USER"]` is unset) → `StatusOK`, Detail `"reachable"`, not
     actionable.
   - **Missing:** default harness (empty keychain, no fallback file) → `StatusWarn`, Detail
     = `cred.Result{Status: cred.StatusMissing}.Message()`, not actionable.
   - **FileFallback:** seed `h.fs.Files["/home/toby/.claude/.credentials.json"] = []byte("{}")`
     with empty keychain → `StatusProblem`, Detail = file-fallback message, `Actionable()`
     contains `"credential unavailable"`.
   - **Locked:** set `h.kc.Locked = true` → `StatusProblem`, Detail = locked message,
     actionable.
   - **Unexpected error:** set `h.kc.Errs[<key>] = errBoom` (or a `Stat` error on the
     fallback path) → `StatusWarn`, Detail prefixed `"could not check credential:"`, not
     actionable, and **other checks still run** (assert e.g. `rep.CA`/`rep.Proxy` populated).
   - **Same-wording guard (AC):** assert the Detail for the locked/file-fallback/missing
     cases equals the corresponding `cred.Result{…}.Message()` exactly — this is what
     proves doctor and run can't drift.

   (Use `cred.KeychainService` and `cred.Result{…}.Message()` from the `cred` package;
   add the import.)

3. **`internal/doctor/report_test.go` — extend `TestActionable`** (`:128-163`):
   - credential Problem → `Actionable()` contains `"credential unavailable"`:
     `Report{Cred: CredSection{State: StatusProblem}}`.
   - credential Warn is **not** actionable: add `Cred: CredSection{State: StatusWarn}` into
     the existing "warnings only are not actionable" case (it must remain `Empty`).

4. **Testscripts** (`internal/cli/testdata/script/`):
   - `doctor_healthy.txtar`: add `stdout 'Credential:'` to assert the section renders. The
     bare `agent-creance doctor` must still exit 0: on a dev host with creds → OK; on a host
     without → Missing (Warn) → still exit 0. Do **not** assert the finding glyph/text
     (host-dependent). Add a one-line comment explaining the credential finding is
     host-dependent and only the header is asserted (mirroring the existing "degrade
     gracefully" note for CA/Proxy).
   - `doctor_missing.txtar`: still exits non-zero from missing prereqs; the credential
     section renders but changes nothing. Add `stdout 'Credential:'` for parity (optional).
   - `doctor_brew_binary.txtar`, `doctor_fix_noop.txtar`: read them, confirm their exit-code
     expectation is unaffected by a missing-cred Warn, and add a `stdout 'Credential:'`
     header assertion only if it does not introduce host dependence. If either asserts exit
     0 and could see a *locked* keychain on a dev machine, leave its assertions as-is (the
     locked-on-test edge is pathological — see caveat).

### Verification

- [ ] `make test` — all green, including the new `TestRun_Credential*`, `TestActionable`,
      `TestRender` (regenerated goldens), and the four doctor testscripts.
- [ ] `make lint` — clean.
- [ ] `make build` — `bin/agent-creance` reflects the change (project rule: rebuild at
      ticket end).
- [ ] Manual: `bin/agent-creance doctor` on this host prints a `Credential:` line
      consistent with the host's keychain state, and exits 0 when the credential is present
      or merely missing.

---

## Success criteria

### Automated
- [ ] `make test` green (unit + golden + testscript, race detector).
- [ ] `make lint` clean (`go vet` + golangci-lint).
- [ ] `go build ./...` / `make build` succeed.
- [ ] `make golden` produces no diff after Phase 1 regeneration (goldens committed).

### Manual / acceptance (maps to ticket ACs)
- [ ] doctor runs `cred.Detect` and shows a `Credential:` finding in report + output.
- [ ] `StatusOK` → ✓ "reachable"; `StatusLocked`/`StatusFileFallback` → ✗ Problem carrying
      `cred.Result.Message()`; `StatusMissing` → ⚠ Warn carrying the not-logged-in message.
- [ ] doctor and run show identical wording (both from `cred.Result.Message()`), asserted by
      the same-wording guard test.
- [ ] An unexpected `cred.Detect` error → ⚠ Warn, other checks still run.
- [ ] A credential Problem flips doctor's verdict/exit like other Problems
      (`Actionable()` + non-zero exit via `cli/doctor.go`).
- [ ] `doctor --fix` does not attempt credential fixes — documented in `checkCred`.
- [ ] Every status is unit-tested through the `sysdep` fakes; goldens cover the render.

## Known caveat (documented, accepted)

doctor's testscripts run the real binary, and `OSKeychain` execs `/usr/bin/security` by
absolute path, so `make test` now performs a read-only `find-generic-password` during the
doctor testscripts. On an active dev machine the login keychain is unlocked, so this returns
quickly (OK or item-not-found → exit 0). The only pathological case is running `make test`
with a *locked* login keychain, where the lookup blocks up to the 10s `securityFindTimeout`
and then yields a Problem — rare, and the same real-keychain dependence the codebase already
accepts for `run` (which routes its credential path through unit tests, not testscript). The
chosen Missing→Warn mapping confines the host-dependent exit-code risk to this edge.

## Testing strategy

- **Pure mapping / verdict:** table/asserted unit tests (`TestRun_Credential*`,
  `TestActionable`) via the `sysdep` fakes — no real keychain.
- **Rendered output:** golden files regenerated with `make golden` and diff-reviewed.
- **CLI behavior:** the four doctor testscripts assert the section header renders and the
  exit code is unchanged for the present/missing states.

## References

- Ticket: `thoughts/shared/tickets/AC-0062-doctor-credential-preconditions.md`
- Research: `thoughts/shared/research/2026-06-25-AC-0062-doctor-credential-preconditions.md`
- Review (origin, Gap 2): `thoughts/shared/reviews/2026-06-23-codebase-vs-design-gap-review.md`
