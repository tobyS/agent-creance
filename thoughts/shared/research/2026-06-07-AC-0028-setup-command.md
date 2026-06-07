---
date: 2026-06-07
ticket: AC-0028
title: "Research: `setup` command (WP-5.3)"
status: complete
branch: main
commit: c49019d05ef45024b9c144d67c724786e8f61c5b
repo: github.com/tobyS/agent-creance
---

# Research: AC-0028 `setup` command (WP-5.3)

## Research question

Wire AC-0026 (CA bootstrap + live verify) and AC-0027 (skill install) behind a single
`agent-creance setup` command with `--no-skill` and `--no-ca-install` opt-outs, where
`--no-ca-install` prints an honest "env-var-only coverage" caveat and skips the
system-trust change. What logic already exists, what is the idiomatic way to add the
command, and what is the correct env-var-only caveat wording?

## TL;DR / answer

**Almost all the work is already done.** The CA-bootstrap and skill-install logic live in
`internal/setup` as methods on `*setup.Installer`, are fully unit-tested, and were written
*explicitly* to be driven by this command (the package doc literally names AC-0028:
`internal/setup/setup.go:1-18`, `setup.go:228-231`). AC-0028 is a thin cobra wrapper plus a
small dependency-injection wiring change.

The command needs to:

1. Add a `newSetupCmd(app *App)` cobra command (model on `internal/cli/doctor.go` and the
   testable-body split in `internal/cli/run.go`), with two `BoolVar` opt-out flags.
2. Add two fields to the `App` composition root — `TLSProber sysdep.TLSProber` and
   `Sleeper sysdep.Sleeper` — and wire `sysdep.OSTLSProber{}` / `sysdep.OSSleeper{}` in
   `cli.Main()` (`internal/cli/cli.go:20-47`, `:79-94`). These are the only two seams the
   `setup.Installer` needs that `App` does not already carry.
3. In the body: construct `setup.NewInstaller(...)` from the App seams, then
   - CA: `Installer.Bootstrap(ctx)` (full: generate → keychain install → live verify) when
     CA-install is on; `Installer.EnsureCA(ctx)` (generate only, so the PEM exists for the
     env-var path) plus the printed caveat when `--no-ca-install`.
   - Skill: `Installer.InstallSkill()` unless `--no-skill`.
4. Return an error on CA-verify failure → cobra/`Main()` turns it into exit code 1
   (`internal/cli/cli.go:95-100`). With `--no-ca-install` there is no verify, so no such
   failure.

**The open question (which tools the env-var-only mode misses) is answered by the code +
web research:** the cage already injects `NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`,
`REQUESTS_CA_BUNDLE`, `GIT_SSL_CAINFO` pointing at the mitmproxy CA PEM
(`internal/cage/cage.go:196-219`). Those cover curl, Node/Claude Code, Python (requests +
stdlib), and git on modern macOS. **Go programs on macOS are the gap** — Go's `crypto/x509`
explicitly ignores `SSL_CERT_FILE`/`SSL_CERT_DIR` on darwin and uses the Apple SecTrust /
keychain verifier, so Go-based tools (and any other Security.framework consumer) only trust
the CA when it is in the keychain. That is exactly what `--no-ca-install` skips, so the
caveat should say: *env-var-based trust covers curl/node/python/git; Go-based tools (which
trust only the macOS keychain) will fail TLS — install the CA for those.*

## Detailed findings

### The reusable library already exists (AC-0026 + AC-0027)

`internal/setup/` is a command-less library, written to be wired by `setup` (AC-0028) and
later `doctor` (AC-0031). It ships no cobra command today — `cli.go:67-71` only registers
`version`, `doctor`, `policy`, `logs`, `run`. There is no `setup`/`ca`/`skill` command yet.

`*setup.Installer` (constructed by `NewInstaller`, `setup.go:69-79`) exposes everything the
command needs:

