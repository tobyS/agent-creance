# AC-0059: Host-side integrity — confine config includes, make policy writes atomic, verify CA trust soundly

**Status:** Done
**Estimated Complexity:** Large
**Created:** 2026-06-22
**Updated:** 2026-06-23

## Problem Statement

The 2026-06-22 security review
(`thoughts/shared/reviews/2026-06-22-codebase-quality-review.md`) found three **High**
findings that share a theme: host-side controls that the security model leans on but
that are weaker or less-pinned than the design implies. They are grouped because they
all concern the integrity/soundness of the host-side machinery (config resolution,
the policy file, the CA trust anchor) rather than the live request path.

**F8 — `include:` paths are unconfined.** The config loader
(`internal/config/load.go`, `resolveIncludePath`) resolves `include: /etc/passwd`,
`include: ~/.ssh/id_rsa`, and `include: ../../../x` verbatim, then reads and parses
them — and surfaces file contents in YAML parse-error messages. This is currently
tested *as supported* (`TestResolveFiles_AbsoluteAndHomeIncludes`). For a confinement
tool, a repo-supplied `.agent-creance.yaml` (a cloned project carries one) being able
to pull in any user-readable absolute/`~`/`..` path is an unintended read-and-leak
surface and an escape from the expected "project subtree + global config dir" scope.
Cycle detection and the depth limit themselves are correct and well tested.

