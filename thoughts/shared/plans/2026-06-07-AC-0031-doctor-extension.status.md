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
- (pending)

## Phase 2: Proxy lifecycle diagnostics — Inspect + CleanOrphan
- **Status**: ⬚ Not started

## Phase 3: CA presence helper + internal/doctor orchestration & rendering
- **Status**: ⬚ Not started

## Phase 4: Wire into the doctor command + testscripts
- **Status**: ⬚ Not started

## Phase 5: Integration test — --fix cleans a real orphan + real seam impls
- **Status**: ⬚ Not started