| Method | Location | What it does |
|---|---|---|
| `Bootstrap(ctx)` | `setup.go:232-248` | End-to-end CA flow: `EnsureCA` → `InstallCA` → live `Verify`; returns the actionable `Message` as an `error` on a failed verification. |
| `EnsureCA(ctx) (string, error)` | `setup.go:84-101` | Idempotently generate the CA via a throwaway `mitmdump`; returns the cert path. Leaves an existing CA untouched. |
| `InstallCA(certPath) error` | `setup.go:133-138` | `security add-trusted-cert` into the login keychain (does **not** prove trust). |
| `Verify(ctx) (Result, error)` | `setup.go:203-226` | Live verify: spawn bare `mitmdump`, probe `https://example.com` through it, check chain validates against system trust. Trusted/untrusted are `Result.Status`; error reserved for environment failures. |
| `InstallSkill() error` | `skill.go:35-45` | Atomically write the embedded `SKILL.md` to `~/.claude/skills/agent-creance/SKILL.md` (idempotent, self-healing, never touches CLAUDE.md). |

`NewInstaller` takes seven sysdep seams (`setup.go:69-79`):
`FileSystem, Keychain, ProcessManager, PortAllocator, TLSProber, Sleeper, PathResolver`.

The actionable untrusted message is deterministic and golden-pinned
(`setup.go:189-191`, golden `internal/setup/testdata/verify_untrusted.golden`):

> "CA verification failed: the mitmproxy CA is not trusted by the system trust store. The
> trust dialog may have been cancelled, or the certificate is not trusted for SSL. Re-run
> `agent-creance setup`."

Existing unit tests cover all of it: `internal/setup/setup_test.go` (EnsureCA/InstallCA),
`internal/setup/verify_test.go` (Verify/Bootstrap), `internal/setup/skill_test.go`
(InstallSkill). Live real-tool coverage is `//go:build integration` in
`internal/setup/setup_integration_test.go`.

### Wiring gap: `App` lacks `TLSProber` and `Sleeper`

`App` (`internal/cli/cli.go:20-47`) already carries `FS`, `Paths`, `Keychain`,
`ProcessManager`, `PortAllocator` — five of the seven `NewInstaller` seams. It is missing
`TLSProber` and `Sleeper`. Both have OS impls ready to wire:

- `sysdep.OSTLSProber{}` — `internal/sysdep/tlsprober.go:68-69` (curl-backed).
- `sysdep.OSSleeper{}` — `internal/sysdep/sleeper.go:24-25` (timer-backed).

Add the two fields to `App` and wire the OS impls in `cli.Main()` (`cli.go:79-94`), the
same pattern as the existing seams. Fakes for unit tests already exist:
`sysdeptest.NewFakeTLSProber()` (defaults to `ProbeTrusted`) and `&sysdeptest.FakeSleeper{}`
(returns immediately).

### Command shape: model on `doctor` + the `run` testable-body split

- Leaf-command template: `internal/cli/doctor.go:14-38` — `func newDoctorCmd(app *App)
  *cobra.Command`, `RunE` reads deps off `app`, writes to `app.Stdout`, returns an error
  with a printed report then non-zero exit on failure.
- Testable-body split (preferred here): `internal/cli/run.go:34-43` — the cobra `RunE` is a
  one-liner `return runRun(cmd.Context(), app, ".")`, with all logic in package-level
  `runRun(ctx, app, dir)` so it is unit-testable against fakes. AC-0028 should do
  `runSetup(cmd.Context(), app, noSkill, noCAInstall)`.
- Registration: add `root.AddCommand(newSetupCmd(app))` in `cli.go:67-71`.
- Flags: `var noSkill, noCAInstall bool` + `cmd.Flags().BoolVar(&noSkill, "no-skill", false,
  "...")` etc., the `BoolVar` idiom from `internal/cli/policy.go:34-62` and
  `internal/cli/logs.go:18-55`. (No `--no-*` flag exists in the repo yet; a false-default
  bool named `no-...` is the consistent choice.)
- Errors / exit codes: never `os.Exit`; return an error from the body. `cli.Main()`
  (`cli.go:95-100`) prints `"error: …"` to stderr and returns 1. Print human-facing
  progress/caveat to `app.Stdout` (as `doctor`/`run` do).

