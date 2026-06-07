---
date: 2026-06-07
topic: "AC-0026 — CA bootstrap + post-install verification (WP-5.1)"
status: complete
kind: ticket-research
branch: main
git_commit: da6f565b44b7983c2a6e844e97b1f52266928f81
ticket: AC-0026
work_package: WP-5.1
depends_on: [AC-0009, AC-0001]
spike_gate: S1 (AC-0001)
source_design: docs/design.md
sources:
  - thoughts/shared/tickets/AC-0026-ca-bootstrap-verification.md
  - thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md
  - thoughts/shared/research/2026-06-04-s1-ca-trust.md
  - thoughts/shared/research/2026-06-04-s2-keychain.md
  - docs/design.md
---

# AC-0026 — CA bootstrap + post-install verification (WP-5.1)

## Research question

How should `internal/setup` (a net-new package) generate the mitmproxy CA if absent,
install it into the login keychain via `security add-trusted-cert`, and run a *live*
post-install verification (spawn a short-lived proxy, `curl https://example.com` through it,
validate the chain against the system trust store) — erroring explicitly on the
"silent-cancel" failure mode where `add-trusted-cert` returns 0 even though trust was never
applied — while staying within the project's seam/testability conventions and reusing the
existing proxy/keychain/process/port infrastructure?

## TL;DR / recommendation

- **AC-0026 delivers a library + integration tests, not a command.** The ticket's own
  *Out of Scope* names AC-0028 (the `setup` command wiring) and AC-0027 (skill install). So
  AC-0026 ships `internal/setup` with `EnsureCA` / `InstallCA` / `Verify` (+ a `Bootstrap`
  convenience), unit-tested with fakes and golden strings, plus `//go:build integration`
  live tests. No `setup`/`doctor` cobra command is added here.
- **The package composes existing seams** — `FileSystem` (CA-file presence), `ProcessManager`
  (spawn/​kill the throwaway mitmdump), `PortAllocator` (random port + readiness probe),
  `PathResolver` (`~/.mitmproxy`) — and needs **two small new OS primitives**:
  1. `Keychain.AddTrustedCert(certPath string) error` — extend the existing Keychain seam
     (it already wraps `/usr/bin/security`). Must **not** use the 10 s find-timeout: the
     trust dialog can block on the user for far longer.
  2. A thin curl-through-proxy probe seam (proposed `sysdep.TLSProber`) whose single OS job
     is "HTTPS GET `url` through HTTP proxy `proxyURL`, validating against the **system trust
     store only** (no `--cacert`), report the outcome." Exit-code → outcome mapping is a pure,
     table-tested helper (`0`→trusted, `60`→untrusted/cancelled-trust, `35`/other→environment
     error).
- **The verification must NOT pass `--cacert`.** S1 proved interception works when the CA is
  trusted *via env vars*; AC-0026's whole point is to prove **keychain/system-trust**, so the
  curl probe must rely on the system trust store alone. `--cacert $CA` would mask exactly the
  silent-cancel failure this ticket exists to catch.
- **The CA lives at the default `~/.mitmproxy/mitmproxy-ca-cert.pem`** (not a project-private
  confdir), because the runtime proxy (`internal/proxy`, `mitmArgs`) uses mitmproxy's default
  confdir and `setupcheck.CACommonName = "mitmproxy"` already assumes the default-generated CA.
- **One genuine open question for the checkpoint:** the ticket AC says the live verification is
  "reusable by `doctor` (AC-0031) and `run`'s precondition check," but AC-0025 already decided
  `run` uses the *cheap* `setupcheck.Verify` (keychain presence) and that "run must not pay
  that [live] cost on every launch." Confirm: keep `run` on the cheap check, reserve the live
  `Verify` for `setup`/`doctor`.

## The ask, decomposed (from the ticket)

Acceptance criteria → concrete obligations:

1. **Generate the CA via mitmproxy if `~/.mitmproxy/` lacks one; idempotent if present.**
2. **Install the CA into the login keychain** (`security add-trusted-cert`).
3. **Post-install verification:** short-lived proxy on a random port, `curl https://example.com`
   through it, assert the chain validates against the system trust store.
4. **Verification failure → explicit, actionable error** naming the likely cause (cancelled
   prompt / missing trust), non-zero exit.
5. **The same verification is reusable** by `doctor` (AC-0031) and `run`'s precondition check.

Verification steps the ticket mandates: `go build ./...`; `go test -race ./internal/setup/...`
with fakes (success path + a golden failure string + non-zero result); an integration test
(gated S1) that does generate+install+verify end-to-end on a real machine; an optional
negative integration test (untrusted cert → fails loudly); `make lint` clean.

