---
date: 2026-07-02
ticket: AC-0068b
title: "Config model — transport×auth two-axis, credentials indirection, value-template — research"
status: complete
git_commit: 223268501b86526f281cd8c3669a3660272ee7f9
branch: main
---

# Research: AC-0068b — Config model (transport×auth two-axis + `credentials:` + value-template)

## Research question

How do we extend the egress config schema (`internal/config`) with a second,
orthogonal **auth axis** on intercepted rules (`inject(<credential>) | in-cage |
default`), a top-level **`credentials:` indirection block** (name → source
reference + header + value-template), and the associated **validation/lint**,
and carry the new fields through the **policy compile/render** pipeline into
`policy.json` — all **without performing any injection** and without any secret
value ever touching the compiled artifact? What are the right answers to the two
`Questions for Research/Planning` (where the `credentials:` block lives; whether
`in-cage` is per-host or per-rule)?

## Summary

This is a **pure schema + validation + pipeline-plumbing** ticket. Every layer it
touches already has a precise, well-tested precedent — the work is almost entirely
"add fields alongside `Mode`/`Reason` and follow the existing conventions."

- **The auth axis is per-rule, modeled exactly like `Mode`.** `config.Rule`
  (`internal/config/config.go:89-95`) already carries the transport axis as a flat
  `Mode string` field validated by a `switch` with a `default:` error
  (`validate.go:30-45`). The auth axis (`inject`/`in-cage`) is a second flat
  per-rule attribute of the same kind — "meaningful only on intercepted hosts"
  maps to "meaningful only when `Mode == intercept`", a per-rule condition. There
  is no per-host object in this schema (rules are a flat list keyed by host
  pattern), so "per-host" isn't even representable; per-rule is the only option.
  (Resolves the ticket's second Question for Research/Planning.)

- **The `credentials:` block carries only *references*, never values — so it lives
  in the normal config as a new top-level section**, layered by the existing
  global/project/include merge machinery. A reference (`op://vault/item/field`) is
  a *pointer*, not a secret: it resolves to nothing without host-side 1Password /
  Keychain auth, which never exists in the cage. So the cage-mounts-`./`-read-write
  gotcha does **not** apply the way it does to compiled policy/audit state. A user
  who still doesn't want references in the repo can place the block in the
  out-of-tree global config (`~/.config/agent-creance.yaml`) — the layering already
  supports that for free, no new sibling file needed. The compiled `policy.json`
  (which is already out-of-tree in `~/.cache/agent-creance/`, not agent-readable)
  carries the reference too; the AC only forbids the resolved **value** there.
  (Resolves the ticket's first Question for Research/Planning — but see Open
  Questions: this is the one call worth confirming with the user given its security
  framing.)

- **The new per-rule fields are annotation-like — invisible to the matcher.**
  `inject`/`in-cage` change how an *already-allowed* request's auth header is
  handled; they never change the allow/deny decision. So they follow the exact
  precedent of `Source`/`LowerTrust` on `policy.Rule` (`policy.go:78-86`): carried
  in the JSON, **explicitly ignored by `Decide`** (`policy.go:76-77`). This means
  **no matcher change, no Python matcher change, and the cross-language
  decision-vector corpus stays valid** — the fields don't enter the decision.

- **The Python enforcer needs (at most) trivial change for AC-0068b.**
  `Rule.from_dict` reads each field via `d.get(...)` and `RuleSet.from_dict`
  reads only `allow`/`deny_always` (`policy.py:73-96`), so a new top-level
  `credentials` key and new per-rule keys are silently ignored — the enforcer
  keeps loading. AC-0068b can leave `policy.py`'s behavior untouched (consumption
  is AC-0068c); the plan should add a test asserting the enforcer still loads a
  policy carrying the new fields, to lock in that parity.

