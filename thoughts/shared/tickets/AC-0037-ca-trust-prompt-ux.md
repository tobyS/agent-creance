# AC-0037: CA trust UX — skip redundant prompts, explain the dialog, point to the cert

**Status:** Open
**Estimated Complexity:** Medium
**Created:** 2026-06-09
**Updated:** 2026-06-09

## Problem Statement

Installing the mitmproxy CA into the login keychain pops a macOS authorization
dialog (Touch ID / password) titled with the generic calling binary name
("security") and the system's fixed text "Du änderst deine Einstellungen für
vertrauenswürdige Zertifikate" (see screenshots). Two problems:

1. **The prompt fires on every `agent-creance setup`.** `Bootstrap`
   (`internal/setup/setup.go`) always calls `InstallCA` →
   `security add-trusted-cert`, which re-triggers the auth dialog even when the
   CA is already installed and trusted. Re-running setup (a normal thing to do)
   is needlessly intrusive.

2. **The prompt is opaque and the installed cert is hard to place.** The user
   is asked to approve a trusted-root change by a binary called "security" with
   no on-screen connection to agent-creance — it looks like something to be
   suspicious of, not to trust. After install, the cert appears in Keychain
   Access only as "mitmproxy", with nothing tying it to agent-creance or
   explaining why it's there.

The macOS authorization dialog's own chrome (the "security" app name and its
body text) is rendered by the OS from the calling binary and is **not**
customizable without replacing the install path with a separate signed helper —
out of scope here. The achievable, high-value improvements are: stop prompting
when trust already holds, explain the prompt in the terminal *before* it
appears, and tell the user afterwards exactly what was installed and where.

## Desired Outcome

When this is complete:

1. Re-running `agent-creance setup` on a machine where the CA is **already
   trusted** completes **without** showing the authorization dialog.
2. When trust is **not** yet in place, setup prints a clear explanation in the
   terminal *before* the dialog appears, so the user understands the otherwise-
   generic "security" prompt is agent-creance installing its egress-proxy CA and
   knows it is expected.
3. After a successful trust install (or when trust already held), setup prints a
   short note telling the user the certificate's name ("mitmproxy"), that it
   lives in the **login keychain**, and how to find or remove it via Keychain
   Access.

## User Stories / Use Cases

- As an operator re-running `setup` (e.g. after upgrading, or to install the
  skill), I want it to skip the trust prompt when the CA is already trusted so
  that I'm not asked for Touch ID / my password for nothing.
- As an operator running `setup` for the first time, I want the terminal to tell
  me a system trust dialog is about to appear and why, so that the generic
  "security" prompt looks expected and trustworthy rather than alarming.
- As an operator, I want setup to tell me the cert's name and where it lives so
  that I can later inspect or remove it in Keychain Access without guessing.

## Acceptance Criteria

- [ ] On a machine where the CA already validates against the system trust store,
      `setup` does **not** call `security add-trusted-cert` and the auth dialog
      does not appear; setup still reports success.
- [ ] Idempotency is gated on **actual trust** (the existing live verification),
      not mere keychain presence: a cert that is present but not trusted is still
      (re-)installed so trust is established.
- [ ] When trust is not yet established, setup prints an explanatory message
      **before** invoking the install (naming agent-creance, the mitmproxy egress
      CA, and that a macOS approval dialog will appear).
- [ ] After a successful install — and on the already-trusted skip path — setup
      prints a note stating the cert name ("mitmproxy"), that it is in the login
      keychain, and how to locate/remove it (Keychain Access or a `security`
      command).
- [ ] First-run behavior is unchanged in outcome: a fresh machine still generates
      the CA, installs trust (with the new pre-prompt message), verifies, and
      ends trusted.
- [ ] A verification failure after install still produces the existing actionable
      error and non-zero exit (the silent-cancel failure mode stays caught).
- [ ] `--no-ca-install` mode is unaffected by the new messaging (no keychain note
      claiming an install that didn't happen).
- [ ] `make test`, `make lint` green; any new/changed user-facing strings that
      are golden-pinned are updated and the golden diff reviewed.

## Out of Scope

- Changing the macOS authorization dialog's app name or body text (OS-rendered
  from the calling binary; would require a separate signed helper).
