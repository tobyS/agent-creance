---
date: 2026-06-07
ticket: AC-0028
title: "Plan: `setup` command (WP-5.3)"
status: ready
research: thoughts/shared/research/2026-06-07-AC-0028-setup-command.md
branch: main
---

# Plan: AC-0028 `setup` command (WP-5.3)

## Overview

Add `agent-creance setup` — the one onboarding command that runs the CA bootstrap + live
verify (AC-0026) and skill install (AC-0027), with `--no-skill` and `--no-ca-install`
opt-outs. The underlying logic already exists in `internal/setup` as `*Installer` methods
written to be driven by this command; AC-0028 is a thin cobra wrapper plus a two-field
addition to the `App` composition root. `--no-ca-install` switches to env-var-only trust:
it generates the CA PEM (so the cage's `SSL_CERT_FILE`/etc. resolve) but skips the keychain
trust + live verify, and prints an honest coverage caveat.

## Current state

- `internal/setup/Installer` exposes `Bootstrap(ctx)` (generate → keychain install → live
  verify), `EnsureCA(ctx)`, `InstallCA`, `Verify(ctx)`, and `InstallSkill()` — all
  unit-tested (`internal/setup/*_test.go`). The package ships no command yet.
- `internal/cli/cli.go` `App` carries 5 of the 7 `NewInstaller` seams; it is missing
  `TLSProber` and `Sleeper`. `cli.go:67-71` registers `version/doctor/policy/logs/run` — no
  `setup`.
- The cage already injects the four CA-bundle env vars (`internal/cage/cage.go:196-219`), so
  env-var-only trust works today for curl/node/python/git; Go-on-macOS is the keychain-only
  gap.

## Desired end state

- `agent-creance setup` runs CA bootstrap+verify and skill install; exits non-zero with the
  actionable message if CA verification fails.
- `--no-skill` skips skill install; `--no-ca-install` skips keychain trust + verify, ensures
  the CA PEM exists, and prints the coverage caveat (naming `gh` as the Go-based example).
  Skill still installs under `--no-ca-install`.
- `setup --no-ca-install --no-skill` is allowed (silent, exit 0): only ensures the CA PEM.
- No CA/skill logic duplicated — the command delegates entirely to `setup.Installer`.
- `go build ./...`, `make test`, `make lint` all green.

## Decisions (resolved at the checkpoint)

1. **Caveat wording:** detailed + remedy, naming the GitHub CLI (`gh`) as the prominent
   Go-based example.
2. **`--no-ca-install`:** call `EnsureCA(ctx)` (generate the PEM if absent), skip
   `InstallCA`/`Verify`.
3. **Both opt-outs together:** allow silently, exit 0.

Reuse `Installer.Bootstrap` for the full CA path rather than re-sequencing
EnsureCA/InstallCA/Verify in the CLI — the ordering is the design's load-bearing contract
(`internal/setup/setup.go:228-231`) and `Bootstrap` already returns the actionable message
as an error.

---

## Phase 1: Command + seam wiring

### Changes

**`internal/cli/cli.go`**

- Add two fields to `App` (near the other sysdep seams), with a short comment that they are
  the remaining seams `setup.NewInstaller` needs:
  ```go
  // TLSProber and Sleeper are the two extra seams setup.Installer needs (beyond
  // those above) for the live CA verification probe and the CA-generation poll;
  // the setup command (AC-0028) drives them. Tests wire the sysdeptest fakes.
  TLSProber sysdep.TLSProber
  Sleeper   sysdep.Sleeper
  ```
- In `Main()` add `TLSProber: sysdep.OSTLSProber{},` and `Sleeper: sysdep.OSSleeper{},` to
  the `App` literal (`cli.go:79-94`).
- Register the command in `newRootCmd`: `root.AddCommand(newSetupCmd(app))` (`cli.go:67-71`).

**`internal/cli/setup.go`** (new file) — model on `doctor.go` (leaf command) + `run.go`
(testable-body split):

```go
package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/tobyS/agent-creance/internal/setup"
)

// newSetupCmd implements `agent-creance setup` — the one-time onboarding command
// that trusts the mitmproxy CA (with a live verification) and installs the
// agent-creance Claude Code skill. It is thin orchestration over the already-
// tested internal/setup.Installer; --no-skill / --no-ca-install opt out of each
// half. (docs/design.md "Commands".)
func newSetupCmd(app *App) *cobra.Command {
	var noSkill, noCAInstall bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Trust the mitmproxy CA and install the agent-creance skill",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSetup(cmd.Context(), app, noSkill, noCAInstall)
		},
	}
	cmd.Flags().BoolVar(&noSkill, "no-skill", false,
		"skip installing the agent-creance Claude Code skill")
	cmd.Flags().BoolVar(&noCAInstall, "no-ca-install", false,
		"don't trust the CA system-wide; rely on CA-bundle env vars only (reduced tool coverage)")
	return cmd
}

// runSetup is the testable body: it constructs the Installer from the App seams
// and drives the CA and skill steps per the opt-out flags.
func runSetup(ctx context.Context, app *App, noSkill, noCAInstall bool) error {
	inst := setup.NewInstaller(
		app.FS, app.Keychain, app.ProcessManager, app.PortAllocator,
		app.TLSProber, app.Sleeper, app.Paths,
	)

	// CA step.
	if noCAInstall {
		// env-var-only trust: ensure the PEM exists (so the cage's SSL_CERT_FILE
		// &c. resolve), but don't change system trust and don't verify.
		if _, err := inst.EnsureCA(ctx); err != nil {
			return fmt.Errorf("ensure CA: %w", err)
		}
		fmt.Fprintln(app.Stdout, caCaveat)
	} else {
		fmt.Fprintln(app.Stdout, "Trusting the mitmproxy CA (you may be prompted for keychain access)…")
		if err := inst.Bootstrap(ctx); err != nil {
			return err // carries the actionable Message; Main → exit 1
		}
		fmt.Fprintln(app.Stdout, "✓ CA installed and verified.")
	}

	// Skill step.
	if noSkill {
		fmt.Fprintln(app.Stdout, "Skipping skill install (--no-skill).")
		return nil
	}
	if err := inst.InstallSkill(); err != nil {
		return fmt.Errorf("install skill: %w", err)
	}
	fmt.Fprintln(app.Stdout, "✓ Skill installed.")
	return nil
}

// caCaveat is the honest coverage notice printed under --no-ca-install. The cage
// injects SSL_CERT_FILE/NODE_EXTRA_CA_CERTS/REQUESTS_CA_BUNDLE/GIT_SSL_CAINFO at
// the mitmproxy CA (internal/cage/cage.go), which covers curl/Node/Python/git;
// Go-on-macOS trusts the CA only via the keychain, so it is the documented gap.
const caCaveat = `Skipping system trust install (--no-ca-install).

The mitmproxy CA is provided to caged tools via environment variables only
(SSL_CERT_FILE, NODE_EXTRA_CA_CERTS, REQUESTS_CA_BUNDLE, GIT_SSL_CAINFO). This
covers curl, Node / Claude Code, Python, and git.

NOT covered: Go-based tools on macOS (for example the GitHub CLI, ` + "`gh`" + `) trust the
CA only via the system keychain, so they will fail TLS inside the cage. Re-run
` + "`agent-creance setup`" + ` without --no-ca-install to trust them too.`
```

### Success criteria

#### Automated
- [x] `go build ./...` compiles.
- [x] `make lint` clean (`go vet` + golangci-lint).

#### Manual
- [x] `agent-creance setup --help` lists `--no-skill` and `--no-ca-install`.

---

## Phase 2: Tests

### Changes

**`internal/cli/setup_test.go`** (new file) — `*App`+fakes unit tests, the project's model
for keychain/curl-dependent command behavior (mirrors `internal/cli/run_test.go:19-108`).
Build a fixture wiring an `App` from `sysdeptest` fakes, including the two new seams
(`NewFakeTLSProber()`, `&FakeSleeper{}`). Pre-seed the CA PEM in the fake FS at
`<home>/.mitmproxy/mitmproxy-ca-cert.pem` so `EnsureCA` takes its idempotent fast path (no
mitmdump spawn/poll needed); `Verify`/`Bootstrap` still spawn one bare mitmdump and probe.

Cases (map to the ticket's Verification & Test Steps):

- **default** (`runSetup(ctx, app, false, false)`): no error; `kc.AddedCerts` contains the
  CA path (Bootstrap → InstallCA ran); `prober.Calls` has one probe to `https://example.com`
  (verify ran); the skill file was written at `<home>/.claude/skills/agent-creance/SKILL.md`
  in the fake FS; stdout has "installed and verified" + "Skill installed".
- **`--no-skill`** (`false, true`... i.e. noSkill=true, noCAInstall=false): no error; CA
  still installed (`kc.AddedCerts` non-empty); **no** skill file written
  (`fs.Files[skillPath]` absent); stdout mentions skipping the skill.
- **`--no-ca-install`** (noSkill=false, noCAInstall=true): no error; `kc.AddedCerts` empty
  (no trust change); `prober.Calls` empty (no verify); stdout contains the caveat
  (assert on a stable substring, e.g. "env" + "Go-based" + "`gh`"); skill **is** written.
- **CA-verify failure** (`prober.Outcome = sysdep.ProbeUntrusted`, default flags): `runSetup`
  returns a non-nil error whose message is the actionable untrusted message
  (`setup` msgUntrusted — assert substring "CA verification failed"); `kc.AddedCerts` shows
  the cert was added (InstallCA runs before Verify); skill not installed (returned before
  the skill step).
- **both opt-outs** (`true, true`): no error; `kc.AddedCerts` empty; `prober.Calls` empty;
  no skill file; exit success (caveat printed).

Assertion helpers already exist in `run_test.go` (same package): reuse `argsContain`; add a
small stdout-substring check inline. Reference fakes: `FakeKeychain.AddedCerts`
(`sysdeptest/keychain.go:25-26`), `FakeTLSProber.Calls`/`.Outcome`
(`sysdeptest/tlsprober.go`), `FakeFileSystem.Files`.

**`internal/cli/testdata/script/setup_help.txtar`** (new) — the hermetic surface that does
not touch the keychain (cobra handles `--help`/arg validation before `RunE`):

```
# setup --help advertises both opt-out flags without touching the keychain.
env PATH=$CREANCE_BIN

agent-creance setup --help
stdout '--no-skill'
stdout '--no-ca-install'

# extra args are rejected before any side effect.
! agent-creance setup bogus
stderr 'unknown command|accepts 0 arg|unknown'
```

(Behavior beyond help/args stays in the unit test, because `cli.Main` wires the real
`OSKeychain` and a bare `setup` would spawn real `mitmdump` / pop a trust dialog — the same
limitation `run_missing_prereq.txtar:7-11` documents.)

### Success criteria

#### Automated
- [ ] `go test -race ./internal/cli/...` green (new unit tests + testscript).
- [ ] `make test` green (full suite, race).
- [ ] `make lint` clean.
- [ ] `go build ./...` compiles.

#### Manual
- [ ] Spot-check `agent-creance setup --no-ca-install` output wording reads correctly (names
  `gh`, lists the four env vars).

---

## Testing strategy

- Pure command behavior under all flag combinations → `*App`+fakes unit tests
  (`setup_test.go`), exercising `runSetup` directly. This is the only way to cover the
  keychain/curl-dependent paths hermetically (the real seams are wired in `cli.Main`).
- Help/arg-validation surface → testscript `.txtar` (`$CREANCE_BIN` minimal PATH).
- The underlying CA/skill mechanics are already covered by `internal/setup/*_test.go` and
  the `//go:build integration` live tests — not re-tested here (out of scope per the
  ticket).

## References

- Research: `thoughts/shared/research/2026-06-07-AC-0028-setup-command.md`
- Ticket: `thoughts/shared/tickets/AC-0028-setup-command.md`
- `internal/setup/setup.go:69-79,84-101,203-248` (NewInstaller, EnsureCA, Verify, Bootstrap)
- `internal/setup/skill.go:35-45` (InstallSkill)
- `internal/cli/cli.go:20-47,67-71,79-94` (App, registration, Main wiring)
- `internal/cli/run.go:34-43`, `internal/cli/doctor.go:14-38` (command templates)
- `internal/cli/run_test.go:19-108` (fakes fixture pattern)
- `internal/cli/script_test.go:26-54`, `testdata/script/run_missing_prereq.txtar` (testscript)
- `internal/cage/cage.go:192-219` (the env vars the caveat describes)
