---
date: 2026-06-12
researcher: Tobias Schlitt
git_commit: c353181f51511d813a91d77dc869093b46b7224e
branch: main
repository: agent-creance
topic: "AC-0045 — In-cage credential access via the shared Keychain item"
tags: [research, codebase, AC-0045, keychain, credentials, seatbelt, seeding, verification]
status: complete
last_updated: 2026-06-12
last_updated_by: Tobias Schlitt
---

# Research: AC-0045 — In-cage credential access via the shared Keychain item

**Date**: 2026-06-12
**Researcher**: Tobias Schlitt
**Git Commit**: c353181f51511d813a91d77dc869093b46b7224e
**Branch**: main
**Repository**: agent-creance

## Research Question

A caged Claude Code session cannot authenticate even though the host is logged
in and `run`'s pre-launch credential gate passes (observed live 2026-06-12:
login flow + `OAuth error: Failed to start OAuth callback server … Is port 0 in
use?`). How do we finish the S2-designed mechanism — Seatbelt keychain grant,
minimal auth-state seeding, failure UX, and an automated verification vector —
given the current codebase?

## Summary

Four findings, one of them new and load-bearing:

1. **The Seatbelt grant is missing exactly as the ticket says.** No generated
   fragment carries the S2 grant (mach-lookup `com.apple.SecurityServer` +
   file-write to `login.keychain-db*`), and `--enable=keychain` is never
   emitted. The CA read fragment (`RenderCAReadFragment`) is the complete
   existing pattern to copy: render function with golden test in
   `internal/profile`, written per-launch by `cage.Builder.Prepare`, handed to
   safehouse via a fourth `--append-profile`.
2. **NEW — the service-name mismatch (verified against installed claude
   2.1.175).** When `CLAUDE_CONFIG_DIR` is set, Claude Code does **not** look
   up `Claude Code-credentials`; it derives
   `Claude Code-credentials-<sha256(NFC(configDir))[:8]>`. So even with a
   perfect Seatbelt grant, the caged session queries a non-existent item. The
   binary also contains the escape hatch: if
   `CLAUDE_SECURESTORAGE_CONFIG_DIR` is **set but empty**, the plain
   (host-shared) service name is used regardless of `CLAUDE_CONFIG_DIR`. This
   undocumented env var is the only way to honor the ticket's "same item, no
   copies" posture.
3. **Onboarding gate is independent of credentials.** Claude Code shows
   login/onboarding when `$CLAUDE_CONFIG_DIR/.claude.json` lacks
   `hasCompletedOnboarding` / `oauthAccount` — today the cage seeds only
   `settings.json` with `{}` and **never writes `.claude.json` at all**. The
   minimal non-executable seed is `hasCompletedOnboarding`,
   `lastOnboardingVersion`, and the host's `oauthAccount` object (account
   metadata, no tokens).
4. **All the infrastructure for the rest exists.** The credential gate
   (`internal/cred`) already refuses pre-launch on missing/locked/file-fallback
   with golden-pinned messages; the verification battery
   (`internal/verify`) has a clear vector pattern (matrix entry + fake-agent
   probe + integration harness); the skill (`internal/setup/SKILL.md`) is
   embedded, installed idempotently, and marker-tested.

## Detailed Findings

### 1. Profile generation — the CA fragment is the pattern, the keychain grant is absent

