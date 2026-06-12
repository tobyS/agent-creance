# AC-0045: In-cage credential access via the shared Keychain item

## Overview

A caged Claude Code session cannot authenticate even though the host is logged
in. Per the decided v0.1 posture (ticket Notes 2026-06-12, decision commit
`eea7622`): the cage mounts the real `~/.claude` (and `~/.claude.json`)
**read-write**, does **not** redirect `CLAUDE_CONFIG_DIR`, and gains exactly
the S2-scoped Seatbelt keychain grant so the caged agent reads/refreshes the
same plain `Claude Code-credentials` login-Keychain item as host Claude Code.
The ephemeral config dir (mount, env redirect, settings.json seed) is removed.
Failure UX gains "log in on the host" guidance in the shipped skill, and the
cage-verification battery gains credential vectors plus a re-pin of the
config-isolation vectors to the deferred config cage (AC-0046).

## Current State Analysis

All grounded in `thoughts/shared/research/2026-06-12-AC-0045-incage-credential-access.md`
plus a post-decision re-read of the pivot's blast radius:

- **No keychain grant exists.** `internal/profile` renders three fragments
  (`network.sb`, `proxy.sb`, `ca.sb`), handed to safehouse via ordered
  `--append-profile` flags (`cage.go:111-118`). Nothing emits the S2 grant;
  `--enable=keychain` is never passed.
- **The ephemeral config dir is wired in three places:**
  - `cage.Build` always appends `Layout.ClaudeConfigDir()` to `--add-dirs`
    (`cage.go:98`) and `buildEnv` sets `CLAUDE_CONFIG_DIR` (`cage.go:256`).
  - `cage.Builder.Prepare` creates the dir and seeds `settings.json` `{}`
    seed-once (`cage.go:187-199`).
  - `state.Layout.ClaudeConfigDir()` + `claudeDirName` (`state.go:49,241-242`).
- **Tests pinning the old posture:** `TestBuildNeverMountsRealClaude`
  (`cage_test.go:127-145`), `TestBuildAlwaysMountsConfigDir`,
  `TestExpandPathViaArgs` (expects the config dir appended),
  `TestBuildEnvPassMatchesEnvKeys` (expects `CLAUDE_CONFIG_DIR`),
  `prepare_test.go` (settings.json seed tests), the invocation golden
  (`internal/cage/testdata/invocation.golden.json`), and `state_test.go`.
- **Battery vectors pinning the old posture:** `fs-real-claude` (BLOCKED,
  keyword "~/.claude") and `doc-config-dir` (DOCUMENTED, keyword
  "config-persistence") in `matrix.go:58-62,147-151`; the fake-agent probes
  (`fake-agent.sh:68-76,221-229`); harness assertions on the planted hook in
  the ephemeral dir (`verification_integration_test.go:91-99`). The drift
  guard (`coverage_test.go`) requires each BLOCKED/DOCUMENTED keyword to
  appear in design.md's threat-model section.
- **design.md** was mostly corrected in the decision commit (lines 64-68 about
  v0.1 mounting `~/.claude` RW; 462-474 credential story), **but line 51 is
  stale**: it still claims "its redirected Claude config dir" and "the *real*
  `~/.claude` are all denied" — contradicting line 68. Must be re-worded when
  the matrix is re-pinned.
- **Already in place, unchanged:** the pre-launch credential gate
  (`internal/cred`, golden-pinned refusal messages, locked-keychain detection
  in `setupcheck.go:100-110`); the skill install/self-heal mechanism
  (`internal/setup/skill.go`); no CLI testscript asserts safehouse argv.
- **S2 grant (validated, `2026-06-04-s2-keychain.md`):**
  `(allow mach-lookup (global-name "com.apple.SecurityServer"))` is necessary
  and sufficient for read; refresh additionally needs
  `(allow file-write* (regex #"^<HOME>/Library/Keychains/login\.keychain-db"))`
  (covers the db + `-wal`/`-shm` sidecars). No read grant on the db file is
  needed (reads are securityd-mediated). No code-signing/ACL binding. Locked
  keychain → blocking GUI prompt (already detected pre-launch).

