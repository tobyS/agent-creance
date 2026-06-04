# AC-0028: `setup` command (WP-5.3)

**Status:** Open
**Estimated Complexity:** Medium
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-5.3 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** AC-0026 (WP-5.1), AC-0027 (WP-5.2)
**Spike gate:** none

## Problem Statement

Operators need one onboarding command that installs the CA (with verification) and the skill, with opt-outs for each. `--no-ca-install` supports the env-var-only mode for users who don't want a system-wide trust change — with an honest caveat that some tools won't honor the CA-bundle env vars.

## Desired Outcome

`agent-creance setup` runs CA bootstrap+verify (AC-0026) and skill install (AC-0027); `--no-skill` and `--no-ca-install` opt out of each; `--no-ca-install` documents the best-effort env-var coverage caveat.

## User Stories / Use Cases

- As an operator, I want one `setup` to get ready so that `run` works.
- As a cautious operator, I want `--no-ca-install` so that I don't trust the CA system-wide, accepting reduced tool coverage.

## Acceptance Criteria

- [ ] `agent-creance setup` performs CA bootstrap+verify and skill install.
- [ ] `--no-skill` skips skill install; `--no-ca-install` skips system trust and prints the env-var-only caveat.
- [ ] Exit code is non-zero if CA verification fails (unless `--no-ca-install`).
- [ ] Reuses AC-0026/AC-0027 (no duplicated logic).

## Verification & Test Steps

1. `go build ./...` → compiles.
2. Hermetic CLI tests (`testscript`) with stubbed `security`/`mitmproxy`/`curl`:
   - default: both CA-verify and skill-install steps run; success exit 0.
   - `--no-skill`: skill step skipped (assert no skill file written).
   - `--no-ca-install`: trust step skipped; output contains the coverage caveat; skill still installed.
   - CA-verify failure: non-zero exit + actionable message.
3. `go test -race ./internal/cli/...` → green.
4. `make lint` → clean.

## Out of Scope

- The mechanics of CA verify / skill install (AC-0026/0027).

## Dependencies & Sequencing

Phase 5. Wires AC-0026 + AC-0027. Precondition surface for `run` (AC-0025).

## Questions for Research/Planning

- [ ] Exact env-var-only caveat wording (which tools are uncovered: `go`/`curl`?).

## References

- `docs/design.md` — "Commands" (setup variants).
- Spec WP-5.3.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.
