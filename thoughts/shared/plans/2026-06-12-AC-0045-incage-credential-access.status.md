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

### Commit
- `1373917` feat(AC-0045): render the keychain + claude-state Seatbelt fragments

## Phase 2: Cage rewiring — mount real ~/.claude, drop the redirect
- **Status**: ✅ Complete
- **Started**: 2026-06-12
- **Completed**: 2026-06-12

### Steps Performed
1. `internal/cage/cage.go`: package doc rewritten to the v0.1 posture; `Build`
   mounts `~/.claude` RW instead of the ephemeral config dir and appends
   `keychain.sb` + `claude.sb` as 4th/5th `--append-profile`; `buildEnv` no
   longer sets `CLAUDE_CONFIG_DIR` (comment explains the Keychain service-name
   reason); `Prepare` drops the settings.json seed, ensures `~/.claude`
   exists, and writes both new fragments with the home dir symlink-resolved
   (firmlink idiom shared with the CA path).
2. `internal/state`: `ClaudeConfigDir()` + `claudeDirName` removed.
3. Tests re-pinned: invocation golden regenerated (reviewed: mount swap, two
   new fragments, no `CLAUDE_CONFIG_DIR`); `TestBuildNeverMountsRealClaude` →
   `TestBuildMountsRealClaudeRW` (incl. no-redirect subtests); prepare tests
   rewritten (fragments, `~/.claude` created, never written into,
   resolved-home test).
4. `internal/verify/verification_integration_test.go`: stale ephemeral-config
   assertion block removed so `-tags=integration` compiles; the replacement
   vectors land in Phase 3.

### Issues Encountered
- None.

### Verification
- ✅ `make test`, `make golden` (diff reviewed), `make lint`
- ✅ `go vet -tags=integration ./...` (integration files still compile)
