---
date: 2026-06-06
topic: "AC-0022 — Credential detection (Keychain vs file fallback) (WP-4.1)"
status: complete
kind: ticket-research
branch: main
git_commit: 3a24cc81ed65ac74d6490c03b4ec94d525489925
ticket: AC-0022
depends_on: [AC-0009]
spike_gate: S2 (AC-0002)
source_design: docs/design.md
---

# AC-0022 — Credential detection (Keychain vs file fallback)

## Research question

How should `internal/cred` detect whether the Anthropic OAuth Keychain credential is
usable, and refuse cleanly (with documented messages) when it is not — given the
`sysdep.Keychain` / `FileSystem` / `PathResolver` seams that already exist, the exact
Keychain identity resolved by spike S2, and the project's detect-and-render conventions?

## Summary / bottom line

- **The spike gate is satisfied.** S2 (AC-0002) is *resolved* with concrete findings:
  the credential is the login-Keychain generic-password item **service `Claude Code-credentials`,
  account = the login short name (`$(id -un)`)**, and **the service name alone is a unique
  lookup key**. (`thoughts/shared/research/2026-06-04-s2-keychain.md:16-17,59-61`) The ticket's
  open-question line "[ ] Exact Keychain service/account from S2" is *stale* — it is answered.
- **The seam is ready and unused.** `sysdep.Keychain` exists with the exact method, sentinels,
  and a fully-featured fake — but has no consumers; `internal/cred` will be its first one.
  (`internal/sysdep/keychain.go`, `internal/sysdep/sysdeptest/keychain.go`)
- **`internal/cred` does not exist yet.** Neither does an `App.Keychain` field, nor any
  `.credentials.json` reference in code. These are the gaps AC-0022 fills.
- **The detection is pure classification over three inputs** (Keychain outcome × file presence)
  → one of a small set of outcomes with documented messages. It maps cleanly onto the existing
  `internal/prereq` "detect → classify → render" pattern (detection never errors; missing is
  data; messages are pinned with golden strings).
- **Two real decisions remain** (carried to the checkpoint): (1) whether AC-0022 also implements
  the **real `sysdep.OSKeychain`** (today a stub returning `ErrNotImplemented`) so the gated
  integration test can find the live item, or keeps it stubbed and ships only the hermetic
  `internal/cred` logic; (2) how `cred` treats the **locked-keychain** outcome for v0.1; plus
  (3) the **exact refusal wording** to pin in the golden strings.

## The ticket

`thoughts/shared/tickets/AC-0022-credential-detection.md` — "Credential detection (Keychain vs
file fallback) (WP-4.1)", Medium, open. Depends on AC-0009 (Keychain seam); spike-gated on S2.

Acceptance criteria (the detection matrix):

| Keychain item | `~/.claude/.credentials.json` | Outcome |
|---|---|---|
| present | (any) | **OK — use Keychain** |
| absent | present | **Refuse** — file creds out of v0.1 scope; run `claude` login differently |
| absent | absent | **Refuse** — point at host `claude` login |

Plus: all Keychain access through the `sysdep.Keychain` seam (hermetic tests); a grep guard
`! grep -rn 'os/exec\|"github.com/.*keychain"' internal/cred/*.go`.

## The spike-gate findings (S2 / AC-0002) — resolved

`thoughts/shared/research/2026-06-04-s2-keychain.md` (status: complete; ticket AC-0002 marked
Resolved):

- **Item identity:** login-keychain `genp` item, `svce = "Claude Code-credentials"`,
  `acct = $(id -un)` (login short name). **Service name alone is unique** — account is only a
  disambiguator. (`:55-61`)
- **Secret shape** (not needed for detection): JSON ~471 B, single top-level key `claudeAiOauth`.
  (`:63-67`) Detection only needs *presence*, not the bytes — `cred` can discard the secret.
- **Locked keychain is the one non-interactive failure mode:** a locked login keychain raises a
  **blocking SecurityAgent GUI prompt** rather than failing cleanly; S2 used an 8 s watchdog to
  characterize it. "`doctor`/`run` must verify the login keychain is unlocked before launching a
  caged session." (`:145-160,193-194`)
