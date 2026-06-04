# AC-0010: Rule model & matcher + decision-vector corpus (WP-2.1)

**Status:** Open
**Estimated Complexity:** Large
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-2.1 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** AC-0007 (WP-1.2)
**Spike gate:** none
**Cross-cutting:** establishes C1 (Go↔Python matcher parity)

## Problem Statement

The allow / soft-deny / hard-deny + per-host `mode` decision is the heart of the cage and it exists twice — Go (`policy explain`) and Python (`enforcer.py`). If the two disagree, `explain` lies about what the proxy will do. This ticket builds the Go matcher *and* the language-neutral decision-vector corpus that both implementations must satisfy.

## Desired Outcome

`internal/policy` exposes a matcher that, given a compiled rule set and a request (host, path, method), returns the decision and matched rule, with precedence and wildcard semantics fully specified — and a `testdata/decision-vectors/` corpus that AC-0017 (Python) will also consume.

## User Stories / Use Cases

- As a developer of `enforcer.py`, I want a shared vector corpus so that my Python implementation provably matches the Go one.
- As an operator, I want `policy explain` to tell me exactly why a URL is allowed/denied, matching real proxy behavior.

## Acceptance Criteria

- [ ] Host matching supports exact, `*.suffix`, and `*` wildcards; path matching supports prefix + `*`/`**` globs; method matching is set-membership.
- [ ] Precedence is implemented and documented: `deny_always` shadows `allow`; unmatched → soft-deny; `mode` is carried on the matched allow.
- [ ] A language-neutral corpus exists at `internal/policy/testdata/decision-vectors/` (JSON: ruleset + request → expected `{decision, mode, matched_rule}`), covering allow, host-wide allow, soft-deny default, hard-deny by host, hard-deny by path glob, passthrough host, and wildcard edge cases.
- [ ] The Go matcher passes 100% of the corpus.

## Verification & Test Steps

1. `go build ./...` → compiles.
2. `go test -race ./internal/policy/...` → pass; a table test loads every file in `testdata/decision-vectors/` and asserts the matcher's output equals the expected decision/mode/rule.
3. Coverage of the corpus is exhaustive: assert the test fails if a vector file is added but unmatched (e.g., iterate the directory, fail on zero vectors).
4. `make lint` → clean.
5. Confirm the corpus is consumable cross-language: vectors are plain JSON with no Go-specific encoding (a `jq '.' ` over each file exits 0).

## Out of Scope

- The Python implementation (AC-0017 must reuse this corpus).
- Reading rules from a compiled `policy.json` on disk (the compiler is AC-0013); this ticket matches against an in-memory rule set.

## Dependencies & Sequencing

Phase 2, first. Gates AC-0013, AC-0015, AC-0017. The corpus is a hard prerequisite for C1.

## Questions for Research/Planning

- [ ] Exact glob semantics for `**` vs `*` in path scoping — does `**/.env` match at any depth including root?
- [ ] How is the "most specific rule wins" tie-break defined when multiple allows match?

## References

- `docs/design.md` — "Network refusal handling", "Per-host enforcement modes".
- Spec WP-2.1, cross-cutting C1.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification. The decision-vector corpus is the single guardrail against Go/Python drift — do not close this ticket without it.
