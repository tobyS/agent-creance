---
date: 2026-06-07
ticket: AC-0026
work_package: WP-5.1
status: ready
branch: main
research: thoughts/shared/research/2026-06-07-AC-0026-ca-bootstrap-verification.md
depends_on: [AC-0009, AC-0001]
spike_gate: S1 (AC-0001, satisfied)
---

# AC-0026 — CA bootstrap + post-install verification (WP-5.1): Implementation Plan

## Overview

Create a new `internal/setup` package that (1) generates the mitmproxy CA into the default
`~/.mitmproxy/` if absent (idempotent), (2) installs it into the login keychain via
`security add-trusted-cert`, and (3) runs a **live** post-install verification — spawn a
short-lived bare `mitmdump` on a random loopback port, `curl https://example.com` through it,
and confirm the chain validates against the **system trust store only** — surfacing an
explicit, actionable error on the silent-cancel failure mode (where `add-trusted-cert` returns
0 but trust was never applied).

This ticket ships a **library + tests**, no cobra command. The `setup` command (AC-0028) and
`doctor` extension (AC-0031) — which will wire `internal/setup` into `App` and the command
tree — are separate, out-of-scope tickets. `run` stays on the cheap `setupcheck.Verify`
(decided in AC-0025); the live `setup.Verify` is reserved for `setup`/`doctor`.

(Decisions confirmed at the research checkpoint: run keeps the cheap check; library-only scope;
extend the `Keychain` seam with `AddTrustedCert` + add a dedicated `TLSProber` curl seam.)

## Current state

- No `internal/setup` package exists.
- `internal/setupcheck` already does the cheap "is the `mitmproxy` cert present in the keychain"
  check and is wired into `run.go:60`. `CACommonName = "mitmproxy"` lives there.
- `internal/proxy` spawns/​tears down `mitmdump` via `ProcessManager`/`PortAllocator`
  (`mitmArgs`, `lifecycle.go:132`/`:168`) — the verify-proxy is a *bare* mitmdump (no enforcer).
- Seams available: `Keychain` (read-only), `ProcessManager`, `PortAllocator`, `FileSystem`,
  `PathResolver`. **Gaps:** no keychain *write* (`add-trusted-cert`), no curl/TLS probe seam,
  no sleep primitive (`Clock` has only `Now`/`Since`).

## Desired end state

- `internal/setup` package with `Installer` (`EnsureCA`, `InstallCA`, `Verify`, `Bootstrap`),
  fully unit-tested with fakes + a golden failure message, plus `//go:build integration`
  live tests gated on S1.
- Three new sysdep capabilities, each with a real impl, a fake, and tests:
  - `Keychain.AddTrustedCert(certPath string) error`
  - `sysdep.TLSProber` (curl-through-proxy probe) + the pure `ClassifyCurlExit` helper
  - `sysdep.Sleeper` (bounded readiness polling)
- `make test`, `make lint`, `go build ./...` all green; `make test-integration` green on a
  configured dev machine (full generate+install+verify gated behind an explicit opt-in env var
  so an automated `make test-integration` run never silently mutates the developer's keychain
  trust settings).

## What we are NOT doing

- No `agent-creance setup` / `doctor` cobra command, and no `App` wiring of the new seams
  (that is AC-0028 / AC-0031). `Installer` is constructed directly by tests for now.
- Not wiring the live verification into `run` (run stays on `setupcheck.Verify`).
- No skill install (AC-0027), no `--no-ca-install` env-only mode (AC-0028).
- Not using a project-private mitmproxy confdir — the CA is the default `~/.mitmproxy` one the
  runtime proxy already uses.
- Not passing `--cacert` to the verification curl (that would mask the very failure we test).

## Key design decisions