- Giving the CA a custom/"speaking" Common Name — decided to keep mitmproxy's
  default CA name ("mitmproxy") and improve messaging instead. (Revisit only if
  messaging proves insufficient.)
- Writing a descriptive comment/metadata attribute onto the keychain item —
  decided in favor of a terminal note over on-cert metadata.
- The `doctor` flow (AC-0031) and `run`'s cheap presence check (AC-0025) — their
  trust handling is unchanged; this ticket touches the `setup` path.
- CA rotation/removal commands.

## Open Questions

_None — scope decisions resolved during ticket creation (see Notes)._

## Questions for Research/Planning

- [ ] **Where the verify-first reorder lives.** `Bootstrap` currently does
      EnsureCA → InstallCA → Verify. The new order is EnsureCA → Verify → (if
      untrusted) InstallCA → Verify. Confirm this reordering in `internal/setup`
      and that the throwaway-mitmdump Verify works correctly *before* any trust
      install (it should: it proves trust functionally and needs only the CA file
      from EnsureCA).
- [ ] **Library/CLI seam for the messaging.** The pre-prompt explanation, the
      already-trusted skip note, and the post-install discoverability note are
      user-facing output, which belongs in the CLI layer (`internal/cli/setup.go`),
      not the I/O-free `internal/setup` library. How should the library signal to
      the CLI which path it took (about-to-install-and-prompt vs.
      already-trusted-skip vs. installed-and-verified) — e.g. a richer result
      struct returned from `Bootstrap`, a small reporter/printer interface passed
      in, or the CLI orchestrating the steps itself?
- [ ] **Exact wording** of the pre-prompt explanation and the post-install note
      (cert name, keychain, find/remove instructions). Mirror the tone of the
      existing `--no-ca-install` caveat and `msgUntrusted`.
- [ ] **Test surface.** Which assertions go to the setup library unit tests
      (fakes: Keychain/TLSProber) vs. the hermetic `setup.txtar` CLI test
      (output strings)? Confirm a "verify passes → AddTrustedCert never called"
      assertion is expressible against the `sysdeptest` Keychain fake.

## References

- Screenshots (attached to the request): the macOS auth dialog ("security" /
  trusted-certificate-settings change) and the Keychain Access entry ("mitmproxy"
  certificate in the login keychain).
- `internal/setup/setup.go` — `Bootstrap` / `InstallCA` / `Verify` / `EnsureCA`.
- `internal/sysdep/keychain.go` — `AddTrustedCert` (the `security add-trusted-cert`
  invocation; note its exit code is advisory) and `FindCertificate`.
- `internal/cli/setup.go` — the `setup` command and its current output.
- AC-0026 (CA bootstrap + verification), AC-0028 (`setup` command, including
  `--no-ca-install`), AC-0031 (`doctor`), AC-0001 (S1 spike, CA trust).

## Implementation Plan

_To be filled by `/create_plan`._

## Notes & Updates

### 2026-06-09

Created from a maintainer report (with screenshots): `setup` re-prompts for CA
trust on every run, and the trust dialog / installed cert give the user no clear,
trustworthy connection to agent-creance.

Decisions made during ticket creation:

- **Idempotency = verify-first.** Run the existing live trust verification before
  installing; skip `add-trusted-cert` (and its auth dialog) entirely when the CA
  already validates. Gated on actual trust, not keychain presence, so a
  present-but-untrusted cert is still re-trusted.
- **Trustworthiness = messaging, not rebranding.** Keep mitmproxy's default CA
  name; the macOS dialog chrome can't be customized without a signed helper
  (out of scope). Instead, explain the prompt in the terminal before it appears.
- **Discoverability = terminal note**, not on-cert keychain metadata: after
  install (and on the already-trusted skip), print the cert name, keychain, and
  how to find/remove it.

Complexity (Medium): the verify-first reorder is small, but it needs a clean
library→CLI seam to drive three distinct messaging paths, careful handling of the
`--no-ca-install` and verification-failure branches, and golden/test updates for
the new user-facing strings.