- **The value-template is `fmt`/`strings`-level string assembly, not a template
  engine.** The codebase uses no template library anywhere; it renders with
  `fmt.Sprintf` into a `strings.Builder` and keeps format pieces as named
  `const`s so code and golden bytes agree (`render.go:28-35, 309-337`). The five
  shapes (`Bearer {token}`, `token {token}`, bare `{token}`,
  `Basic base64({user}:{token})`, custom header) are a small render function over a
  credential entry. **`encoding/base64` is not yet imported anywhere** in the Go
  tree — the `Basic` form introduces it fresh.

- **Validation has an exact template but is error-only today — the "flag neither"
  lint needs a decision.** `internal/config` accumulates every problem as a hard
  error on a `*ValidationError` (`errors.go:39-54`, `validate.go`); there is **no
  warn tier**. Two of the three AC checks map cleanly onto hard errors
  (`passthrough`+`inject`; `inject` → undefined credential). The third — "flags a
  credentialed host with neither `inject` nor `in-cage`" — is genuinely ambiguous:
  config cannot generally know a host is "credentialed", and "flag" implies a warn
  the codebase has no channel for. This is the main design question for the user.

## Detailed findings

### 1. The config schema and where the new fields go (`internal/config`)

`internal/config` decodes `.agent-creance.yaml` into a typed `Config` via a
strict-decode-into-raw-mirror-then-convert pattern, applies defaults, then
cross-validates. No custom `UnmarshalYAML` anywhere (deliberate, so
`KnownFields(true)` strictness survives — `config.go:10-15, 103-105`).

**Top-level `Config`** (`config.go:29-35`): `Agent`, `Safehouse`, `Include`,
`Network`, `Env map[string]string`. Every section optional. A new top-level
`Credentials` section slots in here, mirrored on `rawConfig` (`config.go:106-112`)
with a `yaml:"credentials"` tag.

**The `Rule` type** (`config.go:89-95`) — the auth axis lands here:

```go
type Rule struct {
	Host    string    `yaml:"host"`
	Paths   *[]string `yaml:"paths"`
	Methods *[]string `yaml:"methods"`
	Mode    string    `yaml:"mode"`
	Reason  string    `yaml:"reason"`
}
```

`Paths`/`Methods` are pointers so an omitted key (`nil`) is distinguishable from an
explicit empty list — the passthrough validation depends on it (`config.go:85-88`).

**The transport axis is the flat `Mode string`**, constrained by two package
constants (`config.go:97-101`): `ModeIntercept = "intercept"`,
`ModePassthrough = "passthrough"`. Defaulted to `intercept` when empty in
`defaultRuleModes` (`config.go:206-212`), then validated by a `switch` with a
`default:` error (`validate.go:30-45`). **This is the exact template for the auth
axis.** There is no separate "transport" enum type; there is no per-host object —
rules are a flat `[]Rule` under `Egress.Allow` / `Egress.DenyAlways`
(`config.go:67-71`). So the auth axis must be per-rule.

**Parsing** (`Parse`, `config.go:144-178`): `yaml.NewDecoder` with
`KnownFields(true)` (unknown keys are errors, fails closed), decode into
`rawConfig`, convert to public types, `applyDefaults`, `validate`, return the
accumulated `*ValidationError` if non-empty. A new nested block gets strict-key
checking for free via `KnownFields`.

**`env:` block precedent** (`config.go:34, 111`): `map[string]string`, copied
across in `Parse` (`config.go:168`), merged key-wise by `mergeEnv`
(`merge.go:196-208`). This is the closest precedent for how a new
`map`-shaped top-level section (`credentials:` as name → entry) is threaded through
parse + merge.

**No credential/secret/auth field exists in the schema today** — all grep hits are
comments or fixtures. The `SecretResolver` seam from AC-0068a lives in
`internal/sysdep`, not the config schema.

### 2. Validation conventions (`internal/config/validate.go`, `errors.go`)

- **Errors are accumulated as data, never returned early.** `ValidationError{
  Issues []string }` with `add(format, args...)` and a stable, type-name-free
  `Error()` that renders `invalid .agent-creance.yaml:\n  - ...`
  (`errors.go:39-54`) — deliberately golden-testable.