**F9 — `allow`/`deny`/`import` config writes are not atomic or locked.** The
in-memory append + parse-and-diff re-validation gate (`internal/config/edit.go`) is
robust, but the read-modify-write to disk in the CLI layer has no file lock and no
temp+rename atomicity. Two concurrent `agent-creance allow/deny` invocations (or one
racing the user's editor) can lose a write: both read the original, each appends its
own rule, the second writer overwrites the first. A dropped `deny_always` is the
dangerous direction — the policy silently loses a block the operator believes is in
force.

**F10 — CA live-verification soundness is unproven where it matters.** `setup`'s
cancelled-dialog trap (`security add-trusted-cert` returns 0 even when the user
cancels) is correctly caught and tested through a *fake* prober
(`internal/setup/setup.go:287`, `TestBootstrapUntrustedReturnsActionableError`). But
the load-bearing property — that the **real** `OSTLSProber.ProbeViaProxy` validates
the re-signed cert against the **system trust store** with no `-k`/`--insecure`, no
`--cacert <mitmproxy-ca>`, and no `--proxy-insecure` — is never exercised by a real
curl. If the real prober is lenient in any of those ways, the entire CA verification
(and the same check inside `doctor`) passes spuriously even when the CA is not
trusted, producing the exact "everything looks fine, then mysterious TLS errors"
failure the verification exists to prevent.

## Desired Outcome

- **F8:** `include:` resolution is confined to a documented, defensible scope (the
  declaring file's directory subtree plus the global config dir), or — if unconfined
  includes are an intentional power-user feature — that trust assumption is made
  explicit in `docs/design.md` and a confinement option exists. A `..`/absolute
  escape is rejected (or warned) rather than silently honored, and file contents do
  not leak into error messages for out-of-scope reads.
- **F9:** Mutating the config (`allow`/`deny`/`import`) is atomic and serialized: a
  concurrent edit cannot drop a rule. The write re-validates against the
  freshly-read bytes, and uses an atomic temp+rename (and a lock) so a crash or race
  never leaves a partial or clobbered policy file.
- **F10:** The real TLS prober is audited and proven to validate only against the
  system trust store; an integration test asserts that an **untrusted** CA yields
  `ProbeUntrusted` (and a trusted one `ProbeTrusted`), so the cancelled-dialog and
  revoked-trust paths are caught by a real curl, not just a scripted fake.

## User Stories / Use Cases

- As an operator cloning an untrusted project, I want its config's `include:` lines
  unable to read arbitrary files from my home directory, so opening a repo in the
  cage doesn't expose `~/.ssh` or `~/.aws`.
- As a developer running `agent-creance allow` while my editor also has the config
  open, I want neither write to silently lose a rule — especially not a `deny_always`.
- As an operator, I want `setup`/`doctor` to tell me the truth about whether the
  mitmproxy CA is actually trusted, so a cancelled keychain prompt is reported, not
  hidden behind a green check.

## Acceptance Criteria

### F8 — include confinement
- [ ] An `include:` that resolves outside the allowed scope (declaring file's subtree
      + global config dir) is rejected with a clear error, or warned and documented as
      an explicit opt-in — decide and document the policy in `docs/design.md`.
- [ ] `..`-traversal and absolute/`~` includes are handled per that policy (not
      silently honored as today).
- [ ] Out-of-scope or unreadable include targets do not echo file contents into error
      output.
- [ ] Existing in-scope include behavior (relative includes, the implicit global, the
      cycle/depth limits) is unchanged.

### F9 — atomic config writes
- [ ] `allow`/`deny`/`import` write the config via an atomic replace (temp file +
      rename) so a partial write can never be observed.
- [ ] Concurrent mutations are serialized (e.g. a file lock around read-modify-write)
      such that two simultaneous `allow`/`deny` runs both land — no lost rule.
- [ ] The write re-validates the rule set against the bytes read at write time (not a
      stale in-memory copy), preserving the existing comment/format-preserving
      guarantee.

### F10 — CA verification soundness
- [ ] `OSTLSProber.ProbeViaProxy` is confirmed (and asserted by test) to use no
      `-k`/`--insecure`, no `--cacert`, and no `--proxy-insecure`, so only the system
      trust store can validate the re-signed cert.
- [ ] An integration test (under the `integration` tag) asserts an untrusted CA →
      `ProbeUntrusted` and a trusted CA → trusted, exercising the real curl path.
- [ ] `setup` and `doctor` report an untrusted/cancelled CA correctly end-to-end.

## Testing Protocol

Per `.claude/tce/profile.md`: table-driven for pure logic, golden for artifacts,
testscript for CLI, fakes for unit, real tools only under the `integration` tag.

- **F8:** add table cases to `internal/config/load_test.go` for `..`, absolute, and
  `~` includes under the chosen policy (rejected/warned), and a case asserting
  out-of-scope read errors don't leak file contents. Add a testscript case if the
  behavior is surfaced through a command.
- **F9:** unit-test the atomic-write/locking helper (temp+rename, lock contention)
  via the `sysdep` filesystem/flock seams; add a testscript or table test simulating
  two sequential-but-interleaved edits and asserting both rules survive (especially a
  `deny_always`). Note: the in-memory append/re-validation in `edit.go` is already
  well covered — the new tests target the on-disk write path.
- **F10:** add an `//go:build integration` test in `internal/setup/` (or
  `internal/sysdep/`) that runs the real prober against a spun-up mitmproxy with (a)
  the CA untrusted and (b) trusted, asserting the two `Probe*` outcomes. Keep the
  fast unit tests (fake prober) as-is. Run via `make test-integration`.
- **Gate:** `make test`, `make lint` green; `make test-integration` for the F10 path
  (document that it needs real mitmproxy/curl); `make build` at the end.

## Out of Scope

- v0.2 secret injection / `op://` resolution (the config edit path here is for plain
  allow/deny/import rules only).
- Broader concurrency hardening of the proxy lock file (that is AC-0061).
- Changing what `import` merges or its review UX — only its write atomicity.

## Open Questions

- **F8 policy:** confine-and-reject vs warn-and-allow-with-opt-in. Proposed default:
  confine to the declaring file's subtree + the global config dir, reject escapes
  with a clear error; revisit if a real cross-tree include use case exists. Confirm
  this doesn't break the implicit global-config include (which lives in
  `~/.config/`, outside a project subtree — it must stay allowed).

## Questions for Research/Planning

- [ ] F8 — how `resolveIncludePath` composes paths today and the cleanest place to
      enforce a scope check; how the implicit global include is represented so it
      isn't caught by the new rule.
- [ ] F8 — where YAML parse errors echo file content, to scrub out-of-scope reads.
- [ ] F9 — where the CLI actually writes the config after `edit.AppendRule` (the
      review noted the write is in the command layer, not `edit.go`); whether a
      `sysdep` atomic-write/flock seam already exists or needs adding.
- [ ] F10 — read `internal/sysdep/tlsprober.go` (`OSTLSProber`) to confirm the curl
      flags; how `setup`'s `Verify`/`Bootstrap` and `doctor` consume the outcome; how
      the existing integration tests spin up mitmproxy so the new one can reuse it.

## References

- Review: `thoughts/shared/reviews/2026-06-22-codebase-quality-review.md` (F8, F9, F10).
- F8: `internal/config/load.go` (`resolveIncludePath`, include resolution),
  `internal/config/load_test.go` (`TestResolveFiles_AbsoluteAndHomeIncludes`).
- F9: `internal/config/edit.go` (`AppendRule`, `validateAppend`),
  `internal/cli/allow.go`, `internal/cli/deny.go`, `internal/cli/import.go`,
  `internal/cli/mutate.go` (the write path).
- F10: `internal/setup/setup.go:223-295` (`Verify`/`Bootstrap`),
  `internal/sysdep/tlsprober.go` (`OSTLSProber.ProbeViaProxy`),
  `internal/setup/verify_test.go`.
- `docs/design.md` — "The configuration" (`include:` semantics), "Post-install CA
  verification", "The proxy and the credential story".

## Implementation Plan

- Research: `thoughts/shared/research/2026-06-23-AC-0059-host-side-integrity.md`
- Plan: `thoughts/shared/plans/2026-06-23-AC-0059-host-side-integrity.md`

## Notes & Updates

### 2026-06-22

- Created from the 2026-06-22 security review. Groups the three **High** host-side
  integrity findings: F8 (include confinement), F9 (atomic/locked config writes),
  F10 (CA verification soundness).
- Grouped by severity/theme at the user's request. Marked Large — three distinct but
  related subsystems (config loader, CLI write path, setup/tlsprober).
- All three were review-reported; F10 in particular requires reading the real
  `OSTLSProber` during research to confirm the curl flags before writing the fix.

### 2026-06-23

- Status → In Progress. Research and plan committed. Checkpoint decisions: F8
  confine-and-reject (declaring file's subtree + `~/.config/`), F9 out-of-tree
  per-target advisory lock.
- Status → Done. All three implemented and verified:
  - **F8** — `resolveIncludePath` now confines includes to the declaring file's
    subtree + `~/.config`, rejecting absolute/`~`/`..` escapes with
    `ErrIncludeOutOfScope` before any read; documented in `docs/design.md`.
    (Lexical check; symlink-target confinement explicitly out of scope, backed by
    the in-cage config-write deny.)
  - **F9** — `allow`/`deny`/`import` wrap their read-modify-write in
    `withConfigLock` (new out-of-tree `state.ConfigLock`, keyed by target hash),
    re-reading under the lock; atomic temp+rename unchanged. `-race` test proves a
    concurrent allow+deny both land.
  - **F10** — `OSTLSProber` audited sound; `curlProbeArgs` extracted for a static
    "no `-k`/`--cacert`/`--proxy-insecure`" assertion; integration test proves a
    fresh untrusted CA → `ProbeUntrusted` (and trusted → trusted) against real
    mitmproxy/curl.
  - Gate: `make test` + `make lint` green; F10 integration tests pass (need real
    mitmproxy/curl, run under `make test-integration`); `make build` green.
