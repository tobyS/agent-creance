---
date: 2026-06-07
ticket: AC-0027
title: "Skill install (WP-5.2) — implementation plan"
status: ready
branch: main
research: thoughts/shared/research/2026-06-07-AC-0027-skill-install.md
---

# AC-0027: Skill install (WP-5.2) — Implementation Plan

## Overview

Ship a Claude Code skill that teaches the agent the three network-refusal response
types and install it idempotently to `~/.claude/skills/agent-creance/SKILL.md`.
AC-0027 is **library-only**: embed `SKILL.md` via `go:embed` and add an
`InstallSkill` method to the existing `setup.Installer`. The `--no-skill` flag and
`setup` command wiring are AC-0028/WP-5.3 and are out of scope here.

## Current state

- `internal/setup` (AC-0026) does the CA bootstrap. Its `Installer` already holds the
  `sysdep.FileSystem` and `sysdep.PathResolver` seams (`setup.go:58-79`) but does not
  write any files of its own.
- The repo's only embed+install precedent is `internal/proxy/extract.go`: `go:embed`
  + `MkdirAll(0o755)` + idempotent atomic write `writeIfChanged` (read existing,
  `bytes.Equal` short-circuit, write `.tmp` at `0o644`, `Rename`).
- The skill path is already pinned by `internal/setupcheck`
  (`var skillFileRel = filepath.Join(".claude","skills","agent-creance","SKILL.md")`,
  `setupcheck.go:38`), which checks the file's presence as a `run` precondition.
- `SKILL.md` does not exist yet.
- The wire format the skill teaches is frozen by AC-0017 in
  `internal/proxy/enforcer/responses.py` + golden bodies.

## Desired end state

- `internal/setup` embeds a `SKILL.md` and exposes `func (i *Installer)
  InstallSkill() error` that writes it idempotently to
  `~/.claude/skills/agent-creance/SKILL.md`, creating parent dirs (`0o755`, file
  `0o644`).
- The relative skill path is a **single exported constant** shared by both
  `internal/setupcheck` (the checker) and `internal/setup` (the writer), so they
  cannot drift.
- The embedded `SKILL.md` is a **concise reference**: frontmatter (`name:
  agent-creance` + a `description` front-loading the `X-Cage-Reason`/`agent_cage_`
  markers) and the three response types, each with its header value, `agent_cage_`
  enum, JSON body field names, and the intended agent action.
- The project's `CLAUDE.md` is never read or written (guarded by a test).
- `make test` and `make lint` are green; `go build ./...` resolves the embed.

## Decisions (from the question checkpoint)

1. **Shared path constant.** Export `setupcheck`'s relative-path constant (rename
   `skillFileRel` → exported `SkillFileRel`) and have `internal/setup` import it. No
   import cycle (`setupcheck` imports only `sysdep`; nothing imports `setup`).