## Where this sits in the design

- `docs/design.md` table (line 85): `internal/setup` = "CA bootstrap + post-install
  verification, skill install"; sections "The proxy and the credential story" / "Commands".
- `docs/design.md:443` (**Post-install CA verification**) is the canonical spec:
  > `security add-trusted-cert` on macOS returns exit code 0 even when the user cancels the
  > auth dialog or the trust policy is set wrong — failure mode where the cert exists in the
  > keychain but isn't actually trusted by clients. After running the install, `agent-creance
  > setup` does a live verification: spawn a short-lived mitmproxy on a random localhost port,
  > make a `curl` request to `https://example.com` through that proxy, and verify the cert
  > chain validates against the system trust store. If verification fails, the user gets an
  > explicit error pointing at the likely cause … The same verification runs as part of
  > `agent-creance doctor` on every invocation.
- `docs/design.md:426`: "The first-ever run generates a CA in `~/.mitmproxy/`, which
  `agent-creance setup` installs into the login keychain (one `sudo` prompt, one time). After
  that the agent trusts the CA via the system trust store, plus belt-and-suspenders env vars
  (`NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE`, `GIT_SSL_CAINFO`)."
- Spec `WP-5.1` (`…/2026-06-04-v0.1-technical-specification.md:312-316`): same shape, marked
  **Integration**, gated **S1**.
- **Gate S1 (AC-0001) is satisfied.** `thoughts/shared/research/2026-06-04-s1-ca-trust.md`:
  interception validates end-to-end against the trusted mitmproxy CA (`POST /v1/messages`
  through the proxy succeeds, no host pinned). Crucially, S1 trusted the CA **via env vars
  only, not keychain-installed** (its §"Environment"/§"Limitations"): *"The keychain-install +
  live-verify path is AC-0026 (WP-5.1); the silent-cancel failure mode of
  `security add-trusted-cert` is out of scope here."* AC-0026 is exactly that follow-on.

## Current state of the codebase

### There is no `internal/setup` yet — this ticket creates it

The package list under `internal/` has no `setup`. The closest neighbours, all recently added,
are the building blocks AC-0026 composes:

| Package | Role | Reuse for AC-0026 |
|---|---|---|
| `internal/setupcheck` | *cheap* run-precondition: is the `mitmproxy` cert present in the keychain + is the skill file present | `CACommonName = "mitmproxy"` constant; structural template (Status-as-data + `Message()`); **NOT** the live check |
| `internal/proxy` | refcounted mitmdump lifecycle (`Manager`), `mitmArgs`, enforcer extractor | pattern for spawning/​tearing down mitmdump; the throwaway verify-proxy is a *bare* mitmdump (no enforcer) |
| `internal/cred` | OAuth-credential detection (Keychain vs file) | structural template (`Detect` → `Result`/`Status`/`Message`) |
| `internal/sysdep` (+ `sysdeptest`) | the OS seams + fakes | the seams listed below |

### Seams already available (with exact signatures)

- **`sysdep.Keychain`** (`internal/sysdep/keychain.go:23`) — read-only today:
  `FindGenericPassword(service, account string) ([]byte, error)` and
  `FindCertificate(commonName string) ([]byte, error)`. Real impl `OSKeychain` shells to
  `/usr/bin/security` via `runSecurity(args)` (`:98`) under `securityFindTimeout = 10s` (`:61`),
  mapping exit `44`→`ErrItemNotFound`, timeout→`ErrKeychainLocked`, else `errUnexpectedSecurity`
  (with stderr). `interpretSecurityErr(exitCode, timedOut)` (`:133`) is the pure, table-tested
  mapping. Fake: `sysdeptest.FakeKeychain` with `WithCertificate(cn, pem)`, recorders
  `CertLookups`/`Lookups`, `Locked bool`, `Errs` map.
- **`sysdep.ProcessManager`** (`internal/sysdep/processmanager.go:25`):
  `Spawn(ctx, name, args...) (pid int, err error)` (detached, stdout/stderr discarded),
  `Alive(pid) bool`, `Signal(pid, sig os.Signal) error` (ESRCH = already-gone = nil). Fake:
  `FakeProcessManager` (`SpawnPID`/`SpawnErr`, `AlivePIDs` oracle, `Spawned`/`Signaled`
  recorders).
- **`sysdep.PortAllocator`** (`internal/sysdep/portallocator.go:26`): `Allocate() (int, error)`
  (binds `127.0.0.1:0`), `TryReclaim(port) (bool, error)`, `Probe(port) bool` (200 ms TCP dial).
  Fake: `FakePortAllocator` (`AllocPort`/`AllocErr`, `Listening` oracle).
