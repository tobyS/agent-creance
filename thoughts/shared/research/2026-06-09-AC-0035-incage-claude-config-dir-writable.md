---
date: 2026-06-09
ticket: AC-0035
title: "Redirected CLAUDE_CONFIG_DIR may not be writable inside the cage"
status: research-complete
branch: main
commit: c30b5a11a4b2908f5efe55799a57a981d60f45d1
repo: git@github.com:tobyS/agent-creance.git
tags: [research, cage, safehouse, config-dir, AC-0035, AC-0033, AC-0034]
---

# Research: AC-0035 — in-cage `CLAUDE_CONFIG_DIR` writability at the real cache location

## Research question

The cage redirects the agent's executable config to an ephemeral dir by setting
`CLAUDE_CONFIG_DIR` to `<cache>/agent-creance/projects/<hash>/claude`. The design
relies on this dir being **writable by the caged agent** (so it persists its own
onboarding/theme/session state) while never touching the real `~/.claude`. The
AC-0033 battery only proves writability with a `$TMPDIR`-backed cache — which
agent-safehouse already grants RW — so it never exercises the real
`~/.cache/agent-creance` location, where safehouse's base policy does *not* grant a
generic `~/.cache`. Confirm the gap and find the narrowest mechanism that makes
**exactly** the config dir writable in-cage, preserving the config-persistence
security property and re-guarding it so it can't silently reopen.

## Summary / answer

- **The gap is real (by construction, not yet empirically reproduced at the real
  location).** The config dir is *seeded from the uncaged side* in `cage.Prepare`
  and exported as the `CLAUDE_CONFIG_DIR` env var in `cage.buildEnv`, but it is
  **never added to a safehouse mount** (`--add-dirs`) nor granted by any
  `--append-profile` fragment. In-cage writability therefore depends entirely on
  the dir happening to live under a path safehouse's base policy already grants RW
  (`/tmp`, `$TMPDIR`, specific toolchain dirs). At the real default
  `~/.cache/agent-creance`, none of those cover it, so the caged agent's writes to
  `CLAUDE_CONFIG_DIR` will be denied. The AC-0033 battery masks this because it
  overrides `XDG_CACHE_HOME` to a `t.TempDir()` (which lands under `$TMPDIR`).
  This is already documented as a known limitation (`docs/cage-verification.md`
  item 2).

- **Recommended fix (narrowest, most idiomatic): mount exactly the config dir RW
  via `--add-dirs`.** Add `in.Layout.ClaudeConfigDir()` to the `--add-dirs` list in
  `cage.Build`. `--add-dirs` *is* safehouse's documented mechanism for "extra RW
  paths"; it grants RW to exactly that one directory (not the project state root,
  not a generic `~/.cache`), it leaves the dir distinct from `~/.claude`, and it
  sidesteps the firmlink/symlink-literal fragility that the SBPL-fragment route
  (AC-0034) had to handle. The alternative — a generated `--append-profile` write
  fragment mirroring AC-0034's `ca.sb` — is viable but is more code, reintroduces
  the symlink-resolution concern, and needs ancestor-traversal metadata grants. See
  the open question below; this is the one decision worth a maintainer confirm.

- **Re-guard via the battery**: relocate the battery's cache to a *non-granted*
  location (a temp dir under `$HOME`, not under `$TMPDIR`) so the existing
  `doc-config-dir` vector turns RED pre-fix and GREEN post-fix — an "equivalent
  guard" for the real-location writability, exactly as AC-0034 added RED→GREEN
  vectors.

- **Effort**: small fix (one mount line + an always-emit tweak), one golden
  regen, one battery-setup change. Mirrors the AC-0034 RED→GREEN flow.

## Detailed findings

### 1. How the config dir is wired today (`internal/cage/cage.go`)

- **`buildEnv` sets the redirect** — `internal/cage/cage.go:239-240`:
  ```go
  // Redirect executable config to the ephemeral, sanitized state-dir config.
  env["CLAUDE_CONFIG_DIR"] = in.Layout.ClaudeConfigDir()
  ```
  Computed (not user-overridable; the loop copies `config.Env` first, then
  overwrites). Exported to the cage via `--env-pass` (`cage.go:108-110`) and
  `Invocation.Env` (`cage.go:115`).

- **`Prepare` seeds it from the *uncaged* side** — `internal/cage/cage.go:172-183`:
  ```go
  dir := in.Layout.ClaudeConfigDir()
  if err := b.fs.MkdirAll(dir, 0o700); err != nil { ... }
  settings := filepath.Join(dir, "settings.json")
  if _, err := b.fs.Stat(settings); errors.Is(err, fs.ErrNotExist) {
      if err := b.fs.WriteFile(settings, []byte("{}\n"), 0o600); err != nil { ... }
  }
  ```
  The dir is created mode `0o700` and seeded with `{}\n` only when absent (existing
  in-cage session state is preserved). All I/O via the `sysdep.FileSystem` seam.