## Desired End State

- `agent-creance run` produces a cage where Claude Code reads its real
  `~/.claude.json` account state and the plain `Claude Code-credentials`
  Keychain item — authenticated out of the box; token refresh writes back to
  the shared item.
- The keychain grant is a generated, golden-tested `keychain.sb` fragment
  containing exactly the S2 grant and nothing else.
- `~/.claude.json` reachability is a generated, golden-tested `claude.sb`
  fragment (file-level grant; `~/.claude` itself is a normal `--add-dirs` RW
  mount).
- No `CLAUDE_CONFIG_DIR` is set in the cage; the ephemeral dir code and its
  state accessor are gone.
- The skill explains the in-cage auth-failure case (host-only login).
- `make test-integration` exercises keychain read+write from inside the real
  cage against a throwaway item, plus the re-pinned config vectors.

### Key Discoveries (decisions baked into this plan)

- **Two new fragments, not `--enable=keychain`:** the AC requires the grant to
  be "exactly the S2-scoped one … nothing broader … visible/reviewable in the
  generated profile artifacts". safehouse's `--enable=keychain` breadth is
  version-dependent and unreviewable. `RenderCAReadFragment` +
  `ca.golden` + the least-privilege invariant test is the pattern to copy.
- **`keychain.sb` carries ONLY the S2 grant.** The `~/.claude.json` grant goes
  in a separate `claude.sb` so the keychain artifact stays exactly
  reviewable against S2.
- **`~/.claude` via `--add-dirs`, `~/.claude.json` via fragment.** `--add-dirs`
  is the established directory-mount mechanism; whether safehouse accepts a
  *file* path there is unverified, so the file grant is emitted by our own
  fragment as a prefix regex `^<HOME>/\.claude\.json` — this also covers
  Claude's sibling writes (`.claude.json.backup` etc.). Residual risk (an
  atomic-write temp name NOT prefixed `.claude.json`) is covered by the live
  verification AC and the integration vector.
- **Regex escaping is required:** the home path is interpolated into SBPL
  regexes; a home dir containing regex metacharacters (e.g. `/Users/j.doe`)
  must be escaped. New helper in `internal/profile`.
- **Home is symlink-resolved in `Prepare`** (best-effort, same idiom as the CA
  path, `cage.go:210-213`) because Seatbelt matches kernel-resolved paths.