1. **CA location = default `~/.mitmproxy/mitmproxy-ca-cert.pem`.** Matches the runtime proxy
   (`internal/proxy` uses mitmproxy's default confdir) and `setupcheck.CACommonName = "mitmproxy"`.
2. **Generate by briefly running `mitmdump`** (no standalone gen command exists): allocate an
   ephemeral port, `ProcessManager.Spawn("mitmdump", "--listen-host","127.0.0.1",
   "--listen-port",<port>,"-q")`, poll `FileSystem.Stat` for the cert file (bounded attempts via
   `Sleeper`), then `Signal(pid, SIGTERM)`. Idempotent: if the cert file already exists, skip.
3. **`add-trusted-cert` exit code is advisory, not authoritative.** `InstallCA` runs the command
   and surfaces only genuine spawn/exec failures; proof of trust is the live `Verify`. Uses a
   generous timeout (the auth dialog blocks on the human — the 10 s find-timeout is too short).
4. **Verification validates against the system trust store only** (no `--cacert`). curl exit:
   `0` → trusted; `60` → untrusted (the cancelled-prompt / missing-trust signal, actionable
   message); anything else → genuine environment error. curl `--retry --retry-connrefused`
   absorbs mitmdump's startup race in-subprocess, so no Go-side readiness sleep for `Verify`.
5. **Status-as-data** (mirrors `setupcheck`/`cred`): `Verify` returns `(Result, error)`; expected
   outcomes (trusted / untrusted) are `Result.Status`, error is reserved for genuine failures.
   `Result.Message()` is deterministic and golden-tested.

---

## Phase 1 — New sysdep seams (Keychain.AddTrustedCert, TLSProber, Sleeper)

### 1a. Extend the Keychain seam — `AddTrustedCert`

`internal/sysdep/keychain.go`:
- Add to the `Keychain` interface:
  ```go
  // AddTrustedCert imports the PEM certificate at certPath into the login keychain
  // and marks it a trusted SSL root (security add-trusted-cert, per-user trust
  // domain). NOTE: security returns 0 even when the trust dialog is cancelled, so a
  // nil error proves the command ran, NOT that trust was applied — callers must
  // verify trust functionally (setup.Verify). A non-nil error is a genuine failure.
  AddTrustedCert(certPath string) error
  ```
- `OSKeychain.AddTrustedCert`: run `/usr/bin/security add-trusted-cert -r trustRoot -p ssl
  -k <login.keychain-db> <certPath>` (no `-d` → per-user domain, no sudo). Resolve the login
  keychain path internally via `os.UserHomeDir()` + `Library/Keychains/login.keychain-db`.
  **Do not** reuse `runSecurity` (its 10 s `securityFindTimeout` is too short for the auth
  dialog); add a sibling runner with `addTrustedCertTimeout = 2 * time.Minute`. Wrap a failure
  as `fmt.Errorf("sysdep: add-trusted-cert: exit %d: %s", code, stderr)`.
- `sysdeptest/keychain.go` `FakeKeychain`: add `AddedCerts []string` recorder and `AddCertErr
  error`; `AddTrustedCert(p)` appends and returns `AddCertErr`.

Tests (`keychain_test.go`, `sysdeptest/keychain_test.go`): fake records the path / returns the
scripted error. (The real `security add-trusted-cert` is exercised only in the integration test.)

### 1b. New `TLSProber` seam — curl-through-proxy probe

`internal/sysdep/tlsprober.go` (new):
```go
type ProbeOutcome int
const (
    ProbeTrusted   ProbeOutcome = iota // curl exit 0 — chain validated vs system trust store
    ProbeUntrusted                     // curl exit 60 — CA not trusted (cancelled/missing trust)
    ProbeError                         // any other exit — environment/handshake error, not a verdict
)

type TLSProber interface {
    // ProbeViaProxy issues an HTTPS GET to targetURL through the HTTP proxy at
    // proxyURL, validating the server cert against the SYSTEM trust store only (no
    // extra CA bundle). The outcome classifies whether the chain validated. A
    // non-nil error means the probe could not run at all (e.g. curl not installed).
    ProbeViaProxy(ctx context.Context, proxyURL, targetURL string) (ProbeOutcome, error)
}

// ClassifyCurlExit maps a curl exit code to a ProbeOutcome (pure, table-tested).
func ClassifyCurlExit(code int) ProbeOutcome // 0->Trusted, 60->Untrusted, else->ProbeError
```
- `OSTLSProber` runs `curl -sS -o /dev/null --proxy <proxyURL> --retry 5 --retry-connrefused
  --retry-delay 1 <targetURL>` via `exec.CommandContext`; on `*exec.ExitError` →
  `(ClassifyCurlExit(code), nil)`; if curl can't start (not an ExitError) → `(ProbeError, err)`.
  Constants `curlExitOK = 0`, `curlExitUntrustedCA = 60`.
- `sysdeptest/tlsprober.go` `FakeTLSProber`: `Outcome ProbeOutcome`, `Err error`, recorder
  `Calls []TLSProbe{ProxyURL, TargetURL}`.

Tests (`tlsprober_test.go`): table over `ClassifyCurlExit` (0/60/35/7/other); fake records the
call and returns scripted outcome/error.

### 1c. New `Sleeper` seam — bounded polling

`internal/sysdep/sleeper.go` (new):
```go
type Sleeper interface {
    // Sleep blocks for d or until ctx is cancelled, returning ctx.Err() on cancel.
    Sleep(ctx context.Context, d time.Duration) error
}
```
- `OSSleeper`: `select { case <-time.After(d): return nil; case <-ctx.Done(): return ctx.Err() }`.
- `sysdeptest/sleeper.go` `FakeSleeper`: returns immediately (no real delay), records
  `Sleeps []time.Duration` so a test can assert the poll loop slept.

Tests (`sleeper_test.go`): fake returns instantly and records; (optionally) OSSleeper respects
ctx cancel.

### Success criteria — Phase 1
- [ ] `go build ./...` compiles; compile-time `var _ Keychain/TLSProber/Sleeper = …` assertions hold.
- [ ] `go test -race ./internal/sysdep/...` green (new fakes + `ClassifyCurlExit` table).
- [ ] `make lint` clean.
- [ ] **Commit** (`/commit`, full code checklist).

---

## Phase 2 — `internal/setup`: EnsureCA + InstallCA

`internal/setup/setup.go` (new), package doc tying to `design.md:426,443` and the cheap-vs-live
split (`setupcheck`):
```go
const (
    caDirRel    = ".mitmproxy"               // under $HOME
    caCertFile  = "mitmproxy-ca-cert.pem"    // cert-only PEM — the one to trust
)

type Installer struct { fs; kc; proc; ports; prober; sleeper; paths } // sysdep seams
func NewInstaller(fs sysdep.FileSystem, kc sysdep.Keychain, proc sysdep.ProcessManager,
    ports sysdep.PortAllocator, prober sysdep.TLSProber, sleeper sysdep.Sleeper,
    paths sysdep.PathResolver) *Installer

// EnsureCA returns the CA cert path, generating it via a brief mitmdump run if absent.
func (i *Installer) EnsureCA(ctx context.Context) (certPath string, err error)

// InstallCA imports the CA into the login keychain (security add-trusted-cert).
func (i *Installer) InstallCA(certPath string) error
```
- `caCertPath()`: `home, _ := paths.UserHomeDir(); filepath.Join(home, caDirRel, caCertFile)`.
- `EnsureCA`: `Stat(path)` present → return path (idempotent). Absent → `port :=
  ports.Allocate()`; `pid := proc.Spawn(ctx,"mitmdump","--listen-host","127.0.0.1",
  "--listen-port",strconv.Itoa(port),"-q")`; poll: up to `genMaxAttempts` (~60) times, `Stat`
  the path; if present break, else `sleeper.Sleep(ctx, genPollInterval)` (~50 ms); always
  `defer proc.Signal(pid, SIGTERM)`. If the file never appears → `fmt.Errorf("setup: CA was
  not generated after …")`. Return the path.
- `InstallCA`: `i.kc.AddTrustedCert(certPath)` wrapped (`"setup: install CA: %w"`).

Unit tests (`setup_test.go`) with fakes:
- idempotent: cert present in `FakeFileSystem.Files` → no `Spawn`, returns the path.
- generate: cert absent then present (use a `FakeFileSystem` that reports the file on attempt N,
  or seed it before the loop) → asserts spawn argv (`mitmdump … --listen-port …`), a recorded
  `Sleep`, and a SIGTERM to the spawned PID.
- generate timeout: cert never appears → error; SIGTERM still sent.
- install: `InstallCA(path)` → `FakeKeychain.AddedCerts == [path]`; `AddCertErr` propagates wrapped.

### Success criteria — Phase 2
- [ ] `go build ./...`; `go test -race ./internal/setup/...` green.
- [ ] `make lint` clean.
- [ ] **Commit.**

---

## Phase 3 — `internal/setup`: Verify + Bootstrap + golden message

Add to `internal/setup`:
```go
const verifyTargetURL = "https://example.com"

type Status int
const (
    StatusTrusted   Status = iota // chain validated against the system trust store
    StatusUntrusted               // CA not trusted (cancelled prompt / missing trust)
)
type Result struct { Status Status }
func (r Result) OK() bool          // Status == StatusTrusted
func (r Result) Message() string   // "" when trusted; deterministic actionable string otherwise

// Verify runs the live post-install verification. Expected outcomes (trusted/
// untrusted) are Result.Status; a non-nil error is a genuine failure (mitmdump
// won't spawn, port can't allocate, or the probe could not run / errored).
func (i *Installer) Verify(ctx context.Context) (Result, error)

// Bootstrap is the end-to-end flow `agent-creance setup` will call (AC-0028):
// EnsureCA -> InstallCA -> Verify; returns a non-nil error if verification is not OK.
func (i *Installer) Bootstrap(ctx context.Context) error
```
- `Verify`: `port := ports.Allocate()`; `pid := proc.Spawn(ctx, "mitmdump", bare-args…)`;
  `defer proc.Signal(pid, SIGTERM)`; `outcome, err := i.prober.ProbeViaProxy(ctx,
  fmt.Sprintf("http://127.0.0.1:%d", port), verifyTargetURL)`. Map: err → wrapped error;
  `ProbeTrusted` → `Result{StatusTrusted}`; `ProbeUntrusted` → `Result{StatusUntrusted}`;
  `ProbeError` → `fmt.Errorf("setup: verification probe failed (curl could not validate the
  connection through the proxy)")` (an environment failure, not a trust verdict).
- `Bootstrap`: `EnsureCA` → `InstallCA` → `Verify`; if `!res.OK()` return
  `fmt.Errorf("%s", res.Message())` (so the future command exits non-zero with the actionable text).
- `msgUntrusted` (golden-tested): names the likely cause and the fix, e.g.
  `"CA verification failed: the mitmproxy CA is not trusted by the system trust store. The
  trust dialog may have been cancelled, or the certificate is not trusted for SSL. Re-run
  \`agent-creance setup\`."`

Unit tests + golden (`setup_test.go`, `testdata/`):
- trusted: `FakeTLSProber{Outcome: ProbeTrusted}` → `Result{StatusTrusted}`, `OK()==true`,
  `Message()==""`; assert proxy spawned with the allocated port + SIGTERM teardown; assert the
  prober was called with `http://127.0.0.1:<port>` and `https://example.com`.
- untrusted: `ProbeUntrusted` → `Result{StatusUntrusted}`, `OK()==false`; `Message()` equals the
  golden file (`testdata/verify_untrusted.golden`), with the `-update` flag pattern from
  `internal/prereq/report_test.go`.
- probe error / spawn error / alloc error → non-nil error (genuine failure), SIGTERM still sent
  where a PID exists.
- `Bootstrap`: untrusted → returns the golden message as an error; happy path → nil.

### Success criteria — Phase 3
- [ ] `go build ./...`; `go test -race ./internal/setup/...` green (incl. golden via `make golden` review).
- [ ] Golden file `internal/setup/testdata/verify_untrusted.golden` committed and reviewed.
- [ ] `make lint` clean.
- [ ] **Commit.**

---

## Phase 4 — Integration tests (//go:build integration, gated S1)

`internal/setup/setup_integration_test.go` (`//go:build integration`), mirroring
`internal/sysdep/keychain_integration_test.go` (skip cleanly when preconditions aren't met):

- **`TestVerifyLive`** (always-on under the integration tag): construct an `Installer` with the
  real OS seams (`sysdep.OSKeychain{}`, `OSProcessManager{}`, `OSPortAllocator{}`, `OSTLSProber{}`,
  `OSSleeper{}`, real FS/paths). Run `EnsureCA` (generates the CA if the dev box lacks one — a
  read-mostly, non-destructive op) then `Verify`. If the mitmproxy CA is **already trusted**,
  assert `StatusTrusted`; otherwise `t.Skip` with a pointer to run the opt-in install test. This
  keeps `make test-integration` non-destructive by default.
- **`TestBootstrapLive`** (opt-in, guarded by `if os.Getenv("CREANCE_LIVE_CA_INSTALL") != "1"
  { t.Skip(...) }`): full `EnsureCA` → `InstallCA` → `Verify`. This actually invokes
  `security add-trusted-cert` (prompts for the account password and mutates the developer's
  trust settings), so it must be deliberately opted into — never run silently in CI. Asserts
  `Verify` reports `StatusTrusted` end-to-end. Satisfies ticket verify-step 3.
- **Negative (optional/manual)**: documented in a comment — with the CA removed/untrusted,
  `Verify` returns `StatusUntrusted`. Left as a manual check per the ticket ("manual/optional").

### Success criteria — Phase 4
- [ ] `go build -tags=integration ./...` compiles.
- [ ] `make test-integration` green on a configured dev machine (`TestVerifyLive` passes or skips
      cleanly; `TestBootstrapLive` skipped unless `CREANCE_LIVE_CA_INSTALL=1`).
- [ ] `make lint` clean (integration files included).
- [ ] **Commit.**

---

## Phase 5 — Final verification & ticket close

- [ ] `go build ./...` — compiles.
- [ ] `make test` — full hermetic suite green (race).
- [ ] `make lint` — clean.
- [ ] `make test-integration` — green on the dev machine (document the `CREANCE_LIVE_CA_INSTALL`
      opt-in in the commit/ticket notes).
- [ ] Tick the ticket's Acceptance Criteria + Verification steps; record the run-precondition /
      scope resolutions in the ticket's Notes; set ticket **Status: Done**.
- [ ] **Commit** the ticket update.

## Testing strategy (summary)

- **Pure → table tests:** `ClassifyCurlExit` (0/60/other), `Result.OK()/Message()`, CA-path resolution.
- **Orchestration → fakes:** `EnsureCA`/`InstallCA`/`Verify`/`Bootstrap` driven by
  `FakeFileSystem`, `FakeProcessManager`, `FakePortAllocator`, `FakeKeychain`, `FakeTLSProber`,
  `FakeSleeper`, `FakePathResolver`. Assert idempotency, spawn argv, SIGTERM teardown,
  `AddTrustedCert` path, trusted/untrusted Status, and the golden failure string.
- **Golden:** `internal/setup/testdata/verify_untrusted.golden` (`-update` flag).
- **Integration (`//go:build integration`, S1):** `TestVerifyLive` (non-destructive default),
  `TestBootstrapLive` (opt-in install). External tools (`security`, `mitmdump`, `curl`) only here.

## Automated verification commands (from profile.md)

- Build / typecheck: `go build ./...`
- Tests: `make test` (= `go test -race ./...`); integration: `make test-integration`
- Lint: `make lint`; format: `make fmt`
- Golden: `make golden` (review the diff)

## References

- Research: `thoughts/shared/research/2026-06-07-AC-0026-ca-bootstrap-verification.md`
- Ticket: `thoughts/shared/tickets/AC-0026-ca-bootstrap-verification.md`
- `docs/design.md:426,443`; spec `WP-5.1`; S1 `thoughts/shared/research/2026-06-04-s1-ca-trust.md`
- Patterns: `internal/setupcheck/setupcheck.go`, `internal/cred/cred.go`,
  `internal/proxy/lifecycle.go`, `internal/sysdep/keychain.go`,
  `internal/prereq/report_test.go` (golden), `internal/sysdep/keychain_integration_test.go`.
</content>
