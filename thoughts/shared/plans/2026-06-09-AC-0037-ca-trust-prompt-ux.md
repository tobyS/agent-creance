---
date: 2026-06-09
ticket: AC-0037
title: "CA trust UX — skip redundant prompts, explain the dialog, point to the cert"
status: done
branch: main
research: thoughts/shared/research/2026-06-09-AC-0037-ca-trust-prompt-ux.md
tags: [plan, setup, ca-trust, keychain, cli-ux, AC-0037]
---

# AC-0037 — CA trust UX (skip redundant prompts, explain the dialog, point to the cert)

## Overview

`agent-creance setup` re-triggers the macOS authorization dialog (Touch ID /
password) on **every** run because `Bootstrap` always calls `InstallCA` →
`security add-trusted-cert`, regardless of whether the mitmproxy CA is already
trusted. The dialog's chrome (the generic "security" app name and OS body text)
can't be customised without a signed helper (out of scope), and the installed
cert appears in Keychain Access only as "mitmproxy" with nothing tying it to
agent-creance.

This makes three achievable, high-value improvements to the **`setup` path only**:

1. **Verify-first idempotency** — reorder `Bootstrap` to `EnsureCA → Verify → (if
   untrusted) InstallCA → Verify`, so a machine where the CA already validates
   skips `add-trusted-cert` and its dialog entirely. Gated on *actual trust* (the
   existing live verification), not keychain presence.
2. **Explain the prompt before it appears** — when trust is not yet in place, the
   CLI prints a pre-prompt explanation (naming agent-creance, the mitmproxy egress
   CA, and the upcoming macOS approval dialog) *immediately before* the install.
3. **Discoverability note** — after a successful install, and on the
   already-trusted skip path, the CLI prints the cert name ("mitmproxy"), that it
   lives in the **login keychain**, and how to find/remove it.

**Seam (decided at checkpoint):** `Bootstrap` keeps the security-critical
verify-first sequencing in the tested `internal/setup` library and gains a
`beforeInstall func()` hook (fired right before the dialog) plus a
`BootstrapResult{AlreadyTrusted bool}` return. The CLI (`internal/cli/setup.go`)
owns all user-facing strings: it passes the pre-prompt as the hook and prints the
path-appropriate success line + discoverability note based on the result. The
integration test continues to exercise the real end-to-end flow.

## Current state

- `internal/setup/setup.go:252-268` (`Bootstrap`): `EnsureCA → InstallCA →
  Verify`; `InstallCA` runs unconditionally, so the dialog fires every run.
  Returns only `error`.
- `internal/setup/setup.go:153-158` (`InstallCA`), `:223-246` (`Verify`),
  `:84-101` (`EnsureCA`): the primitives, individually tested. `Verify` needs only
  the CA file and returns `Result{Status}` (trusted/untrusted as data).
- `internal/cli/setup.go:38-70` (`runSetup`): the install branch prints an
  unconditional "Trusting the mitmproxy CA (you may be prompted…)…" line
  (`setup.go:53`), calls `inst.Bootstrap(ctx)`, then prints "✓ CA installed and
  verified." The `--no-ca-install` branch (`:45-51`) prints `caCaveat` only.
- `internal/sysdep/sysdeptest/tlsprober.go`: `FakeTLSProber` returns a single
  fixed `Outcome` for every `ProbeViaProxy` call — cannot model "untrusted then
  trusted" needed by the two-`Verify` install path.
- `internal/setup/verify_test.go:127-153`: `TestBootstrapHappyPath` (default
  trusted prober, asserts `AddedCerts==1`) and
  `TestBootstrapUntrustedReturnsActionableError` call `Bootstrap(ctx)`.
- `internal/cli/setup_test.go:67-177`: `TestSetupDefault` etc. drive `runSetup`
  against fakes; `TestSetupDefault`/`TestSetupNoSkill` use the default trusted
  prober and assert `AddedCerts==1` and the "CA installed and verified" string.
- `internal/setup/setup_integration_test.go:82`: `TestBootstrapLive` calls
  `inst.Bootstrap(context.Background())`.
- `internal/setupcheck/setupcheck.go:34`: `CACommonName = "mitmproxy"` — reuse for
  the cert-name note (avoids drift).

## Desired end state

- Re-running `setup` on an already-trusted machine completes **without** the
  dialog: `Verify` passes first, `AddTrustedCert` is never called, and setup still
  reports success plus the discoverability note.
- A present-but-untrusted CA is still (re-)installed (gated on real trust).
- On the install path, a pre-prompt explanation prints *before* the dialog; after
  a successful install the discoverability note prints.
