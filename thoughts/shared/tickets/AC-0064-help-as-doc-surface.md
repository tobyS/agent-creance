# AC-0064: make --help a real doc surface (Long/Example, root overview, command groups)

**Status:** In Progress
**Estimated Complexity:** Medium
**Created:** 2026-06-27
**Updated:** 2026-06-28

## Problem Statement

The 2026-06-25 UX audit
(`thoughts/shared/research/2026-06-25-ux-audit.md`, finding S4) found that for the
target audience — software engineers on the shell who live in `--help` — the
in-CLI help *is* the documentation, and it is nearly empty:

- **Only `include` has a `Long:`** (`internal/cli/include.go:23-26`). Every other
  command, including `run`, `setup`, `init`, and `doctor`, shows only its
  one-line `Short:` plus its flags. `agent-creance run --help` says nothing about
  prerequisites, what it does, or what to expect.
- **No command has an `Example:`** field anywhere.
- **The root command has no `Long:`** (`internal/cli/cli.go:88-117`):
  `agent-creance --help` is a one-liner plus an alphabetized command list, with no
  "start here" narrative and no recommended sequence.
- **No grouping.** The commands are registered flat (`internal/cli/cli.go:119-132`)
  and cobra alphabetizes them, so `allow, clean, completion, deny, doctor, help,
  import, include, init, logs, policy, run, setup, status, version` appear as one
  undifferentiated list — no signal that `setup` → `init` → `run` is the path, and
  the cobra-provided `completion`/`help` commands sit mixed in.

The result is that the most natural way a shell engineer learns a CLI — reading
`--help` — yields almost no usable orientation.

## Desired Outcome

`agent-creance --help` and the per-command `--help` screens become a usable,
self-contained reference: the root help presents the happy-path sequence and a
grouped command list, and the key commands explain what they do and show at least
one concrete example. A shell engineer can learn how to use the tool from
`--help` alone, without opening `docs/design.md`.

## User Stories / Use Cases

- As an engineer who just installed the tool, I want `agent-creance --help` to
  show me the recommended setup→init→run sequence and a grouped command list, so
  that I know where to start without reading the design doc.
- As an engineer about to run a command, I want `agent-creance <cmd> --help` to
  explain what the command does and show an example invocation, so that I can use
  it correctly the first time.
- As an engineer scanning the command list, I want related commands grouped
  (setup, daily use, inspection, maintenance), so that I can find the right one
  quickly instead of reading an alphabetized wall.

## Acceptance Criteria

- [ ] The root command has a `Long:` that includes the recommended happy-path
      sequence (setup → init → run) and a one-line orientation on what the tool
      does.
- [ ] At minimum `run`, `setup`, `init`, `allow`, and `doctor` each gain a `Long:`
      that explains the command and at least one `Example:` showing a real
      invocation.
- [ ] Commands are organized into labelled groups (e.g. setup / daily / inspect /
      maintenance) in the root help output, using cobra command groups
      (`AddGroup` / `GroupID`), rather than a single alphabetized list.
- [ ] The cobra-provided `completion` and `help` commands are placed sensibly
      relative to the groups (not interleaved confusingly among the primary
      commands).
- [ ] Help text accurately reflects current behavior (e.g. `run`'s preconditions,
      `setup` being once-per-machine, `init` once-per-project) and contains no
      claims contradicted by the code.
- [ ] Existing testscript / golden coverage is updated so the help output is
      asserted and stays stable.

## Out of Scope

- Generating man pages or external/static documentation from the help text
  (cobra can do this later; not part of this ticket).
- README / quickstart / install docs — those are tracked separately (AC-0065).
- The `setup` "Next:" pointer and other ergonomics polish — tracked in AC-0066.
- Rewording the existing one-line `Short:` strings beyond what accuracy requires.

## Open Questions

None — well-understood from the audit.

## Questions for Research/Planning

- [ ] What group taxonomy fits best (setup / daily / inspect / maintenance vs an
      alternative), and which command lands in which group?
- [ ] Does the project want `Long:`/`Example:` on *all* commands or only the
      five named here in this ticket (the rest could follow)? Confirm the minimum
      set vs. completeness expectation.
- [ ] Is there an existing convention for where help strings live (inline in each
      `*.go` command file vs. extracted constants/embedded files like the cage
      briefing) to keep them reviewable?

## References

- `thoughts/shared/research/2026-06-25-ux-audit.md` — UX audit, finding S4.
- `internal/cli/cli.go:88-132` — root command (no `Long:`) and flat command
  registration.
- `internal/cli/include.go:23-26` — the only existing `Long:`, as a style
  reference.
- `docs/design.md:406-475` — the de-facto command reference that help text and
  examples can be distilled from.

## Implementation Plan

[Leave empty — filled when the plan is created.]

## Notes & Updates

### 2026-06-27
Created from UX audit finding S4. Complexity Medium: touches several command
files plus root assembly and refreshes help golden/testscript coverage, but each
change is mechanical and low-risk (output-only, already golden-tested rendering
discipline). Scope deliberately excludes README/quickstart (AC-0065) and other
ergonomics (AC-0066) to keep this focused on the in-CLI help surface.
