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
- [ ] **Phase 4 — integration tests (S1-gated)**
- [ ] **Phase 5 — final verification + ticket close**
</content>
