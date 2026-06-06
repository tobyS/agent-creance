---
date: 2026-06-06
ticket: AC-0022
topic: "Credential detection (Keychain vs file fallback) (WP-4.1)"
status: ready
branch: main
research: thoughts/shared/research/2026-06-06-AC-0022-credential-detection.md
spike_gate: S2 (AC-0002) — resolved
depends_on: AC-0009 (Keychain seam)
---

# AC-0022 — Credential detection (Keychain vs file fallback): implementation plan

## Overview

Build `internal/cred`, the host-side detector that decides whether a caged session
can reach Claude's OAuth credential. It classifies three inputs — the login-Keychain
item `Claude Code-credentials` (present / absent / locked) and the presence of a
file-based `~/.claude/.credentials.json` — into one of four outcomes, returning a
documented refusal message for the three non-OK cases. All Keychain access goes
through the existing `sysdep.Keychain` seam; the file check uses `sysdep.FileSystem`
+ `sysdep.PathResolver`.

This ticket also lands the **real `sysdep.OSKeychain`** (today a stub returning
`ErrNotImplemented`), implemented via the `/usr/bin/security` CLI exactly as spike S2
validated, so the gated integration test can find the live item — and wires a
`Keychain` seam onto the `App` composition root so the detector is reachable by the
future `run`/`doctor` consumers (AC-0025 / AC-0031).

## Decisions locked at the checkpoint

1. **Implement the real `OSKeychain` now**, via the `security` CLI (no cgo). The
   `os/exec` lives in `internal/sysdep`, *not* `internal/cred`, so the ticket's grep
   guard is unaffected.
2. **`internal/cred` treats a locked keychain as a distinct refusal** (`StatusLocked`),
   fully testable via the fake's `Locked`. The hang-avoiding unlock *pre-flight* stays
   with `doctor`/`run` (AC-0031 / AC-0025), per S2.
3. **Refusal wording** uses the strings approved at the checkpoint (pinned in golden
   files; see Phase 2).

## Current state

- `internal/cred` does not exist. No `App.Keychain` field; no `.credentials.json`
  reference in code.
- `sysdep.Keychain` (interface, `ErrItemNotFound` / `ErrKeychainLocked` sentinels) and
  the full-featured `FakeKeychain` exist and are unused — `internal/cred` is the first
  consumer. `OSKeychain.FindGenericPassword` returns `ErrNotImplemented`
  (`internal/sysdep/keychain.go:44-46`), asserted by
  `internal/sysdep/keychain_test.go:11-17`.
- `sysdep.FileSystem.Stat` → `fs.ErrNotExist` for absent paths; `PathResolver`
  provides `UserHomeDir()` and `Getenv(key)`. Fakes back all of these.
- `ErrNotImplemented` is still used by `processgroup.go:53`, so it stays in
  `errors.go` after `OSKeychain` stops returning it.
- The model to mirror is `internal/prereq` (`prereq.go` detect/classify never errors;
  `report.go` exact-byte rendering; `report_test.go` golden + `version_test.go` table).

## Desired end state

- `cred.Detect(kc, fsys, paths)` returns a `cred.Result` whose `Status` is one of
  `StatusOK` / `StatusLocked` / `StatusFileFallback` / `StatusMissing`, plus
  `Result.OK()` and `Result.Message()`. It returns a non-nil `error` only for genuinely
  unexpected failures (a Keychain error that is neither absent nor locked, or an
  unresolvable home dir / unexpected stat failure) — the expected outcomes are data,
  not errors (the prereq philosophy).
- The three refusal messages are pinned with golden files; the table test maps the full
  matrix to outcome + message and asserts the seam was queried with the S2 service name.
- `OSKeychain.FindGenericPassword` reads the item via `security find-generic-password`,
  mapping not-found → `ErrItemNotFound`, the locked/blocking-prompt timeout →
  `ErrKeychainLocked`, success → the secret bytes, anything else → a wrapped error. The
  exit/output→outcome mapping is a **pure, table-tested** helper; only the actual exec
  is integration-only.
