---
date: 2026-06-12
researcher: Claude (quickfix pipeline)
git_commit: 47ce21d
branch: main
repository: git@github.com:tobyS/agent-creance.git
topic: "AC-0043: setup scaffolds the global Claude-defaults egress baseline"
tags: [research, codebase, AC-0043, setup, config, egress, claude-code, baseline]
status: complete
last_updated: 2026-06-12
---

# Research: AC-0043 — global Claude-defaults egress baseline

**Ticket:** `thoughts/shared/tickets/AC-0043-global-claude-baseline.md`

## Problem recap (pre-confirmed)

Nothing ships the design's assumed "global allowlist baseline", so a fresh cage
soft-denies `api.anthropic.com` (403 `agent_cage_not_allowlisted`) and Claude
Code's axios probe reports `ERR_BAD_REQUEST`. Fix (user-chosen option 1):
`setup` scaffolds `~/.config/agent-creance.yaml` when absent.

## Findings

### Required hosts (official docs, verified 2026-06-12)

Source: ["Enterprise network configuration"](https://code.claude.com/docs/en/network-config)
(Claude Code docs; verbatim required-URL table) plus
["Data usage"](https://code.claude.com/docs/en/data-usage) and Anthropic's own
devcontainer `init-firewall.sh`:

| Domain | Purpose | Required? |
|---|---|---|
| `api.anthropic.com` | Claude API requests (+ WebFetch preflight) | **Required** |
| `claude.ai` | claude.ai (subscription) OAuth | Required for subscription auth |
| `platform.claude.com` | Console OAuth (superseded `console.anthropic.com` in current docs) | Required for Console auth |
| `downloads.claude.ai` | Plugin executables; native installer/auto-updater | Required (npm installs may not need it) |
| `raw.githubusercontent.com` | Release-notes feed, plugin marketplace metadata | In the official required table (degrades only) |
| `storage.googleapis.com` | Auto-updater **only for versions < 2.1.116** | Legacy; very broad host — exclude from baseline |
| `bridge.claudeusercontent.com` | Chrome-extension bridge | Feature-specific; irrelevant in the cage |
| `statsig.anthropic.com`, `statsig.com` | Feature flags/metrics | **Optional** (`DISABLE_TELEMETRY=1`; works blocked) |
| `sentry.io` (org-specific `*.ingest.sentry.io`) | Error reporting | **Optional** (`DISABLE_ERROR_REPORTING=1`) |

TLS interception: **no certificate pinning** — docs explicitly endorse
TLS-terminating proxies with the CA in `NODE_EXTRA_CA_CERTS` (which the cage
already injects, `internal/cage/cage.go:251-253`). So passthrough for
`api.anthropic.com` is the design's *credential-privacy* choice
(docs/design.md:254-270: "interception buys near-zero security value" and
carries pinning risk), not a technical necessity. Auth hosts (`claude.ai`,
`platform.claude.com`) carry OAuth grants/refresh tokens → same passthrough
rationale (design.md:457 expects token refresh on the baseline).

### Where the scaffold plugs in (codebase)

- **`runSetup`** (`internal/cli/setup.go:39-82`): flags are parameters
  (`noSkill`, `noCAInstall`); two ordered steps (CA → skill), each printing
  `✓ …` to `app.Stdout`, abort on first error. The skill step's `--no-skill`
  branch does an **early `return nil`** (`setup.go:73-76`) — a third step must
  come before that return or the branch must be restructured
  (`if noSkill {print} else {install}`).
- **`init` reuses `runSetup(ctx, app, false, false)`** as its onboarding gate
  (`internal/cli/init.go:134`) — the new step will also run there; its tests
  assert via `strings.Contains` and fake-FS keys, so a new file/line is safe.
- **Path**: `config.NewLoader(app.FS, app.Paths).GlobalPath()`
  (`internal/config/load.go:77-83`, hardcoded `$HOME/.config/agent-creance.yaml`
  via `PathResolver.UserHomeDir()`) — the exact pattern `mutationTarget` uses
  (`internal/cli/mutate.go:71`), guaranteeing writer and compiler agree.
- **Write-if-absent idiom**: three-way Stat switch (`init.go:63-73`,
  `internal/setup/setup.go:89-96`), then `MkdirAll(dir, 0o755)`
  (`mutate.go:103`) + `writeFileAtomic(..., configFilePerm 0o644)`
  (`init.go:265-275`, `init.go:172`). **Never** the content-compare
  self-healing idiom (`skill.go:52-73`) — the scaffold is user-editable and
  must never be rewritten.
- **Template precedent**: plain `const` string in the cli layer
  (`configTemplate`, `init.go:309-328`), pinned by a `config.Parse` round-trip
  test (`init_test.go:71-79`) and golden/content tests (`init_test.go:50-66`).
- **Config schema/validation** (`internal/config/config.go:89-101`,
  `validate.go:22-45`): rules carry `host`, optional `paths`/`methods`
  (pointers), `mode` (`intercept` default | `passthrough`), free-text
  `reason`. **Passthrough rules must not set paths/methods** (validate.go:35-40).
  Strict decoding (`KnownFields(true)`) — template must match the schema
  exactly. Host matching in the enforcer: exact / `*.suffix` / `*`.
- **Tests that could constrain output**: setup unit tests assert via
  `strings.Contains` (no full-output equality); negative assertions forbid the
  substrings "authorization dialog" / "login keychain" in specific opt-out
  tests (`setup_test.go:96-98,176-178,228-230`) — avoid them in new lines.
  `setup_help.txtar` asserts only the existing flags' presence; a third flag
  doesn't break it. No txtar runs real setup steps.
- **`setupcheck` gate**: must NOT include the global config — every reader
  treats it as optional (`load.go:57`, `compile.go:220`); absence never blocks
  `run`. Scaffold is convenience, not precondition.
- **`doctor`**: zero global-config references today; no consistency work.

## Impact analysis

`runSetup` gains a parameter/flag — call sites: `newSetupCmd` (`setup.go:24`),
`init.go:134`, and the setup/init unit tests that call `runSetup` directly.
The compiler starts merging the scaffolded baseline immediately
(`compile.go:216-223`, rules annotated `source: "global"`), and
`allow --global` appends to the same file (`mutate.go:88-99`). Policy input
hash changes when the global file appears → first `run` after setup recompiles
(correct behavior).

## Baseline content decision (research-resolved)

Active rules — the officially-required set:
- `api.anthropic.com`, `claude.ai`, `platform.claude.com` → `mode: passthrough`
  (API + OAuth traffic carries tokens; design.md:254-270 rationale)
- `downloads.claude.ai`, `raw.githubusercontent.com` → intercept (default mode)

Commented-out (visible, one-uncomment away): `statsig.anthropic.com`,
`statsig.com`, `sentry.io` — officially optional; blocking only degrades
telemetry/error reporting. Excluded entirely: `storage.googleapis.com`
(legacy-only and far too broad) and `bridge.claudeusercontent.com`
(Chrome-extension feature, meaningless in the cage).

## Code references

- `internal/cli/setup.go:18-33,39-82` — command, flags, step ordering
- `internal/cli/init.go:63-73,134,265-275,309-328` — Stat switch, bootstrap reuse, atomic write, template precedent
- `internal/cli/mutate.go:71,103-108` — GlobalPath usage + MkdirAll/write pattern
- `internal/config/load.go:77-83` — GlobalPath; `config.go:89-101` — Rule/modes; `validate.go:22-45` — passthrough constraints
- `internal/cli/setup_test.go:21-65,96-98,176-178,185-205,228-230` — fixture + assertions to respect
- `internal/cli/testdata/script/setup_help.txtar` — flag-presence assertions
- `docs/design.md:155,254-270,457` — baseline intent, passthrough rationale, OAuth-refresh assumption

## Open questions

None — host list, modes, placement, idiom, and test shape all resolved.
