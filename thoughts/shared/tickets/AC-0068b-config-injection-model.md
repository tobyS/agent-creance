# AC-0068b: Config model — transport×auth two-axis, credentials indirection, value-template

**Status:** In Progress
**Estimated Complexity:** Medium
**Created:** 2026-06-29
**Updated:** 2026-07-02

> Sub-ticket of **AC-0068** (Credential injection, Phase 1). Pure schema +
> validation; no live injection. No dependencies (pairs with AC-0068a). Read the
> epic and research doc for context.

## Problem Statement

The egress config today has a single transport axis (`intercept | passthrough`). To
express credential injection declaratively, the config needs a second, orthogonal
auth-handling axis (meaningful only on intercepted hosts), a way to name and
reference credentials, and a value-template general enough to cover the common
header shapes — all without yet performing any injection.

## Desired Outcome

The config schema gains:

- An **auth axis** on intercepted hosts: `inject(<credential>) | in-cage | default`.
  - `inject` — proxy supplies the credential (overwrite + fail-closed; behavior in
    AC-0068c).
  - `in-cage` — proxy guarantees it never adds/strips/modifies any auth header; the
    honest, lint-able marker for cases injection structurally cannot serve (SigV4,
    `op`, SDK-minted) and a threat-model flag that a real credential lives in-cage.
  - `default` — today's behavior (proxy doesn't touch auth).
- A **`credentials:` indirection block** mapping a name → source reference (resolved
  via AC-0068a's `SecretResolver`) + header name + **value-template**:
  `Bearer {token}`, `token {token}`, bare `{token}`,
  `Basic base64({user}:{token})` with a username sentinel, and arbitrary custom
  header name.
- **Validation / lint:** a host marked credentialed must declare either `inject` or
  `in-cage` (warn/error on "neither"); `passthrough` + `inject` on the same host is
  rejected (Anthropic stays passthrough → any API key there is necessarily
  `in-cage`); an `inject` rule must reference a defined credential.

## Acceptance Criteria

- [ ] Config types in `internal/config` model the auth axis and the `credentials:`
      block; round-trip parse/serialize covered.
- [ ] The value-template supports Bearer / `token` / bare / Basic(+username
      sentinel) / custom-header forms; rendering is unit-tested (no real secret).
- [ ] Validation rejects: `passthrough`+`inject` on one host; `inject` referencing
      an undefined credential. It flags a credentialed host with neither `inject`
      nor `in-cage`.
- [ ] Policy compile/render (`internal/policy`) carries the new fields through to
      whatever the proxy reads; golden files updated and reviewed (`make golden`).
- [ ] No secret value appears in compiled `policy.json` (only the reference/name).
- [ ] `make test` green; schema changes table-driven + golden.

## Out of Scope

- Resolving secrets or performing injection (AC-0068c).
- CLI commands to author these (AC-0068d).
- Phantom-priming `env:` plumbing (AC-0068c).

## Open Questions

None blocking.

## Questions for Research/Planning

- [ ] Where the `credentials:` block lives given the cage mounts `./` read-write —
      main config vs. an out-of-tree sibling (cf. the gotcha that security-critical
      state is kept under `~/.cache/agent-creance/`). The compiled `policy.json`
      must carry only the reference, not the value.
- [ ] Whether `in-cage` is a per-host flag or a per-rule attribute, and how it
      interacts with existing `intercept`/`passthrough` precedence.

## References

- Epic: AC-0068. Research: `thoughts/shared/discussions/2026-06-28-credential-injection.md`
- Code: `internal/config/config.go` (`Egress`, `Rule`), `internal/policy/`
  (compile/render), `internal/proxy/enforcer/policy.py` (`Rule`, matcher).
- Envoy credential injector (overwrite / fail-closed vocabulary):
  https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/credential_injector_filter

## Implementation Plan

(Filled when planned.)

## Notes & Updates

### 2026-06-29
Created as the config-schema sub-ticket of AC-0068. Value-template generality is
intentional (Version A): one mechanism for all shapes, Bearer gold-plated later in
AC-0068e, custom-header/Basic present but not validated.
