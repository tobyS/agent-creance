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
- (pending)

## Phase 3: CA presence helper + internal/doctor orchestration & rendering
- **Status**: ⬚ Not started

## Phase 4: Wire into the doctor command + testscripts
- **Status**: ⬚ Not started

## Phase 5: Integration test — --fix cleans a real orphan + real seam impls
- **Status**: ⬚ Not started