- `--no-ca-install` is unchanged (no install/keychain note); a verification
  failure after install still returns the actionable `msgUntrusted` and exits
  non-zero; `doctor` and `run` are untouched.
- `make test` and `make lint` green; new user-facing strings asserted in
  `cli/setup_test.go`; goldens reviewed (only `verify_untrusted.golden` exists for
  setup — no new library golden expected unless a `Result.Message` is added, which
  it is not).

## What we are NOT doing

- Not changing the macOS dialog's app name / body text (needs a signed helper).
- Not giving the CA a custom Common Name (keep "mitmproxy"; improve messaging).
- Not writing comment/metadata onto the keychain item (terminal note instead).
- Not touching `doctor` (AC-0031), `run`'s cheap `setupcheck` probe (AC-0025), or
  adding CA rotation/removal commands.
- Not adding a reporter interface or moving sequencing into the CLI (the chosen
  seam keeps `Bootstrap` as the single tested sequencing point).

## Implementation approach

Three small, ordered changes: (1) extend the `FakeTLSProber` so tests can script
the two-probe install path; (2) rework `Bootstrap` (library) to verify-first with
a hook + result; (3) wire the CLI messaging. Each phase is independently
verifiable with `make test`.

---

## Phase 1 — Extend `FakeTLSProber` with a per-call outcome sequence

The verify-first install path probes twice (untrusted pre-install, trusted
post-install). The fake must script an ordered sequence while staying
backward-compatible with the existing single-`Outcome` tests.

### Changes

**`internal/sysdep/sysdeptest/tlsprober.go`**

- Add a field `Outcomes []sysdep.ProbeOutcome`. When non-empty, `ProbeViaProxy`
  consumes one entry per call (in order) and falls back to `Outcome` once
  exhausted. `Err` still short-circuits to `ProbeError` as today.
- Document it: "models a verify-first flow where the first probe is untrusted
  (pre-install) and the second trusted (post-install)."

```go
func (f *FakeTLSProber) ProbeViaProxy(_ context.Context, proxyURL, targetURL string) (sysdep.ProbeOutcome, error) {
	f.Calls = append(f.Calls, TLSProbe{ProxyURL: proxyURL, TargetURL: targetURL})
	if f.Err != nil {
		return sysdep.ProbeError, f.Err
	}
	if len(f.Outcomes) > 0 {
		out := f.Outcomes[0]
		f.Outcomes = f.Outcomes[1:]
		return out, nil
	}
	return f.Outcome, nil
}
```

**`internal/sysdep/sysdeptest/tlsprober_test.go`**

- Add a case asserting the sequence is consumed in order and then falls back to
  `Outcome`; assert existing single-`Outcome` behaviour is unchanged when
  `Outcomes` is empty.

### Success criteria

#### Automated
- [x] `go build ./...` passes.
- [x] `make test` passes (existing prober/setup tests still green — `Outcomes`
      defaults empty, so the fallback path is identical to today).

#### Manual
- [ ] None.

---

## Phase 2 — Rework `Bootstrap` to verify-first with a hook + result

### Changes

**`internal/setup/setup.go`**

- Add a result type:

```go
// BootstrapResult reports which path Bootstrap took, so the CLI can print the
// matching message. AlreadyTrusted is true when the live verification passed
// before any install — i.e. the keychain authorization dialog was skipped.
type BootstrapResult struct {
	AlreadyTrusted bool
}
```

- Rework `Bootstrap` to verify-first and accept a `beforeInstall func()` hook
  invoked immediately before the (dialog-popping) `InstallCA`:

```go
// Bootstrap is the end-to-end CA flow the `setup` command (AC-0028) drives,
// idempotent on real trust: it generates the CA if needed, then verifies trust
// BEFORE touching the keychain. If the CA already validates it returns
// {AlreadyTrusted:true} without calling security add-trusted-cert (no auth
// dialog). Otherwise it calls beforeInstall (if non-nil) so the caller can warn
// about the upcoming dialog, installs the CA, and re-verifies; a failed
// post-install verification is returned as an error carrying msgUntrusted.
func (i *Installer) Bootstrap(ctx context.Context, beforeInstall func()) (BootstrapResult, error) {
	certPath, err := i.EnsureCA(ctx)
	if err != nil {
		return BootstrapResult{}, err
	}
	// Verify-first: prove trust functionally before any keychain write, so an
	// already-trusted CA skips add-trusted-cert and its authorization dialog.
	switch res, err := i.Verify(ctx); {
	case err != nil:
		return BootstrapResult{}, err
	case res.OK():
		return BootstrapResult{AlreadyTrusted: true}, nil
	}
	if beforeInstall != nil {
		beforeInstall()
	}
	if err := i.InstallCA(certPath); err != nil {
		return BootstrapResult{}, err
	}
	res, err := i.Verify(ctx)
	if err != nil {
		return BootstrapResult{}, err
	}
	if !res.OK() {
		return BootstrapResult{}, errors.New(res.Message())
	}
	return BootstrapResult{AlreadyTrusted: false}, nil
}
```

