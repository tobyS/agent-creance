# AC-0067: Config-editing commands with interactive TUI fallback

**Status:** Done
**Estimated Complexity:** Extra Large
**Created:** 2026-06-27
**Updated:** 2026-06-28

## Problem Statement

Maintaining `.agent-creance.yaml` by hand is cumbersome: a user has to remember the
schema for egress rules (`host`/`paths`/`methods`/`mode`), `network.host_services`
(`label:port`), and `safehouse.add_dirs_rw`/`add_dirs_ro`. The current command surface
is too thin to avoid this:

- `allow URL` / `deny URL` take a **bare URL only** — a bare host or host + a *single*
  path. There is no way to set HTTP methods, multiple paths, or `mode`
  (intercept/passthrough) from the CLI (`internal/cli/mutate.go` comment: "v0.1 has no
  --method flag").
- There is **no removal** of any config entry — the `internal/config` editor is
  append-only.
- `host_services` can only be added via `import`/`init`, never via a dedicated command,
  and never removed.
- `safehouse.add_dirs_*` (filesystem mounts) have **no command surface at all** —
  `import` explicitly ignores a `safehouse:` section. They are edit-the-YAML-by-hand
  only.

The goal is to let users maintain the security-relevant parts of the config entirely
through commands, supplying everything as flags or — when choices are missing —
through an interactive TUI.

## Desired Outcome

Noun-verb command groups that add and remove config entries, each fully specifiable via
flags, and each falling back to an interactive TUI for any choice the user did not
supply:

```
agent-creance domain add api.github.com --path /repos/ --method GET
agent-creance domain add react.dev --all-paths
agent-creance domain add w3schools.com --deny --reason "Low-quality source"
agent-creance domain remove api.github.com                 # whole rule
agent-creance domain remove api.github.com --path /repos/   # one path from the rule

agent-creance service add mysql:3306
agent-creance service remove 3306

agent-creance mount add ./data --rw
agent-creance mount remove ./data
```

When this is complete: a user never has to recall the YAML schema to add or remove a
domain rule (allow or deny), a host-port bind, or a filesystem mount; missing decisions
are gathered interactively; and config edits preserve the file's comments and formatting.

## User Stories / Use Cases

- As a developer configuring a project, I want to add an allowed domain (optionally
  scoped to paths/methods) without remembering the YAML structure, so that I can grow
  the allowlist quickly and correctly.
- As a developer, I want to run `domain add somehost.dev` and be *asked* whether to allow
  all paths or specific ones, so that I don't have to know the flags up front.
- As a developer, I want to remove a domain rule — or just one path from it — so that I
  can tighten the allowlist without hand-editing YAML and risking a syntax error.
- As a developer, I want to add and remove host-port binds and filesystem mounts via
  commands, so that the whole security-relevant config is command-maintainable.
- As a developer with a running cage, I want a clear notice when a `service`/`mount`
  edit won't take effect until the next run, so that I'm not misled into thinking the
  live session changed.

## Acceptance Criteria

### Command surface
- [x] New `domain` command group: `domain add HOST` and `domain remove HOST`.
- [x] New `service` command group: `service add LABEL:PORT` and `service remove PORT`.
- [x] New `mount` command group: `mount add PATH` and `mount remove PATH`.
- [x] Existing `allow` / `deny` verbs continue to work unchanged (kept as aliases that
      delegate to the shared `runDomainAdd` body).

### Domain rules (allow + deny)
- [x] `domain add HOST --path P` (repeatable) scopes the rule to specific paths; `--method M`
      (repeatable) constrains methods; `--mode intercept|passthrough` sets the enforcement mode.
- [x] `domain add HOST --all-paths` produces a host-wide rule (no `paths`). `--all-paths`
      and `--path` together is a clear error.
- [x] `domain add HOST --deny --reason "..."` writes a `deny_always` rule instead of an allow.
- [x] `domain add` / `domain remove` accept `--global` to target `~/.config/agent-creance.yaml`
      (allow and deny only).
- [x] `domain remove HOST` with no `--path` removes the entire rule; with `--path P` removes
      only that path from the rule, dropping the whole rule when it was the last path
      (decision: drop-whole-rule, per the planning checkpoint).
- [x] Passthrough with `paths`/`methods` is rejected with a clear, early error (not a later
      compile failure) — the invalid combination is guarded in the command.

### Host services and mounts
- [x] `service add LABEL:PORT` appends a `host_services` entry; `service remove PORT` removes it.
- [x] `mount add PATH --rw|--ro` appends to `add_dirs_rw`/`add_dirs_ro`; `mount remove PATH`
      removes it (from both lists if present in both, per the planning checkpoint).

### Interactive TUI fallback ("explicit-or-prompt")
- [x] For every choice a command needs, if it is supplied via flag the command runs
      non-interactively; if it is *omitted*, an interactive prompt collects it. (See the
      deviation below: methods/mode use safe defaults rather than prompting.)
- [x] `domain add HOST` with neither `--path` nor `--all-paths` prompts: "allow all paths, or
      specific paths?" (and collects the paths if "specific"). **Deviation:** methods and mode
      are NOT prompted when omitted — they use safe defaults (any method / intercept), which
      matches the primary user story and avoids prompt noise; `--method`/`--mode` set them
      explicitly. See Notes & Updates.
- [x] `service add` prompts for a missing label; `mount add` prompts for missing `--rw`/`--ro`.
- [x] When an interactive prompt is needed but stdin/stdout is not a terminal, the command
      fails with a clear hint naming the flags to supply, and never hangs.

### Edit semantics
- [x] Adding and **removing** entries preserves the file's existing comments and formatting
      (no decode/re-encode reflow) — removal is new infrastructure (a text-splice delete bounded
      by the element's own node subtree, so neighbouring comments survive).
- [x] Removing a non-existent entry is a clear, well-defined outcome — an explicit error with a
      non-zero exit (decision: error, per the planning checkpoint), not a crash or corruption.
- [x] All edits go through the existing config lock (`withConfigLock`) and atomic write path.

### Live-session behavior
- [x] `domain`/deny edits recompile `policy.json` so a running proxy hot-reloads (as today).
- [x] `service`/`mount` edits, when a cage is running for the project, write the change and
      print a warning that it takes effect on the next `agent-creance run` and the live session
      is unchanged (because these are baked into the frozen Seatbelt profile).

### Testing
- [x] Pure edit/removal logic covered by table-driven unit tests (no new `.sb`/policy artifacts
      were needed, so no new golden files).
- [x] Command behavior (flag paths, non-TTY fallback, error cases) covered by hermetic
      `testscript` `.txtar` files (`domain.txtar`, `service_mount.txtar`); the prompt fits the
      `internal/sysdep` injected-terminal seam and is unit-tested without touching the real OS.

## Out of Scope

- Editing `env:`, `include:` (already has its own `include` command), or `generators:` via
  these commands.
- Any change to the security model, the config cage, or the in-cage config-write deny
  (`config-ro.sb`).
- Live profile updates for a running cage (host-ports/mounts remain frozen at launch — the
  "write + warn" behavior is the accepted limitation).
- `bundle:` / `plugins:` schema sections.
- `--once` session-overlay support on the new `domain` command. The legacy `allow --once`
  alias keeps its session-overlay behavior; `domain` writes persistent config only.

## Open Questions

None — resolved during authoring (command shape, removal granularity, sections, mode
exposure, live-session behavior, global scope, deny command shape, `--once` handling).

## Questions for Research/Planning

- [ ] Which TUI approach fits the codebase's testability constraints (injected
      `sysdep.Terminal`, hermetic `testscript`)? Evaluate a lightweight library (bubbletea,
      survey) vs. extending the existing hand-rolled `confirm()` prompt pattern. A new
      dependency must be scriptable and non-TTY-safe.
- [ ] Removal infrastructure in `internal/config`: how to delete a `yaml.Node` (whole rule,
      a single path within a rule, a host_service, a mount) while preserving surrounding
      comments — and which trailing/leading comments belong to a removed entry.
- [ ] Exact behavior of `domain remove --path P` when `P` is the rule's last path (drop the
      whole rule vs. leave an empty paths list — the latter changes rule semantics).
