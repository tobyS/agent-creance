# AC-0038: init bootstraps host setup when it hasn't been done

**Status:** Done
**Estimated Complexity:** Medium
**Created:** 2026-06-09
**Updated:** 2026-06-10

## Problem Statement

Onboarding is split across two commands with different scopes:

- `init` (AC-0029) — **per project**: writes a starter `.agent-creance.yaml`,
  pure filesystem, no sudo, no external tools.
- `setup` (AC-0028) — **per host**: trusts the mitmproxy CA (one-time keychain /
  sudo dialog) and installs the Claude Code skill.

The split is principled, but the two-step sequence is **hard to remember**. A
new user naturally runs `init` first, edits their config, and only discovers the
host-level `setup` step when `run` refuses with a pointer to it. The refusal is
correct (`run` already gates on `setupcheck`), but the round-trip — scaffold,
try to run, get refused, run setup, try again — is avoidable friction and a
source of confusion.

We already have the cheap primitive to remove it: `setupcheck.Verify`
(`internal/setupcheck`) reports whether host setup has run (mitmproxy CA present
in the login keychain + skill file present) **without** triggering sudo — it is
the same gate `run` uses. `init` can call it as a guard and, when host setup is
missing, run `setup` as part of onboarding so the user never has to remember it.

## Desired Outcome

When this is complete:

1. Running `init` on a host where setup is **already complete** behaves exactly
   as today: pure config scaffold, no sudo, no setup work — the `setupcheck`
   guard short-circuits.
2. Running `init` on a host where setup is **not** complete: `init` explains that
   one-time host setup is required and **prompts for confirmation**; on yes, it
   runs the full setup (CA bootstrap + skill install), then writes the config.
3. **Setup runs before the config is written.** If setup fails — or the user
   declines the prompt — `init` aborts **without** writing `.agent-creance.yaml`
   (all-or-nothing onboarding; re-running `init` retries cleanly since it is
   idempotent).
4. In a **non-interactive** invocation (no TTY to prompt on), `init` does not
   silently run a sudo/keychain dialog: it prints the actionable "run
   `agent-creance setup`" instruction and aborts (matching `run`'s refusal
   style), unless an explicit opt-out flag asks it to scaffold the config only.

## User Stories / Use Cases

- As a first-time user, I want `init` to walk me through host setup when it
  hasn't been done, so that I don't have to know about a second command or hit a
  refusal from `run` later.
- As a returning user scaffolding a new project on an already-set-up machine, I
  want `init` to stay fast and side-effect-free (no sudo, no prompts) so that it
  is exactly as cheap as today.
- As a CI / scripted user generating a config to commit, I want a way to scaffold
  the config without host setup, so that `init` works on a machine that will
  never run the agent.

## Acceptance Criteria

- [x] When `setupcheck.Verify` reports `StatusOK`, `init` does **no** setup work,
      triggers **no** sudo/keychain dialog, and writes the config (output differs
      only in the final "Next:" line, now pointing at `run`). `TestInitAlreadySetUp`.
- [x] When `setupcheck.Verify` reports `StatusCANotTrusted` or
      `StatusSkillMissing` and stdin is interactive, `init` prints an explanation
      and prompts to run setup; on confirmation it runs the full setup flow (CA
      bootstrap + skill install) and, **only on setup success**, proceeds to
      write the config. `TestInitInteractiveConfirmDrivesSetup`.
- [x] If the user **declines** the prompt, `init` aborts with a clear message,
      does **not** write the config, and exits non-zero.
      `TestInitInteractiveDeclineAborts`.
- [x] If setup **fails** (e.g. verification failure), `init` surfaces setup's
      existing actionable error, does **not** write the config, and exits
      non-zero. `TestInitSetupFailureAborts`.
- [x] When `setupcheck.Verify` reports `StatusKeychainLocked`, `init` surfaces
      the unlock instruction and aborts without writing the config.
      `TestInitKeychainLockedAborts`.
- [x] In a non-interactive invocation with setup missing, `init` does not run
      sudo; it prints the "run `agent-creance setup`" instruction (+ a
      `--no-setup` hint) and aborts — unless `--no-setup` is given, in which case
      it writes the config and skips setup. `TestInitNonInteractiveMissingAborts`
      / `TestInitNoSetupSkipsGate`.
- [x] `make test`, `make lint` green; golden files reviewed (no diff — template
      unchanged). **Test-coverage note:** the confirm/decline/already-set-up/
      non-interactive paths are covered by `*App`+fakes unit tests in
      `init_test.go`, **not** the testscript: `OSKeychain` shells to the absolute
      `/usr/bin/security` (not stubbable via `$PATH`), so `init.txtar` (real
      `cli.Main`) can't drive keychain state hermetically — same constraint
      `run_missing_prereq.txtar` documents. The testscript covers `--no-setup`
      (config-only) + arg/`--help` validation.

## Out of Scope

- The `setup`-side idempotency, verify-first reorder, and CA-prompt messaging —
  that is **AC-0037**. This ticket invokes the existing `setup` path; it does not
  change `setup`'s internal behavior. (The two are complementary and independent;
  see Notes on sequencing.)
- `run`'s cheap precondition gate (AC-0025) — unchanged; it remains the safety net
  for users who skip or opt out of `init`'s bootstrap.
- Any change to `setup`'s own flags or output beyond being driven from `init`.
- CA rotation/removal, doctor (AC-0031).

## Open Questions

_Resolved during ticket creation (see Notes):_

- Trigger style: **prompt-first, then run** (not silent auto-run, not a passive
  nudge).
- Failure handling: **setup-first, abort on failure** — no config is written if
  setup is declined or fails.

## Questions for Research/Planning

