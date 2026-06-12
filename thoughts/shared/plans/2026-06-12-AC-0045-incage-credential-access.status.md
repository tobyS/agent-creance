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

### Commit
- `b02e61a` feat(AC-0045): mount real ~/.claude, drop the config redirect, wire the grants

## Phase 3: Verification battery + threat-model re-pin
- **Status**: ✅ Complete
- **Started**: 2026-06-12
- **Completed**: 2026-06-12

### Steps Performed
1. `docs/design.md:51`: stale bullet rewritten — exceptions (CA PEM,
   `~/.claude`/`~/.claude.json` v0.1 mount, S2 keychain grant) named; the
   "redirected config dir" language removed; keywords the matrix pins kept.
2. `internal/verify/matrix.go`: `fs-real-claude` → `fs-home-write` (BLOCKED:
   the mount must not widen `$HOME`); `doc-config-dir` → `doc-claude-rw`
   (DOCUMENTED: planted file persists in the real `~/.claude`, AC-0046);
   new ALLOWED `kc-read`/`kc-write` (throwaway item) and `claude-json-rw`
   (prefix-named probe file).
3. `fake-agent.sh`: probes renamed/replaced accordingly; new `security
   find/add -U` probes against `CREANCE_KC_SERVICE`/`CREANCE_KC_ACCOUNT`; a
   `~/.claude.json.creance-probe-$$` create/read/delete probe.
4. Integration harness: plants + cleans the throwaway login-keychain item,
   threads the `CREANCE_KC_*` env vars, asserts the `doc-claude-rw` marker in
   the real `~/.claude` (with cleanup), requires `security` on PATH; stale
   cache-placement comment fixed.
5. `docs/cage-verification.md`: §4 re-pinned to the honest v0.1 deferral
   (planted file persists; `$HOME` widening check added), limitation #2
   rewritten to the AC-0045 mechanism, vector count un-hardcoded.

### Issues Encountered
- None. Drift guard green against the reworded design.md.

### Verification
- ✅ `make test` (incl. coverage/drift guards), `make lint`
- ✅ `go vet -tags=integration ./...`; `sh -n fake-agent.sh`
- ⏳ Live `make test-integration` runs in Phase 5.

### Commit
- `37b68ef` feat(AC-0045): re-pin the verification battery to the v0.1 credential posture

## Phase 4: Skill — in-cage auth-failure guidance
- **Status**: ✅ Complete
- **Started**: 2026-06-12
- **Completed**: 2026-06-12

### Steps Performed
1. `internal/setup/SKILL.md`: frontmatter description gains the auth-failure
   activation triggers (in-cage login/onboarding prompt, "Failed to start
   OAuth callback server" / "Is port 0 in use?"); new body section "4.
   Authentication failure — log in on the host, never in the cage" (inbound
   binds blocked by design → host login + restart; locked-keychain note).
   The install self-heal ships the update on the next `setup`/`run`.
2. `internal/setup/skill_test.go`: trigger markers extended; new
   `TestSkillAuthTriggersInFrontmatter` pins the activation language to the
   frontmatter description specifically.

### Issues Encountered
- None.

### Verification
- ✅ `make test`, `make lint`
