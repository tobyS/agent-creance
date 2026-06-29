# AC-0068c: Proxy injection engine — in-memory delivery, overwrite, fail-closed, 472

**Status:** Open
**Estimated Complexity:** High
**Created:** 2026-06-29
**Updated:** 2026-06-29

> Sub-ticket of **AC-0068** (Credential injection, Phase 1). The core mechanism and
> the only place real secrets flow. **Depends on AC-0068a + AC-0068b.** Read the
> epic and research doc for context.

## Problem Statement

With the resolver (AC-0068a) and config model (AC-0068b) in place, the proxy must
actually inject the credential: resolve host-side at spawn, deliver the value to the
`mitmdump` addon without exposing it on disk/argv/env, overwrite any
client-supplied auth header on inject-hosts, fail closed if the secret won't
resolve, and prime clients that refuse to send a request without a credential. When
injection fails, body-blind clients (WebFetch discards body and headers) must still
see an actionable signal — requiring a distinct status code.

## Desired Outcome

- **Resolve in Go at proxy spawn** (via AC-0068a) and hand the value to the
  `mitmdump` addon over an **inherited file descriptor** (`exec.Cmd.ExtraFiles` /
  stdin) — never argv (visible in `ps`), never env (`KERN_PROCARGS2`/`ps -E`
  readable by same-uid processes), never disk. The addon holds it in memory for the
  proxy's lifetime.
- **Overwrite** the auth header on inject-hosts: even if a prompt-injected agent
  reads a broad token and sets the header itself, the proxy clobbers it with the
  scoped one for that host. The cage cannot exceed the injected scope against that
  host.
- **Fail-closed:** if the secret won't resolve, deny the request rather than
  forwarding unauthenticated or with the phantom.
- **Phantom-token priming:** the cage receives a non-secret placeholder (e.g.
  `GH_TOKEN=<phantom>` via the config `env:` block) so clients like `gh` will send a
  request; the proxy overwrites it. The phantom may need a plausible shape if the
  client format-checks.
- **`in-cage` honored:** on `in-cage` hosts the proxy never adds/strips/modifies any
  auth header — only host/path/method enforcement.
- **Status 472 = injection-unavailable:** a genuine third refusal category —
  allowlisted, transient, **recoverable by the human, not the agent** — distinct
  from 470 (agent-recoverable via `allow`) and 471 (permanent). Returned on
  fail-closed. `SKILL.md` and `internal/cage/briefing.md` updated so the agent tells
  the user to unlock the secret store rather than try `allow`.
- **Upstream rejection annotation:** when an injected credential is rejected upstream
  (real 401/403), annotate the response (e.g. `X-Cage-Injected: <name>`) rather than
  inventing a status — the upstream owns that status — so the agent blames the
  injected credential, not its phantom.

## Acceptance Criteria

- [ ] The proxy resolves the configured credential at spawn and delivers it to the
      addon over an inherited fd; the value never appears in argv, env, disk, or
      logs (asserted).
- [ ] On an inject-host, an agent-supplied `Authorization`/auth header is replaced
      by the injected value (test).
- [ ] A non-resolvable secret yields **472** and the request is not forwarded.
- [ ] 472 is wired into the refusal machinery alongside 470/471 (reason phrase,
      `X-Cage-Reason`/body for header-visible clients), clear of ALB's 460/463/464.
- [ ] An injected credential rejected upstream returns the upstream 401/403 plus
      `X-Cage-Injected: <name>`.
- [ ] On an `in-cage` host the proxy provably leaves auth headers untouched.
- [ ] Phantom priming via the `env:` block lets `gh` issue a request that the proxy
      then authenticates.
- [ ] `SKILL.md` and `briefing.md` document 472 and the human-recoverable action.
- [ ] `make test` green; addon behavior covered by enforcer tests; secret-handling
      paths unit-tested with the AC-0068a fake. Real-tool paths behind `integration`.

## Out of Scope

- CLI to author credentials/bindings (AC-0068d).
- Opening `/graphql` and GitHub end-to-end validation (AC-0068e).
- Unix-socket broker / Go-side zeroization (Phase 2, AC-0069b) — Python holds the
  secret in memory in v1 (accepted trade-off).
- Minted/rotating tokens (Phase 2, AC-0069a).

## Open Questions

None blocking.

## Questions for Research/Planning

- [ ] Exact phantom shape `gh` accepts (and whether `GH_TOKEN` vs `GITHUB_TOKEN`).
- [ ] How the addon reads the inherited fd under `mitmdump`'s process model, and the
      lifetime/cleanup of the in-memory secret.
- [ ] Reuse of the existing refusal-rendering path (cf. AC-0047, AC-0050) for 472.
- [ ] Null-byte / normalized-host hardening on the matcher (the SOCKS5 bypass
      lesson) — confirm it already applies to injected hosts.

## References

- Epic: AC-0068. Research: `thoughts/shared/discussions/2026-06-28-credential-injection.md`
- Code: `internal/proxy/enforcer/enforcer.py` (request hook), `policy.py`,
  `internal/proxy/lifecycle.go` (`mitmArgs`, spawn), `internal/cred/`
  (status-as-data), `internal/setup/SKILL.md`, `internal/cage/briefing.md`,
  `docs/design.md` (286–327 refusals).
- Related: AC-0047 (WebFetch visible refusals), AC-0050 (refusal reason phrases),
  AC-0057 (stream proxied responses).
- nono credential injection (phantom token, header-strip):
  https://nono.sh/blog/blog-credential-injection
- macOS env exposure (`KERN_PROCARGS2`): https://getargv.narzt.cam/
- IMDSv2 SSRF lesson (no in-cage token endpoint):
  https://aws.amazon.com/blogs/security/defense-in-depth-open-firewalls-reverse-proxies-ssrf-vulnerabilities-ec2-instance-metadata-service/

## Implementation Plan

(Filled when planned.)

## Notes & Updates

### 2026-06-29
Created as the core-mechanism sub-ticket of AC-0068. 472 and the SKILL/briefing
updates live here (not a separate ticket) because the desired agent action only
becomes meaningful once injection can fail.
