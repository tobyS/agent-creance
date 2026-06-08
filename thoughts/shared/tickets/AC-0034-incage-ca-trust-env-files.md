# AC-0034: Caged tools can't load the mitmproxy CA from the injected env-var files

**Status:** Done
**Estimated Complexity:** Medium
**Created:** 2026-06-08
**Updated:** 2026-06-08

## Problem Statement

The cage injects four CA-trust environment variables into the caged agent —
`NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE`, `GIT_SSL_CAINFO` —
all pointing at `~/.mitmproxy/mitmproxy-ca-cert.pem` (see
`internal/cage/cage.go` `buildEnv` / `caCertPath`). These are the "belt-and-
suspenders" CA trust mechanism from spike S4, meant so non-keychain TLS clients
(node/npm, python `requests`, git, etc.) trust mitmproxy's intercepted TLS.

The AC-0033 adversarial battery surfaced that **`~/.mitmproxy` is not readable
inside the cage** — agent-safehouse's base policy denies it
(`cat ~/.mitmproxy/mitmproxy-ca-cert.pem` from inside the cage →
`Operation not permitted`). So those four env vars point at a path the caged
process **cannot open**. Any tool that trusts the proxy CA *only* via these files
(not the macOS keychain) will fail TLS through the proxy from inside the cage.

This matters because the cage's whole egress story depends on toolchains trusting
the CA. macOS `curl`/CFNetwork tools validate via the keychain (`trustd`), so they
are fine once `agent-creance setup` has run — but **node/npm and python-requests
do not consult the macOS keychain**; they rely on `NODE_EXTRA_CA_CERTS` /
`REQUESTS_CA_BUNDLE`. If those files are unreadable in-cage, a caged
`npm install` / `pip install` over HTTPS would fail its TLS handshake against the
proxy. The S4 spike validated these env vars **on the host**, not necessarily from
*inside* the cage, so this may be a live gap that breaks a common agent workflow.

## Desired Outcome

A caged tool that relies on the env-var CA files (node/npm, python-requests) can
successfully complete TLS through the proxy to an allowlisted host — i.e. the four
injected CA env vars point at a CA file the caged process can actually read. The
behavior is proven by a caged reproduction, and AC-0033 guards it going forward.

## User Stories / Use Cases

- As a developer running a caged agent, I want `npm install` / `pip install` to
  work through the egress proxy so that the agent can install dependencies inside
  the cage instead of hard-failing on a TLS/CA error.
- As the maintainer, I want the cage's CA-trust mechanism to actually function
  in-cage (not just on the host) so that the S4 "belt-and-suspenders" promise is
  real and regression-guarded.

## Acceptance Criteria

- [x] A caged reproduction demonstrates the failure first: node (`NODE_EXTRA_CA_CERTS`)
      and python (`SSL_CERT_FILE`/`REQUESTS_CA_BUNDLE`) fail their TLS handshake to an
      allowlisted host through the proxy from inside the cage — `env-ca-node` →
      `UNABLE_TO_VERIFY_LEAF_SIGNATURE`, `env-ca-python` → `ERR` (commit `6081186`).
- [x] After the fix, the same caged reproduction succeeds: both get a 200 from the
      allowlisted upstream in-cage (commit `644498f`).
- [x] macOS keychain-based clients (`curl`/CFNetwork, `go`) continue to work as
      before — the existing proxy/allow/passthrough vectors stay green; the fix only
      adds a filesystem read-grant.
- [x] The fix does not broaden filesystem access beyond the single CA PEM: a third
      `--append-profile` fragment (`ca.sb`) grants `file-read*` on exactly
      `mitmproxy-ca-cert.pem` + metadata-only on its parent dir. Verified in-cage:
      the CA private key (`mitmproxy-ca.pem`) stays unreadable.
- [x] The AC-0033 battery is extended with two caged env-var-CA TLS probes
      (`env-ca-node`, `env-ca-python`); a regression turns the battery red (count: 18).

