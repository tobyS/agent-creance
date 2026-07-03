# AC-0068c: Proxy injection engine — in-memory delivery, overwrite, fail-closed, 472

**Status:** Done
**Estimated Complexity:** High
**Created:** 2026-06-29
**Updated:** 2026-07-03

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

- [x] The proxy resolves the configured credential at spawn and delivers it to the
      addon over an inherited fd; the value never appears in argv, env, disk, or
      logs (asserted). — `SpawnWithSecret` (fd 3) + `resolveInjectionSecrets` (lazy,
      spawn-path only); hygiene asserted in the fake + resolver tests; real-fd delivery
      proven by the `/bin/sh` test.
- [x] On an inject-host, an agent-supplied `Authorization`/auth header is replaced
      by the injected value (test). — enforcer overwrite test (+ e2e integration).
- [x] A non-resolvable secret yields **472** and the request is not forwarded. —
      enforcer 472 test (+ e2e integration).
- [x] 472 is wired into the refusal machinery alongside 470/471 (reason phrase,
      `X-Cage-Reason`/body for header-visible clients), clear of ALB's 460/463/464. —
      `responses.injection_unavailable` + golden + tests.
- [x] An injected credential rejected upstream returns the upstream 401/403 plus
      `X-Cage-Injected: <name>`. — `responseheaders` annotation test (+ e2e integration).
- [x] On an `in-cage` host the proxy provably leaves auth headers untouched. —
      enforcer in-cage test.
- [x] Phantom priming via the `env:` block lets `gh` issue a request that the proxy
      then authenticates. — mechanism proven by the overwrite test (a client-supplied
      phantom header is clobbered); the config `env:` block already delivers the phantom
      with no new code; live `gh`+GraphQL is AC-0068e.
- [x] `SKILL.md` and `briefing.md` document 472 and the human-recoverable action. —
      plus the `docs/design.md` refusal taxonomy (per the planning checkpoint).
- [x] `make test` green; addon behavior covered by enforcer tests; secret-handling
      paths unit-tested with the AC-0068a fake. Real-tool paths behind `integration`
      (written; run out-of-cage via `make test-enforcer-integration`).

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

### 2026-07-03
Implemented in six phases (plan
`thoughts/shared/plans/2026-07-03-AC-0068c-proxy-injection-engine.md`): (1) a new
`ProcessManager.SpawnWithSecret` delivering the secret over inherited fd 3; (2)
`run.go` resolving injection secrets host-side lazily on the proxy spawn path (first
consumer of `App.SecretResolver`); (3) `policy.py` reading inject/in_cage/credentials
+ `inject.py` porting the value-template; (4) the enforcer's fd intake, request-hook
overwrite / in-cage / 472 and the `responseheaders` `X-Cage-Injected` annotation, plus
`responses.injection_unavailable` (472); (5) 472 + `X-Cage-Injected` documented in
`SKILL.md`, `briefing.md`, and the `docs/design.md` refusal taxonomy; (6) three
enforcer injection e2e integration probes.

Planning checkpoint decisions: add the 472 entry to `docs/design.md` now; deliver via
`ExtraFiles` (fd 3). Phantom priming needs no new code — the config `env:` block
already reaches the cage; the overwrite test proves a client-supplied phantom header
is clobbered.

Verified: in-cage gate green (`make test`, `make test-enforcer`, `make lint`,
`make build`); `make test-enforcer-integration` 15/15 including the 3 new injection
e2e tests against a real `mitmdump` fed a secret over a real fd. `make test-integration`
shows only pre-existing `kc-read`/`kc-write` battery failures — confirmed identical at
the pre-code commit, unrelated to this ticket (all proxy/credential vectors pass).
Status → Done.
