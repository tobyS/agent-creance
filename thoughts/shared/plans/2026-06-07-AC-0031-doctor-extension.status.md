# Implementation Status: AC-0031 — doctor extension (WP-6.2)

Plan: `thoughts/shared/plans/2026-06-07-AC-0031-doctor-extension.md`

## Phase 1: New sysdep seams — filesystem type + listener scan
- **Status**: ✅ Complete
- **Started**: 2026-06-07
- **Completed**: 2026-06-07

### Steps Performed
1. `internal/sysdep/fstype.go` — `FilesystemTyper` interface + `FSInfo{Name,Local}`, `OSFilesystemTyper` via `unix.Statfs` (NUL-trimmed `Fstypename`, `MNT_LOCAL` flag). `Fstypename` is `[16]byte` on this platform.
2. `internal/sysdep/listener.go` — `ListenerScanner` interface + `Listener{Command,PID,Address}`, `OSListenerScanner` shelling `lsof -nP -iTCP -sTCP:LISTEN -F pcn`; pure `ParseLsof` + `IsExposed` helpers.
3. Fakes: `sysdeptest/fstype.go` (`FakeFilesystemTyper`), `sysdeptest/listener.go` (`FakeListenerScanner`).
4. Wired `FSType` + `Listeners` into `App` (`cli.go`) and `Main()`.
5. Table tests: `listener_test.go` (ParseLsof, IsExposed), `fstype_test.go` (fstypeName).

### Verification
- ✅ `go build ./...`, `go test ./...` (race) all green
- ✅ `gofmt -s` + `golangci-lint` clean

### Commit
- `0d9ddae` feat(AC-0031): filesystem-type + listener-scan sysdep seams (WP-6.2, Phase 1)

## Phase 2: Proxy lifecycle diagnostics — Inspect + CleanOrphan
- **Status**: ✅ Complete
- **Completed**: 2026-06-07

### Steps Performed
1. `internal/proxy/lifecycle.go` — added `Diagnosis` + `Manager.Inspect` (read-only orphan/stranded detection reusing the Attach liveness composite) and `CleanResult` + `Manager.CleanOrphan` (re-checks under flock; only tears down a true orphan: SIGTERM proxy + purge overlay + clear lock; safe no-op otherwise).
2. `internal/state/state.go` — added `Resolver.CacheDir()` returning `<cache>/agent-creance` for the filesystem warning.
3. Tests: `internal/proxy/diagnose_test.go` (no-lock, healthy, orphan, stranded; clean tears-down / no-op-live-agents / no-op-proxy-down) using the existing harness; `state_test.go` CacheDir cases.

### Verification
- ✅ `go test ./...` (race) all green; `gofmt -s` + `golangci-lint` clean

### Commit
- `c1b8f90` feat(AC-0031): proxy orphan/stranded diagnosis + cleanup (WP-6.2, Phase 2)

## Phase 3: CA presence helper + internal/doctor orchestration & rendering
- **Status**: ✅ Complete
- **Completed**: 2026-06-07

### Steps Performed
1. `internal/setup/setup.go` — added read-only `Installer.CAGenerated()` (Stat the CA cert without generating); test in `setup_test.go`.
2. `internal/doctor/report.go` — `Report` data model (`Status`, `CASection`/`ProxySection`/`ExposedSection`/`FSSection`), pure `Render` (golden-pinned), and `Actionable()` (untrusted CA + un-fixed orphan + missing prereqs).
3. `internal/doctor/doctor.go` — `Checker` + `Run` orchestrating all checks with graceful degradation (status-as-data); `checkCA`/`checkProxy`/`checkExposed`/`checkFS`; `probeFS` ancestor-walk; pure `classifyFS` (iCloud path-based, network via MNT_LOCAL).
4. Tests: `report_test.go` (4 `Render` golden fixtures via `-update`; `classifyFS` + `Actionable` tables); `doctor_test.go` (`Checker.Run` over fakes: CA trusted/untrusted/not-generated/env-error; orphan actionable→fixed; exposed warn/skipped; fs network+iCloud).

### Issues Encountered
- FakeCommander had no tools → prereqs read as missing; seeded both tools in the harness. → resolved.
- Verify's deferred `Signal(0)` on its throwaway mitmdump pollutes `Signaled`; the orphan test filters to pid 111. → resolved.
- revive error-var naming: renamed sentinel `assertErr`→`errBoom`. → resolved.

### Verification
- ✅ `go test ./...` (race) all green; `gofmt -s` + `golangci-lint` clean; goldens generated & reviewed

### Commit
- `d481f09` feat(AC-0031): doctor orchestration, rendering + CA presence (WP-6.2, Phase 3)

## Phase 4: Wire into the doctor command + testscripts
- **Status**: ✅ Complete
- **Completed**: 2026-06-07

### Steps Performed
1. `internal/cli/doctor.go` — rewired to build `doctor.Checker` from the App seams, added `--fix` flag, render the report, and return a one-line error (→ exit 1) when `Actionable()` is non-empty.
2. `internal/cli/doctor_test.go` — `*App`+fakes command tests: healthy exit-0; untrusted-CA exit-non-zero; orphan actionable→`--fix`-cleaned exit-0; exposed service warning exit-0.
3. Testscripts: extended `doctor_healthy.txtar` to assert the new section headers; added `doctor_fix_noop.txtar` (no proxy state → `--fix` no-op exit 0). `doctor_missing.txtar` still green (missing-prereq block still rendered + exit 1).

### Notes
- The new checks degrade gracefully through `cli.Main`'s real seams, so the hermetic testscripts stay deterministic (CA not generated → warning, no project lock → "no proxy state", lsof-absent → skipped). Full-report correctness is the Go unit/golden tests.

### Verification
- ✅ `go test ./...` (race) all green; `gofmt -s` + `golangci-lint` clean

### Commit
- `534a27a` feat(AC-0031): wire full doctor command with --fix (WP-6.2, Phase 4)

## Phase 5: Integration test — --fix cleans a real orphan + real seam impls
- **Status**: ✅ Complete
- **Completed**: 2026-06-07

### Steps Performed
1. `internal/cli/doctor_fix_integration_test.go` — spawns a real mitmdump orphan (real port + a dead agent PID in a real proxy.lock), drives the real `runDoctor(ctx, app, true)` with OS seams, asserts the orphan stops listening, the lock is cleared, the overlay purged, and "cleaned orphan proxy" is reported.
2. `internal/sysdep/listener_integration_test.go` — real `OSListenerScanner` finds a loopback listener (not exposed) and flags a 0.0.0.0 bind as exposed.
3. `internal/sysdep/fstype_integration_test.go` — real `OSFilesystemTyper` returns a non-empty local fstype for a temp dir; a missing path is fs.ErrNotExist.

### Issues Encountered
- Liveness via `kill(pid,0)` is unreliable in this harness: the test process is the proxy's parent and never reaps it, so a SIGTERM'd proxy lingers as a zombie that reads "alive". → Switched the assertion to the closed listen socket (`!PortAllocator.Probe(port)`), the true liveness signal; documented why. In production the short-lived spawner exits and launchd reaps the proxy.

### Verification
- ✅ new integration tests pass (`go test -tags=integration` for the three); `go test ./...` unit suite green; `golangci-lint --build-tags=integration` clean

### Commit
- (pending)