- **Vector re-pin:** `fs-real-claude` (BLOCKED) becomes `fs-home-write`
  (BLOCKED — `$HOME` outside `./` and `~/.claude` is still denied, proving the
  mount didn't widen to all of home); `doc-config-dir` is replaced by
  `doc-claude-rw` (DOCUMENTED — planting into the real `~/.claude` succeeds;
  the honest AC-0046 deferral). New ALLOWED vectors: `kc-read`, `kc-write`
  (throwaway login-keychain item; never the real credential) and
  `claude-json-rw` (create/read/delete `~/.claude.json.creance-probe-$$`,
  exercising the `claude.sb` regex grant without touching the real file).
- **Throwaway keychain item:** planted by the harness via
  `security add-generic-password -a <acct> -s agent-creance-verify-<nonce> -w <val>`
  (default = login keychain, which the write probe needs), deleted in
  `t.Cleanup`. The in-cage probe and the host planter are the same
  `/usr/bin/security` binary, so the item ACL admits the probe without a GUI
  prompt (S2-validated).
- The pre-launch missing-credential refusal already satisfies its AC bullet —
  no `internal/cred` changes.

## What We're NOT Doing

- No config cage / `CLAUDE_CONFIG_DIR` redirect of any kind — **AC-0046**.
- No `.claude.json` seeding/merging — the real file is used directly (the
  earlier merge-on-launch checkpoint decision is superseded by the mount).
- No use of `CLAUDE_SECURESTORAGE_CONFIG_DIR` or any undocumented Claude
  internals (moot without the redirect).
- No `--enable=keychain`.
- No API-key auth, no non-Claude secret injection, no in-cage OAuth.
- No tested-against-claude-version constant in `internal/buildinfo` (that
  hardening belonged to the rejected env-var posture).

## Implementation Approach

Five phases, each independently green and committed: (1) pure fragment
renderers + state accessors, (2) cage rewiring (mounts/env/Prepare) and test
re-pin, (3) battery matrix/probes/harness + design.md threat-model re-pin,
(4) skill auth guidance, (5) final verification incl. the integration battery.

---

## Phase 1: Seatbelt fragment renderers + state accessors

### Changes Required:

#### 1. `internal/profile/profile.go`

- New header consts `keychainHeader`, `claudeStateHeader` (style of `caHeader`,
  citing AC-0045 + S2).
- New helper:

```go
// sbplRegexEscape escapes a literal path for interpolation into an SBPL regex.
func sbplRegexEscape(p string) string  // escape \ . + * ? ( ) [ ] { } | ^ $
```

- `RenderKeychainFragment(home string) (string, error)` — validates `home` is
  non-empty and absolute, emits exactly:

```
;; agent-creance keychain.sb — … (AC-0045, spike S2). Generated; do not edit.
(allow mach-lookup (global-name "com.apple.SecurityServer"))
(allow file-write* (regex #"^<rx(home)>/Library/Keychains/login\.keychain-db"))
```

- `RenderClaudeStateFragment(home string) (string, error)` — same validation,
  emits the `~/.claude.json` file-level RW grant (the `~/.claude` dir itself is
  a `--add-dirs` mount, not handled here):

```
;; agent-creance claude.sb — … v0.1 config-cage deferral (AC-0045/AC-0046). Generated; do not edit.
(allow file-read-metadata (literal "<home>"))
(allow file-read* file-write* (regex #"^<rx(home)>/\.claude\.json"))
```

  (The metadata grant on the home dir literal mirrors the `ca.sb` precedent —
  reachability for open() under safehouse's deny-default; entries' existence
  only, no sibling contents.)

#### 2. `internal/profile/profile_test.go` + `testdata/`

- Golden tests `TestRenderKeychainFragment` / `TestRenderClaudeStateFragment`
  with fixture home `/home/test` → `testdata/keychain.golden`,
  `testdata/claude.golden` (follow `profile_test.go:134-155`).
- Invariant tests (follow `TestRenderCAReadFragment_GrantsOnlyTheOnePEM`):
  - keychain fragment contains **no** `file-read`, no `subpath`, exactly one
    `mach-lookup` and one `file-write*` rule scoped to `login\.keychain-db`;
  - claude fragment contains no `subpath`, and its write rule is anchored at
    `\.claude\.json`;
  - a home dir with regex metacharacters (e.g. `/home/j.doe`) is escaped (the
    rendered regex must not treat the dot as a wildcard);
  - relative/empty home → error.

#### 3. `internal/state/state.go` + `state_test.go`

- New consts `keychainSBName = "keychain.sb"`, `claudeSBName = "claude.sb"`;
  accessors `KeychainProfileSB()`, `ClaudeProfileSB()` (doc comments noting
  4th/5th `--append-profile`, rewritten per launch). `ClaudeConfigDir` removal
  happens in Phase 2 with its last callers.

### Success Criteria:

#### Automated Verification:
- [ ] `make test` passes (incl. new golden + invariant tests)
- [ ] `go build ./...` passes
- [ ] `make lint` passes

#### Manual Verification:
- [ ] Review `testdata/keychain.golden` against the S2 grant — exactly two
  rules, nothing broader.

---

## Phase 2: Cage rewiring — mount real `~/.claude`, drop the redirect, write the fragments

### Changes Required:

#### 1. `internal/cage/cage.go`

- **Package doc:** rewrite the paragraph claiming "deliberately never mounts
  the real ~/.claude" to the v0.1 posture (mounts it RW; config-persistence
  vector documented, AC-0046; keychain via `keychain.sb`).
