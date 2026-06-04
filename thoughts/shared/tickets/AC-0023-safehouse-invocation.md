# AC-0023: Safehouse invocation (WP-4.2)

**Status:** Open
**Estimated Complexity:** Large
**Created:** 2026-06-04
**Updated:** 2026-06-04
**Plan reference:** WP-4.2 (`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`)
**Depends on:** AC-0014 (WP-2.5, the .sb), AC-0022 (WP-4.1, creds)
**Spike gate:** **S4 (AC-0004), S5 (AC-0005)**

## Problem Statement

agent-creance composes Safehouse rather than reimplementing isolation. It must construct the exact Safehouse flags and environment: mount dirs, the generated network profile, the proxy env vars, the CA-bundle env vars, and a redirected ephemeral `CLAUDE_CONFIG_DIR` (so the real `~/.claude` is never writable) seeded with sanitized settings.

## Desired Outcome

`internal/cage` builds the Safehouse argv + env for a given compiled config: `--add-dirs*`/`--enable`/`--append-profile`/`--env-pass`, `HTTPS_PROXY`, the four CA-bundle vars, and `CLAUDE_CONFIG_DIR` pointed at the state-dir ephemeral config.

## User Stories / Use Cases

- As an operator, I want my project mounted RW and my git config RO exactly as declared so that the agent has what it needs and nothing more.
- As a security-conscious user, I want the real `~/.claude` non-writable so that the agent can't plant a persistent hook.

## Acceptance Criteria

- [ ] Constructs `--add-dirs_rw`/`--add-dirs_ro`/`--enable` from `safehouse:` config and `--append-profile` from AC-0014's `.sb`.
- [ ] Injects env: `HTTPS_PROXY` (live proxy URL), `NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE`, `GIT_SSL_CAINFO` (per S4's confirmed set).
- [ ] Redirects `CLAUDE_CONFIG_DIR` to `~/.cache/agent-creance/projects/<hash>/claude/`, seeded with a minimal sanitized settings file; real `~/.claude` is RO/absent.
- [ ] The constructed argv+env is a pure function of (config, port, paths) — golden-testable without launching anything.

## Verification & Test Steps

1. `go build ./...` → compiles.
2. `go test -race ./internal/cage/...`: golden argv+env for a fixture config + port (assert exact flags, the four CA vars, `HTTPS_PROXY`, and the redirected `CLAUDE_CONFIG_DIR`). `make golden` diff reviewed.
3. Negative: assert the argv never mounts the real `~/.claude` read-write.
4. Integration (`make test-integration`, gated S4/S5): launch Safehouse with the built command running a trivial caged command (e.g. `echo ok`) and assert exit 0 + that egress is blocked except via the proxy.
5. `make lint` → clean.
6. **Systematic verification:** the "egress blocked except via proxy" smoke here is the minimal check; the full isolation matrix is verified end-to-end by **AC-0033** (the M3 gate).

## Out of Scope

- Process-group/signal forwarding (AC-0024).
- The `run` orchestration (AC-0025).
- Generating the `.sb` (AC-0014).

## Dependencies & Sequencing

Phase 4. Gated by S4 + S5. Critical path to M3.

## Questions for Research/Planning

- [ ] Exact Safehouse flag names/spellings (`--add-dirs`, `--env-pass`, `--append-profile`) and how multiple dirs are passed.
- [ ] What goes in the "sanitized settings" seed file?

## References

- `docs/design.md` — "Architecture", "The proxy and the credential story" (executable-config redirect).
- Spec WP-4.2.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification.
