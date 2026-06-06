# AC-0023: Safehouse invocation (WP-4.2)

**Status:** Done
**Estimated Complexity:** Large
**Created:** 2026-06-04
**Updated:** 2026-06-06
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

- [x] Constructs `--add-dirs`/`--add-dirs-ro`/`--enable=` from `safehouse:` config and `--append-profile` from the `.sb` (`internal/profile`). Real safehouse 0.10.1 spellings: colon-joined `--add-dirs`/`--add-dirs-ro`, comma-joined `--enable=`; two ordered `--append-profile` (network.sb then the launch-time proxy.sb).
- [x] Injects env (full S4 confirmed set): `HTTPS_PROXY`/`HTTP_PROXY`/`NO_PROXY` + lowercase variants, and the four CA vars `NODE_EXTRA_CA_CERTS`/`SSL_CERT_FILE`/`REQUESTS_CA_BUNDLE`/`GIT_SSL_CAINFO`. Delivered via `--env-pass` (names listed; values set on the safehouse process by the caller).
- [x] Redirects `CLAUDE_CONFIG_DIR` to `~/.cache/agent-creance/projects/<hash>/claude/`, seeded with a minimal sanitized `settings.json` (`{}`, seed-only-if-absent); the real `~/.claude` is never mounted (absent).
- [x] The constructed argv+env is a pure function of (config, port, paths) — `cage.Build` does no I/O and is golden-tested. Side effects (seed + proxy fragment) are isolated in `Builder.Prepare`.

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

### 2026-06-06
Implemented in `internal/cage` (+ `state.Layout.ProxyProfileSB()`). Research:
`thoughts/shared/research/2026-06-06-AC-0023-safehouse-invocation.md`; plan:
`thoughts/shared/plans/2026-06-06-AC-0023-safehouse-invocation.md`.

Decisions: full S4 env set; empty `{}` sanitized seed (seed-only-if-absent); full
invocation (config.env via `--env-pass`, `agent.workdir` → `--workdir`,
`agent.command` after `--`). Computed vars take precedence over `config.env` so
egress filtering can't be disabled from config. The real `~/.claude` is never
mounted.

Verification: `go build`, `make test` (race, hermetic), and `make lint` are green;
golden + negative (never-mounts-real-`~/.claude`) + env-precedence/validation +
fake-FS Prepare tests pass. **The integration test (step 4) could not run
end-to-end in this environment** — the session host is itself sandboxed and cannot
nest a `sandbox-exec` policy (`sandbox_apply: Operation not permitted`), so the
test detects that and skips cleanly; it must be run on an unsandboxed macOS host to
exercise the real safehouse path.

Follow-up (not blocking AC-0023): `internal/buildinfo` records tested-against
`agent-safehouse: 1.4.2`, but the installed binary is `0.10.1` (the version S5
verified and these flags came from). Reconcile the constant / installed tool per
the buildinfo convention.
