# AC-0026 implementation status

Plan: `2026-06-07-AC-0026-ca-bootstrap-verification.md`

- [x] **Phase 1 — new sysdep seams** (commit: feat AC-0026 Phase 1)
  - Keychain.AddTrustedCert; TLSProber + ClassifyCurlExit; Sleeper; fakes + tests.
  - `go build`, `go test -race ./internal/sysdep/...`, `make lint` green.
- [x] **Phase 2 — internal/setup EnsureCA + InstallCA** (commit: feat AC-0026 Phase 2)
  - Installer + NewInstaller; idempotent EnsureCA (generate via throwaway mitmdump,
    poll for the CA file, SIGTERM teardown) + InstallCA. Unit tests with fakes.
  - `go build`, `go test -race ./internal/setup/...`, `make lint` green.
- [x] **Phase 3 — internal/setup Verify + Bootstrap + golden** (commit: feat AC-0026 Phase 3)
  - Verify (spawn bare proxy, probe via curl seam, classify), Bootstrap, Status/Result/
    Message; golden `testdata/verify_untrusted.golden`. Unit tests with fakes.
  - `go build`, `go test -race ./internal/setup/...`, `make lint` green.
- [x] **Phase 4 — integration tests (S1-gated)** (commit: test AC-0026 Phase 4)
  - `setup_integration_test.go`: `TestVerifyLive` (non-destructive: generate-if-absent
    + verify, skip if untrusted) and `TestBootstrapLive` (opt-in via
    `CREANCE_LIVE_CA_INSTALL=1`, does the real `add-trusted-cert`).
  - `go build -tags=integration ./...` + `go vet -tags=integration` green.
  - **NOTE:** could not be *executed* in this dev harness — `~/.mitmproxy` returns
    EPERM here even to a plain `ls`/`stat` (an environment restriction, not a code
    issue; the code correctly surfaces the genuine stat error). Run it on an
    unrestricted machine with `make test-integration`.
- [x] **Phase 5 — final verification + ticket close** (commit: docs AC-0026 close)
  - `make test` (hermetic, race), `make lint`, `go build ./...`,
    `go build -tags=integration ./...` all green.
  - Ticket ACs ticked, open questions answered, Notes added, Status: Done.

**All phases complete.** Live integration tests await execution on an unrestricted
machine (`make test-integration`); everything else is verified green.
</content>