(Note: the `switch` with no tag using the first `Verify` keeps the early-return
shape readable; an `if/else` is equally fine — match surrounding style.)

**`internal/setup/verify_test.go`**

- Replace `TestBootstrapHappyPath` with two tests:
  - `TestBootstrapAlreadyTrusted`: default trusted prober → `res.AlreadyTrusted ==
    true`, `len(AddedCerts) == 0`, exactly one `prober.Calls`, and a
    `beforeInstall` spy is **not** called.
  - `TestBootstrapFreshInstall`: `prober.Outcomes = []sysdep.ProbeOutcome{
    sysdep.ProbeUntrusted, sysdep.ProbeTrusted}` → `res.AlreadyTrusted == false`,
    `len(AddedCerts) == 1`, two `prober.Calls`, `beforeInstall` called exactly once.
- Update `TestBootstrapUntrustedReturnsActionableError`: keep `Outcome =
  ProbeUntrusted` (no `Outcomes`), so first verify untrusted → install → second
  verify untrusted → error `msgUntrusted`; assert `len(AddedCerts) == 1` and the
  `beforeInstall` spy was called once. Update the call to the new signature.

**`internal/setup/setup_integration_test.go`**

- Update `TestBootstrapLive` to `res, err := inst.Bootstrap(context.Background(),
  nil)`; `require.NoError`. (On an already-trusted host it now skips install and
  returns `{AlreadyTrusted:true}` — still a pass; optionally log which path.)

### Success criteria

#### Automated
- [x] `go build ./...` passes (all `Bootstrap` callers updated).
- [x] `make test` passes; new `TestBootstrapAlreadyTrusted` / `TestBootstrapFreshInstall`
      assert the skip vs install paths and the hook.

#### Manual
- [ ] None (integration test is opt-in; not required for unit-suite green).

---

## Phase 3 — Wire the path-aware CLI messaging

### Changes

**`internal/cli/setup.go`**

