# AC-0035: Redirected CLAUDE_CONFIG_DIR may not be writable inside the cage

**Status:** Open
**Estimated Complexity:** Medium
**Created:** 2026-06-08
**Updated:** 2026-06-08

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

- [ ] A caged reproduction at the **real** cache location (`~/.cache/agent-creance`,
      not a `$TMPDIR`-backed test cache) shows whether the caged agent can write to
      `CLAUDE_CONFIG_DIR`; if it currently cannot, the repro demonstrates the
      failure first.
- [ ] After the fix, the caged agent can create directories and write files under
      `CLAUDE_CONFIG_DIR` at the real location.
- [ ] The redirected dir remains distinct from the real `~/.claude` (the
      config-persistence security property — a plant there still does not appear in
      `~/.claude`) — i.e. the fix must not re-open AC-0033's `doc-config-dir`
      security assertion.
- [ ] The fix does not broaden cage writability beyond the specific config dir
      (no blanket `~/.cache` write-grant if a narrower mount suffices).
- [ ] The AC-0033 battery is adjusted so its `doc-config-dir` vector exercises the
      real-location writability (or an equivalent guard), so the gap can't silently
      reopen.

## Out of Scope

- Relocating the out-of-tree state root away from `~/.cache` (CLAUDE.md is explicit
  that security-critical runtime state lives there; this ticket is about making the
  config dir writable in-cage, not moving it).
- The in-cage CA-file trust gap (separate ticket, AC-0034).
- Any change to what gets seeded into the config dir (still just `{}` settings).

## Open Questions

- None blocking — maintainer-facing correctness fix.

## Questions for Research/Planning

- [ ] Confirm empirically whether the caged agent can write `CLAUDE_CONFIG_DIR` at
      the real `~/.cache/agent-creance` location (the AC-0033 battery only tested a
      `$TMPDIR`-backed cache). Does safehouse's base policy already cover it via
      some grant not obvious from a static read of the default policy?
- [ ] What is the right mechanism to make the config dir writable: add the
      `CLAUDE_CONFIG_DIR` (or the project state root) to the cage's `--add-dirs`
      automatically in `cage.Build`, or emit a scoped `--append-profile` write-grant
      for it? Which is more consistent with how safehouse expects extra RW paths?
- [ ] Should the project state root more broadly be mounted (the agent also can't
      read network.sb/proxy.sb at runtime, but those are read by the uncaged
      safehouse wrapper, so likely only the config dir needs in-cage RW)?
- [ ] Does this interact with AC-0034's question about where the CA copy lives (if
      the state dir becomes a mounted, in-cage-readable location, it could serve
      both)?

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

## Notes & Updates

### 2026-06-08
Created from a finding surfaced by the AC-0033 cage-verification battery. Framed as
investigate-then-fix because the gap is currently unverified at the real cache
location (the battery uses a `$TMPDIR`-backed cache, which safehouse grants).
Step one is a real-location caged repro; if confirmed, the fix is the narrowest
mount/grant that makes only `CLAUDE_CONFIG_DIR` writable in-cage, preserving the
config-persistence security property and guarded by an updated AC-0033 vector.
Complexity Medium for the same reason as AC-0034: small likely fix, but needs a
careful in-cage repro and a safehouse-aware decision on the mount mechanism.
