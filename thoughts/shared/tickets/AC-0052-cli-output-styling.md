# AC-0052: Polish CLI output — semantic color and visual hierarchy, TTY-aware

**Status:** Open
**Estimated Complexity:** Large
**Created:** 2026-06-17
**Updated:** 2026-06-17

## Problem Statement

`agent-creance`'s command output is deliberately monochrome plain text today:
`fmt.Fprint` everywhere, the glyphs `✓ ⚠ ✗`, two-space indents, and an explicit
"no ANSI escapes, the codebase uses none" rule (`internal/progress/printer.go`).
The reports are functional but flat — nothing draws the eye to what matters in a
given run (a deny, an untrusted CA, an orphan proxy, a warning). For a tool whose
whole job is surfacing the security posture of a cage, a wall of same-weight grey
text buries the signal. The output reads as utilitarian and, in the user's words,
"love-less."

This is a **styling** problem, not a content problem: the commands report the
right information, but they present it without hierarchy or semantic emphasis.

## Desired Outcome

Every command shares one consistent, restrained visual language that makes the
important parts pop without turning the CLI into a toy:

- Semantic color carries meaning: green = ok, yellow = warn, red = problem, on
  glyphs and the key tokens of a line.
- **Bold** section headers establish structure at a glance.
- Dimmed secondary detail (pids, ports, durations, "tested" versions) recedes so
  the primary message stands out.
- No banners, no emoji, no celebratory footers — professional, not playful.

Crucially, this is additive: color and weight reinforce information that is
already conveyed by glyphs and words, so nothing is lost when color is absent.

## User Stories / Use Cases

- As an operator running `doctor`, I want failures and warnings to stand out in
  red/yellow so I can spot an untrusted CA or orphan proxy without reading every
  line.
- As a developer watching `run` progress, I want the ✓ completions and the active
  step visually distinct so I can track where a launch is at a glance.
- As a user piping output into a log, a file, or CI, I want clean, parseable,
  color-free text — exactly what I get today — so nothing downstream breaks.
- As a colorblind user (or anyone in a plain terminal), I want every status still
  conveyed by its glyph and wording, so color is a bonus, never the sole signal.

## Acceptance Criteria

- [ ] Every command's output adopts the shared visual language: semantic
      green/yellow/red on glyphs and key tokens, bold section headers, dimmed
      secondary detail. Covers at minimum `doctor`, `status`, `run` (progress),
      `policy`, `import`, `init`, `setup`, `logs`, `version`.
- [ ] Color is **never the only signal**: each status remains identifiable by its
      glyph and wording with color stripped.
- [ ] Color mode auto-detects a TTY and honors the `NO_COLOR` environment
      variable; a `--color=auto|always|never` flag overrides detection, default
      `auto`.
- [ ] When stdout/stderr is not a TTY, or `NO_COLOR` is set, or `--color=never`,
      the output is **byte-for-byte identical to today's** plain output (verified
      by the existing plain golden files remaining unchanged).
- [ ] Colorized output is also covered by golden-file tests, so both the plain
      and colored renderings are pinned.
- [ ] The progress printer's in-place `\r` rewrite math accounts for ANSI escape
      bytes vs. on-screen rune width (no misaligned padding or residue when color
      is active).
- [ ] `make test`, `make lint` pass; `make build` at the end.

## Out of Scope

- Any live/interactive TUI: alt-screen takeover, redrawing dashboards, spinners,
  a live `status` or `run` panel. This ticket is styled static output only.
- User-configurable themes or palettes; truecolor/256-color theming config. A
  single built-in palette is the goal.
- Restructuring *what* each command reports (which fields, ordering, sections) —
  this is presentation, not a content redesign.
- The `tmt`/`tce` plugin scripts and their output (e.g. `open_tickets.sh`) — they
  are not part of `agent-creance`.

## Open Questions

None — scope, intensity (restrained & semantic), breadth (every command), and
plain-fallback behavior were all settled during ticket authoring.

## Questions for Research/Planning

- [ ] Where should TTY detection live? Project convention routes all OS access
      through the `internal/sysdep` seam (injected, with fakes) — does color/TTY
      detection get a new interface there, or is it derived from the writer?
- [ ] Hand-rolled minimal ANSI helper vs. a small dependency (e.g.
      fatih/color, lipgloss). The codebase currently has zero color/TUI deps;
      weigh that against consistency and the `\r` width math.
- [ ] Golden-file strategy for two modes: how to parametrize plain vs. colored
      goldens, and how to keep colored `testdata` readable (escape sequences).
- [ ] How does the `--color` flag wire into the cobra command tree — a global
      persistent flag resolved once into the `App`?
- [ ] Per-command token audit: enumerate exactly which tokens get color/dim in
      each renderer (`internal/doctor/report.go`, `internal/status/report.go`,
      `internal/prereq/report.go`, `internal/policy/render/render.go`, the
      progress printer, and the remaining command writers).

## References

- `internal/progress/printer.go` — in-place `\r` rendering; the "no ANSI" rule
  lives here and the width math will need to change.
- `internal/doctor/report.go` — representative golden-tested report renderer
  (glyphs, sections, status-as-data).
- `internal/prereq/report.go`, `internal/status/report.go`,
  `internal/policy/render/render.go` — other report renderers in scope.
- `docs/design.md` — architecture, command surface.

## Implementation Plan

[Leave empty — filled when the plan is created.]

## Notes & Updates

### 2026-06-17

- Created from a discussion about the CLI's "love-less" output.
- Decisions: (a) styled **static** output, not a live TUI; (b) cover **every**
  command; (c) degrade to **plain** (byte-identical to today) when piped /
  `NO_COLOR` / `--color=never`; (d) **restrained & semantic** intensity — color
  carries meaning only, bold headers, dimmed secondary detail, no banners/emoji.
- Complexity Large: touches every renderer, introduces a color/TTY layer through
  the testability seam, and needs a two-mode golden strategy. The hard part is
  the constraints (plain parity, accessibility, `\r` width math), not the colors.