- **`Build` mounts only the user's RW/RO dirs** — `internal/cage/cage.go:80-90`:
  ```go
  if len(sh.AddDirsRW) > 0 {
      args = append(args, "--add-dirs", expandColonList(sh.AddDirsRW, in))
  }
  if len(sh.AddDirsRO) > 0 {
      args = append(args, "--add-dirs-ro", expandColonList(sh.AddDirsRO, in))
  }
  ```
  RW uses `--add-dirs`; RO uses `--add-dirs-ro`. `expandColonList` colon-joins and
  per-entry expands (`cage.go:263-269` → `expandPath` `cage.go:248-259`:
  `~`→home, absolute→Clean, relative/`.`→resolved against `in.Layout.Canonical`).
  **`ClaudeConfigDir()` / `Layout.Root` are added to neither flag.** This is the
  whole gap: the config dir is on disk and in the env, but not mounted into the
  cage.

- **`--append-profile` fragments (the AC-0034 pattern)** — `cage.go:104-106`:
  three profiles (`network.sb`, `proxy.sb`, `ca.sb`), all *read*/network grants;
  no write grant exists anywhere today.

### 2. The config dir's on-disk location (`internal/state/state.go`)

- **Cache root honors `XDG_CACHE_HOME`, else `$HOME/.cache`** —
  `internal/state/state.go:197-209` (`cacheRoot`). Deliberately *not*
  `os.UserCacheDir` (wants XDG-style `~/.cache` on macOS, not `~/Library/Caches`).
- **Per-project root** = `<cache>/agent-creance/projects/<hash>`
  (`state.go:119-126`); `<hash>` = first 8 bytes of SHA-256 of the realpath'd
  project dir, 16 hex chars (`state.go:52-57, 112-117`).
- **Config dir** = `<root>/claude` — `state.go:241-242`:
  ```go
  func (l Layout) ClaudeConfigDir() string { return filepath.Join(l.Root, claudeDirName) }
  ```
- **Siblings** under `Root` (all written/read by the *uncaged* side): `policy.json`,
  `network.sb`, `proxy.sb`, `ca.sb`, `proxy.lock`, `egress.jsonl[.1]`,
  `session-overlay.yaml` (`state.go:211-246`). None of these need in-cage write
  access — confirming the fix should mount **only** `claude/`, not `Root`.

### 3. Why the AC-0033 battery doesn't catch it (`internal/verify`)

- **Cache diverted to `$TMPDIR`** — `internal/verify/verification_integration_test.go:138-145`:
  ```go
  cacheDir := t.TempDir()
  t.Setenv("XDG_CACHE_HOME", cacheDir)
  ...
  layout, err := state.New(paths).Resolve(proj)
  ```
  On macOS `t.TempDir()` is under `$TMPDIR`, which safehouse grants RW — so
  `CLAUDE_CONFIG_DIR` is writable for the *wrong* reason. The production fallback
  (`$HOME/.cache`) is never reached.

- **The `doc-config-dir` vector** — matrix entry `internal/verify/matrix.go:147-151`
  (`Label: LabelDocumented, Expected: "planted", Keyword: "config-persistence"`).
  In-cage probe `internal/verify/testdata/fake-agent.sh:221-229`:
  ```sh
  hookdir="$CLAUDE_CONFIG_DIR/hooks"
  if mkdir -p "$hookdir" 2>/dev/null && (echo '{"planted":true}' >"$hookdir/creance-escape.json") 2>/dev/null; then
      emit doc-config-dir planted
  else
      emit doc-config-dir blocked
  fi
  ```
  Host-side assertions `verification_integration_test.go:91-99`: the plant exists in
  the ephemeral dir **and** that dir is not `~/.claude`.

- **Outcome wiring**: a write *failure* emits `blocked`; since the vector is
  `LabelDocumented` (not BLOCKED), a `blocked` observation sets `Failed` (not
  `Escaped`) via `battery.go:75,88-90`, and `assert.FileExists`
  (`verification_integration_test.go:95`) also fails. So a non-granted cache
  location reliably turns this vector RED.

### 4. The AC-0034 precedent (the pattern to consider mirroring)

AC-0034 made one host file readable in-cage and is the most recent, closest
precedent. It deliberately chose a generated SBPL `--append-profile` fragment over
`--add-dirs-ro ~/.mitmproxy`, **because that directory also holds the CA private
key** and a directory mount would expose it (research
`thoughts/shared/research/2026-06-08-AC-0034-incage-ca-trust-env-files.md:40-66`).
Mechanics worth noting for AC-0035:

- Renderer `profile.RenderCAReadFragment` (`internal/profile/profile.go:100-113`)
  emits `(allow file-read-metadata (literal <dir>))` + `(allow file-read* (literal
  <file>))`. For a **directory** grant the analogous Seatbelt form is
  `(subpath <dir>)` (not `literal`), and the write verb would be `file-write*`
  (none exists in the codebase today — only `file-read*`/`file-read-metadata`).