- **S2's explicit instruction to AC-0022:** "keys detection off the service name
  `Claude Code-credentials`; 'Keychain item absent **but** `~/.claude/.credentials.json` present'
  remains the file-fallback **refuse** case." (`:190-192`)

## The seam (dependency AC-0009) — present, unused

`internal/sysdep/keychain.go`:

- `Keychain` interface, one method: `FindGenericPassword(service, account string) ([]byte, error)`.
- Sentinels: `ErrItemNotFound` (absent), `ErrKeychainLocked` (locked). Callers distinguish via
  `errors.Is`.
- `OSKeychain` is a **deferred stub**: `FindGenericPassword` returns `ErrNotImplemented`. The doc
  comment says "Its real behaviour is deferred to **WP-4.1 (internal/cred)**" — i.e. this ticket.
  (`internal/sysdep/keychain.go:35-46`)

`internal/sysdep/sysdeptest/keychain.go` — `FakeKeychain`: pre-load `Items` (service+account →
secret) via `WithItem`, inject per-key `Errs`, set `Locked` (returns `ErrKeychainLocked` for every
lookup), and it records every `Lookups` query for assertions. This fake already models all three
outcomes `cred` must classify — hermetic tests need nothing new here.

## Supporting seams for the file-fallback check

- **File presence:** `sysdep.FileSystem.Stat(name)` — a non-existent path yields
  `errors.Is(err, fs.ErrNotExist)`, so "is `~/.claude/.credentials.json` present?" is a `Stat`
  whose error is checked with `errors.Is`. (`internal/sysdep/filesystem.go:28-30`) The
  `FakeFileSystem` returns `fs.ErrNotExist` for absent paths. (Prefer `Stat` over `ReadFile`:
  detection must not read a credential file's contents.)
- **Home dir + account:** `sysdep.PathResolver.UserHomeDir()` resolves `~` for the
  `~/.claude/.credentials.json` path; `PathResolver.Getenv("USER")` is the hermetic-testable way
  to obtain the login short name for the Keychain account. (`internal/sysdep/pathresolver.go:28-32`;
  `FakePathResolver` backs both via `HomeDir` and `Env`.) **Note:** there is *no* dedicated
  username seam (`os/user`) anywhere in the tree (grep returned nothing); `Getenv("USER")` is the
  established env-read primitive. Since the service name is unique (S2), the account is only a
  disambiguator, so an empty/derived account is acceptable.

## The pattern to follow — `internal/prereq`

The closest existing model is the prereq vertical slice (three-file split, the canonical example
cited in CLAUDE.md):

- `internal/prereq/prereq.go` — **detection/classification**. `Check(...)` "never returns an
  error: a missing tool is *data*, not a failure — callers decide whether missing is fatal (run)
  or merely reported (doctor)." (`:62-90`) `Missing` / `MissingInstructions` derive human messages
  from results. `cred` should mirror this: a `Detect(...)` that returns a `Result`/outcome value
  (never an error for the expected absent/locked cases), plus message accessors.
- `internal/prereq/report.go` — **rendering** with exact-byte constants so code and golden agree.
- `internal/prereq/report_test.go` — **golden-file** pattern: `var update = flag.Bool("update", …)`,
  fixture builder, join `testdata/<name>.golden`, write-when-`-update`/else-compare. This is the
  template for the ticket's "golden message strings."
- `internal/prereq/version_test.go` — **table-driven** pattern (`cases := []struct{…}` +
  `t.Run`). The ticket's "three table cases mapping the matrix" follow this shape.
- `internal/cli/doctor.go` — how a command **consumes** the pattern: `prereq.Check(...)` then
  `fmt.Fprint(app.Stdout, prereq.Report(...))`, returning an error when something is missing. The
  template for a future `run`/`doctor` credential precondition.

## Wiring (`internal/cli/cli.go`)

`App` (line 20) currently holds `Commander`, `Stdout`, `Stderr`, `Tested`, and the seams `FS`,
`Paths`, `Clock`, `HTTP` — **no `Keychain` field**. `Main()` (line 63) wires the real impls. To
let a future `run`/`doctor` use `cred`, AC-0022 adds `Keychain sysdep.Keychain` to `App` and wires
`sysdep.OSKeychain{}` in `Main()`. (Whether to wire it now or defer until a consumer exists is a
minor scoping call; wiring it now is harmless and matches how the other seams were added ahead of
their first command consumer.)

## Design-doc grounding

`docs/design.md` "The proxy and the credential story" (`:424-443`):

- v0.1 is OAuth-only, no credential injection. The credential lives in the login **Keychain**,
  keyed by service name; agent-creance does **not** clone it per project. (`:428,435`)
- **File-based fallback (out of scope):** "v0.1 should **detect** the situation (Keychain item
  absent but a credentials file present) at `run`/`doctor` and refuse with a clear message —
  *'caged sessions require a Keychain-stored credential; run `claude` login on the host'* — instead
  of a confusing mid-session TLS/auth failure." (`:437`) This is the source of the refusal wording.