- `App.Keychain sysdep.Keychain` is wired to `OSKeychain{}` in `Main()`.
- `make test` and `make lint` are green; the grep guard passes; the gated integration
  test finds the real item on this machine (skips cleanly where absent).

## What we are NOT doing

- No file-based credential *support* (detect-and-refuse only).
- No Seatbelt profile grant (AC-0014 / AC-0023).
- No `run` precondition wiring (AC-0025) and no `doctor` surfacing (AC-0031) — `cred` is
  built to be reusable by both, but neither command calls it in this ticket.
- No cgo / Security.framework binding — the `security` CLI is the v0.1 mechanism.

---

## Phase 1 — Real `OSKeychain` via the `security` CLI

### Changes

**`internal/sysdep/keychain.go`** — replace the stub body and update the doc comment.

- Add a **pure helper** (no I/O), unit-testable:

  ```go
  // interpretSecurityErr maps a failed `security find-generic-password` invocation
  // to a Keychain sentinel. exitCode is the process exit code (or -1 if it could not
  // be determined); timedOut is true when the call exceeded the watchdog (a locked
  // login keychain raises a blocking GUI unlock prompt rather than failing — spike
  // S2 §4 — which we surface as ErrKeychainLocked instead of hanging).
  func interpretSecurityErr(exitCode int, timedOut bool) error
  ```

  Mapping: `timedOut` → `ErrKeychainLocked`; `exitCode == 44`
  (`errSecItemNotFound`, observed in S2 §1) → `ErrItemNotFound`; otherwise a sentinel
  `errUnexpectedSecurity` (wrapped with context by the caller).

