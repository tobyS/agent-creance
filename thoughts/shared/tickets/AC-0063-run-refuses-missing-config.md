# AC-0063: run refuses with an init pointer when the project has no config

**Status:** Open
**Estimated Complexity:** Low
**Created:** 2026-06-27
**Updated:** 2026-06-27

## Problem Statement

The 2026-06-25 UX audit
(`thoughts/shared/research/2026-06-25-ux-audit.md`, finding S1) found that the
single most natural first action a shell engineer takes — install the tool,
`cd` into a project, type `agent-creance run` — dead-ends when the project has no
`.agent-creance.yaml`.

`run` already gates on three preconditions with model-grade, command-bearing
refusals (prerequisites, setup, credential — `internal/cli/run.go:56-85`), but a
**missing project config is not treated as a precondition**. The project config
is loaded as *required* during the policy compile
(`internal/policy/compile/compile.go:224`, `optional=false`, in contrast to the
global config which is optional at `compile.go:220`), so a config-less project
fails at step 5 (compile policy, `internal/cli/run.go:109-111`) — before the
later `load config` step — with a cryptic triple-wrapped error and **no pointer
to `init`**:

```
error: compile policy: compile: load project: config: file not found: .agent-creance.yaml
```

This is a dead-end at exactly the moment a new user needs a nudge: the tool knows
the project isn't initialized but doesn't say so or name the command that fixes
it.

## Desired Outcome

`agent-creance run` treats a missing project `.agent-creance.yaml` as a fourth
up-front precondition and refuses early with a clear, actionable message that
names `agent-creance init` — matching the style of the existing precondition
refusals — instead of letting the failure surface as a low-level
`compile policy: …: file not found` wrap. An initialized project is completely
unaffected.

## User Stories / Use Cases

- As an engineer trying `agent-creance` for the first time, I want `run` in an
  uninitialized project to tell me to run `agent-creance init`, so that I'm not
  stuck reading a cryptic "file not found" error and guessing the next step.
- As a returning user who `cd`'d into the wrong directory, I want an immediate,
  obvious "no config here" message so that I notice my mistake before anything
  else happens.

## Acceptance Criteria

- [ ] Running `agent-creance run` in a directory with no `.agent-creance.yaml`
      exits non-zero with a message that explicitly names `agent-creance init`
      (and does not show the `compile policy: compile: load project: …` wrap).
- [ ] The refusal happens up front, before the policy compile / proxy / cage
      steps run (no progress lines printed first).
- [ ] The refusal message style is consistent with the existing precondition
      refusals (prereq / setup / credential) in `internal/cli/run.go`.
- [ ] An initialized project (config present) runs exactly as before — no
      behavior change on the happy path.
- [ ] A hermetic testscript case under `internal/cli/testdata/script/` covers the
      missing-config refusal and asserts the `init` pointer in the output.

## Out of Scope

- Auto-creating a config or running `init` implicitly from `run` (the refusal
  points the user at `init`; it does not do it for them).
- Changing whether the *global* config is optional (it stays optional).
- Any change to the three existing preconditions.

## Open Questions

None — well-understood from the audit; the failure path and the
required-vs-optional config behavior were verified in code.

## Questions for Research/Planning

- [ ] Where exactly should the new check live — a dedicated precondition block in
      `runRun` before the compile step, or a friendlier error mapped from the
      compiler's not-found error? (A pre-check matches the existing precondition
      pattern; confirm it doesn't duplicate the compiler's path resolution.)
- [ ] Should the check reuse a config-presence helper (does one already exist in
      `internal/config` / `internal/state`), or stat the file directly?

## References

- `thoughts/shared/research/2026-06-25-ux-audit.md` — UX audit, finding S1 (and
  the resolved Open Question confirming no implicit-default config path).
- `internal/cli/run.go:56-85` — the three existing preconditions to match.
- `internal/policy/compile/compile.go:220-224` — required project config vs
  optional global.

## Implementation Plan

[Leave empty — filled when the plan is created.]

## Notes & Updates

### 2026-06-27
Created from UX audit finding S1. Complexity Low: a single up-front precondition
plus a testscript case, mirroring an established pattern in `run`. The audit's
follow-up verification confirmed the project config is required (no fallback) and
that the error currently surfaces at the compile step, triple-wrapped.
