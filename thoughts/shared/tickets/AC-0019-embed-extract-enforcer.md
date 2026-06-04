# AC-0019: Embed & extract enforcer.py (WP-3.3)

**Status:** Open
**Estimated Complexity:** Small
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-3.3 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** AC-0017 (WP-3.1)
**Spike gate:** none
**Cross-cutting:** C4 (out-of-tree)

## Problem Statement

Users should never install or version the Python addon themselves — they just see "mitmproxy is running." The addon is a constant shipped inside the Go binary and extracted to the state dir on first run.

## Desired Outcome

`internal/proxy` embeds `enforcer.py` via `go:embed` and extracts it to the out-of-tree state dir on first run, idempotently, refreshing when the binary's embedded copy changes.

## User Stories / Use Cases

- As an operator, I want the enforcer to "just be there" so that I never manage a Python file.

## Acceptance Criteria

- [ ] `enforcer.py` is embedded in the binary (`go:embed`).
- [ ] On first run it's written to the state dir (a constant location, not per-project content); re-runs are idempotent.
- [ ] When the embedded copy differs from the extracted one (binary upgraded), the extracted file is refreshed.
- [ ] Extraction target is out-of-tree.

## Verification & Test Steps

1. `go build ./...` → compiles (embed directive resolves; the file exists at the embed path).
2. `go test -race ./internal/proxy/...` (extraction tests): first call writes the file; second call is a no-op (mtime/content stable); a simulated content change triggers a rewrite.
3. Content integrity: a test asserts the extracted bytes equal the embedded bytes.
4. C4 guard: extraction path is under the state dir; nothing is written into the project tree.
5. `make lint` → clean.

## Out of Scope

- The addon's behavior (AC-0017/0018).
- Starting mitmproxy with it (AC-0020).

## Dependencies & Sequencing

Phase 3. Small enabler between the addon and the lifecycle manager.

## Questions for Research/Planning

- [ ] Version/checksum the embedded addon so "differs" is cheap to detect?

## References

- `docs/design.md` — "Config compilation" (enforcer embedded), "Tech stack".
- Spec WP-3.3.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.
