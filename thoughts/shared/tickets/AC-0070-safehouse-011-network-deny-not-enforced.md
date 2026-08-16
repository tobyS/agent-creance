# AC-0070: agent-safehouse 0.11.0 does not enforce the appended network deny-baseline — the cage's egress guarantee is void

**Status:** Rejected
**Estimated Complexity:** High
**Created:** 2026-07-13
**Updated:** 2026-08-16

> **Rejected 2026-08-16:** the agent-creance project was abandoned in favour of
> [nono](https://nono.sh). This security-critical defect is therefore **never
> fixed** — the cage's egress guarantee stays void on affected hosts. Do not
> rely on this project for isolation.

> **Security-critical.** On a host running agent-safehouse **0.11.0** (tested-against:
> **0.10.1**), the caged agent can open arbitrary outbound connections — raw TCP to
> the internet, direct DNS, non-allowlisted localhost ports — completely bypassing
> the mitmproxy egress filter. The proxy allowlist becomes advisory.

## Problem Statement

The cage's *hard* egress guarantee is kernel-level: `network.sb` emits
`(deny network*)` (`internal/profile/profile.go:47`) and re-opens only
`(allow network-outbound (remote tcp "localhost:<port>"))` per allowlisted port.
This fragment is handed to agent-safehouse via `--append-profile`
(`internal/cage/cage.go:143-150`). The env vars (`HTTPS_PROXY` etc.) are only the
*soft* half, for well-behaved clients; the deny-baseline is what stops a
prompt-injected agent from simply ignoring them.

**With safehouse 0.11.0 installed, that deny is not in force.** Discovered while
running `make test-integration` during AC-0069b (2026-07-13). Two integration
tests fail, and they fail for this reason:

- `internal/cage` — `TestLiveSafehouseEgressDenied`: "direct egress should be
  denied by the deny-all baseline; got: <nil>" (the connection succeeded).
- `internal/verify` — `TestCageVerificationBattery`: **ESCAPE DETECTED**. Five
  BLOCKED vectors leak, all of them network:

  ```
  [ESCAPE] net-raw-tcp       want=blocked  got=LEAK   (raw TCP to 1.1.1.1:443)
  [ESCAPE] net-dns           want=blocked  got=LEAK   (direct DNS to 8.8.8.8:53)
  [ESCAPE] net-localhost-v4  want=blocked  got=LEAK
  [ESCAPE] net-localhost-v6  want=blocked  got=LEAK
  [ESCAPE] net-child         want=blocked  got=LEAK
  ```

Every *filesystem* vector still passes (`fs-outside`, `fs-home-write`), as do the
proxy-side refusals and the new `broker-socket` vector. So the append-profile
mechanism is not wholly broken — the **network** denies specifically are not
taking effect.

## Evidence — the fault is in the safehouse composition, not our SBPL

The narrowest test isolates it. `internal/profile`'s live tests drive
`sandbox-exec` **directly** with our generated profile and **pass**
(`TestLiveLocalhostRefusal`: a non-allowlisted loopback port is refused over v4
and v6, an allowlisted one connects):

```
ok  github.com/tobyS/agent-creance/internal/profile   0.647s   # sandbox-exec directly
FAIL github.com/tobyS/agent-creance/internal/cage             # same SBPL, via safehouse
```

So the fragment we generate is correct and Seatbelt honours it. What has changed is
how **safehouse 0.11.0 composes `--append-profile` with its own base** — the
last-match-wins ordering our design depends on (see `docs/design.md`, and the
recorded S5/AC-0005 finding that our allows are disjoint from the base's denies).

Installed vs tested:

- `safehouse --version` → **Agent Safehouse 0.11.0**
- `internal/buildinfo.TestedVersions[ToolSafehouse]` → **0.10.1**

Both failures reproduce at `0d00e23` (before AC-0069b's implementation), so this is
not a regression introduced by that ticket — it was surfaced by it.

## Desired Outcome

- The root cause in safehouse 0.11.0 is identified: what changed in profile
  composition/ordering such that an appended `(deny network*)` no longer binds.
- The cage's kernel-level egress deny is enforced again on 0.11.0 — either by
  fixing how agent-creance emits/orders its fragments, or by pinning/requiring a
  safehouse version known to honour them, or upstream in safehouse.
- **Version skew of this kind is loud, not silent.** A safehouse whose profile
  composition we have not validated must not silently degrade the cage's core
  promise. `doctor` should refuse or warn severely, and the failure mode should be
  impossible to miss.
- The verification battery is what caught this; it should keep catching it.

## Acceptance Criteria

- [ ] Root cause of the 0.11.0 behaviour change is documented (what safehouse does
      differently with `--append-profile` / its network base).
- [ ] `TestLiveSafehouseEgressDenied` and `TestCageVerificationBattery` pass against
      the supported safehouse version, with no escapes.
- [ ] `internal/buildinfo.TestedVersions` names a safehouse version that genuinely
      enforces the deny-baseline, re-tested.
- [ ] An unvalidated/known-bad safehouse version is surfaced by `doctor` in a way
      that reflects the severity (the cage's egress guarantee is void, not merely
      "skewed").
- [ ] `make test-integration` green on a host with the supported safehouse.

## Out of Scope

- The credential broker (AC-0069b) and minted tokens (AC-0069a) — unrelated; the
  broker socket stayed unreachable even in the weakened cage (the filesystem layer
  held when the network layer did not), which is why the defence-in-depth is there.

## Open Questions

- Is this a safehouse **bug** (0.11.0 regression in append-profile composition) or a
  deliberate change in its base profile that agent-creance must now adapt to?
- Does 0.11.0 re-open network *after* our appended fragment (last-match-wins working
  against us), or does it not append our fragment at all in the network case?
- Should agent-creance hard-refuse to launch on an unvalidated safehouse version,
  rather than warn? A cage that silently does not filter egress is arguably worse
  than no cage, because the user believes they are protected.

## Questions for Research/Planning

- [ ] Diff safehouse 0.10.1 vs 0.11.0 profile generation; find the composition change.
- [ ] Determine whether the emitted `.sb` fragments still land in the final profile
      (dump the composed profile that 0.11.0 actually applies).
- [ ] Decide the doctor/prereq posture for unvalidated versions (warn vs refuse).

## References

- Discovered: 2026-07-13, during AC-0069b integration verification. See
  `thoughts/shared/plans/2026-07-13-AC-0069b-secret-broker.md`, Phase 5
  implementation log.
- Code: `internal/profile/profile.go:47` (`DenyBaseline`), `internal/cage/cage.go:143-150`
  (the `--append-profile` ordering), `internal/buildinfo` (tested versions).
- Tests: `internal/cage` `TestLiveSafehouseEgressDenied`; `internal/verify`
  `TestCageVerificationBattery` (the AC-0033 adversarial battery).
- Related: AC-0023 (append-profile ordering contract), AC-0033 (the battery),
  AC-0005/S5 (the "our allows are disjoint from safehouse's denies" finding).

## Implementation Plan

(Filled when planned.)

## Notes & Updates

### 2026-07-13
Filed from the AC-0069b integration run. The battery did exactly its job: it
detected the escape rather than rubber-stamping a broken cage.