- **Per-rule validation** (`validateRules`, `validate.go:22-47`): a `ruleRef(i, r)`
  helper names a rule by host-or-1-based-index; a `switch r.Mode` with a `default:`
  "unknown mode %q (want %q or %q)" message. The passthrough branch rejects a
  non-nil `Paths`/`Methods` with distinct messages (`validate.go:37-42`). The new
  auth-axis checks extend this same function.
- **Exported pure validators are reused by the compiler.** `ValidateHost`
  (`validate.go:151-177`) and `ValidateMethods` (`validate.go:181-194`) are
  exported precisely so `internal/policy/compile` can re-validate generator-emitted
  rules (`compile.go:533-538`). A new credential-reference validator (e.g.
  `ValidateCredentialRef` / a scheme check) should follow the same "export the pure
  validator for reuse" convention.
- **There is no warn tier.** Every problem in `internal/config` is a hard error.
  (A `Loud()`/severity idea exists in `internal/prereq`, `version_test.go:39-47`,
  but not in config.) The AC's "flags"/"warn/error on neither" language has no
  existing channel — see Open Questions.
- **Fixed-set string patterns available:** the untyped `const` group + `switch`
  default (the `Mode` axis — direct template for the auth axis), and the
  `map[string]bool` membership set (`knownMethods`, `validate.go:137-140`). The
  scheme-prefix dispatch (`secretresolver.go:59-63, 87-98`) is the model for
  validating a credential's `source:` reference.

### 3. Reference shape from AC-0068a (`internal/sysdep/secretresolver.go`)

The `credentials:` block's `source:` field names a reference the AC-0068a
`SecretResolver` resolves. Its interface: `Resolve(ctx, ref string) ([]byte,
error)` (`secretresolver.go:30-38`). Accepted schemes are **unexported** prefix
constants: `op://`, `keychain://`, `env://` (`secretresolver.go:59-63`).

**Key constraint for AC-0068b:** there is **no exported `ValidateSecretRef` /
`ParseSecretRef` / scheme-enum**. The only way to check "is this a well-formed,
known-scheme reference" today is to call `Resolve` and inspect for
`ErrUnknownSecretScheme` — but the config layer must **not** resolve (that's
host-side, and resolving in config would both be a layering violation and defeat
the "no value in the artifact" rule). So config-time validation of a `source:`
reference should be a **pure scheme/shape check** that does not resolve. Options:
(a) add a small exported pure helper in `sysdep` (e.g. `IsKnownSecretScheme(ref)`
or `ValidateSecretRefSyntax(ref)`) that the config validator reuses — DRY, keeps
the scheme list in one place; (b) duplicate a minimal prefix check in `config`.
Option (a) is more consistent with the `ValidateHost` reuse precedent. The plan
should pick one. (The `op://` grammar is validated by `op` itself at resolve time,
per AC-0068a research — config need only check the scheme prefix and non-empty
remainder.)

### 4. The compile/render pipeline — every layer a new per-rule field crosses

Config → in-memory policy → `policy.json` → Python enforcer. A new per-rule field
follows `Mode`/`Reason` through:

1. **`config.Rule`** (`config.go:89-95`) — add field(s) + yaml tag; extend
   `applyDefaults`/`validate` as needed.
