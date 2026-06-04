# AC-0004: Spike S4 — Proxy-env-var coverage across the toolchain (WP-0.4)

**Status:** Done
**Decision:** all 5 tools route via `HTTPS_PROXY`; `go` needs *keychain* CA trust (ignores CA env vars on macOS); git-over-SSH unsupported (resolved 2026-06-04)
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

- [x] Research note exists at `thoughts/shared/research/2026-06-04-s4-proxy-env.md`.
- [x] The note records PASS/FAIL (routes through proxy / ignores it) for: Claude Code, `npm`, `pip`, `git` (HTTPS), `go` — all PASS for routing; `go` carries a CA-trust caveat.
- [x] Any tool needing an extra/alternate env var (not just `HTTPS_PROXY`) is documented (per-tool CA env vars + `go`'s keychain-only trust).
- [x] A `Decision:` line lists the exact env set AC-0023 must inject, and confirms `git`-over-SSH stays unsupported.

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

- [x] Does `go` need `GOPROXY`/`GONOSUMDB` tweaks under the cage, or does `HTTPS_PROXY` suffice? — **`HTTPS_PROXY` suffices for routing; no `GOPROXY`/`GOSUMDB` tweak needed.** The only requirement is that the allowlist (AC-0017) includes both `proxy.golang.org` and `sum.golang.org` (or `GOSUMDB=off`). Separately, `go` ignores the CA env vars on macOS and needs the CA in the keychain.

## References

- `docs/design.md` — "Open spikes" (S4).
- Spec WP-0.4.

## Implementation Plan

## Notes & Updates

### 2026-06-04
Created from the v0.1 technical specification. Gating spike.

### 2026-06-04 — Resolved
Ran each tool through a live `mitmdump` (12.2.3) on `127.0.0.1:18080` with the agent-creance
env set, and read the proxy log's `server connect <host>` lines as the authoritative
routed-through-proxy signal, plus a CA-env-unset negative control per tool. **All five tools
route through `HTTPS_PROXY`** (claude→`api.anthropic.com`, npm→`registry.npmjs.org`,
pip→`pypi.org`+`files.pythonhosted.org`, git→`github.com`, go→`proxy.golang.org`). CA trust
splits: `claude`/`npm`/`pip`/`git` honor the belt-and-suspenders CA env vars
(`NODE_EXTRA_CA_CERTS`/`SSL_CERT_FILE`/`REQUESTS_CA_BUNDLE`/`GIT_SSL_CAINFO`); **`go` ignores
all of them on macOS** (proven via dead-port + direct-with-CA-only controls) and trusts the
CA only via the system keychain — so `go` works on the default `setup` (CA installed) but
NOT on `setup --no-ca-install`. No `GOPROXY`/`GOSUMDB` tweak needed. **Decision:** exact env
set for AC-0023 recorded in the note; git-over-SSH stays unsupported (SSH can't traverse an
HTTP proxy). Findings note: `thoughts/shared/research/2026-06-04-s4-proxy-env.md`. Incidental:
Claude Code also hits `mcp-proxy.anthropic.com` and `http-intake.logs.us5.datadoghq.com`
(Datadog telemetry) — flagged for AC-0017.

Gates released: AC-0023 (WP-4.2 env injection) may now proceed on the recorded env set. Two
`docs/design.md` corrections noted for the gated build tickets (go CA = keychain-only; go
allowlist needs `proxy.golang.org` + `sum.golang.org`).