### The `--no-ca-install` env-var-only mode actually works today

The cage's `buildEnv` (`internal/cage/cage.go:196-219`) injects, for every caged agent:

- proxy vars: `HTTP(S)_PROXY` / lowercase + `NO_PROXY` → the loopback proxy.
- **CA vars (all four point at the single mitmproxy CA PEM)**: `NODE_EXTRA_CA_CERTS`,
  `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE`, `GIT_SSL_CAINFO`.
- `CLAUDE_CONFIG_DIR` → the sanitized ephemeral config dir.

The code comment (`cage.go:192-195`) records the spike-S4 decision verbatim: *"go is
intentionally not special-cased (on macOS it trusts the CA only via the keychain)"*. So the
env-var-only path is already plumbed end-to-end; `--no-ca-install` just means "don't also
add the CA to the keychain," and the documented consequence is that Go tools lose trust.

Implication for the command: under `--no-ca-install` the PEM must still **exist** at
`~/.mitmproxy/mitmproxy-ca-cert.pem` for those env vars to resolve, so the body should call
`EnsureCA(ctx)` (generate-if-absent) and skip `InstallCA`/`Verify`. (The runtime proxy also
materialises the CA on first run, but generating it in `setup` keeps the env-var-only mode
self-contained and avoids a first-run surprise.)

### Env-var CA-bundle coverage on macOS (answers the ticket's open question)

From web research (Go docs/issues, curl, Node, requests, Python, git, Claude Code docs):

| Tool | Env var honored on macOS | Covered without keychain? |
|---|---|---|
| curl (system = LibreSSL) | `CURL_CA_BUNDLE` / `SSL_CERT_FILE` | ✅ yes |
| Node / Claude Code | `NODE_EXTRA_CA_CERTS` (additive; read once at startup) | ✅ yes |
| Python `requests` | `REQUESTS_CA_BUNDLE` (→ `CURL_CA_BUNDLE`) | ✅ yes |
| Python stdlib `ssl`/`urllib` | `SSL_CERT_FILE` (OpenSSL-linked) | ✅ yes |
| git (modern macOS = LibreSSL) | `GIT_SSL_CAINFO` / `http.sslCAInfo` | ✅ yes (legacy Secure-Transport git: keychain) |
| **Go programs on macOS** | **none** — `crypto/x509` ignores `SSL_CERT_FILE`/`SSL_CERT_DIR` on darwin; uses Apple SecTrust/keychain | ❌ **no — needs keychain** |
| Other Security.framework consumers (Swift/ObjC `URLSession`, native apps) | none | ❌ no — needs keychain |

Bottom line for the caveat: the cage's env vars cover curl, Node/Claude Code, Python, and
git. **Go-based tools (and any tool using the macOS keychain directly) will fail TLS under
`--no-ca-install`** — to support those, omit the flag so the CA is trusted system-wide.
(Per-process `GODEBUG=x509usefallbackroots=1` is the only env-driven Go workaround and is
too fragile to recommend.)