- [ ] How `service`/`mount` commands detect a live cage for the project (reuse the lock-file
      liveness probe used by `run`/`status`).
- [ ] Whether `domain add` should keep `mode` defaulting to `intercept` and how passthrough
      guarding integrates with the existing compile-time validation.
- [ ] Whether `mount remove PATH` should error when the path is present in *both* rw and ro
      lists, or remove from both.

## References

- `docs/design.md` — "The configuration", "Per-host enforcement modes", "Config compilation",
  "Session-scoped allows", "In-session config hot-reload", "Commands".
- Existing implementation: `internal/cli/allow.go`, `deny.go`, `import.go`, `include.go`,
  `mutate.go`; `internal/config/edit.go`, `edit_hostservice.go`, `edit_include.go`,
  `config.go`; interactive primitive `confirm()` in `internal/cli/init.go`.
- Related: AC-0066 (CLI ergonomics bundle), AC-0051 (import / agent-prompt flow),
  AC-0055 (init writes own-remote allows).

## Implementation Plan

[Leave empty — filled when the plan is created.]

## Notes & Updates

### 2026-06-27
Created after sanity-checking the request against the current implementation. Key decisions
made during authoring:

- **Command shape:** noun-verb groups (`domain`/`service`/`mount`) rather than extending the
  flat `allow`/`deny` verbs; `allow`/`deny` kept as aliases.