- **`sysdep.FileSystem`** — `Stat` for CA-file presence (used the same way `setupcheck`/`cred`
  stat the skill/credentials file). Fake: `FakeFileSystem` (`Files`, `StatErrs`).
- **`sysdep.PathResolver`** — `UserHomeDir()` / `Getenv` to resolve `~/.mitmproxy`. Fake:
  `FakePathResolver` (`HomeDir`, `HomeErr`).
- **`sysdep.Commander`** (`internal/sysdep/commander.go:24`): `LookPath`, `Output(ctx, name,
  args...) ([]byte, error)` — **combined** output, generic error. **Note:** `FakeCommander`
  keys outputs by executable *name only* (ignores args) and `Errs` returns a plain error — it
  cannot simulate per-arg output or an exit code. This is why a dedicated probe seam (below) is
  preferable to driving `curl` through `Commander` for the trust check.
- **`sysdep.Clock`** (`internal/sysdep/clock.go:12`): `Now()` / `Since(t)` only — **no `Sleep`/
  `After`.** Relevant to the readiness-wait decision (below).
- **`App`** (`internal/cli/cli.go:20`) already injects `Keychain`, `ProcessManager`,
  `PortAllocator`, `FS`, `Paths`, `Clock`, `HTTP`, `Commander` — every seam a future
  `setup`/`doctor` command would need is already on `App`.

### The cheap-vs-live split is already designed and partly built

`internal/setupcheck/setupcheck.go` is explicit about the division of labour (its package doc,
lines 9-15): it probes **presence** of the `mitmproxy` cert in the login keychain as the cheap
check, and says verbatim:

> presence proves the import happened, not that the trust dialog was confirmed (security
> add-trusted-cert returns 0 even on cancel, design.md). **The robust live verification — spawn
> mitmproxy, curl through it, validate the chain — stays setup/doctor's job; run must not pay
> that cost on every launch.**

`run.go:60` already wires `setupcheck.Verify(app.Keychain, app.FS, app.Paths)` as run's
precondition. **So the "live verification" AC-0026 builds is for `setup`/`doctor`, and the
"run precondition" is the already-shipped cheap check.** (See open question.)

## How each acceptance criterion maps to an implementation

### AC1 — Generate the CA if `~/.mitmproxy/` lacks one (idempotent)

- **Detect:** `FileSystem.Stat(filepath.Join(home, ".mitmproxy", "mitmproxy-ca-cert.pem"))`.
  Present → skip generation (idempotent). `home` via `PathResolver.UserHomeDir()`.
- **Generate:** there is **no standalone "make CA" mitmproxy subcommand** (web research,
  docs.mitmproxy.org/concepts/certificates). The CA is materialised as a side effect of the
  first `mitmdump` start. So: `ProcessManager.Spawn(ctx, "mitmdump", "--listen-host",
  "127.0.0.1", "--listen-port", <port>, "-q")`, wait until `mitmproxy-ca-cert.pem` exists, then
  `Signal(pid, SIGTERM)`. The files produced are `mitmproxy-ca.pem` (cert+key),
  **`mitmproxy-ca-cert.pem` (cert only — the one to trust)**, `…-cert.p12`, `…-cert.cer`.
- **Readiness wait:** we must wait for the CA file to appear after spawn. The Clock seam has no
  `Sleep`. Options (decide in plan): (a) add a tiny `sysdep.Sleeper` seam (`Sleep(ctx, d)`)
  with an instant fake — cleanest, reusable; (b) poll `PortAllocator.Probe` then read the file;
  (c) fold generation behind a single integration seam. **Recommendation: (a).** In unit tests
  the fake FS returns the CA present on the first check, so the sleep never fires.

### AC2 — Install the CA into the login keychain