## Out of Scope

- Changing how `agent-creance setup` installs/﻿trusts the CA in the keychain
  (AC-0026 territory) — this ticket is about the *in-cage* env-var-file path only.
- Removing the env-var CA injection entirely (that is a larger design decision
  about whether keychain trust alone is sufficient; capture separately if desired).
- The `CLAUDE_CONFIG_DIR` writability gap (separate ticket).

## Open Questions

- None blocking — this is a maintainer-facing correctness fix.

## Questions for Research/Planning

- [ ] What is the cleanest way to make the single CA PEM readable in-cage? Options
      to weigh: a narrow safehouse read-grant for the CA file, copying the CA into
      a path the cage already mounts (e.g. under the project state dir / a mounted
      location) and pointing the env vars there, or relying on a safehouse
      capability (does `--enable=...` cover this?).
- [ ] Does agent-safehouse expose a supported way to grant read of a single file
      outside the default policy, or must this be an `--append-profile` fragment
      from agent-creance?
- [ ] Which exact clients must be covered (node/npm, pip/requests, git-over-HTTPS,
      go)? Re-confirm the S4 matrix *from inside the cage*, not on the host.
- [ ] Should the four CA env vars point at a copy in the out-of-tree state dir
      (and is that dir itself readable in-cage — cf. the cache-mount question in
      the sibling ticket)?

## References

- AC-0033 (`thoughts/shared/tickets/AC-0033-adversarial-cage-verification.md`) —
  the battery that surfaced this; its `testdata/fake-agent.sh` works around the gap
  with an in-mount `--cacert` copy.
- `internal/cage/cage.go` — `buildEnv` (the four CA env vars), `caCertPath`.
- Spike S4 (`thoughts/shared/research/2026-06-04-s4-proxy-env.md`) — proxy-env / CA
  coverage, validated on the host.
- Spike S1 (`thoughts/shared/research/2026-06-04-s1-ca-trust.md`) — CA trust.
- `docs/cage-verification.md` — "Known limitations" item 1.

## Implementation Plan

## Notes & Updates

### 2026-06-08 — Done
Implemented Option A (single-file SBPL read-grant), chosen at the planning
checkpoint because the mitmproxy CA is a single global host-wide cert at a stable
path (not per-project), so a fixed literal-path grant needs no per-project logic and
the env vars stay unchanged. `profile.RenderCAReadFragment` emits `(allow file-read*
(literal <cert>))` + `(allow file-read-metadata (literal <dir>))`; `cage.Prepare`
writes it to `ca.sb` with the symlink-resolved CA path (macOS firmlinks), and
`cage.Build` appends it as a third `--append-profile`. The AC-0033 battery gained
`env-ca-node` + `env-ca-python` (both runtimes proven to execute in-cage).

Red→green proof on a real host: Phase-1 commit reproduced the failure
(`UNABLE_TO_VERIFY_LEAF_SIGNATURE` / `ERR`); Phase-2 commit turned it green (both →
200), all 18 vectors PASS, negative control still detects the escape, stable
`-count=2`. Empirically confirmed the CA private key remains unreadable in-cage.
Research: `thoughts/shared/research/2026-06-08-AC-0034-incage-ca-trust-env-files.md`;
plan: `thoughts/shared/plans/2026-06-08-AC-0034-incage-ca-trust-env-files.md`.

### 2026-06-08
Created from a finding surfaced by the AC-0033 cage-verification battery. Framed as
investigate-then-fix: the in-cage TLS failure for env-var-CA clients should be
reproduced first (the host-side S4 spike did not exercise it from inside the cage),
then fixed with the narrowest filesystem change that lets the caged process read
the one CA PEM, then guarded by an added AC-0033 vector. Complexity Medium: the
likely fix is small, but it needs a careful in-cage repro across the relevant
toolchains and a safehouse-aware decision on *how* to expose the CA.
