---
date: 2026-06-09
ticket: AC-0037
title: "CA trust UX — skip redundant prompts, explain the dialog, point to the cert"
status: complete
tags: [research, setup, ca-trust, keychain, cli-ux]
git_commit: 0a9fef8e5fccbeb09ebe323aded95628ddc29dc7
---

# Research: AC-0037 — CA trust UX (skip redundant prompts, explain the dialog, point to the cert)

## Research question

How can `agent-creance setup` (1) stop re-triggering the macOS authorization
dialog when the mitmproxy CA is already trusted, (2) explain the otherwise-opaque
"security" prompt in the terminal *before* it appears, and (3) tell the user
afterwards what was installed and where — without touching the OS-rendered dialog
chrome, the `doctor`/`run` trust paths, or the `--no-ca-install` mode?

## Summary

The ticket's three resolved scope decisions map cleanly onto the existing code:

1. **Skip redundant prompts = verify-first.** Today `Bootstrap`
   (`internal/setup/setup.go:252`) always runs `EnsureCA → InstallCA → Verify`, so
   `security add-trusted-cert` (and its auth dialog) fires on every `setup`. The
   fix is to reorder to `EnsureCA → Verify → (if untrusted) InstallCA → Verify`.
   The live `Verify` already proves *actual trust* functionally (spawns a throwaway
   mitmdump, curls `https://example.com` through it), which is exactly the
   "gated on real trust, not keychain presence" criterion the ticket demands. It
   needs only the CA file from `EnsureCA`, so running it before any install is
   safe (it materialises nothing new — `EnsureCA` already produced the CA).

2. **Trustworthy prompt = terminal messaging.** The macOS dialog chrome can't be
   customised (out of scope). The achievable win is a pre-prompt explanation
   printed in the CLI layer *immediately before* the install call, plus a
   discoverability note after. These are user-facing strings, so per the project's
   "logic packages are I/O-free" convention they belong in `internal/cli/setup.go`,
   not `internal/setup`.

3. **Discoverability = terminal note.** After a successful install — and on the
   already-trusted skip path — print the cert name ("mitmproxy"), that it lives in
   the **login keychain**, and how to find/remove it via Keychain Access or a
   `security` command.

The one real design decision is the **library→CLI seam**: how `internal/setup`
tells `internal/cli` which of three paths it took (already-trusted-skip /
about-to-install-and-prompt / installed-and-verified) so the CLI can interleave
the right messages — *and crucially* emit the pre-prompt **before** the dialog
appears (which happens mid-`Bootstrap` today). See "Open design decision" below.

`doctor` and `run` are genuinely untouched: `doctor` calls `CAGenerated` +
`Verify` directly (not `Bootstrap`), and `run` uses the cheap
`setupcheck.Verify` keychain-presence probe. Only the `setup` path changes.

## Detailed findings

### The current bootstrap flow (the thing that re-prompts)

`Bootstrap` is the end-to-end flow the `setup` command drives
(`internal/setup/setup.go:252-268`):

```go
func (i *Installer) Bootstrap(ctx context.Context) error {
	certPath, err := i.EnsureCA(ctx)      // generate CA if absent (idempotent)
	if err != nil { return err }
	if err := i.InstallCA(certPath); err != nil { return err }  // ← always prompts
	res, err := i.Verify(ctx)             // live trust proof
	if err != nil { return err }
	if !res.OK() { return errors.New(res.Message()) }
	return nil
}
```

`InstallCA` (`internal/setup/setup.go:153`) unconditionally calls
`kc.AddTrustedCert(certPath)` → `security add-trusted-cert`
(`internal/sysdep/keychain.go:113`), which pops the Touch ID / password dialog
**every run**, even when the CA already validates. That is the problem.

The building blocks needed for verify-first all already exist and are individually
unit-tested:

- `EnsureCA` (`setup.go:84`) — idempotent; returns the CA path, generating it via a
  throwaway mitmdump only if absent. Tested in `setup_test.go`.
- `Verify` (`setup.go:223`) — live trust proof; returns `Result{Status}` where
  `StatusTrusted`/`StatusUntrusted` are *data*, not errors (a non-nil error is
  reserved for environment failures). Tested in `verify_test.go`.
- `InstallCA` (`setup.go:153`) — the write side; a nil error means the command
  ran, NOT that trust applied. Tested in `setup_test.go`.

So the verify-first reorder is a re-sequencing of existing, tested primitives —
not new behaviour in any of them.

### Why `Verify`-before-`InstallCA` is safe on a fresh machine