2. **Concise reference** SKILL.md body (matches the ticket's out-of-scope note).
3. `name: agent-creance` set explicitly (matches the fixed skill directory; kebab-
   case, no reserved words — valid under the strict Agent Skills rules).
4. `InstallSkill()` is a plain no-arg method; `--no-skill` is the AC-0028 caller's
   concern.

---

## Phase 1: Share the skill-path constant ✅

Make the relative skill path a single source of truth so the writer (setup) and the
checker (setupcheck) cannot diverge.

### Changes

`internal/setupcheck/setupcheck.go`
- Rename the unexported `skillFileRel` (`:38`) to an exported, documented
  `SkillFileRel`:
  ```go
  // SkillFileRel is the agent-creance skill location that setup installs and run's
  // precondition check looks for, relative to the user's home directory.
  var SkillFileRel = filepath.Join(".claude", "skills", "agent-creance", "SKILL.md")
  ```
- Update the in-package use at `setupcheck.go:115`
  (`filepath.Join(home, SkillFileRel)`).

### Success criteria

#### Automated
- [ ] `go build ./...` compiles.
- [ ] `make test` passes (existing `setupcheck` tests still green).
- [ ] `make lint` clean.

#### Manual
- [ ] `grep` confirms no remaining lowercase `skillFileRel` reference.

---

## Phase 2: Embed SKILL.md and implement InstallSkill (with tests)

### 2a. Author `internal/setup/SKILL.md`

Concise reference. Frontmatter then body. Content must use the **current** AC-0017
wording (the reworded soft-deny copy — no "route around silently" framing) and the
exact markers/enums from `internal/proxy/enforcer/responses.py`.

Target file (`internal/setup/SKILL.md`):

```markdown
---
name: agent-creance
description: Explains how to react to agent-creance network egress refusals. Use when an HTTP request returns 403 with an "X-Cage-Reason" header or a JSON body whose "error" starts with "agent_cage_" (agent_cage_not_allowlisted = soft-deny, agent_cage_hard_deny = hard-deny). Covers the three response types — allowed, soft-deny, hard-deny — and the right action for each.
---

# Reacting to agent-creance network refusals

agent-creance runs you inside an egress-filtered cage. Outbound HTTP requests pass
through a proxy that returns one of three response types. Recognize them by the
`X-Cage-Reason` response header and the `error` field of the JSON body (it starts
with `agent_cage_`).

## 1. Allowed

A normal upstream HTTP response. The URL matched the project allowlist. No
`X-Cage-Reason` header. Proceed as usual.

## 2. Soft-deny — not allowlisted, could be added

HTTP `403` with header `X-Cage-Reason: soft-deny`. JSON body fields: `error`
(`agent_cage_not_allowlisted`), `url`, `host`, `path`, `method`, `how_to_proceed`,
`allow_command_suggestion`.

**What to do:** Ignore the resource and proceed if you can find the needed
information elsewhere or can work reliably without it. Only if the information is
important and would contribute significantly to your success, prompt the user and
ask them to add the resource to the allowlist (the body's `allow_command_suggestion`
shows the exact `agent-creance allow '<host><path>'` command). Do not retry the same
URL blindly.

## 3. Hard-deny — permanently blocked, find another way

HTTP `403` with header `X-Cage-Reason: hard-deny`. JSON body fields: `error`
(`agent_cage_hard_deny`), `url`, `reason` (why it is blocked), `how_to_proceed`.

**What to do:** Treat it as final. Do NOT ask the user to allow it. Do NOT retry.
Find an alternative source, or tell the user no authoritative source could be found.
```

> Implementation note: keep the prose aligned with `responses.py:31-40`. If the
> golden `how_to_proceed` strings have changed at implementation time, re-read the
> goldens and reconcile.

### 2b. Embed + install in `internal/setup`

Mirror `internal/proxy/extract.go`'s idempotent write. Add to `internal/setup`
(either in `setup.go` or a new `skill.go` in `package setup` — prefer a new
`skill.go` to keep CA and skill concerns separate):

```go
import (
    "bytes"
    _ "embed"
    "errors"
    "fmt"
    "io/fs"
    "path/filepath"

    "github.com/tobyS/agent-creance/internal/setupcheck"
)

//go:embed SKILL.md
var skillMD string

const (
    skillDirPerm  fs.FileMode = 0o755
    skillFilePerm fs.FileMode = 0o644
)

// InstallSkill writes the embedded Claude Code skill to
// ~/.claude/skills/agent-creance/SKILL.md, creating parent directories. It is
// idempotent: if the file already holds the embedded content, it is left untouched.
// It never reads or writes the project's CLAUDE.md.
func (i *Installer) InstallSkill() error {
    home, err := i.paths.UserHomeDir()
    if err != nil {
        return fmt.Errorf("setup: resolve home dir: %w", err)
    }
    dest := filepath.Join(home, setupcheck.SkillFileRel)
    if err := i.fs.MkdirAll(filepath.Dir(dest), skillDirPerm); err != nil {
        return fmt.Errorf("setup: create skill dir: %w", err)
    }
    return i.writeSkillIfChanged(dest, []byte(skillMD))
}
```

`writeSkillIfChanged` mirrors `extract.go:93-114`:
- `i.fs.ReadFile(dest)`: if `bytes.Equal(got, want)` → return nil; if
  `errors.Is(err, fs.ErrNotExist)` → fall through; else wrap+return.
- `i.fs.WriteFile(dest+".tmp", want, skillFilePerm)`; on error wrap+return.
- `i.fs.Rename(tmp, dest)`; on error, best-effort `i.fs.Remove(tmp)` then wrap+return.

(If a private write helper already exists to reuse, prefer reuse; otherwise this
local helper keeps the skill self-contained.)

### 2c. Tests — `internal/setup/skill_test.go` (`package setup`)

Follow the `setup_test.go` fakes harness (`paths.HomeDir = testHome`). Compute
`skillPath := filepath.Join(testHome, ".claude","skills","agent-creance","SKILL.md")`.

- **Writes to the expected path.** Fresh fake FS → `InstallSkill()` → assert
  `f.fs.Files[skillPath] == []byte(skillMD)`, `f.fs.Perms[skillPath] == 0o644`, and
  the parent dir created with `0o755`.
- **Idempotent re-install is a no-op.** Pre-seed `f.fs.Files[skillPath] =
  []byte(skillMD)`, wrap the fake so `WriteFile`/`Rename` fail if called (à la
  `appearingFS`, `setup_test.go:52-67`), then assert `InstallSkill()` returns nil
  without writing (proves the `bytes.Equal` short-circuit).
- **Content drift triggers a rewrite.** Pre-seed `Files[skillPath]` with stale bytes,
  run `InstallSkill()`, assert the file now equals `skillMD`.
- **CLAUDE.md guard.** After `InstallSkill()`, assert no key in `f.fs.Files`,
  `f.fs.Dirs`, or `f.fs.Perms` contains the substring `"CLAUDE.md"`.
- **Content check (no fake).** Assert `skillMD` contains `X-Cage-Reason`,
  `soft-deny`, `hard-deny`, `agent_cage_not_allowlisted`, `agent_cage_hard_deny`.
- **Error propagation (optional but cheap).** Inject `f.fs.MkdirErrs[dir]` and
  `f.fs.WriteErrs[tmp]`; assert `InstallSkill()` surfaces a wrapped error.

### Success criteria

#### Automated
- [ ] `go build ./...` compiles (embed resolves).
- [ ] `go test -race ./internal/setup/...` passes (new skill tests green).
- [ ] `make test` passes (whole hermetic suite green).
- [ ] `make lint` clean.

#### Manual
- [ ] Spot-read `SKILL.md`: frontmatter `name`/`description` present; description
  front-loads `X-Cage-Reason` and `agent_cage_`; all three types covered; soft-deny
  copy matches `responses.py` (no "route around" framing).

---

## Testing strategy

- All hermetic via `sysdeptest` fakes — no real FS, no external tools. No integration
  test is needed (pure FS write through the seam).
- The five ticket verification steps map to Phase 2c:
  build (compiles), write-to-path, idempotency, CLAUDE.md guard, content check, lint.

## Out of scope (per ticket)

- The `setup` command wiring and the `--no-skill` flag (AC-0028/WP-5.3).
- Deep skill prose beyond the three response types.

## References

- Research: `thoughts/shared/research/2026-06-07-AC-0027-skill-install.md`
- Pattern to mirror: `internal/proxy/extract.go:30-114`
- Pinned path: `internal/setupcheck/setupcheck.go:38,115`
- Frozen wire format: `internal/proxy/enforcer/responses.py:19-40` + goldens
- Ticket: `thoughts/shared/tickets/AC-0027-skill-install.md`
