# AC-0062: doctor surfaces host credential preconditions (locked keychain, file-fallback, missing)

**Status:** Done
**Estimated Complexity:** Low
**Created:** 2026-06-25
**Updated:** 2026-06-25

## Problem Statement

The 2026-06-23 design-conformance review
(`thoughts/shared/reviews/2026-06-23-codebase-vs-design-gap-review.md`, Gap 2) found
that `agent-creance doctor` reports **no** credential precondition at all. Three
sources already assign that job to doctor:

- `docs/design.md:530` — "`run`/`doctor` detect the situation (Keychain item absent
  but a credentials file present) and refuse with a clear message".
- `docs/design.md:20` (spike S2) — a *locked* login keychain "blocks GUI prompt …
  `doctor` must detect it".
- `internal/cred/cred.go:7-9` — cred's own package doc: "run/doctor ask cred whether
  the credential is reachable so they can refuse up front".

But `doctor.Run` (`internal/doctor/doctor.go:39-47`) never calls `cred.Detect`; only
`run` does (`internal/cli/run.go:78`). So a developer whose caged Claude session fails
with a confusing TLS/auth error — because their login keychain is locked, they have an
unsupported file-based credential, or they simply aren't logged in on the host — gets
no signal from the one command whose entire purpose is diagnosis. This is incremental
drift: the credential check was built as a `run`-launch guard and never wired into
doctor when it was extended.

## Desired Outcome

`agent-creance doctor` includes a dedicated credential finding that surfaces every
non-OK `cred.Detect` outcome — locked keychain, file-based fallback, and
missing/not-logged-in — using the cred package's existing actionable messages, and
shows a healthy state when the Keychain item is present. Running doctor answers "is my
Claude credential reachable, and if not, why" without starting a caged session.

## User Stories / Use Cases

- As a developer whose caged session fails with a confusing auth/TLS error, I want
  `agent-creance doctor` to tell me *which* credential precondition is wrong (locked
  keychain / unsupported file credential / not logged in), so that I fix the real
  cause instead of guessing.
- As a user onboarding to agent-creance, I want doctor to confirm my Claude credential
  is reachable before my first caged run, so that I find problems at diagnosis time,
  not mid-session.

## Acceptance Criteria

- [x] doctor runs `cred.Detect` as part of its diagnostics and includes a credential
      finding in its report and rendered output.
- [x] `StatusOK` → an OK/healthy finding (credential reachable).
- [x] `StatusLocked` → a Problem finding carrying the cred package's locked-keychain
      message (`cred.Result.Message()`), not a re-worded copy.
- [x] `StatusFileFallback` → a Problem finding carrying the cred package's
      file-fallback message.
- [x] `StatusMissing` → a finding carrying the cred package's not-logged-in message
      (mapped to a non-actionable Warn — see Notes).
- [x] doctor and run show the **same** wording for the same condition (both source it
      from `cred.Result.Message()`), so the two paths can't drift.
- [x] An unexpected `cred.Detect` error degrades to a Warn finding and never aborts the
      other doctor checks (matches doctor's status-as-data convention,
      `internal/doctor/doctor.go:34-38`).
- [x] doctor's overall verdict reflects a credential Problem the same way other Problem
      findings do.
- [x] `doctor --fix` does not attempt to auto-fix credentials (login and keychain
      unlock are interactive user actions) — this is intentional and documented, not a
      silent no-op.
- [x] The credential check is unit-testable through the `sysdep` fakes (no real
      keychain / logged-in session), and golden/output tests cover each status.

## Out of Scope

- Changing `run`'s existing credential refusal — it is already correct.
- Supporting file-based credentials (`~/.claude/.credentials.json`) — still out of
  scope for v0.1 per the design; doctor only *reports* the situation.