- **`Build`:**
  - RW mounts: replace `in.Layout.ClaudeConfigDir()` with
    `filepath.Join(in.HomeDir, ".claude")`; update the comment block
    (`cage.go:91-99`).
  - Append-profiles: add `in.Layout.KeychainProfileSB()` and
    `in.Layout.ClaudeProfileSB()` after `ca.sb` (filesystem/mach rules are
    order-independent of the network pair; keep network → proxy first).
- **`buildEnv`:** delete the `CLAUDE_CONFIG_DIR` line + comment. (A host-set
  `CLAUDE_CONFIG_DIR` cannot leak in: safehouse only forwards `--env-pass`
  keys.)
- **`Prepare`:**
  - Drop the config-dir MkdirAll + `settings.json` seed block entirely.
  - `MkdirAll(filepath.Join(in.HomeDir, ".claude"), 0o700)` — ensure the mount
    target exists on first run (no-op when present; `setup`'s skill install
    already creates it on most hosts).
  - Resolve `home := in.HomeDir` via `b.paths.EvalSymlinks` (best-effort, same
    idiom as the CA path) and write `profile.RenderKeychainFragment(home)` →
    `KeychainProfileSB()` and `profile.RenderClaudeStateFragment(home)` →
    `ClaudeProfileSB()` every launch (host-dependent, like `ca.sb`).
  - Update the `Prepare` doc comment.

#### 2. `internal/state/state.go`

- Remove `claudeDirName` + `ClaudeConfigDir()` (callers gone after this
  phase); update `state_test.go`.

#### 3. Test re-pin (`internal/cage`)

- `make golden` → review `invocation.golden.json` diff: `--add-dirs` now
  `.:/home/test/.claude`-style, two extra `--append-profile`s, no
  `CLAUDE_CONFIG_DIR` in env/`--env-pass`.
- `TestBuildNeverMountsRealClaude` → **invert** into
  `TestBuildMountsRealClaudeRW`: `--add-dirs` contains `<home>/.claude` even
  with empty `AddDirsRW`; env contains **no** `CLAUDE_CONFIG_DIR`.
- `TestBuildAlwaysMountsConfigDir` → re-target to the `~/.claude` mount
  (guards the v0.1 posture instead of AC-0035).
- `TestExpandPathViaArgs`: expected `--add-dirs` suffix becomes
  `/home/test/.claude`.
- `TestBuildEnvPassMatchesEnvKeys`: drop `CLAUDE_CONFIG_DIR` from the
  required-names list.
- `prepare_test.go`: replace seed tests with: fragments written and matching
  the renderers for the (fake-resolved) home; `~/.claude` dir created;
  port-change rewrite test kept.

### Success Criteria:

#### Automated Verification:
- [ ] `make test` passes
- [ ] `make golden` diff reviewed and committed
- [ ] `make lint` passes

