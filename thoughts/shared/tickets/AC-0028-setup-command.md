# AC-0028: `setup` command (WP-5.3)

**Status:** Done
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

- [x] `agent-creance setup` performs CA bootstrap+verify and skill install.
- [x] `--no-skill` skips skill install; `--no-ca-install` skips system trust and prints the env-var-only caveat.
- [x] Exit code is non-zero if CA verification fails (unless `--no-ca-install`).
- [x] Reuses AC-0026/AC-0027 (no duplicated logic).

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

- [x] Exact env-var-only caveat wording (which tools are uncovered: `go`/`curl`?).
  Resolved: the cage injects SSL_CERT_FILE/NODE_EXTRA_CA_CERTS/REQUESTS_CA_BUNDLE/
  GIT_SSL_CAINFO, so curl/Node/Python/git are covered; Go-on-macOS trusts the CA only via
  the keychain and is the gap. Caveat names the GitHub CLI (`gh`) as the example.

## References

- `docs/design.md` — "Commands" (setup variants).
- Spec WP-5.3.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.

### 2026-06-07
Implemented (WP-5.3). `setup` command in internal/cli/setup.go delegates to
setup.Installer (Bootstrap / EnsureCA / InstallSkill); App gained TLSProber/Sleeper seams.
--no-ca-install ensures the CA PEM exists, skips trust+verify, prints the coverage caveat
(curl/Node/Python/git covered; Go-on-macOS, e.g. `gh`, is the keychain-only gap); --no-skill
skips the skill; both opt-outs together are allowed. Fakes-based unit tests cover all flag
combinations + the verify-failure non-zero exit; setup_help.txtar covers help/args. Research:
thoughts/shared/research/2026-06-07-AC-0028-setup-command.md; plan:
thoughts/shared/plans/2026-06-07-AC-0028-setup-command.md.