`internal/profile/profile.go` renders three append fragments, each handed to
`agent-safehouse` as a file path via repeated `--append-profile` flags (appended
after safehouse's own base profile; Seatbelt last-match-wins):

- **network.sb** — `RenderNetworkSB` (`profile.go:66-80`): `(deny network*)`
  baseline (`DenyBaseline`, `profile.go:47`) + one
  `(allow network-outbound (remote tcp "localhost:<port>"))` per host service
  (`allowRule`, `profile.go:58-60`). Written by `profile.Compiler.Compile`
  (`compile.go:52-70`) every launch (run step 7).
- **proxy.sb** — `RenderProxyFragment` (`profile.go:86-91`): single allow for
  the live mitmproxy port. Written by `Prepare` every launch.
- **ca.sb** — `RenderCAReadFragment` (`profile.go:100-113`): **the precedent
  for a new keychain fragment**. Validates an absolute path, emits a header +
  exactly two rules via `fmt.Fprintf` with `%q`:
  `(allow file-read-metadata (literal "<dir>"))` +
  `(allow file-read* (literal "<pem>"))`. Golden test
  (`profile_test.go:134-155`, `testdata/ca.golden`) uses host-independent
  fixture path `/home/test/.mitmproxy/...`; invariant test
  `TestRenderCAReadFragment_GrantsOnlyTheOnePEM` (`profile_test.go:157-177`)
  pins least-privilege.

**HOME templating**: `internal/profile` receives fully resolved absolute paths;
resolution happens in `cage.Builder.Resolve` (`cage.go:149-161`) via
`sysdep.PathResolver.UserHomeDir()`. The CA path is additionally
`EvalSymlinks`-resolved in `Prepare` (`cage.go:210-213`) because Seatbelt
literals match kernel-resolved paths (macOS firmlinks) — a keychain-db path
grant needs the same treatment (S2 used a regex
`#"^<HOME>/Library/Keychains/login\.keychain-db"`, which matches the db plus
its `-wal`/`-shm` sidecars).

**Safehouse invocation** (`cage.Build`, `cage.go:77-132`): argv order is
`--add-dirs` (incl. ephemeral config dir), `--add-dirs-ro`, `--enable=<list>`
(only from user YAML `safehouse.enable` — **no code path adds `keychain`**;
golden fixture uses `shell-init`), `--workdir`, three `--append-profile`s
(network → proxy → ca; first two order-load-bearing, `cage.go:112-118`),
`--env-pass <sorted keys>`, `--`, agent command. The full argv+env is pinned in
`internal/cage/testdata/invocation.golden.json` via `TestBuildGolden`
(`cage_test.go:48-64`) — a new fragment/env var shows up there.

**Fragment vs `--enable=keychain`**: AC-0023 research
(`2026-06-06-AC-0023-safehouse-invocation.md:85`) only records that the enable
value exists; its grant breadth is safehouse-version-dependent and not
reviewable. The ticket's AC ("exactly the S2-scoped one … nothing broader; the
grant is visible/reviewable in the generated profile artifacts") is only
satisfiable by generating our own fragment.

**Artifact locations**: fragments live out-of-tree in
`<cache>/agent-creance/projects/<hash>/` (`internal/state/state.go:32-56`,
accessors `NetworkSB()`/`ProxyProfileSB()`/`CAProfileSB()`); a new
`keychain.sb` constant + accessor follows the same shape. `make golden`
discovers golden-bearing packages by grepping for the
`"regenerate golden files"` flag description (`Makefile:92-97`).

### 2. The service-name derivation (verified in claude 2.1.175 on this host)

Extracted from the installed binary
(`/Users/toby/.local/share/claude/versions/2.1.175`), deminified:

```js
function by(suffix = "") {                       // suffix = "-credentials"
  const override = process.env.CLAUDE_SECURESTORAGE_CONFIG_DIR;
  const useDefault = override !== undefined ? !override          // set-but-empty → default
                                            : !process.env.CLAUDE_CONFIG_DIR;
  const dir = override !== undefined ? override.normalize("NFC")
                                     : configDir();              // CLAUDE_CONFIG_DIR or ~/.claude
  const hash = useDefault ? "" : `-${sha256(dir).digest("hex").substring(0, 8)}`;
  return `Claude Code${OAUTH_FILE_SUFFIX}${suffix}${hash}`;
}
```

Consequences:

- With the cage's `CLAUDE_CONFIG_DIR` set (it always is — `buildEnv`,
  `cage.go:256`), the caged Claude Code reads/writes
  `Claude Code-credentials-<hash>` — **not** the host's item. This alone
  explains why a Seatbelt grant by itself cannot fix auth.
- Setting `CLAUDE_SECURESTORAGE_CONFIG_DIR=""` (present, empty) forces
  `useDefault = true` → plain `Claude Code-credentials`, the host's item —
  while `CLAUDE_CONFIG_DIR` keeps redirecting all file state. This is the
  shared-item mechanism. (Setting it to a non-empty path would hash that path
  instead — not useful here.)