#### Manual Verification:
- [ ] `bin/agent-creance run` on this repo launches a caged `claude` that shows
  the normal authenticated prompt — no login/onboarding (ticket AC, verified
  live). *(Deferred to Phase 5's live check if more convenient.)*

---

## Phase 3: Verification battery + threat-model re-pin

### Changes Required:

#### 1. `docs/design.md` (threat-model section, line 51 area)

- Rewrite the first "Prevented" bullet: host files outside `./` are denied
  **except** the deliberate v0.1 exceptions — the mitmproxy CA PEM (read,
  `ca.sb`) and the real `~/.claude`/`~/.claude.json` (read-write, the AC-0046
  deferral); `$HOME` itself stays unwritable. Keep the keywords the matrix
  pins (see below) present in the section.
- Verify the "Not prevented" config-persistence parenthetical (line 68) still
  carries the `config-persistence` keyword (it does — no change expected).

#### 2. `internal/verify/matrix.go`

- `fs-real-claude` → replace with:
  `{ID: "fs-home-write", Label: LabelBlocked, Expected: "blocked", Keyword: "outside `./`", DesignRef: "design.md:51", Desc: "write into $HOME outside ./ and ~/.claude → denied (the ~/.claude mount must not widen to home)"}`.
- `doc-config-dir` → replace with:
  `{ID: "doc-claude-rw", Label: LabelDocumented, Expected: "planted", Keyword: "config-persistence", DesignRef: "design.md:68", Desc: "the real ~/.claude is mounted RW: a planted file persists — the documented v0.1 config-persistence deferral (AC-0046)"}`.
- New ALLOWED vectors (drift-guard-exempt):
  - `kc-read` — Expected `found`: in-cage `security find-generic-password` on
    the throwaway item succeeds (mach-lookup grant).
  - `kc-write` — Expected `updated`: in-cage
    `security add-generic-password -U` on the throwaway item succeeds
    (login.keychain-db file-write grant).
  - `claude-json-rw` — Expected `rw-ok`: create + read back + delete
    `~/.claude.json.creance-probe-$$` (the `claude.sb` prefix-regex grant).
- Keep `Keyword: "~/.claude"` represented: attach it to `doc-claude-rw`?
  No — one keyword per vector; `doc-claude-rw` uses `config-persistence`.
  Ensure design.md's rewritten bullet still contains "~/.claude" only if a
  vector pins it; otherwise no constraint. Adjust
  `coverage_test.go`'s `required` list if wording changes (it pins
  `config-persistence`, `outside ./`, etc. — all retained).

#### 3. `internal/verify/testdata/fake-agent.sh`

- Rename the `$CREANCE_HOME` write probe's emit ID to `fs-home-write`
  (probe logic unchanged — `$HOME/.creance-escape-$$`).
- Replace the `doc-config-dir` probe with `doc-claude-rw`: plant
  `$CREANCE_HOME/.claude/creance-escape-marker.json` → `planted`/`blocked`.
- New probes (inputs via `CREANCE_KC_SERVICE`, `CREANCE_KC_ACCOUNT`):

```sh
if security find-generic-password -a "$CREANCE_KC_ACCOUNT" -s "$CREANCE_KC_SERVICE" -w >/dev/null 2>&1; then
    emit kc-read found
else
    emit kc-read blocked
fi
if security add-generic-password -U -a "$CREANCE_KC_ACCOUNT" -s "$CREANCE_KC_SERVICE" -w updated-by-cage >/dev/null 2>&1; then
    emit kc-write updated
else
    emit kc-write blocked
fi
probe="$CREANCE_HOME/.claude.json.creance-probe-$$"
if (echo probe >"$probe") 2>/dev/null && [ "$(cat "$probe" 2>/dev/null)" = "probe" ] && rm -f "$probe" 2>/dev/null; then
    emit claude-json-rw rw-ok
else
    emit claude-json-rw blocked
fi
```

#### 4. `internal/verify/verification_integration_test.go`

- `requireUncagedHost`: add `security` to the required tools.
- `runBattery`: plant the throwaway item
  (`security add-generic-password -a <user> -s agent-creance-verify-<nonce> -w verify-secret`)
  with `t.Cleanup` running `security delete-generic-password -a … -s …`;
  thread `CREANCE_KC_SERVICE`/`CREANCE_KC_ACCOUNT` through `cfg.Env`.
- Post-battery assertions: replace the ephemeral-config-dir block
  (`verification_integration_test.go:91-99`) with: the planted
  `~/.claude/creance-escape-marker.json` exists in the **real** `~/.claude`
  (documenting the deferral) and is removed via `t.Cleanup`; also clean up any
  leftover `.claude.json.creance-probe-*` defensively.
- Negative control unchanged (network-baseline strip still detects escapes).

#### 5. `docs/cage-verification.md`

- Update the vector list/wording to the new matrix (fs-home-write,
  doc-claude-rw, kc-read/kc-write/claude-json-rw; CLAUDE_CONFIG_DIR vector
  removed).

### Success Criteria:

#### Automated Verification:
- [ ] `make test` passes (drift guard green against the reworded design.md)
- [ ] `make lint` passes
- [ ] `go vet ./...` clean (part of lint)

