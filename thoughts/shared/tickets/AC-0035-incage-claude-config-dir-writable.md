# AC-0035: Redirected CLAUDE_CONFIG_DIR may not be writable inside the cage

**Status:** Done
**Estimated Complexity:** Medium
**Created:** 2026-06-08
**Updated:** 2026-06-09

## Problem Statement

The cage redirects the agent's executable config to an ephemeral, sanitized
directory by setting `CLAUDE_CONFIG_DIR` to `<cache>/agent-creance/projects/
<hash>/claude` (see `internal/cage/cage.go` `buildEnv`, `internal/state` layout).
The design relies on this dir being **writable by the caged agent** so the agent
can persist its own session state (onboarding, theme, etc.) while never touching
the real `~/.claude` — that redirect is what closes the config-persistence vector.

The AC-0033 battery surfaced that the cache root is `~/.cache/agent-creance`, but
agent-safehouse's base policy grants read-write only to `/tmp`, `$TMPDIR`, and a
fixed set of toolchain dirs (`~/.cargo`, `~/.cache/go-build`, …) — **not a generic
`~/.cache`**. The cage adds only the user's configured `add_dirs_rw` (default `.`,
the project) to the mounts; it does **not** add `CLAUDE_CONFIG_DIR`. So during a
real `agent-creance run` (cache under `~/.cache`), the redirected config dir may
not be writable inside the cage — which would break the agent's ability to persist
config, the very thing the redirect is meant to enable.

This is currently **unverified end-to-end**: the AC-0033 battery passes the
`doc-config-dir` vector because it points `XDG_CACHE_HOME` at a `t.TempDir()` under
`$TMPDIR` (which safehouse *does* grant), so the test does not exercise the real
`~/.cache` location. The gap needs a real-location repro to confirm.

## Desired Outcome

During a real caged run with the cache at its default `~/.cache/agent-creance`
location, the caged agent can create and write files under `CLAUDE_CONFIG_DIR`
(e.g. seed/update `settings.json`, write session state), while that directory
remains the ephemeral redirect and never the real `~/.claude`. Proven by a caged
reproduction and guarded by AC-0033.

## User Stories / Use Cases

- As a developer running a caged agent, I want the agent to persist its own
  config/session state across runs so that I don't get a broken/repeating
  onboarding experience inside the cage.
- As the maintainer, I want the config-dir redirect to be functional (writable) at
  the real cache location, not just in tests, so that the documented persistence
  behavior actually holds.

## Acceptance Criteria

- [x] A caged reproduction at a non-granted cache location (a temp dir under `$HOME`,
      outside safehouse's base RW grants — the production-equivalent of
      `~/.cache/agent-creance`, without touching the real cache) demonstrated the
      failure first: `doc-config-dir want=planted got=blocked` (RED commit).
- [x] After the fix, the caged agent creates dirs and writes files under
      `CLAUDE_CONFIG_DIR` at that location: `doc-config-dir got=planted` (GREEN).
- [x] The redirected dir remains distinct from the real `~/.claude`: `fs-real-claude`
      stays BLOCKED and the host-side assert confirms the plant lands only in the
      ephemeral dir — AC-0033's `doc-config-dir` security assertion is not re-opened.
- [x] The fix does not broaden cage writability beyond the config dir: `cage.Build`
      mounts exactly `…/projects/<hash>/claude` via `--add-dirs`, not the state root
      and not a blanket `~/.cache`.
- [x] The AC-0033 battery now exercises real-location writability — `runBattery`
      places the cache under `$HOME` (a non-granted path), so `doc-config-dir` would
      turn RED again if the mount regressed.

## Out of Scope

- Relocating the out-of-tree state root away from `~/.cache` (CLAUDE.md is explicit
  that security-critical runtime state lives there; this ticket is about making the
  config dir writable in-cage, not moving it).
- The in-cage CA-file trust gap (separate ticket, AC-0034).
- Any change to what gets seeded into the config dir (still just `{}` settings).

