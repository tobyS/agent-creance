# AC-0041: Progress and status output for the `run` command

**Status:** Done
**Estimated Complexity:** Medium
**Created:** 2026-06-11
**Updated:** 2026-06-11

## Problem Statement

The first `run` in a monorepo with multiple components (e.g. 2× `composer.json` + 1× `package.json`) takes a long time and prints **nothing** while it works. The dominant cost is policy compilation: one sequential HTTP registry lookup (npm/packagist) per direct dependency across all manifests. Proxy startup and policy compilation are equally silent. The user cannot tell whether the command is working, hung, or what is consuming the time — the terminal just sits frozen until the agent eventually launches.

Subsequent runs are fast (results are cached), which makes the silent first run even more confusing: there is no hint that the wait is a one-time cost.

## Desired Outcome

`run` is transparent about what it is doing, on every run:

- Each major step is announced as it starts (policy compilation, rule generation per manifest, proxy startup, launching the agent).
- During the network-heavy rule-generation phase, the user sees live per-dependency progress per manifest (e.g. `backend/composer.json: looking up 42/87 packages`).
- Before slow work begins, the output sets expectations and explains the cause (e.g. "first run: fetching metadata for 120 packages from npm/packagist — results are cached for next time").
- Each step reports its duration, so the user can see after the fact what consumed the time.
- Fast (cached) runs still show the steps, which complete quickly with their durations — everyday runs remain readable, first runs are no longer a black box.

## User Stories / Use Cases

- As a developer running `agent-creance run` for the first time in a monorepo, I want to see what the command is doing and how far along it is, so that I know it is working and roughly how long it will take.
- As a developer waiting on a slow first run, I want the output to tell me *why* it is slow (fetching registry metadata for N packages, one-time cost), so that I don't kill the process or file a bug.
- As a returning user on a cached run, I want to see the steps confirm quickly with their durations, so that I can spot when something unexpectedly becomes slow (e.g. a changed manifest re-triggering lookups).

## Acceptance Criteria

- [x] Every `run` prints each major step as it starts: prerequisite/setup checks may stay silent on success, but policy compilation, rule generation (per manifest), profile compilation, proxy startup, and agent launch are each announced.
- [x] During rule generation, each manifest is announced with its component-relative path (e.g. `backend/composer.json`), and a live counter shows per-dependency lookup progress (`42/87 packages`).
- [x] Before network-heavy lookups begin, the output states the reason and scale upfront (first run or changed manifest; per-manifest package counts as each generator starts; registries involved) and notes that results are cached for subsequent runs.
- [x] Each step reports its duration on completion; on a slow first run the user can tell from the output which step consumed the time.
- [x] On a fully cached run, the steps still appear and the total added output remains compact (no per-dependency counters when no lookups happen).
- [x] A first run in a monorepo with 3 components never leaves the terminal without output for the duration of the registry-lookup phase.
- [x] When a step fails, the error appears in the context of the announced step, so the user knows which phase failed.

## Out of Scope

- Making the first run actually faster (parallelizing registry lookups, prefetching, cache warming) — follow-up ticket.
- Progress/status output for other commands (`policy refresh` already has its own summary; `setup`, `doctor` unchanged).
- Configurable verbosity levels / `--quiet` or `--verbose` flags.
- Changing any policy-compilation or generator behavior — this ticket is output-only.

## Open Questions

None — all business questions resolved during ticket creation.

## Questions for Research/Planning

- [ ] How should live counters degrade when stdout/stderr is not a TTY (CI, piped output) — line-per-update vs. milestone lines?
- [ ] Should progress go to stdout or stderr (the command ultimately execs the agent, which owns the terminal)?
- [ ] Where do progress hooks fit in the existing `compile`/`generator` interfaces without violating the sysdep-injection testing conventions (`internal/policy/compile/compile.go`, `internal/generator/generator.go`)?
- [ ] The total package count is only known after manifests are parsed — can the upfront expectation message include an accurate count, or per-manifest counts as each generator starts?
- [ ] How does progress output interact with the existing version-skew warnings on stderr (`internal/cli/run.go:165-172`)?

## References

- `internal/cli/run.go:49-160` — the 11-step `run` flow; currently prints only on precondition failures.
- `internal/generator/generator.go:164-186` — sequential per-dependency lookup loop (the slow part).
- `internal/policy/compile/compile.go:246-254` — input-hash cache gate that makes subsequent runs fast.
- `internal/policy/render/render.go:193-211` — existing summary rendering used by `policy refresh`.

## Implementation Plan

[Leave empty - will be filled when plan is created]

## Notes & Updates

### 2026-06-11 (implementation)
- Implemented across three commits: `internal/progress` package + stderr TTY
  probe; Reporter threading through `compile.New`/`generator.New` (nil = silent,
  registry layer untouched); `runRun` wiring + docs/design.md paragraph.
- Checkpoint decisions: in-place `\r` counter with append-only ~25/50/75%
  milestone fallback for non-TTY stderr; all progress on **stderr**.
- The upfront expectation message states the cause and registries; accurate
  package counts appear per manifest as each generator starts (counts are only
  known after manifest parsing — recorded in the research doc).
- One total-count caveat: the AC's "42/87" counter is per manifest, not global,
  matching the research finding that a global total isn't knowable upfront.
- Live verification in a real monorepo (3 components) recommended as a final
  smoke test; behavior is byte-exact-tested in `internal/progress` and
  asserted end-to-end in `TestRunProgressOutput`.

### 2026-06-11
- Decided on per-dependency progress granularity (not just step-level) — the registry-lookup phase is the dominant cost and needs a visible counter.
- Decided steps are shown on **every** run, not only slow ones; cached runs stay compact because counters only appear when lookups happen.
- Decided on upfront expectation-setting plus per-step durations, so users learn both *why* it's slow and *what* consumed the time.
- Performance work (parallel lookups) explicitly split into a follow-up ticket; this ticket changes output only.
- Complexity Medium: output must be threaded through compiler/generator layers, but no policy-logic changes.