- [ ] **Interactive-input seam.** `App` (`internal/cli/cli.go`) currently has no
      `Stdin`; this confirm prompt would be the CLI's first interactive input.
      Per the project's testability rules it must be drivable from testscript
      (which can supply `stdin`). Add an `App.Stdin io.Reader` (wired to
      `os.Stdin` in `Main`) plus TTY detection — and decide whether TTY detection
      needs a `sysdep` seam or can read the input's file descriptor directly.
      Confirm a small confirm-prompt helper is the right shape.
- [ ] **Shared orchestration, not duplication.** `init`'s auto-setup must reuse
      the existing `setup` path (`runSetup` / `setup.Installer.Bootstrap` +
      `InstallSkill`) and its messages, not re-implement them. Factor a shared
      helper both `newSetupCmd` and `runInit` call, so the two commands can't
      drift.
- [ ] **Config-only opt-out flag.** What is the escape hatch for
      non-interactive/CI scaffolding — a new `init --no-setup` (skip the gate,
      write config only), or reuse `setup`'s `--no-ca-install` / `--no-skill`
      semantics? Decide the flag surface and how it interacts with the
      non-interactive abort.
- [ ] **Exit-code semantics** for "user declined the prompt" vs. "setup failed"
      vs. "non-interactive, setup missing" — confirm all are non-zero with
      distinct, testable messages.
- [ ] **Ordering vs. AC-0037.** With the `setupcheck` guard, `init` only invokes
      `setup` when the CA/skill are absent, so it does not depend on AC-0037's
      verify-first idempotency. Confirm whether to sequence AC-0037 first anyway
      (so the whole `setup` story is "always safe to call") or land independently.
- [ ] **Test surface.** Which assertions live in `init.txtar` (stdin yes/no,
      stubbed `security`/`mitmdump`, already-set-up short-circuit, non-interactive
      abort) vs. reused setup-library unit tests. Confirm the `sysdeptest`
      Keychain/TLSProber fakes express the "already-set-up → no setup run" and
      "missing → setup driven" cases.

## References

- `internal/cli/init.go` — `runInit` (today: pure FS scaffold; the guard +
  bootstrap hook lands here).
- `internal/setupcheck/setupcheck.go` — `Verify(kc, fsys, paths) → Result`
  (`StatusOK` / `StatusCANotTrusted` / `StatusSkillMissing` /
  `StatusKeychainLocked`); the cheap, no-sudo gate.
- `internal/cli/setup.go` — `runSetup`; the orchestration to share.
- `internal/setup/setup.go` — `Installer.Bootstrap` / `InstallSkill`.
- `internal/cli/cli.go` — `App` struct (no `Stdin` today) and `Main` wiring.
- AC-0029 (`init` command), AC-0028 (`setup` command), AC-0025 (`run` gate /
  `setupcheck`), AC-0037 (CA-trust prompt UX / setup idempotency).

## Implementation Plan

- Research: `thoughts/shared/research/2026-06-10-AC-0038-init-bootstraps-host-setup.md`
- Plan: `thoughts/shared/plans/2026-06-10-AC-0038-init-bootstraps-host-setup.md`

## Notes & Updates

### 2026-06-09

Created from a maintainer observation: the `init` → `setup` two-step is hard to
remember, and users only learn about `setup` when `run` refuses. Idea: have
`init` detect (cheaply, via `setupcheck`, no sudo) whether host setup ran and, if
not, run it as part of onboarding.

Decisions made during ticket creation:

- **Guarded by the cheap check.** `init` calls `setupcheck.Verify` first; the
  whole bootstrap path is skipped (no sudo, no prompt) when setup is already
  complete, keeping `init`'s fast, side-effect-free contract on the common path.
- **Prompt-first, then run.** When setup is missing and stdin is interactive,
  `init` explains and asks before invoking `setup` (which can raise a Touch ID /
  keychain dialog), so the system prompt is expected. Non-interactive runs don't
  silently sudo — they print the instruction and abort (or scaffold config only
  via the opt-out flag).
- **Setup-first, abort on failure.** Setup runs before the config is written; a
  declined prompt or a setup failure aborts `init` without writing
  `.agent-creance.yaml`. `init` is idempotent, so retrying after fixing the
  problem is clean.

Complexity (Medium): the guard + orchestration reuse is small, but it introduces
the CLI's first interactive prompt (new `App.Stdin` + TTY detection, kept
testscript-drivable), a new opt-out flag, and several golden/testscript paths
(confirm, decline, already-set-up, non-interactive).

### 2026-06-10 — Implemented & closed

Landed in one commit (the gate makes existing `init` tests red until updated).
Resolutions of the planning questions:

- **Interactive-input seam:** added `App.Stdin io.Reader` + a `sysdep.Terminal`
  TTY seam (`OSTerminal` via `golang.org/x/sys/unix` — no new module;
  `FakeTerminal` for tests) + a `confirm` helper. `App.Terminal` is wired in
  `Main`.
- **Shared orchestration:** `init` reuses `runSetup(ctx, app, false, false)`
  verbatim — no duplicated CA/skill messages.
- **Config-only opt-out:** new `init --no-setup` (skips the gate entirely).
- **Ordering:** the host-setup gate runs **before** the clobber guard
  (setup-first / all-or-nothing). Trade-off accepted: an "already exists" refusal
  can come after a completed (idempotent) setup.
- **Test surface (deviation from the AC wording):** the keychain-dependent paths
  live in `*App`+fakes unit tests, because `OSKeychain` shells to the absolute
  `/usr/bin/security` (not `$PATH`-stubbable); the testscript covers `--no-setup`
  + arg/help. See the Acceptance Criteria note.

`make test` (race), `make lint`, `go build ./...`, `make golden` (no diff) green.