- `OSKeychain.FindGenericPassword(service, account)`:
  - Build args `["find-generic-password", "-s", service]`, appending `"-a", account`
    **only when account != ""** (service name is unique per S2; an empty account is
    valid), and `"-w"` to print the secret to stdout (honors the seam contract:
    "returns the secret bytes").
  - Run `/usr/bin/security` under a bounded watchdog (`exec.CommandContext` with a
    `context.WithTimeout`, e.g. 10s — comfortably above S2's observed ~1s success and
    its 8s locked-prompt characterization). Capture stdout/stderr separately.
  - On success: return stdout with a single trailing `\n` trimmed (the CLI appends one
    after the password), nil error.
  - On `context.DeadlineExceeded`: return `interpretSecurityErr(-1, true)` →
    `ErrKeychainLocked`.
  - On `*exec.ExitError`: return `interpretSecurityErr(exitErr.ExitCode(), false)`,
    wrapping `errUnexpectedSecurity` with the trimmed stderr for diagnosability.
  - On any other exec error (e.g. binary missing): wrap and return.
  - Update the doc comment: real behaviour now lives here (drop the "deferred to WP-4.1
    / returns ErrNotImplemented" language). Keep the `var _ Keychain = …` assertion.

**`internal/sysdep/keychain_test.go`** — replace
`TestOSKeychainFindGenericPasswordNotImplemented` (the real impl no longer returns
`ErrNotImplemented`) with a **table-driven test of `interpretSecurityErr`**: cases
`{exitCode: 44} → ErrItemNotFound`, `{timedOut: true} → ErrKeychainLocked`,
`{exitCode: 1} → errUnexpectedSecurity` (assert via `errors.Is`). This keeps the risky
mapping logic hermetic; the actual exec is covered only by the integration test
(Phase 3), per the "external tools never invoked in unit tests" rule.

### Success criteria

#### Automated
- [ ] `go build ./...` compiles.
- [ ] `go test -race ./internal/sysdep/...` passes (the new `interpretSecurityErr` table).
- [ ] `grep -n "ErrNotImplemented" internal/sysdep/keychain.go` → no matches (stub gone),
      while `internal/sysdep/processgroup.go` still uses it (sentinel retained).

#### Manual
- [ ] The exec path uses `exec.CommandContext` with a timeout (no unbounded `security`
      call that could hang on a locked keychain).

---

## Phase 2 — `internal/cred` detection + hermetic tests

### Changes

**`internal/cred/cred.go`** (new) — package doc explains it is the host-side credential
*detector* (presence/usability), not a refresher; refresh happens in-cage.

```go
const (
    // KeychainService is the login-Keychain generic-password service name of the
    // Anthropic OAuth credential (spike S2). The service name alone is a unique key;
    // the account (login short name) is only a disambiguator.
    KeychainService = "Claude Code-credentials"
)

// Status is the outcome of credential detection.
type Status int
const (
    StatusOK           Status = iota // Keychain item present — caged sessions can use it.
    StatusLocked                     // login keychain locked — refuse (S2: doctor/run pre-flight the unlock).
    StatusFileFallback               // Keychain absent, file-based ~/.claude/.credentials.json present — refuse (out of v0.1 scope).
    StatusMissing                    // neither present — refuse, point at host `claude` login.
)

type Result struct{ Status Status }
func (r Result) OK() bool { return r.Status == StatusOK }
func (r Result) Message() string // "" for OK; the documented refusal otherwise.

// Detect classifies credential availability. Like prereq.Check, the expected
// absent/locked/file-fallback outcomes are data (encoded in Status), not errors;
// Detect returns a non-nil error only for a genuinely unexpected failure.
func Detect(kc sysdep.Keychain, fsys sysdep.FileSystem, paths sysdep.PathResolver) (Result, error)
```

`Detect` logic:
1. `account := paths.Getenv("USER")` (login short name; hermetic via `FakePathResolver.Env`).
2. `_, err := kc.FindGenericPassword(KeychainService, account)` — discard the secret
   (host-side never needs it):
   - `err == nil` → `StatusOK`.
   - `errors.Is(err, sysdep.ErrKeychainLocked)` → `StatusLocked`.
   - `errors.Is(err, sysdep.ErrItemNotFound)` → resolve the file fallback:
     - `home, herr := paths.UserHomeDir()`; if `herr != nil` → return wrapped error.
     - `path := filepath.Join(home, ".claude", ".credentials.json")`.
     - `_, serr := fsys.Stat(path)`: `serr == nil` → `StatusFileFallback`;
       `errors.Is(serr, fs.ErrNotExist)` → `StatusMissing`; else → return wrapped error.
   - any other `err` → return wrapped error (`fmt.Errorf("cred: keychain lookup: %w", err)`).

`Message()` returns the strings approved at the checkpoint, built from per-status
constants so code and golden files agree byte-for-byte:
- `StatusOK` → `""`.
- `StatusLocked` → `"The login keychain is locked, so the Claude credential can't be read. Unlock your login keychain and retry."`
- `StatusFileFallback` → `"A file-based Claude credential (~/.claude/.credentials.json) was found, but caged sessions require a Keychain-stored credential. File-based credentials are not supported in v0.1 (they can't be refreshed under a read-only ~/.claude). Run `claude` on the host to log in to the Keychain."`
- `StatusMissing` → `"No Claude credential found. Run `claude` on the host and log in before starting a caged session."`

**`internal/cred/cred_test.go`** (new) — table-driven, mirroring `version_test.go`,
covering the full matrix:

| name | Keychain fake | FS fake | want Status | want OK |
|---|---|---|---|---|
| keychain present | `WithItem(KeychainService, "toby", "{…}")` | empty | `StatusOK` | true |
| keychain locked | `Locked = true` | empty | `StatusLocked` | false |
| absent + file present | empty | `~/.claude/.credentials.json` seeded | `StatusFileFallback` | false |
| absent + neither | empty | empty | `StatusMissing` | false |

Each case sets `FakePathResolver{HomeDir: "/home/toby", Env: {"USER": "toby"}}`, calls
`Detect`, asserts `Status`, `OK()`, and that `kc.Lookups` recorded one query with
`Service == KeychainService` (proving the seam was used with the S2 name). For the three
refusals it compares `Message()` against a golden file (write-on-`-update`, else compare),
exactly as `report_test.go` does. Also a case asserting `Detect` returns an error when
`FindGenericPassword` yields an unexpected error (inject via `FakeKeychain.Errs`).

**`internal/cred/testdata/`** — `refuse_locked.golden`, `refuse_file_fallback.golden`,
`refuse_missing.golden` (generated with `-update`, reviewed in the diff).

### Success criteria

#### Automated
- [ ] `go build ./...` compiles.
- [ ] `go test -race ./internal/cred/...` passes (matrix + golden messages + error case).
- [ ] Grep guard: `! grep -rn 'os/exec\|"github.com/.*keychain"' internal/cred/*.go`
      (access is via the `sysdep.Keychain` seam only).
- [ ] `make golden` produces no unexpected diff after the goldens are committed.

#### Manual
- [ ] `Detect` never reads the secret bytes or the contents of the credentials file
      (presence via `Stat` only); the golden message strings read as intended to an
      operator.

---

## Phase 3 — Wire `App.Keychain` + gated integration test + final verification

### Changes

**`internal/cli/cli.go`** — add `Keychain sysdep.Keychain` to the `App` struct (next to
`FS`/`Paths`/`Clock`/`HTTP`, with a short comment that `internal/cred` is its consumer)
and wire `Keychain: sysdep.OSKeychain{}` in `Main()`. This puts the real impl into the
binary; no command reads it yet (run/doctor are later tickets).

**`internal/sysdep/keychain_integration_test.go`** (new, `//go:build integration`) —
construct `OSKeychain{}`, derive the account from `os.Getenv("USER")`, call
`FindGenericPassword("Claude Code-credentials", account)` (literal service name with a
comment citing S2 — `sysdep` must not import `cred`):
- `errors.Is(err, sysdep.ErrItemNotFound)` → `t.Skip("no Claude credential on this machine")`.
- else `require.NoError`, and assert `len(secret) > 0` **without printing the secret**
  (S2 secret-hygiene). This satisfies the ticket's verification step 4.

### Success criteria

#### Automated
- [ ] `go build ./...` compiles.
- [ ] `make test` (race, hermetic) is green across the module.
- [ ] `make lint` is clean (`go vet` + `golangci-lint`).
- [ ] `make test-integration` runs the new integration test; on this machine it finds the
      real item (or skips cleanly if absent).

#### Manual
- [ ] `git grep -n "Claude Code-credentials"` shows the constant lives in `internal/cred`
      and the literal in the sysdep integration test only (no stray duplication).
- [ ] Re-read `make golden` diff one last time; goldens are intentional.

---

## Testing strategy

- **Pure logic → table tests:** `interpretSecurityErr` (Phase 1) and the `Detect` matrix
  (Phase 2), following `version_test.go`.
- **Documented messages → golden files** with `-update` (Phase 2), following
  `report_test.go`.
- **Real OS tool → integration only** (Phase 3), behind the `integration` tag, per the
  project rule that `security` is never invoked in unit tests.
- **Seam discipline:** the grep guard plus `kc.Lookups` assertions prove `internal/cred`
  touches the Keychain only through `sysdep.Keychain`.

## References

- Research: `thoughts/shared/research/2026-06-06-AC-0022-credential-detection.md`
- Ticket: `thoughts/shared/tickets/AC-0022-credential-detection.md`
- Spike S2: `thoughts/shared/research/2026-06-04-s2-keychain.md`
- Seam: `internal/sysdep/keychain.go` (+ `sysdeptest/keychain.go`)
- Pattern: `internal/prereq/{prereq,report}.go`, `{report,version}_test.go`
- Design: `docs/design.md:424-443`
</content>