`Verify` spawns a bare mitmdump and curls through it; it needs the CA *file* (from
`EnsureCA`) but does not need the CA to be *trusted* to run — an untrusted CA just
yields `StatusUntrusted` (the chain fails to validate), which is precisely the
signal to proceed with `InstallCA`. On a fresh machine: `EnsureCA` generates the
CA, the first `Verify` returns untrusted (never installed), so `InstallCA` runs
(with the new pre-prompt), and the second `Verify` returns trusted. First-run
*outcome* is unchanged; only an extra (cheap, local) verify is added before
install. (Confirms ticket planning question #1.)

### The CLI command today

`runSetup` (`internal/cli/setup.go:38-70`) is the testable body. The CA branch:

```go
if noCAInstall {
	if _, err := inst.EnsureCA(ctx); err != nil { ... }
	fmt.Fprintln(app.Stdout, caCaveat)
} else {
	fmt.Fprintln(app.Stdout, "Trusting the mitmproxy CA (you may be prompted for keychain access)…")
	if err := inst.Bootstrap(ctx); err != nil { return err }
	fmt.Fprintln(app.Stdout, "✓ CA installed and verified.")
}
```

The existing "you may be prompted for keychain access" line (`setup.go:53`) is
printed unconditionally before `Bootstrap` — but it always claims a prompt is
coming, even when verify-first will skip it. This line is what the new
path-aware messaging replaces.

`--no-ca-install` (`setup.go:45-51`) ensures the PEM exists and prints `caCaveat`
(`setup.go:76-84`) — it must stay free of any install/keychain note (acceptance
criterion: "no keychain note claiming an install that didn't happen").

### Existing message tone to mirror

- `msgUntrusted` (`internal/setup/setup.go:209`): a deterministic, golden-pinned,
  actionable sentence ending in "Re-run `agent-creance setup`."
- `caCaveat` (`internal/cli/setup.go:76`): a multi-paragraph raw-string caveat
  naming concrete env vars and the `gh` gap, ending with the remediation command.
- The setupcheck refusal messages (`internal/setupcheck/setupcheck.go:83-90`):
  short "X has not happened — run `agent-creance setup`." form.

The new pre-prompt and discoverability strings should match this register:
deterministic, concrete (name `agent-creance`, "mitmproxy" CA, login keychain),
end with an actionable pointer. (Ticket planning question #3.) The cert common
name is already a shared constant: `setupcheck.CACommonName = "mitmproxy"`
(`internal/setupcheck/setupcheck.go:34`) — reuse it rather than hardcoding.

### Test surface (ticket planning question #4)

Two layers, matching how setup is already tested:

- **`internal/cli/setup_test.go`** (the primary surface for this ticket): drives
  `runSetup` directly against the `sysdep` fakes (`cli.Main` wires the real OS
  seams, so a bare `setup` can't run hermetically — same limitation as
  `run_missing_prereq.txtar`). This is where the new orchestration + output
  strings get asserted. The key new assertion — **"already trusted ⇒
  `AddTrustedCert` never called"** — is directly expressible: seed
  `FakeTLSProber` with `ProbeTrusted` (the default) and assert
  `len(f.kc.AddedCerts) == 0`. The `FakeKeychain` records every `AddTrustedCert`
  in `AddedCerts` (`internal/sysdep/sysdeptest/keychain.go:91-94`), and
  `FakeTLSProber.Outcome` controls trusted/untrusted
  (`prober.Outcome = sysdep.ProbeUntrusted`).
- **`internal/setup` unit tests** (`setup_test.go` / `verify_test.go`): if the
  verify-first sequencing lives in the library (e.g. a reworked `Bootstrap`),
  add/adjust `TestBootstrap*` to assert the skip path (`AddedCerts` empty when the
  first `Verify` is trusted) and the install path (one `AddedCert`, two `Verify`
  probes when first untrusted). `TestBootstrapUntrustedReturnsActionableError`
  must still pass (verification failure after install → `msgUntrusted`).

The existing CLI tests pin output substrings (`strings.Contains(got, "CA installed
and verified")`, `setup_test.go:85`); the new path messages get the same
substring assertions. No golden file is currently used for `setup`'s CLI output
(only `verify_untrusted.golden` for `msgUntrusted`); the new strings are pinned by
substring in `setup_test.go`, so "golden review" mainly means re-running `make
golden` if any *library* string (e.g. a new `Result.Message`) gets pinned.

### What is NOT affected (verified)

- **`doctor`** (`internal/doctor/doctor.go:53-69`): `checkCA` calls
  `CAGenerated()` then `Verify(ctx)` directly — never `Bootstrap`, never
  `InstallCA`. Reordering/reworking `Bootstrap` cannot change doctor's behaviour.
- **`run`** (`internal/cli/run.go:60`): uses `setupcheck.Verify` (cheap keychain
  presence via `FindCertificate("mitmproxy")`), entirely separate from the
  install path.
- **`--no-ca-install`**: separate branch in `runSetup`; the new messaging is added
  only to the install branch.
- **The integration test** (`setup_integration_test.go:82`) calls
  `inst.Bootstrap(...)`. If `Bootstrap` keeps its signature it still works
  (already-trusted hosts will now skip install and return success); if its
  signature changes (e.g. to return a result or take a callback), this call site
  must be updated.

## Code references

- `internal/setup/setup.go:252` — `Bootstrap` (the EnsureCA→InstallCA→Verify flow to reorder).
- `internal/setup/setup.go:153` — `InstallCA` (the unconditional `AddTrustedCert` call).
- `internal/setup/setup.go:223` — `Verify` (live trust proof; the verify-first gate).
- `internal/setup/setup.go:84` — `EnsureCA` (idempotent CA generation).
- `internal/setup/setup.go:209` — `msgUntrusted` (tone reference; golden-pinned).
- `internal/cli/setup.go:38` — `runSetup` (where the path-aware messaging goes).
- `internal/cli/setup.go:53` — the unconditional "you may be prompted" line to replace.
- `internal/cli/setup.go:76` — `caCaveat` (`--no-ca-install`; must stay install-note-free).
- `internal/sysdep/keychain.go:113` — `OSKeychain.AddTrustedCert` (the `security add-trusted-cert` invocation; exit code advisory).
- `internal/sysdep/sysdeptest/keychain.go:91` — `FakeKeychain.AddTrustedCert` records `AddedCerts` (the skip assertion).
- `internal/setupcheck/setupcheck.go:34` — `CACommonName = "mitmproxy"` (reuse for the cert-name note).
- `internal/cli/setup_test.go:67` — `TestSetupDefault` (primary CLI test to extend).
- `internal/setup/verify_test.go:127` — `TestBootstrapHappyPath` / `TestBootstrapUntrusted...` (library Bootstrap tests).
- `internal/doctor/doctor.go:53` — `checkCA` (proves doctor is unaffected).
- `docs/design.md:468` — "Post-install CA verification" (the live-verify rationale).

## Open design decision (for the question checkpoint)

**How should `internal/setup` signal the path taken so `internal/cli` can print
the right messages — including the pre-prompt that must appear *before* the
dialog?** The dialog currently fires inside `Bootstrap`, so the CLI cannot print
"a dialog is about to appear" before it unless the library either (a) hands the
decision back to the CLI, or (b) calls a hook at the right moment. Three viable
shapes (the ticket enumerated these):

1. **CLI orchestration** — CLI calls `EnsureCA` → `Verify` → (if untrusted)
   print pre-prompt, `InstallCA`, `Verify` itself, using the already-exported
   primitives. Pro: no new types/interfaces; output and timing live naturally in
   the CLI; `runSetup` is *already* unit-tested against fakes so the orchestration
   is covered. Con: the security-critical sequencing moves out of the library;
   `Bootstrap` becomes used only by the integration test (drift risk).

2. **`Bootstrap` returns a result + takes a pre-install hook** — e.g.
   `Bootstrap(ctx, beforeInstall func()) (BootstrapResult, error)` where
   `BootstrapResult{AlreadyTrusted bool}` tells the CLI which success line +
   discoverability note to print, and `beforeInstall` is invoked immediately
   before `InstallCA` so the CLI prints the pre-prompt at the right moment.
   Pro: keeps the verify-first sequencing in the tested library and exercised by
   the integration test; all strings stay in the CLI. Con: a callback param is
   slightly less idiomatic than this codebase's interface seams.

3. **Reporter/printer interface passed into `Bootstrap`** — a small interface
   (`AboutToInstall()`, `AlreadyTrusted()`, `Installed()`) the CLI implements.
   Pro: most "seam-like", matches the sysdep injection philosophy. Con: heaviest
   machinery for three one-line messages; pushes message *triggering* into the
   library even though the strings stay in the CLI.

**Recommendation: option 2** (result struct + `beforeInstall` hook). It keeps the
security-critical verify-first sequencing in one tested place (and keeps the
integration test exercising the real flow end-to-end), satisfies the
pre-prompt-before-dialog timing constraint, and keeps every user-facing string in
the CLI per the project's I/O-free-library convention — at the cost of one small
callback parameter. Option 1 is the close runner-up if we prefer zero new
library surface and accept that `Bootstrap` becomes integration-test-only.

## Related research / prior tickets

- AC-0026 — CA bootstrap + verification (introduced `EnsureCA`/`InstallCA`/`Verify`/`Bootstrap`).
- AC-0028 — the `setup` command, including `--no-ca-install`.
- AC-0031 — `doctor` (reuses `CAGenerated` + `Verify`; out of scope here).
- AC-0025 — `run`'s cheap `setupcheck` presence probe (out of scope here).
- AC-0001 — S1 spike: CA trust / interception works (`thoughts/shared/research/2026-06-04-s1-ca-trust.md`).
