# AC-0068a — implementation status

Plan: `thoughts/shared/plans/2026-06-29-AC-0068a-secretresolver-seam.md`

- [x] Phase 1 — Extend the `Commander` seam with a secret-safe stdout capture
- [x] Phase 2 — `SecretResolver` seam: interface, impl, fake, tests
- [x] Phase 3 — Wire `SecretResolver` into the composition root

**COMPLETE.** All phases implemented, committed to `main`, and verified
(`make test` + `make lint` + `make build` green). Ticket → Done.

## Log

### 2026-06-29
Ticket → In Progress. Research + plan committed.

All three phases implemented:
- Phase 1: `Commander.OutputStdout` (separated-stream, stdout-only) on the
  interface + `ExecCommander`; `FakeCommander.OutputStdout` + a `Calls` recorder +
  compile-time assertion.
- Phase 2: `internal/sysdep/secretresolver.go` (`SecretResolver`, sentinels, pure
  parse helpers, `OSSecretResolver` composing Commander/Keychain/PathResolver);
  white-box helper tests; black-box dispatch tests; integration test (op:// /
  keychain://); `sysdeptest.FakeSecretResolver` + its test.
- Phase 3: `App.SecretResolver` field + `Main()` wiring; no consumer yet.

Hermetic verification (in-cage): `go build ./...`, `go vet ./...`,
`gofmt -s -l`, and `go test -race ./...` ALL GREEN except four pre-existing
socket-binding test groups the cage blocks (`bind: operation not permitted` in
TestOSHTTPGetter*/TestOSListener*/TestOSTLSProber*/TestOSPortAllocator* — files
untouched by this ticket). `go vet -tags=integration` of the new integration
test also compiles.

**Out-of-cage batch (cage closed by the user) — DONE.** Three phase commits
landed on `main` (028c2de, 3eb0ab1, 57bd934); each pre-commit ran the full
`go test -race ./...` green. `make lint` clean, `make build` OK. `make
test-integration`: the SecretResolver live tests pass (keychain://) / skip (op://
when no `AC_TEST_OP_REF` / tool); the only failing vectors are `internal/verify`
`kc-read`/`kc-write`, confirmed **pre-existing/environmental** — they fail
identically on the pre-ticket commit (781ea09), and this ticket touches no
verify/profile/cage/keychain-grant code. Ticket → Done.