- Import `internal/setupcheck` for `CACommonName` (already imported in the package
  via `run.go`; add to this file's import block).
- Define the new strings near `caCaveat`. Mirror the tone of `caCaveat` /
  `msgUntrusted` (deterministic, concrete, actionable):

```go
// msgPrePrompt is printed immediately before the keychain authorization dialog
// (install path only), so the generic OS "security" prompt looks expected.
const msgPrePrompt = `agent-creance needs to trust the mitmproxy CA it uses to filter the cage's
network egress. macOS will now show an authorization dialog (titled "security")
asking you to allow a trusted-certificate change — that is agent-creance adding
its egress-proxy CA to your login keychain. Approve it with Touch ID or your
password to continue.`

// keychainNote tells the user what was installed and where, on both the install
// and already-trusted paths. CACommonName keeps the cert name in sync with the
// run/setupcheck presence probe.
func keychainNote() string {
	return fmt.Sprintf(`The %q certificate is trusted in your login keychain. To inspect or remove it,
open Keychain Access (login keychain → Certificates) and search for %q, or run:
  security delete-certificate -c %q ~/Library/Keychains/login.keychain-db`,
		setupcheck.CACommonName, setupcheck.CACommonName, setupcheck.CACommonName)
}
```

- Rewrite the CA install branch of `runSetup` (replacing the unconditional
  "Trusting…" line + `Bootstrap(ctx)` + "✓ CA installed and verified."):

```go
} else {
	fmt.Fprintln(app.Stdout, "Checking whether the mitmproxy CA is already trusted…")
	res, err := inst.Bootstrap(ctx, func() {
		fmt.Fprintln(app.Stdout, msgPrePrompt)
	})
	if err != nil {
		return err // carries the actionable Message; Main → exit 1
	}
	if res.AlreadyTrusted {
		fmt.Fprintln(app.Stdout, "✓ mitmproxy CA already trusted — skipped the keychain prompt.")
	} else {
		fmt.Fprintln(app.Stdout, "✓ CA installed and verified.")
	}
	fmt.Fprintln(app.Stdout, keychainNote())
}
```

**`internal/cli/setup_test.go`**

- Rename/repurpose `TestSetupDefault` → `TestSetupAlreadyTrusted`: default trusted
  prober ⇒ skip path. Assert `len(AddedCerts) == 0`, exactly one `prober.Calls`,
  stdout contains "already trusted" and the keychain note ("mitmproxy", "login
  keychain", "Keychain Access"), stdout does **not** contain `msgPrePrompt`'s
  distinctive text, and the skill is installed.
- Add `TestSetupFreshInstall`: `f.prober.Outcomes = []sysdep.ProbeOutcome{
  sysdep.ProbeUntrusted, sysdep.ProbeTrusted}`. Assert `len(AddedCerts) == 1`, two
  `prober.Calls`, stdout contains the pre-prompt text ("authorization dialog"),
  "CA installed and verified", and the keychain note; skill installed.
- `TestSetupNoSkill`: set `f.prober.Outcomes = {ProbeUntrusted, ProbeTrusted}` to
  keep exercising a real install (so `AddedCerts==1` stays meaningful); assert the
  skill is **not** written and the skill-skipped notice prints.
- `TestSetupVerifyFailure`: unchanged outcome — keep `Outcome = ProbeUntrusted`
  (no `Outcomes`); first verify untrusted → install → second untrusted → error.
  Assert the actionable message, `AddedCerts==1`, and that the skill is not
  written. (Pre-prompt also printed, but the failure assertion is the point.)
- `TestSetupNoCAInstall` / `TestSetupBothOptOuts`: unchanged (no verify, no
  install, no keychain note — confirm stdout does **not** contain the keychain
  note text, guarding the "no install note when nothing was installed" criterion).

### Success criteria

#### Automated
- [x] `go build ./...` passes.
- [x] `make test` passes; CLI tests assert the skip path (no `AddTrustedCert`,
      already-trusted message), the install path (pre-prompt before install,
      installed message), the keychain note on both, and its absence under
      `--no-ca-install`.
- [x] `make lint` passes.
- [x] `make golden` produces no unexpected diff (no new setup golden expected).

#### Manual
- [x] Re-read the final `msgPrePrompt` / `keychainNote` wording for tone against
      `caCaveat` and `msgUntrusted`.

---

## Phase 4 — Verification & ticket close

### Changes

- Re-run the full suite and lint from the repo root.
- Tick the ticket's Acceptance Criteria; set AC-0037 **Status: Done** in
  `thoughts/shared/tickets/AC-0037-ca-trust-prompt-ux.md` with a closing note.
- (Docs: `docs/design.md:468` already describes the live verification; the
  verify-first reorder is a behavioural refinement of the same step. Add a one-line
  note there only if it reads as stale — optional, low priority.)

### Success criteria

#### Automated
- [x] `make test` and `make lint` green.

#### Manual
- [x] Walk each ticket Acceptance Criterion against the diff; all satisfied.

---

## Testing strategy

- **Library (`internal/setup`):** verify-first paths via the `FakeTLSProber`
  sequence — already-trusted skip (no `AddTrustedCert`, hook not called), fresh
  install (one `AddTrustedCert`, two probes, hook called once), post-install
  verification failure (actionable error). These are the authoritative tests for
  the sequencing.
- **CLI (`internal/cli/setup_test.go`):** the user-facing output and orchestration
  per path, driven through `runSetup` against the fakes (the established pattern;
  `cli.Main` can't run setup hermetically). Substring assertions for the
  pre-prompt, the already-trusted line, and the keychain note; negative assertions
  for `--no-ca-install`.
- **Fake (`sysdeptest/tlsprober_test.go`):** the new `Outcomes` sequence + fallback.
- **Integration (opt-in):** `TestBootstrapLive` still drives the real
  generate/verify/install/verify against live tools; not part of `make test`.

## References

- Research: `thoughts/shared/research/2026-06-09-AC-0037-ca-trust-prompt-ux.md`
- Ticket: `thoughts/shared/tickets/AC-0037-ca-trust-prompt-ux.md`
- `internal/setup/setup.go:252` (`Bootstrap`), `:153` (`InstallCA`), `:223` (`Verify`)
- `internal/cli/setup.go:38` (`runSetup`), `:76` (`caCaveat`)
- `internal/sysdep/sysdeptest/tlsprober.go` (`FakeTLSProber`)
- `internal/setupcheck/setupcheck.go:34` (`CACommonName`)
- `docs/design.md:468` ("Post-install CA verification")
