# AC-0015: `policy show` / `policy explain` commands (WP-2.6)

**Status:** Open
**Estimated Complexity:** Medium
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-2.6 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** AC-0010 (WP-2.1), AC-0013 (WP-2.4)
**Spike gate:** none
**Cross-cutting:** C1 (uses the shared matcher), C3 (golden output)

## Problem Statement

Operators need to see the fully-resolved policy and understand *why* a given URL is allowed or denied — including which source (explicit/generated/global) a rule came from and whether a host is a passthrough (audit) blind spot.

## Desired Outcome

`agent-creance policy show` dumps the resolved policy with per-rule source annotations and a distinct flag for `passthrough` rules; `agent-creance policy explain URL` reports the matching rule and its source, using the same matcher the proxy uses.

## User Stories / Use Cases

- As an operator, I want `policy show` to reveal generator output so that I can audit what my deps opened.
- As an operator debugging a block, I want `policy explain <url>` to name the deciding rule so that I know whether to `allow` it.

## Acceptance Criteria

- [ ] `policy show` lists every rule with `[explicit]`/`[generated:…]`/`[global:…]` annotation; passthrough rules are visibly flagged.
- [ ] `policy explain URL` prints the decision (allow/soft-deny/hard-deny + mode) and the matching rule + source, consistent with AC-0010's matcher (C1).
- [ ] Output is stable and golden-tested.

## Verification & Test Steps

1. `go build ./...` → compiles.
2. Hermetic CLI test: `go test -race ./internal/cli/...` includes a `testscript` `.txtar` that compiles a fixture policy and runs `agent-creance policy show` + `policy explain <url>`, comparing against golden output matching the design's sample.
3. C1 consistency: a test asserts `policy explain` decisions for a sample of URLs equal the matcher's decisions for the same decision-vectors used in AC-0010.
4. `make golden` diff for the show/explain golden is reviewed.
5. `make lint` → clean; `make test` → green.

## Out of Scope

- `policy refresh` (AC-0016).
- Mutating policy (`allow`/`deny`, AC-0030).

## Dependencies & Sequencing

Phase 2. Reaches **Milestone M1** together with AC-0013.

## Questions for Research/Planning

- [ ] Output format: plain aligned columns (per design sample) — any `--json` mode for v0.1 or defer?
- [ ] How does `explain` resolve a URL to host/path/method (parse rules)?

## References

- `docs/design.md` — "Allowlist generators" (Visibility), "Per-host enforcement modes" (Visibility).
- Spec WP-2.6.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification. With AC-0013 this hits M1 ("Policy visible").