- Auto-unlocking the keychain or running `claude` login from `doctor --fix`.
- The companion prereq/version-check drift from the same review (the "runs on every
  command" / "on run, setup, and doctor" wording). Per the review follow-up, that is
  being reconciled by editing `docs/design.md` to match the as-built behavior, not by
  changing code, so it is not part of this ticket.

## Open Questions

None — well-understood, scoped by the 2026-06-23 review.

## Questions for Research/Planning

- [x] The doctor `Checker` struct (`internal/doctor/doctor.go:23-32`) has `Paths` but
      not `Keychain` or `FileSystem` seams; `cred.Detect` needs both. Plan how to wire
      them in from the App via `internal/cli/doctor.go`. → Added `Keychain`/`FS` fields to
      `Checker`, wired from `app.Keychain`/`app.FS` (already present on the App).
- [x] Decide the severity mapping in doctor's report vocabulary (StatusOK → OK;
      Locked/FileFallback → Problem; Missing → Problem or Warn?) and how it folds into
      doctor's overall verdict and exit status. → OK→OK, Locked/FileFallback→Problem
      (actionable), **Missing→Warn** (non-actionable), unexpected error→Warn.
- [x] Decide where the credential finding sits in the report ordering and its render
      shape (likely a new report section + golden files, mirroring the existing CA /
      Proxy sections). → New `Credential:` section rendered immediately after `CA trust:`,
      `{State, Detail}` shape like `CASection`; golden files regenerated.

## References

- Review: `thoughts/shared/reviews/2026-06-23-codebase-vs-design-gap-review.md` (Gap 2)
- `docs/design.md` — "The proxy and the credential story" (line 530); spike S2 note
  on the locked keychain (line 20)
- `internal/cred/cred.go` — `Detect`, `Status`, the refusal messages
- `internal/cli/run.go:78` — the run-side credential-detection precedent to mirror
- Related: AC-0022 (credential detection), AC-0045 (in-cage credential access),
  AC-0031 (doctor extension)

## Implementation Plan

- Research: `thoughts/shared/research/2026-06-25-AC-0062-doctor-credential-preconditions.md`
- Plan: `thoughts/shared/plans/2026-06-25-AC-0062-doctor-credential-preconditions.md`

## Notes & Updates

### 2026-06-25 — Implemented (Done)
Added a `Credential:` section to `doctor` that calls `cred.Detect` (mirroring the
`checkCA` pattern), sourcing every non-OK message from `cred.Result.Message()` so the
`run` and `doctor` paths can't drift. Wired `Keychain`/`FS` seams onto `doctor.Checker`;
renamed `caGlyph`→`stateGlyph` (now shared by CA and Credential). Each status is covered
by fakes-based unit tests; render is pinned by regenerated goldens; the verdict by
`TestActionable`; the section header by the `doctor_healthy` testscript.

**Severity decision (checkpoint):** `StatusMissing` → non-actionable **Warn**;
`StatusLocked`/`StatusFileFallback` → actionable **Problem**; unexpected error → Warn.
Rationale: doctor is a diagnostic, not a gate, and "not logged in" is a precondition the
user resolves by logging in. Decisively, this keeps doctor's exit code host-independent
for the common "no credential" state — important because doctor's testscripts run the
**real** binary and `OSKeychain` execs `/usr/bin/security` by absolute path, so the four
doctor testscripts now query the real host keychain. With Missing→Warn they stay hermetic
(exit 0) whether or not the host has Claude credentials.

### 2026-06-25
Created from the 2026-06-23 design-conformance review (Gap 2). Scope decision (with
the user): cover the **full** credential section — every non-OK `cred.Detect` outcome
— rather than only the file-fallback case literally named in the review, because
doctor currently surfaces none of them and the design (lines 20 + 530) plus cred's own
package doc already assign all of them to doctor. Complexity is Low: it reuses the
existing, side-effect-free `cred.Detect` and the established doctor section/golden
pattern; the only real work is wiring two seams into the `Checker` and adding a report
section. The companion prereq drift from the same review is being handled as a
design-doc reconciliation, not a code change.
