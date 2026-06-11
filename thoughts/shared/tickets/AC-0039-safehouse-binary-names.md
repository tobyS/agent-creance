# AC-0039: Accept both safehouse binary names (`safehouse` / `agent-safehouse`)

**Status:** In Progress
**Estimated Complexity:** Small
**Created:** 2026-06-11
**Updated:** 2026-06-11

## Problem Statement

`agent-creance run` refuses to start on a machine where agent-safehouse is correctly
installed. The Homebrew formula (`brew install eugene1g/safehouse/agent-safehouse`)
installs the executable as `safehouse`, but the prerequisite check looks up
`agent-safehouse` on PATH and reports it as "not installed". The user gets a
refuse-and-suggest message pointing at the very brew command they already ran — a
dead end.

The codebase itself is internally inconsistent: the cage launcher already invokes
`safehouse` (`cage.Binary`), while the prereq check and `agent-creance version` look
for `agent-safehouse`. Even if the check name were "fixed" in isolation, a machine
that only has the other name would pass the check and then fail at launch — the two
must agree.

Since the tool's binary name has appeared under both names (formula name vs.
installed executable), agent-creance should tolerate either.

## Desired Outcome

agent-creance treats the safehouse prerequisite as satisfied if **either**
`safehouse` **or** `agent-safehouse` is on PATH, and every part of the app
(prerequisite check, `doctor`, `version`, cage launch) uses the **same resolved
binary** — the check passing guarantees the launch uses an existing executable.

## User Stories / Use Cases

- As a user who installed agent-safehouse via the official brew formula, I want
  `agent-creance run` to recognise my install so that I can start the cage without
  chasing a misleading "not installed" error.
- As a user on a machine where the binary is named `agent-safehouse` (older or
  alternative install), I want run/doctor/version to find and use that binary so
  that agent-creance works regardless of which name my install produced.
- As a user debugging my setup, I want `doctor` / `version` to show which binary
  name was actually resolved so that I can spot a stale or shadowed second install.

## Acceptance Criteria

- [ ] With only `safehouse` on PATH, `agent-creance run` passes the prerequisite
      check and launches the cage via that binary.
- [ ] With only `agent-safehouse` on PATH, `agent-creance run` passes the
      prerequisite check and launches the cage via that binary.
- [ ] With **both** names on PATH, `safehouse` wins (the name the current brew
      formula installs), consistently across check, reports, and launch.
- [ ] With **neither** name on PATH, the refusal message still lists the brew
      install hint (`brew install eugene1g/safehouse/agent-safehouse`).
- [ ] The binary name used to launch the cage is the same one the prerequisite
      check resolved — no code path where the check passes but the launch execs a
      name that does not exist.
- [ ] `agent-creance doctor` and `agent-creance version` report the resolved binary
      name, so a user can see which of the two names satisfied the check.
- [ ] Version detection and skew classification work against whichever binary was
      resolved (the installed 0.10.1 banner `Agent Safehouse 0.10.1` parses as
      today).

## Out of Scope

- Renaming or aliasing the upstream brew formula / binary (upstream concern).
- Supporting user-configurable safehouse binary paths (e.g. a config key or env var
  pointing at an arbitrary executable).
- Any change to the mitmproxy prerequisite handling.
- Verifying that the two names, when both present, point at the same version
  (precedence answers the ambiguity; no version cross-check).

## Open Questions

None — precedence, scope, and reporting were decided during ticket creation (see
Notes).

## Questions for Research/Planning

- [ ] Where should the single source of truth for the candidate name list live so
      that prereq, `version`, and `cage` cannot drift again (today: `cage.Binary`
      const vs. hardcoded strings in `internal/prereq/prereq.go` and
      `internal/cli/version.go`)?
- [ ] How should the resolved name flow from the prereq check to the cage
      invocation (resolve once and pass through vs. re-resolve with the same
      ordered candidate list)?
- [ ] What is the cleanest extension of `prereq.Tool` for multi-name lookup —
      candidate list per tool, separate display name vs. exec name?
- [ ] Which golden files / testscripts encode the current "agent-safehouse" label
      and need regenerating (`internal/prereq/testdata`, CLI testscripts)?

## References

- Debugging session 2026-06-10: `run` refused on a machine with
  `/opt/homebrew/bin/safehouse` 0.10.1 installed (the exact tested version in
  `internal/buildinfo`).
- `internal/cage/cage.go:38` — `const Binary = "safehouse"` (launch-side name).
- `internal/prereq/prereq.go:39` and `internal/cli/version.go:23` — check-side name
  `agent-safehouse`.

## Implementation Plan

[Leave empty - will be filled when plan is created]

## Notes & Updates

### 2026-06-11

Decisions made during ticket creation:

- **Resolve once, use everywhere**: the resolved binary name must be shared by the
  prereq check, `doctor`/`version` reporting, and the cage launch. A check-only fix
  was rejected because it can pass the check and still fail at exec.
- **Precedence**: `safehouse` wins when both names are installed — it is the name
  the current brew formula installs and the tested 0.10.1 reports as
  "Agent Safehouse". No error-on-ambiguity.
- **Reporting**: `doctor`/`version` show the resolved name so a stale second
  install is diagnosable.
- Complexity **Small**: two-name lookup plus threading one resolved value through
  existing seams; the sysdep fakes already support per-name `LookPath` scripting.