- **Removal granularity:** both whole-rule and single-path removal for domains.
- **Sections in scope:** egress allow, `deny_always`, `host_services`, and
  `safehouse.add_dirs_*`.
- **Mode exposed** (intercept/passthrough) with the invalid passthrough+paths/methods combo
  guarded.
- **Live-session behavior:** `service`/`mount` edits write + warn (frozen Seatbelt profile);
  `domain`/deny edits hot-reload as today. This is the main honest-limitation call.
- **`--global`** on domains and denies only.
- **Deny** is a `--deny` flag on `domain add`, not a separate noun.
- **`--once`** stays on the legacy `allow` alias only; `domain` writes persistent config.
- **Complexity:** Extra Large, kept as a single ticket per author preference (large
  implement pass expected; planning may phase it internally).

### 2026-06-28 — Implemented (Done)

Implemented across five internal phases (research + plan: `thoughts/shared/research/` and
`thoughts/shared/plans/2026-06-27-AC-0067-*`):

1. `config.AppendDir` — comment-preserving append for `safehouse.add_dirs_*` (flow lists
   rewritten in place; block lists get an item).
2. Removal infrastructure (`internal/config/remove.go`): `RemoveRule` (whole + single-path,
   drop-on-last), `RemoveHostService` (by port), `RemoveDir` (both lists), `ErrNotFound`.
   A deleted element's span is bounded by its own node subtree, so neighbouring comments
   survive.
3. `domain` group + a hand-rolled prompt helper (`prompt.go`); `allow`/`deny` re-pointed to
   delegate to `runDomainAdd`.
4. (folded into 3) interactive paths prompt + non-TTY flag hint.
5. `service`/`mount` groups with `applyAndWarn` (live-cage probe via `proxy.Manager.Inspect`,
   write-and-warn, no recompile).

**Checkpoint decisions (resolved with the user before planning):** last-path removal drops
the whole rule; removing a missing entry errors with a non-zero exit; `mount remove` detaches
from both rw and ro lists; `allow`/`deny` are aliases over the shared `domain add` body.

**Deviation from the acceptance criteria (flag for review):** the AC asked that *methods and
mode* also prompt when omitted. Implemented only the **paths** decision as an interactive
prompt (the primary user story); `--method`/`--mode` default silently to "any method" /
`intercept` when omitted, to avoid prompting on every `domain add`. The flags set them
explicitly. Easy to extend to prompt for these too if strict adherence is preferred.

**Tests:** table/inline unit tests for the config edits and removals; `domain_test.go`,
`domain_interactive_test.go`, `service_test.go`, `mount_test.go`; hermetic `domain.txtar` and
`service_mount.txtar`. The live-cage warning is unit-tested with a seeded `proxy.lock` + alive
PID/port. Full suite (`make test`, 28 packages), `make lint`, and `make build` all green; the
built binary was smoke-tested end-to-end.
