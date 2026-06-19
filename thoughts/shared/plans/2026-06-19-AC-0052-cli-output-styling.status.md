---
ticket: AC-0052
plan: thoughts/shared/plans/2026-06-19-AC-0052-cli-output-styling.md
started: 2026-06-19
---

# AC-0052 implementation status

- [x] Phase 0 — Foundation (fatih/color dep, internal/style, IsStdoutTerminal, --color flag + App stylers)
- [x] Phase 1 — Progress printer color (run / stderr)
- [x] Phase 2 — doctor + prereq renderers
- [x] Phase 3 — status renderer (two-path tabwriter/manual)
- [x] Phase 4 — policy/render renderer
- [x] Phase 5 — remaining inline command glyphs
- [x] Phase 6 — testscript coverage + final verification + build

## Notes
- Phase 0 (2026-06-19): added fatih/color v1.19.0 (+ go-isatty, go-colorable);
  internal/style with Styler (nil-safe identity when disabled), Resolve, and
  VisibleWidth; sysdep.Terminal.IsStdoutTerminal + fake; App.OutStyle/ErrStyle
  resolved per-stream in a root PersistentPreRunE behind a --color persistent
  flag. No visible output change yet. color_flag.txtar covers flag plumbing +
  invalid-value error. make test + make lint green.
- Phase 1 (2026-06-19): progress printer takes a styler; ✓ completion glyphs are
  green and the trailing (duration) is dimmed; openLine/rewrite now measure
  style.VisibleWidth so the \r pad math ignores escape bytes. run.go wires
  app.ErrStyle into NewPrinter and colors its inline ⚠ warnings (skew detail
  dimmed). Plain output byte-identical (existing printer assertions unchanged);
  added two colored-mode tests. make test + make lint green.
- Phase 2 (2026-06-19): doctor.Render + prereq.Report take a styler. Bold section
  headers; glyphs colored (green ok / yellow warn / red miss); secondary detail
  dimmed — "tested against X", the tested-versions paragraph, and the mid-message
  pids/ports/paths/reasons (isolate-every-token). Width-before-inject: prereq
  column pads computed from plain fields. Existing plain goldens unchanged
  (verified via git: only render_*_color / doctor_report_color added). doctor.go
  passes app.OutStyle. make test + make lint green.
- Phase 4 (2026-06-19): policy render Show/Explain/Refresh take a styler (JSON
  variants untouched). Bold ALLOW/DENY + refresh headers; ⚠ markers yellow;
  Explain decision word colored (allow green / soft-deny yellow / hard-deny red);
  dimmed [source] tags, (method), (reason), the passthrough Note, mode, and the
  refresh "(… cleared)". Width-before-inject for the dimmed tag column. Plain
  + JSON goldens unchanged; _color siblings added; render_vectors_test passes a
  plain styler. policy.go passes app.OutStyle. make test + make lint green.
- Phase 5 (2026-06-19): inline ✓ success glyphs greened in init/setup/import/
  mutate (via app.OutStyle.OK); version dims the (commit/built) detail and the
  tested-version values, bolds "tested against:". Testscripts (non-tty → plain)
  unchanged. make test + make lint green.
- Phase 5b/logs (2026-06-19): colored the audit renderers behind `logs`
  (FormatEntry, Summary.Render, Dump, Follow) — decision verdict colored, ts +
  detail dimmed, summary header bold, decision column width-before-inject. Plain
  format_lines/summary goldens unchanged; _color siblings added. logs.go passes
  app.OutStyle. Completes per-command coverage.
- Phase 6 (2026-06-19): color_output.txtar asserts --color=always emits escapes,
  auto/never/NO_COLOR stay plain, and --color=always overrides NO_COLOR. Manual
  smoke on the built binary confirmed colored TTY-forced output and byte-plain
  fallback. Final: make test (28 pkgs, no failures) + make lint green; make build.
  Ticket marked Done, acceptance criteria ticked.
- Phase 3 (2026-06-19): status.Render takes a styler; two-path — plain keeps
  tabwriter verbatim (byte-identical), color uses a manual visible-width layout
  with bold headers, state words colored (running green / orphan red / stranded
  yellow / down dim) and the port dimmed. Color alignment matches the plain
  table. Existing render_*.golden unchanged; _color siblings added. status.go
  passes app.OutStyle. make test + make lint green.