Sources: `pkg.go.dev/crypto/x509` ("other than macOS the environment variables
SSL_CERT_FILE and SSL_CERT_DIR can be used…"); golang/go#37907, #46287; curl.se SSL CA
docs; nodejs.org enterprise-network config; requests advanced-usage docs; PEP 476;
code.claude.com corporate-proxy docs.

### Testing approach

Mirrors AC-0026/0027 and `run`:

- **Body unit tests (`internal/cli/setup_test.go`)** against `*App` + `sysdeptest` fakes —
  the project's stated model for keychain/curl-dependent command behavior (see
  `internal/cli/run_test.go:19-35` header and fixture). `cli.Main()` wires the real
  `OSKeychain`, so these paths cannot be testscript-hermetic. Cases (map to the ticket's
  Verification & Test Steps):
  - default: assert `kc.AddedCerts` has the CA, the TLS prober was called, and the skill
    file was written to `~/.claude/skills/agent-creance/SKILL.md`; exit success.
  - `--no-skill`: assert no skill file written; CA still installed.
  - `--no-ca-install`: assert `kc.AddedCerts` empty, prober **not** called, stdout contains
    the coverage caveat, and the skill **is** installed; CA PEM ensured to exist.
  - CA-verify failure (`FakeTLSProber{Outcome: ProbeUntrusted}`): body returns an error
    carrying the actionable message; `kc` shows the cert was added but exit is non-zero.
  Fakes to wire beyond `run`'s fixture: `sysdeptest.NewFakeTLSProber()`,
  `&sysdeptest.FakeSleeper{}` (so the EnsureCA poll loop doesn't burn wall-clock).
- **testscript (`internal/cli/testdata/script/setup_*.txtar`)** for the hermetic surface
  that does not touch the keychain — at minimum arg/usage (`cobra.NoArgs`) and help. The
  on-PATH stub pattern (`doctor_healthy.txtar`) and `$CREANCE_BIN` minimal-PATH pattern
  (`doctor_missing.txtar`, `run_missing_prereq.txtar`) are the references; harness is
  `internal/cli/script_test.go:26-54`. Keep behavior assertions in the unit test, as
  `run` does (`run_missing_prereq.txtar:7-11` documents this split).
- Verification commands (from `.claude/tce/profile.md`): `go build ./...`,
  `make test` (= `go test -race ./...`), `make lint`.

## Code references

- `internal/setup/setup.go:1-18,69-79,84-101,133-138,203-248` — Installer, NewInstaller,
  EnsureCA, InstallCA, Verify, Bootstrap; package doc names AC-0028.
- `internal/setup/setup.go:189-191` + `internal/setup/testdata/verify_untrusted.golden` —
  golden-pinned untrusted message.
- `internal/setup/skill.go:35-45` — InstallSkill.
- `internal/setupcheck/setupcheck.go` — `SkillFileRel` (`:39`), `CACommonName` (`:34`), the
  cheap run-time `Verify` (`:100`) `run` uses (not this command).
- `internal/cli/cli.go:20-47` (App), `:56-73` (root + AddCommand), `:79-101` (Main, exit).
- `internal/cli/doctor.go:14-38` — leaf-command template.
- `internal/cli/run.go:34-43` (cobra→testable-body split), `:48-67` (precondition/error
  idiom).
- `internal/cli/policy.go:34-62`, `internal/cli/logs.go:18-55` — `BoolVar` flag idiom.
- `internal/cage/cage.go:192-219` — `buildEnv`: proxy + 4 CA env vars; go-not-special-cased
  comment.
- `internal/sysdep/tlsprober.go:68-69` (`OSTLSProber`),
  `internal/sysdep/sleeper.go:24-25` (`OSSleeper`).
- `internal/sysdep/sysdeptest/tlsprober.go` (`NewFakeTLSProber`),
  `internal/sysdep/sysdeptest/sleeper.go` (`FakeSleeper`).
- `internal/cli/run_test.go:19-108` — `*App`+fakes fixture/unit-test pattern.
- `internal/cli/script_test.go:26-54`, `internal/cli/testdata/script/doctor_healthy.txtar`,
  `doctor_missing.txtar`, `run_missing_prereq.txtar` — testscript harness/patterns.
- `docs/design.md` "Commands" — `setup` / `setup --no-skill` / `setup --no-ca-install`.

## Open questions for the checkpoint

1. **Env-var-only caveat wording.** Research resolves the *content* (curl/node/python/git
   covered; Go-on-macOS not). Confirm the exact phrasing/tone for the `--no-ca-install`
   stdout message (and whether to name specific tools / the keychain-install remedy).
2. **`--no-ca-install` + CA generation.** Confirm the command should `EnsureCA` (generate
   the PEM if absent) under `--no-ca-install` so the cage's `SSL_CERT_FILE` &c. resolve,
   rather than assuming the runtime proxy materialises it on first `run`.
3. **`--no-ca-install --no-skill` together.** Both opt-outs at once is a near-no-op (only
   ensures the CA PEM exists). Acceptable, or should it warn/refuse?
