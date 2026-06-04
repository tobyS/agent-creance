# AC-0004: Spike S4 — Proxy-env-var coverage across the toolchain (WP-0.4)

**Status:** Open
**Estimated Complexity:** Small
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-0.4 / Spike S4 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** none
**Spike gate:** gates AC-0023 (WP-4.2)
**Kind:** Investigation (time-boxed, produces a findings note + a decision)

## Problem Statement

Under the deny-all network baseline the cage's only egress is the proxy, reached via `HTTPS_PROXY`. Any tool the agent uses that ignores `HTTPS_PROXY` will hard-fail. We must confirm the real toolset (Claude Code, `npm`, `pip`, `git`-over-HTTPS, `go`) all honor it, and document any that don't.

## Desired Outcome

A findings note listing each tool and whether it routes through `HTTPS_PROXY`, plus any required env vars or known gaps (e.g. `git` over SSH is unsupported by design).

## User Stories / Use Cases

- As an operator, I want `npm install`/`pip install`/`git fetch` to work caged so that normal dev flows don't break.
- As the maintainer, I want a documented list of unsupported tools so that failures are explainable.

## Acceptance Criteria

- [ ] Research note exists at `thoughts/shared/research/2026-06-DD-s4-proxy-env.md`.
- [ ] The note records PASS/FAIL (routes through proxy / ignores it) for: Claude Code, `npm`, `pip`, `git` (HTTPS), `go`.
- [ ] Any tool needing an extra/alternate env var (not just `HTTPS_PROXY`) is documented.
- [ ] A `Decision:` line lists the exact env set AC-0023 must inject, and confirms `git`-over-SSH stays unsupported.

## Verification & Test Steps

> Manual/integration spike; deliverable is the note.

1. Start mitmproxy on `127.0.0.1:P` with logging.
2. With `HTTPS_PROXY=http://127.0.0.1:P` (CA trusted), run each and confirm the request appears in the proxy log:
   - `npm view react version` → expected: request to `registry.npmjs.org` seen in proxy log.
   - `pip download --no-deps requests -d /tmp/x` → expected: PyPI requests via proxy.
   - `git ls-remote https://github.com/golang/go` → expected: via proxy.
   - `go list -m -versions github.com/spf13/cobra` (with `GOPROXY` default) → expected: via proxy.
   - `claude --version` + trivial prompt → expected: API via proxy.
3. For any tool with no proxy-log entry, record it as a gap and the workaround.
4. Self-check: `test -f thoughts/shared/research/2026-06-*-s4-proxy-env.md && grep -q '^Decision:' thoughts/shared/research/2026-06-*-s4-proxy-env.md` → exit 0.

## Out of Scope

- Implementing env injection (AC-0023).
- Supporting tools that fundamentally cannot use an HTTP proxy (documented, not fixed).

## Dependencies & Sequencing

Phase 0. Gates AC-0023.

## Questions for Research/Planning

- [ ] Does `go` need `GOPROXY`/`GONOSUMDB` tweaks under the cage, or does `HTTPS_PROXY` suffice?

## References

- `docs/design.md` — "Open spikes" (S4).
- Spec WP-0.4.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification. Gating spike.