2. **`policy.Rule`** (`policy.go:78-86`) — add field(s) + `json:` tag. `Decide`
   ignores annotation fields (`Source`/`LowerTrust` precedent, `policy.go:76-77`);
   the auth-axis fields join that ignored set (they don't affect matching).
3. **`RuleFromConfig`** (`policy.go:216-229`) — copy the new field(s) config→policy.
4. **Compile** (`internal/policy/compile/compile.go`): config-sourced rules flow
   through `annotate` → `RuleFromConfig` automatically (`compile.go:563-574`); only
   change needed if generators can set the field (they can't for auth — generators
   emit public-host allow rules, no credentials). **`dedupe`/`ruleKey`
   (`compile.go:579-606`)** currently keys on `{Host, Paths, Methods, Mode,
   Reason}` — a decision point: two rules differing only by `inject`/`in-cage` are
   behaviorally different, so the auth fields likely belong in the key (unlike
   `Source`/`LowerTrust`, which are excluded). Decide in plan.
5. **`policy.json` serialization** — driven purely by the `json:` tags; written by
   `Compiler.write` via `json.MarshalIndent` (`compile.go:610-630`). No code change
   beyond tags.
6. **Render** (`internal/policy/render/render.go`) — operator-facing `policy show`
   / `explain`. `ShowJSON`/`ExplainJSON` pick new fields up automatically via the
   embedded `policy.Rule`; the human text view (`renderAllow`/`markers`,
   `render.go:73-124, 328-337`) needs a small addition if we want the auth axis
   visible (e.g. an `inject:<name>` / `in-cage` marker) — nice-to-have, arguably in
   scope for "carries the new fields through".
7. **Python enforcer** (`policy.py`): `Rule.from_dict` / `RuleSet.from_dict` use
   `.get` and ignore unknown keys (`policy.py:73-96`) → no change required for
   AC-0068b; add a load test. The matcher (`decide`) is untouched because the
   fields aren't matcher-relevant, so the **decision-vector corpus stays valid**.

**The top-level `credentials:` block in `policy.json`:** it should serialize as a
normalized top-level map (name → {source, header, template, username?}) mirroring
the config, so the reference appears once and each `inject` rule references it by
name — matching the config's indirection. `policy.Compiled` (`policy.go:104-108`)
embeds `RuleSet` and adds `Version`/`InputHash`; a `Credentials` field added there
serializes alongside `allow`/`deny_always`. Whether the enforcer eventually reads
a top-level map or inlined per-rule copies is AC-0068c's call; AC-0068b just has
to emit it with no value and keep the golden stable.

### 5. Golden-file and table-test conventions

- **Golden tests** use a package-local `var update = flag.Bool("update", …)` and
  branch write-vs-compare; regenerate with **`make golden`** (= `go test ./...
  -update`). Config error goldens: `TestGoldenErrors` reads
  `testdata/<name>.yaml`, asserts `Parse` errors, compares `err.Error()` to
  `testdata/<name>.golden` (`validate_test.go:13-49`). New invalid configs
  (`passthrough_with_inject.yaml`, `inject_undefined_credential.yaml`, …) drop in
  there.
- **Compile golden:** `TestCompile_Golden` runs a real `Compiler.Compile` over
  in-memory fakes and compares to `internal/policy/compile/testdata/policy.golden`
  (`compile_test.go:114-142`). The fixture (`representativeFiles`,
  `compile_test.go:77-88`) is where a credential + `inject` rule gets added so the
  golden exercises the new path end-to-end.
- **Render golden:** `render_test.go` builds a `policy.Compiled` fixture
  (`render_test.go:24-43`) and `assertGolden`/`assertGoldenModes` compare
  plain+`_color` variants (`render_test.go:45-60`). Update the fixture if the human
  view shows the auth axis.
- **Table tests** for pure logic (value-template rendering, reference-syntax
  validation): the canonical shape is `internal/prereq/version_test.go:10-37`
  (anonymous struct slice + `t.Run`); the testify variant used in config is
  `validate_test.go:54-173` (`require.Error`/`require.Contains`); the
  `errors.Is`-sentinel variant is `secretresolver_test.go:13-45`. The
  value-template must be rendered with a **non-secret** placeholder token in tests
  (AC: "no real secret").

### 6. The value-template design space (from the discussion)

`thoughts/shared/discussions/2026-06-28-credential-injection.md:127-142` fixes the
five shapes and their real consumers:

- `Bearer {token}` — gold-plated v1 (GitHub, most APIs).
- `token {token}` and bare `{token}` — `gh` REST accepts `token`; crates.io / classic
  Linear use the bare token.
- `Basic base64({user}:{token})` with a **parameterized username sentinel** —
  git-over-HTTPS (`x-access-token`), PyPI (`__token__`), GitLab git (`oauth2`),
  Jira (email). The username is a per-credential constant → a `username:` field on
  the credential entry, used only by the Basic form.
- arbitrary **custom header name** — Anthropic `x-api-key`, Qdrant `api-key`,
  GitLab `PRIVATE-TOKEN`, etc. → the credential entry carries a `header:` field
  (default `Authorization`).

Bearer is validated end-to-end; the others are present via the template but not
gold-plated (Out of Scope in the epic). So a natural credential entry shape:

```yaml
credentials:
  github-token:
    source: op://Private/GitHub PAT/token   # AC-0068a reference
    header: Authorization                    # default; optional
    template: "Bearer {token}"               # one of the fixed forms
    # username: x-access-token               # only for Basic base64({user}:{token})
```

`{token}` (and `{user}` for Basic) are the only substitution tokens. This is a
proposal for the plan, not a settled schema — the exact field names/defaults and
whether `template` is a free string vs. an enum of known forms are plan-level
decisions (a constrained enum is easier to validate and matches the `Mode`
`const`-group precedent; a free string is more general but needs a
`{token}`-presence check).

## Code references

- `internal/config/config.go:29-35` — top-level `Config` (where `Credentials`
  slots in); `:67-71` `Egress`; `:89-101` `Rule` + `Mode` constants (auth-axis
  template); `:106-138` `rawConfig` mirror + yaml tags; `:144-178` `Parse`;
  `:183-212` `applyDefaults`/`defaultRuleModes`.
- `internal/config/validate.go:14-56` — validation entry + `validateRules` +
  `ruleRef`; `:137-140` `knownMethods` set; `:151-194` exported `ValidateHost`/
  `ValidateMethods` (reuse-across-layers precedent).
- `internal/config/errors.go:39-54` — `ValidationError` accumulator (no warn tier);
  `:57-80` `reformat` (strict-decode error mapping).
- `internal/config/merge.go:196-208` — `mergeEnv` (map-section merge precedent for
  `credentials:`).
- `internal/config/testdata/example.yaml` — canonical config; `validate_test.go`
  golden-error pattern.
- `internal/policy/policy.go:78-86` — `policy.Rule` (+ `Source`/`LowerTrust`
  annotation-ignored-by-`Decide` precedent at `:76-77`); `:104-108` `Compiled`
  (where a `Credentials` field lands); `:216-229` `RuleFromConfig`.
- `internal/policy/match.go:72-94` — `ruleMatches` (confirms auth fields aren't
  matcher-relevant).
- `internal/policy/compile/compile.go:563-574` `annotate`; `:579-606`
  `dedupe`/`ruleKey` (dedupe-key decision); `:610-630` `write` (json.MarshalIndent).
- `internal/policy/compile/compile_test.go:114-142` + `testdata/policy.golden` —
  compile golden.
- `internal/policy/render/render.go:73-124, 293-337` — human view + markers;
  `:62-68` `ShowJSON`; `render_test.go:24-60` render golden harness.
- `internal/proxy/enforcer/policy.py:58-96` — `Rule`/`RuleSet` `from_dict`
  (unknown-key tolerant → enforcer keeps loading); `:139-177` `decide` (untouched).
- `internal/sysdep/secretresolver.go:30-63, 87-98` — `SecretResolver` interface,
  scheme constants, dispatch (no exported syntax validator today).
- `internal/cli/cli.go:46-50, 219-223` — `App.SecretResolver` wired, no consumers
  yet (AC-0068c/d).

## Architecture insights

- **Two orthogonal axes, one flat rule.** Transport (`Mode`) and auth
  (`inject`/`in-cage`) are independent, and both live as flat fields on the same
  `Rule` — no nested per-host object exists or is warranted. The auth axis is
  "intercept-only" purely as a validation rule (`inject`/`in-cage` on a
  `passthrough` rule is an error), not as a structural nesting.
- **Reference ≠ secret.** The entire "where does it live" anxiety dissolves once
  you separate the reference (a non-secret pointer, safe anywhere config lives,
  safe in the out-of-tree `policy.json`) from the resolved value (never on disk,
  never in the artifact, only in proxy memory at inject time — AC-0068c). AC-0068b
  only ever handles references.
- **Annotation-not-decision.** Adding auth fields as ignored-by-`Decide`
  annotations (the `Source`/`LowerTrust` pattern) is what keeps the dual-language
  matcher and its decision-vector corpus untouched — a big simplification and a
  strong argument for that modeling.
- **Fail-closed lineage.** Strict `KnownFields` decoding + accumulate-all-errors
  validation means a malformed credential block or a bad `inject` reference stops
  the config from loading rather than silently degrading — consistent with the
  project's fail-closed posture, and the right home for the two hard-error checks.

## Open Questions

These shape the schema and are worth resolving with the user before planning. (1)
and (3) are genuine judgment calls; (2) has a strong recommendation but is
user-facing config API worth confirming.

1. **Where does the `credentials:` block live?** Recommendation: a new top-level
   `credentials:` section in the normal config, layered by existing merge; it
   carries only references, so it is safe in a cage-mounted project config, and a
   user who prefers can put it in the out-of-tree global config via the existing
   layering (no new sibling file). The compiled `policy.json` (already out-of-tree)
   carries the reference, never the value. *Confirm because the ticket explicitly
   defers this and it has a security framing.*

2. **How is the auth axis modeled in YAML?** Recommendation: two flat optional
   fields on the rule mirroring the flat `mode` field — `inject: <credential-name>`
   (string) and `in_cage: true` (bool) — validated mutually exclusive and
   intercept-only. Alternative: a single heterogeneous `auth:` field (scalar
   `in-cage` / mapping `{inject: name}`), which is more "one axis, one key" but
   needs scalar-or-mapping decoding (the generators `[]yaml.Node` pattern) and is
   harder to validate/golden. The two-flat-fields shape is simpler and is the
   surface AC-0068d's CLI will author.

3. **What does "flags a credentialed host with neither `inject` nor `in-cage`"
   mean, and is it a warn or an error?** Config cannot generally know a host is
   "credentialed", and `internal/config` has **no warn tier** (error-only today).
   Sub-options to decide: (a) reinterpret as a hard error on a *dangling
   credential* — a `credentials:` entry never referenced by any `inject` rule; (b)
   defer this lint entirely to AC-0068c/d where injection context exists, keeping
   AC-0068b to the two unambiguous hard errors; (c) introduce a real warning
   channel in config validation (new mechanism) and emit a non-fatal warning. This
   is the least-determined part of the ticket and needs a human call.

Not blocking (proposed in-plan, no user input needed unless they want to weigh in):
the exact `credentials:` entry field names/defaults (`source`/`header`/`template`/
`username`), whether `template` is a constrained enum of the five known forms vs. a
free string with a `{token}`-presence check, whether the auth fields enter the
compile `dedupe` key, and whether the human `policy show` view renders the auth
axis.

## Related decisions from the epic / discussion

- `thoughts/shared/discussions/2026-06-28-credential-injection.md:96-142` — the
  agreed "Version A" shape: two orthogonal axes, one value-template for all shapes,
  Bearer+GitHub as the single validated flagship; custom-header/Basic present but
  not gold-plated.
- `AC-0068` epic, "Out of Scope (Phase 1)": gold-plating custom-header/Basic is
  deferred; SigV4/`op`/SDK-minted are declared `in-cage`, not injectable.
- AC-0068a research (`thoughts/shared/research/2026-06-29-AC-0068a-secretresolver-seam.md`)
  — the reference grammar and the "resolve once host-side, never per-request, never
  on disk" decisions that AC-0068b's references feed into.
- Ticket Out of Scope: no resolution/injection (AC-0068c), no CLI authoring
  (AC-0068d), no phantom-priming `env:` plumbing (AC-0068c).

## tce config drift

None observed. `profile.md` and `tickets.md` match the codebase (Go 1.26 module,
`internal/config` + `internal/policy` layout, `make test`/`make golden` commands,
tmt `AC-NNNN` tickets under `thoughts/shared/tickets/`) as encountered during this
research.