- The env var is **undocumented and reverse-engineered** (corroborated by
  community multi-account tooling; hash derivation also reported in
  gists/issues for earlier versions). It must be guarded by the integration
  vector so a future claude release that changes the scheme fails
  `make test-integration` rather than silently breaking auth.
- Reads shell out to
  `security find-generic-password -a "$USER" -w -s "<name>"` (2 s timeout);
  account from `process.env.USER` (sanitized, fallback `claude-code-user`) —
  consistent with `cred.Detect`'s `Getenv("USER")` and the S2 finding that the
  legacy `SecKeychain` path needs the keychain-db file-write for updates.

Web-research corroboration (community, not official): the onboarding gate and
the field set below are stable v1.x→v2.1.x but non-contractual; Anthropic
closed the pre-seeding request as "not planned"
(anthropics/claude-code#4714). Official docs confirm `CLAUDE_CONFIG_DIR`
relocates `~/.claude.json` into the config dir; on macOS the Keychain is the
only credential read path (`.credentials.json` is ignored, #29816).

### 3. Auth/account state seeding — what exists, what's needed

**Today** (`cage.Builder.Prepare`, `cage.go:187-199`): creates
`<state>/claude/` (0700, every launch) and seeds **only** `settings.json` with
`"{}\n"` (0600), gated on `Stat` → `fs.ErrNotExist` ("seed-once") so
agent-written state survives relaunches (rationale at `cage.go:169-176`). No
`.claude.json` is ever written; no host→cage copying exists anywhere
(`TestBuildNeverMountsRealClaude`, `cage_test.go:127-145`, guards against
mounting real `~/.claude`).

**The config dir is persistent per project**
(`<cache>/agent-creance/projects/<hash>/claude`,
`state.go:241-242`), not per-run — so seeding semantics on relaunch matter
(stale `oauthAccount` after the host re-logs-in under a different account).

**Minimal non-executable seed** (verified on this host's `~/.claude.json`):

```json
{
  "hasCompletedOnboarding": true,
  "lastOnboardingVersion": "<host's value>",
  "oauthAccount": { /* host's object: account/org UUIDs, email, roles, tiers — no tokens */ }
}
```

`oauthAccount` is hydrated only by host `/login` (#57026) and contains no
secrets. `lastOnboardingVersion` matters: if much older than the running
version, some versions re-show onboarding screens.

**Write idioms available** (all through `sysdep.FileSystem`):
- seed-once Stat-gate (`Prepare`'s settings.json, `cage.go:192-199`),
- compare-then-atomic-write `writeIfChanged` (`proxy/extract.go:93-114`,
  `setup/skill.go:52-73`).
A `.claude.json` seed that must track host login changes while preserving
in-cage agent-written keys would need a third shape: read host file → extract
the three fields → read cage file → merge → write if changed.

### 4. Credential gate and failure UX

`cred.Detect` (`internal/cred/cred.go:103-117`) classifies via
`sysdep.Keychain.FindGenericPassword(KeychainService, $USER)`
(`KeychainService = "Claude Code-credentials"`, `cred.go:36`; real impl shells
`security find-generic-password -s … -a … -w` with 10 s timeout,
`sysdep/keychain.go:96-105,151-179`; timeout → `ErrKeychainLocked` because a
locked keychain raises a blocking GUI prompt — S2 §4). `run` gates at step 3
(`internal/cli/run.go:71-80`) before any state/proxy/cage work; refusal
messages are golden-pinned (`cred.go:87-96`,
`internal/cred/testdata/refuse_*.golden`). The locked keychain is additionally
caught earlier by `setupcheck.Verify` (`setupcheck.go:100-110`). Note: only
`run` calls `cred.Detect` — `doctor` does not (contrary to the package doc
comment).

So "missing credential refuses pre-launch with an actionable message" already
holds; the gap is **mid-session** unusable credentials (expired beyond
refresh, keychain locked mid-session), which today surface as the cryptic
OAuth/port error. The in-cage OAuth callback server can never bind
(`(deny network*)` baseline with outbound-only allows, `profile.go:47,58-60`)
— by design (design.md:461).

### 5. The verification battery — vector pattern

`internal/verify/`:
- `matrix.go:51-152` — 18 `Vector` entries (`ID`, `Label` ∈
  BLOCKED/ALLOWED/DOCUMENTED, `Expected`, `Keyword` (design.md drift guard;
  ALLOWED vectors exempt, `coverage_test.go:43-45`), `DesignRef`, `Egress`,
  `Desc`).
- `testdata/fake-agent.sh` — POSIX-sh probe run **inside** the real cage; emits
  `CREANCE::<id>::<observed>` per vector (`emit()`, line 32); inputs arrive as
  `CREANCE_*` env vars.
- `battery.go` — pure evaluator (`ParseProbeOutput` + `Evaluate`), unit-tested.
- `verification_integration_test.go` (`//go:build integration`) — live
  harness: real proxy via `proxy.NewManager(...).Attach`, real
  `cage.Resolve/Prepare/Build`, execs the invocation, parses output;
  `TestCageVerificationNegativeControl` proves the harness detects a weakened
  cage. Skips when host tools are missing or sandbox nesting is denied.
  Fixtures are planted in `runBattery` (lines 135-228) with `t.Cleanup`
  (e.g. the planted secret outside cage mounts, lines 159-163).

**A credential vector** would be `LabelAllowed` (failure = battery FAILURE, not
escape), `Egress: false`: `runBattery` plants a **throwaway** generic-password
item (synthetic service name, e.g. `agent-creance-verify-<nonce>`) in the real
login keychain via `security add-generic-password` + `t.Cleanup` delete;
threads the service name through a `CREANCE_*` env var; `fake-agent.sh` probes
`security find-generic-password -s "$CREANCE_KC_SERVICE" -w` and emits
found/denied. This proves mach-lookup reachability from inside the cage without
ever touching `Claude Code-credentials`. A write-path probe
(`add-generic-password -U` on the throwaway item) would additionally exercise
the keychain-db file-write grant — but note S2: writes go to
`login.keychain-db` itself, so the throwaway item must live in the **login**
keychain for the write probe to be meaningful.

### 6. The skill — where auth guidance would go

`internal/setup/SKILL.md` (embedded via `//go:embed`, `skill.go:22-23`) is
installed idempotently to `~/.claude/skills/agent-creance/SKILL.md`
(`InstallSkill`, `skill.go:35-45`; `writeSkillIfChanged`, `skill.go:52-73` —
self-heals stale content, so an updated skill ships on next `setup`/`run`).
Frontmatter `description` defines activation triggers (currently only egress
403s / `agent_cage_*` errors); body has three sections (allowed / soft-deny /
hard-deny). `skill_test.go` pins content markers
(`TestSkillContentMentionsTriggers`, `skill_test.go:93-104`) and idempotency.
A new auth-failure section needs: new trigger language in the frontmatter
description (OAuth callback error, login prompt in cage), a body section
("log in on the host, restart the cage; login can never happen in-cage"), and
new marker assertions. `setupcheck.Verify` only checks the file exists — stale
content self-heals on install, not on verify.

## Code References

- `internal/profile/profile.go:47,58-60` — deny-all baseline, outbound-only allows
- `internal/profile/profile.go:100-113` — `RenderCAReadFragment`, the fragment pattern
- `internal/profile/profile_test.go:134-177` — golden + least-privilege invariant tests
- `internal/cage/cage.go:77-132` — `Build` argv assembly (`--enable`, `--append-profile`, `--env-pass`)
- `internal/cage/cage.go:187-223` — `Prepare`: settings.json seed-once + per-launch fragments
- `internal/cage/cage.go:236-259` — `buildEnv`: computed env (incl. `CLAUDE_CONFIG_DIR`) overrides user env
- `internal/cage/testdata/invocation.golden.json` — pinned full invocation
- `internal/state/state.go:32-56,241-242` — artifact names, persistent per-project `claude/` dir
- `internal/cred/cred.go:36,87-96,103-137` — service constant, refusal messages, Detect
- `internal/sysdep/keychain.go:96-105,151-195` — `security` CLI invocation + error mapping
- `internal/cli/run.go:62-80,123,158-177` — setup check, cred gate, compile, Prepare/Build/Run order
- `internal/verify/matrix.go:34-152` — vector schema + matrix
- `internal/verify/testdata/fake-agent.sh` — in-cage probe script
- `internal/verify/verification_integration_test.go:135-264` — live harness + fixtures
- `internal/setup/SKILL.md`, `internal/setup/skill.go:22-73` — embedded skill + install idiom
- `internal/setupcheck/setupcheck.go:100-122` — CA/keychain-locked + skill-file checks

## Architecture Insights

- Fragments are the reviewable grant surface: every cage capability beyond
  safehouse's base profile is a generated, golden-tested `.sb` file under the
  project state dir — a keychain grant should be the fourth fragment, not an
  opaque `--enable` toggle.
- `buildEnv`'s computed-overrides-user precedence (`cage.go:225-227`) is the
  right place for `CLAUDE_SECURESTORAGE_CONFIG_DIR=""`: the user must not be
  able to repoint secure storage any more than `HTTPS_PROXY`.
- The persistent per-project config dir means seeding is a merge problem, not
  a write-once problem, for state that tracks the host (auth fields) — unlike
  `settings.json` whose `{}` must never clobber agent-written state.
- The battery's ALLOWED vectors are exactly the "false-negative guard" slot
  for the credential mechanism: a profile regression flips the vector to a
  FAILURE.

## Historical Context (from thoughts/)

- `thoughts/shared/research/2026-06-04-s2-keychain.md` — the authoritative S2
  spike: exact grants (`mach-lookup com.apple.SecurityServer` necessary and
  sufficient for read; file-write regex
  `^<HOME>/Library/Keychains/login\.keychain-db` for refresh), no
  code-signing/ACL binding, concurrent refresh serialized by securityd, locked
  keychain → blocking GUI prompt, and the design.md wording fix (the grant is
  file-level, not "the item's ACL").
- `thoughts/shared/research/2026-06-06-AC-0022-credential-detection.md` —
  detection-only scope; grant explicitly deferred to AC-0014/AC-0023 (both
  shipped without it — the gap this ticket closes).
- `thoughts/shared/research/2026-06-06-AC-0023-safehouse-invocation.md` —
  `--enable=keychain` exists but unused; sanitized-seed contents were an open
  question ("exact contents not specified anywhere").
- `thoughts/shared/plans/2026-06-09-AC-0035-…` — made the config dir an RW
  mount (`--add-dirs`), why `claude/` is persistent.
- `thoughts/shared/tickets/AC-0044-incage-dx-state.md` — sibling: all
  non-auth config state is out of scope here.
- `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md` —
  spec-level framing of keychain item, battery, skill, `.claude.json` seed.

## Related Research

- `2026-06-08-AC-0033-adversarial-cage-verification.md` — battery design.
- `2026-06-07-AC-0027-skill-install.md` — skill install mechanism.

## Open Questions

1. **Reliance on `CLAUDE_SECURESTORAGE_CONFIG_DIR=""`** — undocumented,
   reverse-engineered from claude 2.1.175. It is the only mechanism that
   honors the decided "same item, no copies" posture (the alternative —
   duplicating the credential under the derived hash-suffixed name — creates
   exactly the token-copy/divergence the ticket rejects). Needs user sign-off
   + the integration vector as a regression tripwire. Does the user accept
   pinning auth on this undocumented env var?
2. **`.claude.json` seeding semantics on relaunch** — seed-once (stale
   `oauthAccount` if the host re-logs-in differently) vs merge-auth-fields-
   every-launch (host wins for the three auth fields; agent-written keys
   preserved). Research favors merge-every-launch but it is a behavior choice.
3. **Write-path probe in the credential vector** — read probe (mach-lookup
   reachability) is straightforward with a throwaway item; should the vector
   also exercise the keychain-db file-write grant by updating the throwaway
   item in the **login** keychain (touches the real db file, never the real
   credential)?
4. (Minor) Whether safehouse's `--env-pass` forwards a set-but-empty env var
   faithfully — to be confirmed by the integration vector; if not, the value
   can be set to a constant non-empty sentinel only if claude's derivation
   changes, so empty-string forwarding must be verified first.