- New `Keychain.AddTrustedCert(certPath string) error`, real impl shells to
  `security add-trusted-cert -r trustRoot -p ssl -k <login.keychain-db> <certPath>` (per-user
  domain, **no `-d`** → no admin/system change, no `sudo`; modern macOS still prompts for the
  account password to authorise the trust-settings write — web research, Apple DTS forum #671582).
- **Do not reuse `runSecurity`'s 10 s timeout** — the auth dialog can block on the human far
  longer. Use a generous/no timeout for this call.
- **Treat the exit code as advisory, not authoritative.** Research confirmed the silent-cancel
  failure mode: the cert can be imported while trust is *not* applied, and exit status is
  unreliable across macOS versions/domains. `InstallCA` surfaces only genuine spawn/exec
  failures as errors; **proof of trust is AC3's live verification**, never this call's exit code.

### AC3 — Live post-install verification

Orchestration in `internal/setup` composing seams:
1. `port := PortAllocator.Allocate()`.
2. `pid := ProcessManager.Spawn(ctx, "mitmdump", "--listen-host","127.0.0.1",
   "--listen-port",<port>,"-q")` — a **bare** mitmdump (no enforcer addon, no policy).
3. Wait until listening: `PortAllocator.Probe(port)` (or let curl's `--retry-connrefused`
   absorb the startup race — see below). `defer ProcessManager.Signal(pid, SIGTERM)`.
4. Probe: HTTPS GET `https://example.com` through `http://127.0.0.1:<port>`, **validating
   against the system trust store only** (no `--cacert`). Via the new `TLSProber` seam.
5. Classify the outcome (pure helper) → `Result{Status, …}`.

**curl invocation** (real impl of the probe): `curl -sS -o /dev/null --proxy
http://127.0.0.1:<port> https://example.com` — for an `https://` target through an HTTP proxy,
curl issues CONNECT and validates the re-signed leaf against the system trust store. Exit codes
(web research, everything.curl.dev/cmdline/exitcode): **`0`** = trusted (TLS validated) →
success; **`60`** = "peer certificate cannot be authenticated with known CA certificates" =
**CA not trusted** (the silent-cancel / missing-trust signal); **`35`** / other = environment
error (proxy unreachable, DNS) — distinct from a trust verdict. `--retry 5
--retry-connrefused --retry-delay 1` lets curl absorb mitmdump's startup race in-subprocess
(no Go-side sleep for readiness). **Never** add `-k/--insecure` (defeats the test).

### AC4 — Explicit, actionable error on failure; non-zero exit

Follow the `setupcheck`/`cred` convention: `Verify` returns `(Result, error)` where expected
outcomes (trusted / untrusted / locked) are `Status` data and `error` is reserved for genuine
failures (mitmdump won't spawn, port can't allocate). `Result.Message()` returns a deterministic,
**golden-testable** string for the untrusted case, e.g. naming "the trust dialog may have been
cancelled, or the CA isn't trusted for SSL — re-run `agent-creance setup`." The future `setup`/
`doctor` command prints `Message()` and returns a non-nil error → exit 1 (the same
`return fmt.Errorf(...)` → `Main` → exit-1 pattern used by `doctor.go`/`run.go`).

### AC5 — Reusable by doctor and run's precondition

`Verify` is a public method on the `setup` package's installer, taking injected seams — directly
callable by the future `doctor` (AC-0031) and `setup` (AC-0028) commands. **Run's precondition
is the already-shipped cheap `setupcheck.Verify`** (see open question / AC-0025 decision).

## Proposed shape (for the plan to refine)

```go
// internal/setup
type Installer struct { fs; kc; proc; ports; prober; sleeper; paths } // sysdep seams
func NewInstaller(...) *Installer

func (i *Installer) EnsureCA(ctx context.Context) (certPath string, err error) // AC1
func (i *Installer) InstallCA(ctx context.Context, certPath string) error       // AC2
func (i *Installer) Verify(ctx context.Context) (Result, error)                 // AC3/4/5
func (i *Installer) Bootstrap(ctx context.Context) error // EnsureCA→InstallCA→Verify (what setup calls)

type Status int // Trusted, Untrusted, KeychainLocked(?)…
type Result struct { Status Status }
func (r Result) OK() bool
func (r Result) Message() string // deterministic, golden-tested
```

New sysdep additions:
- `Keychain.AddTrustedCert(certPath string) error` (+ `OSKeychain` impl + `FakeKeychain` recorder).
- `sysdep.TLSProber` seam: `ProbeViaProxy(ctx, proxyURL, targetURL string) (ProbeOutcome, error)`
  (+ `OSTLSProber` curl impl + `FakeTLSProber`), with an exported pure `ClassifyCurlExit(code int)
  ProbeOutcome` table-tested helper.
- (decision) `sysdep.Sleeper` for bounded readiness polling, or rely on curl `--retry-connrefused`
  + a `Sleeper` only for CA-file generation.

## Testing strategy (mirrors project conventions)

- **Pure logic → table tests:** `ClassifyCurlExit` (0/60/35/other), `Result.Message()`/`OK()`,
  CA-path resolution.
- **Orchestration → fakes:** drive `EnsureCA`/`InstallCA`/`Verify` with `FakeFileSystem`,
  `FakeProcessManager`, `FakePortAllocator`, `FakeKeychain`, `FakeTLSProber`, `FakePathResolver`.
  Assert: idempotent skip when CA present; spawn argv + SIGTERM teardown; `AddTrustedCert` called
  with the right path; trusted vs untrusted Status; **golden failure string** + non-OK result
  (ticket verify-step 2).
- **Golden files** in `internal/setup/testdata/` with the `-update` flag (mirror
  `internal/prereq/report_test.go` and `internal/cred/testdata/refuse_*.golden`).
- **`//go:build integration`** live test (gated S1) — generate+install+verify end-to-end, assert
  trusted; mirror `internal/sysdep/keychain_integration_test.go` (skip cleanly when keychain
  locked / preconditions absent). Optional negative live test: an untrusted/removed cert →
  `Status == Untrusted`.
- External tools (`security`, `mitmdump`, `curl`) **only** under the integration tag — unit tests
  stay hermetic.

## Open questions (for the checkpoint)

1. **Run-precondition wording vs the AC-0025 decision.** Ticket AC5 says the live verification is
   "reusable by … `run`'s precondition check," but `setupcheck` (AC-0025) deliberately makes run
   use the *cheap* keychain-presence check and states "run must not pay that [live] cost on every
   launch." **Proposed:** keep `run` on `setupcheck.Verify`; expose the live `setup.Verify` for
   `setup`/`doctor` only. Confirm.
2. **Scope.** Confirm AC-0026 = `internal/setup` library + unit/golden + integration tests, with
   **no** cobra command wired this ticket (the `setup` command is AC-0028, `doctor` extension is
   AC-0031, both explicitly out of scope / separate tickets). Acknowledge that without a command,
   the integration test is the only end-to-end driver until AC-0028 lands.
3. **Seam placement** (lower-stakes, default chosen): extend the existing `Keychain` seam with
   `AddTrustedCert` (vs a new `TrustStore` seam), and add a dedicated `TLSProber` curl seam (vs
   driving curl through `Commander`, which the fake can't model by arg/exit-code). Flagging for
   awareness; will proceed with the defaults unless you object.

## Code references

- `thoughts/shared/tickets/AC-0026-ca-bootstrap-verification.md` — the ticket.
- `docs/design.md:443` — Post-install CA verification spec (silent-cancel + live curl test).
- `docs/design.md:426` — CA in `~/.mitmproxy/`, install into login keychain, belt-and-suspenders.
- `thoughts/shared/research/2026-06-04-s1-ca-trust.md` — S1 gate: interception works (env-var
  trust); keychain-install + live-verify explicitly deferred to AC-0026.
- `internal/setupcheck/setupcheck.go:9-15,99` — cheap check; `CACommonName = "mitmproxy"`; the
  explicit cheap-vs-live division of labour.
- `internal/cli/run.go:60` — run wires the cheap `setupcheck.Verify`.
- `internal/proxy/lifecycle.go:230` (`mitmArgs`), `:132` (spawn), `:168` (SIGTERM teardown) —
  mitmdump spawn/teardown pattern (the verify-proxy is a *bare* mitmdump, no enforcer).
- `internal/sysdep/keychain.go:23,98,133` — Keychain seam, `runSecurity`, `interpretSecurityErr`
  (extend with `AddTrustedCert`; do **not** reuse the 10 s timeout).
- `internal/sysdep/processmanager.go:25`, `portallocator.go:26`, `commander.go:24`,
  `clock.go:12` — the seams to compose (note Clock has no `Sleep`; FakeCommander ignores args).
- `internal/cli/cli.go:20` — `App` already injects every needed seam.
- `internal/prereq/report_test.go`, `internal/cred/testdata/refuse_*.golden` — golden-test pattern.
- `internal/sysdep/keychain_integration_test.go` — `//go:build integration` live-test precedent.

## Sources (web research)

- docs.mitmproxy.org/stable/concepts/certificates — CA in `~/.mitmproxy`, generated on first run,
  `mitmproxy-ca-cert.pem` is the cert to trust; no standalone generate command.
- ss64.com/mac `security add-trusted-cert` / `verify-cert` / `dump-trust-settings`; Apple Developer
  Forums #671582 (Big Sur prompt + cert-imported-but-not-trusted), anchordotdev/cli#34 (cancel →
  non-zero in the admin path) — the silent-cancel evidence base; verify trust *functionally*.
- everything.curl.dev — `-x/--proxy` (CONNECT for https), exit `60` (untrusted CA) vs `35`
  (TLS-connect/environment), `--retry-connrefused` for startup races.
- mitmproxy/mitmproxy#4544 — mitmdump can hang if the listen port is taken; pre-allocate the port
  and bound readiness with a timeout.
</content>
</invoke>