## Scope boundaries (from the ticket)

- **In:** `internal/cred` detection logic + documented refusal messages; access via the
  `sysdep.Keychain` seam; hermetic table + golden tests; (gated) integration test.
- **Out:** implementing file-based credential *support* (detect-and-refuse only); granting Keychain
  access in the Seatbelt profile (AC-0023 / AC-0014, using S2's mach-lookup + file-write grant).

## Open questions for the checkpoint

1. **Real `OSKeychain` now, or keep the stub?** The seam's `OSKeychain` returns
   `ErrNotImplemented` and the comment defers the real impl to "WP-4.1 (internal/cred)" = this
   ticket. The ticket's verification step 4 is a **gated integration test that finds the real item
   on a real machine** — which needs a real impl. Options: (a) implement it now via the
   `/usr/bin/security` CLI (matches S2 exactly; no cgo; `os/exec` lives in `sysdep`, *not*
   `internal/cred`, so the grep guard is unaffected) and add the gated integration test; (b) keep
   the stub, ship only hermetic `internal/cred` + tests, defer the real impl + integration test to
   AC-0025/AC-0031. Affects whether the feature is functional in production this ticket.
2. **Locked-keychain handling in `cred`.** S2 shows a locked keychain raises a *blocking GUI
   prompt*, not a clean error, and assigns the unlock **pre-flight** to `doctor`/`run`. How should
   `internal/cred` treat the `ErrKeychainLocked` outcome? Recommended: `cred` maps it to a distinct
   third **refusal** ("unlock your login keychain, then retry"), keeping `cred` pure and fully
   testable via the fake's `Locked`; the *hang-avoiding pre-flight* (timeout/`show-keychain-info`)
   stays with `doctor`/`run` (AC-0031/AC-0025).
3. **Exact refusal wording** (pinned in golden strings; the ticket flags this). Proposed, aligned
   with `docs/design.md:437`:
   - *Absent + file present:* "A file-based Claude credential (`~/.claude/.credentials.json`) was
     found, but caged sessions require a Keychain-stored credential. File-based credentials are not
     supported in v0.1 (they can't be refreshed under a read-only `~/.claude`). Run `claude` on the
     host to log in to the Keychain."
   - *Neither present:* "No Claude credential found. Run `claude` on the host and log in before
     starting a caged session."
4. **Should `doctor` (AC-0031) surface this now?** Recommendation: **out of scope** — AC-0031 owns
   `doctor`; AC-0025 owns the `run` precondition. AC-0022 just makes `cred` reusable by both. (No
   user input strictly required; folded into the summary.)

## Code/document references

- Ticket: `thoughts/shared/tickets/AC-0022-credential-detection.md`
- Spike S2: `thoughts/shared/research/2026-06-04-s2-keychain.md` (esp. `:16-25,55-67,145-194`)
- Spec: `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md` (WP-4.1 ~`:260-263`)
- Seam: `internal/sysdep/keychain.go`; fake `internal/sysdep/sysdeptest/keychain.go`
- File/path seams: `internal/sysdep/filesystem.go:28-30`, `internal/sysdep/pathresolver.go:28-32`
- Pattern: `internal/prereq/{prereq,report}.go`, `internal/prereq/{report,version}_test.go`,
  `internal/cli/doctor.go`
- Wiring: `internal/cli/cli.go:20-32,63-73`
- Design: `docs/design.md:424-443`
- Dependency docs: `thoughts/shared/{tickets,plans,research}/*AC-0009*`
</content>
</invoke>
