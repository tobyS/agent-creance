# Implementation Status: AC-0045 — in-cage credential access

## Phase 1: Seatbelt fragment renderers + state accessors
- **Status**: ✅ Complete
- **Started**: 2026-06-12
- **Completed**: 2026-06-12

### Steps Performed
1. `internal/profile/profile.go`: `RenderKeychainFragment` (exactly the S2
   grant: securityd mach-lookup + `file-write*` regex on
   `login.keychain-db*`), `RenderClaudeStateFragment` (home-dir metadata +
   anchored `~/.claude.json*` prefix-regex RW), shared `validateHome`, and
   `sbplRegexEscape` for interpolating paths into SBPL regexes.
2. `internal/profile/profile_test.go` + `testdata/keychain.golden` /
   `testdata/claude.golden`: golden tests (host-independent `/home/test`
   fixture), least-privilege invariants (keychain = exactly the two S2 rules,
   no read/subpath; claude = file-level only, anchored), regex-escaping test
   (`/home/j.doe`), error cases.
3. `internal/state/state.go` + `state_test.go`: `keychain.sb` / `claude.sb`
   names and `KeychainProfileSB()` / `ClaudeProfileSB()` accessors.

### Issues Encountered
- None.

### Verification
- ✅ `make test`
- ✅ `make lint`