## Open Questions

- None blocking — maintainer-facing correctness fix.

## Questions for Research/Planning — resolved

- [x] Empirically confirmed: the caged agent **cannot** write `CLAUDE_CONFIG_DIR` at
      a non-granted cache location. safehouse's base policy does not cover `~/.cache`
      (only `/tmp`, `$TMPDIR`, specific toolchain dirs). The `$TMPDIR`-backed test
      cache passed only because `$TMPDIR` is granted. Reproduced RED in the battery.
- [x] Mechanism (maintainer decision): add `CLAUDE_CONFIG_DIR` to `--add-dirs` in
      `cage.Build`. `--add-dirs` is safehouse's idiomatic "extra RW path" mechanism,
      narrowest, and avoids the firmlink/symlink-literal handling an `--append-profile`
      SBPL write-grant would need. AC-0034 used an SBPL fragment only because its
      target dir held a secret (the CA private key); the config dir holds nothing
      secret, so a directory mount is appropriate here.
- [x] Only the config dir is mounted, not the project state root. The other state
      files (network.sb/proxy.sb/policy.json/egress.jsonl/lock) are read/written by
      the uncaged side, so they need no in-cage access — keeping them unmounted
      preserves their integrity.
- [x] No interaction with AC-0034: the CA copy lives in `~/.mitmproxy` (granted via
      `ca.sb`), not in the state dir, so mounting the config dir does not affect CA
      trust.

## References

- AC-0033 (`thoughts/shared/tickets/AC-0033-adversarial-cage-verification.md`) —
  the battery that surfaced this; `doc-config-dir` vector.
- `internal/cage/cage.go` — `buildEnv` (sets `CLAUDE_CONFIG_DIR`), `Prepare` (seeds
  it from the *uncaged* side), `Build` (the `--add-dirs` construction that does not
  include it).
- `internal/state` — the layout placing the config dir under `~/.cache`.
- `docs/design.md` — "The proxy and the credential story" (config-persistence /
  ephemeral config dir); `docs/cage-verification.md` "Known limitations" item 2.

## Implementation Plan

See `thoughts/shared/plans/2026-06-09-AC-0035-incage-claude-config-dir-writable.md`
and the research at
`thoughts/shared/research/2026-06-09-AC-0035-incage-claude-config-dir-writable.md`.

## Notes & Updates

### 2026-06-09 — Done

Fixed via a one-line mount in `cage.Build`: the redirected `CLAUDE_CONFIG_DIR`
(`…/projects/<hash>/claude`) is always appended to safehouse's `--add-dirs`, so it
is writable in-cage even when the cache is outside safehouse's base RW grants.
RED→GREEN flow against the AC-0033 battery, whose cache was relocated to a
non-granted `$HOME` temp dir so `doc-config-dir` exercises the real-location
writability:

- RED `ae77ccd` — `doc-config-dir want=planted got=blocked` (gap reproduced).
- GREEN `f9647a1` — all 18 vectors PASS (`doc-config-dir got=planted`),
  `fs-real-claude` still BLOCKED, negative control still detects escapes, stable
  across `-count=2`.

`make test` + `make lint` green; invocation golden updated (single `--add-dirs`
segment). Docs: cage-verification.md limitation #2 rewritten as resolved;
design.md notes the explicit `--add-dirs` mechanism.

### 2026-06-08
Created from a finding surfaced by the AC-0033 cage-verification battery. Framed as
investigate-then-fix because the gap is currently unverified at the real cache
location (the battery uses a `$TMPDIR`-backed cache, which safehouse grants).
Step one is a real-location caged repro; if confirmed, the fix is the narrowest
mount/grant that makes only `CLAUDE_CONFIG_DIR` writable in-cage, preserving the
config-persistence security property and guarded by an updated AC-0033 vector.
Complexity Medium for the same reason as AC-0034: small likely fix, but needs a
careful in-cage repro and a safehouse-aware decision on the mount mechanism.