- State accessor `Layout.CAProfileSB()` (`ca.sb`), written each launch in `Prepare`
  with a **symlink-resolved** path via `b.paths.EvalSymlinks` (firmlinks: Seatbelt
  literals match the kernel's resolved path), appended as the 3rd `--append-profile`
  in `Build`.
- Tests: renderer golden (`internal/profile/testdata/ca.golden`) + behavioral
  asserts (`profile_test.go:134-186`), `prepare_test.go` assertion, invocation
  golden (`internal/cage/testdata/invocation.golden.json`), and two integration
  vectors. RED commit (`6081186`) added vectors that reproduce the failure; GREEN
  commit (`644498f`) shipped the fix.

**Key difference for AC-0035**: the config dir contains *nothing secret* (it's an
ephemeral dir agent-creance owns end-to-end), so the reason AC-0034 avoided a
directory mount **does not apply here**. A whole-directory RW mount via `--add-dirs`
is appropriate and is the idiomatic "extra RW path" mechanism.

## Code references

- `internal/cage/cage.go:80-90` — `Build` RW/RO mount construction (the fix site).
- `internal/cage/cage.go:172-183` — `Prepare` seeds the config dir (mkdir + `{}`).
- `internal/cage/cage.go:239-240` — `buildEnv` sets `CLAUDE_CONFIG_DIR`.
- `internal/cage/cage.go:104-106` — the three `--append-profile` fragments.
- `internal/cage/cage.go:248-269` — `expandPath` / `expandColonList`.
- `internal/state/state.go:241-242` — `ClaudeConfigDir()` accessor.
- `internal/state/state.go:197-209` — `cacheRoot` (XDG honoring / `$HOME/.cache`).
- `internal/verify/verification_integration_test.go:138-145` — battery cache via
  `XDG_CACHE_HOME=t.TempDir()` (the masking setup).
- `internal/verify/verification_integration_test.go:91-99` — `doc-config-dir`
  host-side assertions.
- `internal/verify/matrix.go:147-151` — `doc-config-dir` vector definition.
- `internal/verify/testdata/fake-agent.sh:221-229` — in-cage plant probe.
- `internal/profile/profile.go:100-113` + `caHeader` `:50-53` — AC-0034 renderer.
- `internal/cage/testdata/invocation.golden.json` — argv golden (Root pinned to
  `/home/test/.cache/agent-creance/projects/0123456789abcdef`, so adding the config
  dir to `--add-dirs` is deterministic).
- `docs/cage-verification.md:112-131` (item 2) — the documented limitation.
- `docs/design.md:68` — config-persistence / "the real `~/.claude` is never
  writable" property the fix must preserve.

## Architecture / impact

- **Blast radius is tiny**: `cage.Build` (one always-emit `--add-dirs` including the
  config dir), the invocation golden, and the battery setup. No new sysdep seam, no
  new package. `Prepare` already creates the dir before `Build` references it
  (`Resolve`→`Prepare`→`Build`).
- **Security properties preserved**:
  - The mounted dir is the ephemeral redirect `<root>/claude`, still **distinct
    from `~/.claude`** (the `fs-real-claude` BLOCKED vector and the `doc-config-dir`
    host assert both continue to hold) → AC-0033's `doc-config-dir` assertion is not
    reopened.
  - The mount is **only** `claude/`, not `Root` and not a blanket `~/.cache`, so no
    broadening (egress log, policy, lock file stay unmounted/unwritable in-cage) —
    satisfies the "narrowest mount" acceptance criterion.
- **No interaction with AC-0034**: the CA copy lives in `~/.mitmproxy` (handled by
  `ca.sb`), not in the state dir, so mounting the state config dir does not affect
  CA trust (answers ticket open question 4: no).

## Open questions for the checkpoint

1. **Mechanism: `--add-dirs` RW mount vs. a generated `--append-profile` write
   fragment.** Research strongly favors `--add-dirs in.Layout.ClaudeConfigDir()`
   (idiomatic "extra RW path", narrowest, no firmlink fragility, ~1 line). The
   alternative mirrors AC-0034's SBPL-fragment style for consistency but is more
   code and reintroduces symlink resolution + ancestor-traversal grants. Because
   AC-0034 *deliberately* used an SBPL fragment, this is a maintainer
   consistency-vs-idiom call worth confirming. **Recommend `--add-dirs`.**

(No other blocking questions. Test approach — relocating the battery's
`XDG_CACHE_HOME` to a non-`$TMPDIR`, non-granted temp dir under `$HOME` so
`doc-config-dir` reproduces RED then GREEN — is an implementation detail resolved in
planning.)

## Related documents

- Ticket: `thoughts/shared/tickets/AC-0035-incage-claude-config-dir-writable.md`
- AC-0034 (closest precedent):
  `thoughts/shared/research/2026-06-08-AC-0034-incage-ca-trust-env-files.md`,
  `thoughts/shared/plans/2026-06-08-AC-0034-incage-ca-trust-env-files.md`
- AC-0033 (battery that surfaced this):
  `thoughts/shared/tickets/AC-0033-adversarial-cage-verification.md`