#### Manual Verification:
- [ ] Reworded design.md threat-model bullet reads honestly (exceptions named,
  no stale "redirected config dir" language). *(integration battery itself
  runs in Phase 5)*

---

## Phase 4: Skill — in-cage auth-failure guidance

### Changes Required:

#### 1. `internal/setup/SKILL.md`

- Frontmatter `description`: extend triggers — also use when Claude Code
  inside the cage shows a login/onboarding prompt or an OAuth error like
  "Failed to start OAuth callback server" / "Is port 0 in use?".
- New body section "4. Authentication failure — log in on the host": the cage
  blocks inbound binds by design, so in-cage OAuth login can never work; the
  fix is: tell the user to run `claude` (login) **on the host, outside the
  cage**, then restart the caged session; never retry the in-cage login flow.

#### 2. `internal/setup/skill_test.go`

- Extend `TestSkillContentMentionsTriggers` (or sibling) with markers:
  `OAuth callback`, `log in on the host` (or the exact phrases used), both in
  frontmatter-description and body. Idempotency/self-heal tests need no change
  (content hash changes propagate automatically).

### Success Criteria:

#### Automated Verification:
- [ ] `make test` passes
- [ ] `make lint` passes

#### Manual Verification:
- [ ] Skill text gives actionable, correct guidance (host login, restart; no
  in-cage retry).

---

## Phase 5: Final verification

- `make test` && `make lint` && `make build` (binary refreshed for the user).
- `make test-integration` — the full battery incl. `kc-read`/`kc-write`/
  `claude-json-rw`/`doc-claude-rw` against a real cage on this host.
- Live AC check (user-assisted where needed):
  - `bin/agent-creance run` in a project → caged Claude Code reaches an
    authenticated prompt with no login step.
  - After a caged session, host `claude` is still logged in (refresh
    divergence check — may need a long session or token near expiry; verify
    best-effort).
- Update ticket: status, acceptance-criteria checkboxes, implementation-plan
  pointer, notes entry with the implementation commits.

### Success Criteria:

#### Automated Verification:
- [ ] `make test` passes
- [ ] `make lint` passes
- [ ] `make test-integration` passes (or skips only for documented host
  reasons)
- [ ] `make build` succeeds; `bin/agent-creance` reflects the final commit

#### Manual Verification:
- [ ] Caged session authenticated out of the box (ticket AC 1) — verified live 2026-06-12: `claude doctor` in-cage reports logged-in state; keychain item READ + refresh-WRITE (`security add-generic-password -U`) both succeed in-cage; host login intact afterwards
- [ ] Host login intact after caged refresh (ticket AC 2, best-effort) — verified via in-cage write to throwaway item + host read-back; real credential untouched and host `claude` still authenticated

## Testing Strategy

- **Pure logic:** table-driven + golden tests in `internal/profile`
  (fragments) and `internal/state` (accessors).
- **Invocation:** the cage golden (`invocation.golden.json`) pins the full
  argv+env; inverted mount test guards the new posture.
- **Battery:** fast suite covers matrix/evaluator/drift-guard; the real
  keychain + cage behavior is integration-tagged only (never in `make test`).
- **Never touch the real credential:** all keychain probes use the throwaway
  `agent-creance-verify-<nonce>` item; `.claude.json` probes use a
  prefix-named sibling file, not the real file.

## References

- Ticket: `thoughts/shared/tickets/AC-0045-incage-credential-access.md`
- Research: `thoughts/shared/research/2026-06-12-AC-0045-incage-credential-access.md`
  (§1 fragment pattern, §5 vector pattern; the §2 env-var posture is
  **superseded** by the decision in the ticket notes)
- S2 spike: `thoughts/shared/research/2026-06-04-s2-keychain.md` (exact grant)
- Decision commit: `eea7622` (design.md credential/config story, AC-0046)
- Follow-ups: `thoughts/shared/tickets/AC-0046-config-cage-revisit.md`
  (re-isolation), AC-0044 (mooted for v0.1)
