---
date: 2026-07-02
ticket: AC-0068b
title: "Config model — transport×auth two-axis, credentials indirection, value-template"
status: ready
git_commit: a3f803c
branch: main
research: thoughts/shared/research/2026-07-02-AC-0068b-config-injection-model.md
---

# Implementation Plan: AC-0068b — Config injection model

## Overview

Add the credential-injection **schema** (no live injection) to `internal/config`
and carry it through the `internal/policy` compile/render pipeline into
`policy.json`:

1. A second, orthogonal **auth axis** on egress rules — two flat fields
   `inject: <credential-name>` and `in_cage: true` — alongside the existing
   transport axis (`mode: intercept | passthrough`).
2. A new top-level **`credentials:`** section mapping a name → a *source
   reference* (AC-0068a `op://`/`keychain://`/`env://`), a header name, a
   value-template, and an optional username sentinel.
3. **Validation**: local structural checks (per document) plus cross-reference
   checks (post-merge), including a new **non-fatal warning tier** in config
   validation.
4. **Value-template rendering** (`Bearer {token}`, `token {token}`, bare
   `{token}`, `Basic base64({user}:{token})`, custom header) as a pure, unit-tested
   spec function — no real secret.
5. Threading the new fields (rules + credentials) through compile → `policy.json`,
   with **no resolved secret value** in the artifact, golden files updated, and the
   Python enforcer confirmed to still load.

Nothing resolves a secret or injects anything (AC-0068c); no CLI authoring commands
(AC-0068d); no phantom-priming `env:` (AC-0068c).

### Decisions locked at the planning checkpoint

- **Credentials block home:** a top-level `credentials:` section in the normal
  config, layered by the existing global/project/include merge. It carries only
  references (never values); a user who prefers keeps it in the out-of-tree global
  config via the same layering.
- **Auth axis YAML shape:** two flat fields on the rule — `inject: <name>` (string)
  and `in_cage: true` (bool) — mirroring the flat `mode:` field.
- **The "flag neither" lint:** implemented via a **new warning tier** in config
  validation (non-fatal). Concretely, a defined-but-unreferenced credential is a
  warning; unresolvable structural problems and a bad `inject` reference stay hard
  errors.

## Current state

- `config.Rule` (`internal/config/config.go:89-95`) is a flat struct with
  `Host/Paths/Methods/Mode/Reason`; `Mode` is the transport axis, constants
  `ModeIntercept`/`ModePassthrough` (`config.go:97-101`), defaulted in
  `defaultRuleModes` (`config.go:206-212`), validated by a `switch` with a
  `default:` error (`validate.go:30-45`).
- `Config` (`config.go:29-35`) has an `Env map[string]string` top-level section;
  the map-merge precedent is `mergeEnv` (`merge.go:196-208`).
- `Parse` (`config.go:144-178`) validates **per document**; the `Loader.Load`
  (`load.go:48-71`) merges the implicit global + project (each fully
  include-resolved). There is **no post-merge validation step** today, and
  **no warning channel** — validation is hard-error-only via `*ValidationError`
  (`errors.go:39-54`).
- `policy.Rule` (`internal/policy/policy.go:78-86`) mirrors the config rule plus
  compiler annotations `Source`/`LowerTrust` that `Decide` **ignores**
  (`policy.go:76-77`). `RuleFromConfig` (`policy.go:216-229`) copies config→policy.
  `Compiled` (`policy.go:104-108`) embeds `RuleSet` (+ `Version`/`InputHash`).
- The compiler unions layers in `buildRuleSet` (`compile.go:481-502`), dedupes on
  `ruleKey = {Host,Paths,Methods,Mode,Reason}` (`compile.go:579-606`), and writes
  `policy.json` via `json.MarshalIndent` (`compile.go:610-630`). Compile golden:
  `internal/policy/compile/testdata/policy.golden`.
- Render human/JSON views: `internal/policy/render/render.go`; goldens in
  `internal/policy/render/testdata/`.
- The Python enforcer reads each rule field via `d.get(...)` and reads only
  `allow`/`deny_always` from the top level (`policy.py:73-96`), so unknown keys are
  ignored; the matcher (`decide`) is unaffected by non-matcher fields.

See the research doc for full file:line detail.

## Desired end state

- A config with a `credentials:` block and rules carrying `inject:`/`in_cage:`
  parses, merges across layers, validates, and compiles into a `policy.json` that
  carries the new per-rule fields and a top-level `credentials` map holding only
  references.
- The value-template renders all five shapes correctly under unit test with a
  placeholder token.
- Invalid configs fail closed with clear errors; a dangling credential produces a
  visible non-fatal warning.
- `make test` green; `make golden` diff reviewed; the Python enforcer loads a
  policy carrying the new fields without error.

### Verification (automated, from profile.md)

- `make test` (= `go test -race ./...`) green.
- `make test-enforcer` (Python enforcer unit tests incl. the new load test) green.
- `make lint` (= `go vet ./...` + `golangci-lint run`) clean; `make fmt` applied.
- `make golden` regenerates `policy.golden` + render goldens; diff reviewed and
  intentional.
- `make build` at the end so `bin/agent-creance` reflects the final commit.

## What we are NOT doing

- No secret resolution, no header injection, no overwrite/fail-closed, no 472, no
  phantom priming (AC-0068c).
- No CLI `credential add/list/rm` or `allow --inject` authoring (AC-0068d).
- No end-to-end GitHub validation, no `docs/design.md` credential-injection section
  (AC-0068e).
- No comment-preserving text-splice writers for the new fields (the `edit*.go`
  family) — that authoring surface is AC-0068d. Round-trip here means
  parse→struct→merge→compile, not surgical rewrite.

## Implementation approach

Bottom-up: schema types first, then the pure value-template, then validation
(local + the new post-merge/warning tier), then the policy pipeline + goldens +
enforcer load test. Each phase is independently testable and committed.

---

## Phase 1 — Config schema: types, parsing, merge

### Changes

**`internal/config/config.go`**
- Add two fields to `Rule` (keep `Reason` last):
  ```go
  Inject string `yaml:"inject"`  // auth axis: name of the credential to inject
  InCage bool   `yaml:"in_cage"` // auth axis: proxy guarantees it won't touch auth headers
  ```
- Add a `Credential` type (shared between raw and public trees, carrying yaml tags,
  like `Rule`/`Generator` — so `KnownFields(true)` strict-checks its keys):
  ```go
  // Credential is one entry in the top-level credentials: block: a named,
  // indirected reference the proxy will later resolve host-side and inject.
  // Source is an AC-0068a reference (op:// / keychain:// / env://); the resolved
  // value never appears here or in the compiled policy. Header defaults to
  // "Authorization". Template is the value-template (see template.go). Username is
  // the sentinel used only by the Basic base64({user}:{token}) form.
  type Credential struct {
      Source   string `yaml:"source"`
      Header   string `yaml:"header"`
      Template string `yaml:"template"`
      Username string `yaml:"username"`
  }
  ```
- Add `Credentials map[string]Credential` to `Config` (`config.go:29-35`) and to
  `rawConfig` (`config.go:106-112`) with `yaml:"credentials"`.
- In `Parse` (`config.go:157-168`), copy `raw.Credentials` across into the public
  `Config` (map copy, mirroring `Env`).
- Add a `DefaultCredentialHeader = "Authorization"` constant; apply it in
  `applyDefaults` (an empty `Header` on any credential becomes the default), so
  validation and downstream see a concrete header. Keep it a value default like
  `defaultRuleModes`.

**`internal/config/merge.go`**
- Add `mergeCredentials(base, over map[string]Credential) map[string]Credential`
  mirroring `mergeEnv` (`merge.go:196-208`): key-wise, over wins on collision,
  return `nil` when empty (keeps `reflect.DeepEqual` stable). Wire it into `merge`
  (`merge.go:20-41`). `Rule` dedupe (`dedupeRules`, `merge.go:167-191`) already uses
  `reflect.DeepEqual`, so the two new `Rule` fields are covered automatically.

### Tests
- `config_test.go`: parse a document with a `credentials:` block and rules carrying
  `inject`/`in_cage`; assert the resulting `Config` struct (including the defaulted
  header). Assert an unknown key inside a credential entry is rejected (KnownFields).
- `merge_test.go`: two layers, one defining a credential and another overriding it /
  adding another; assert key-wise merge and over-wins.

### Success criteria
- [x] `go test ./internal/config/...` green.
- [x] Round-trip parse of the new fields covered; strict unknown-key rejection
      inside `credentials:` covered.

---

## Phase 2 — Value-template rendering (pure)

### Changes

**`internal/config/template.go`** (new) — pure, no OS, no secret at rest:
- `RenderCredentialValue(template, username, token string) (string, error)`:
  1. Locate an optional single `base64( ... )` wrapper (balanced, non-nested).
  2. Substitute `{user}` → `username` and `{token}` → `token` (in the whole string,
     or inside the wrapper's inner expression).
  3. If wrapped, `base64.StdEncoding`-encode the substituted inner and splice it
     back into place. Introduces `encoding/base64` (first use in the Go tree).
- `validateTemplate(template, username string) error` (pure, used by Phase 3
  validation): the template must contain `{token}`; if it contains `{user}` then
  `username` must be non-empty; a `base64(` must have a matching `)` and no nesting;
  no unknown `{...}` placeholders other than `{user}`/`{token}`.

Keep format/marker pieces as named constants where helpful, per the codebase's
"named const so code and golden bytes agree" idiom. This Go renderer is the **spec**
for the value-template; AC-0068c ports the equivalent into the Python enforcer (the
runtime injector), mirroring the existing dual-implementation matcher pattern.

### Tests
- `template_test.go`: table-driven (`internal/prereq/version_test.go` shape,
  testify), rendering each shape with a **placeholder** token
  (`{token}`→`PLACEHOLDER`, `{user}`→`x-access-token`):
  - `Bearer {token}` → `Bearer PLACEHOLDER`
  - `token {token}` → `token PLACEHOLDER`
  - `{token}` → `PLACEHOLDER`
  - `Basic base64({user}:{token})` → `Basic ` + base64(`x-access-token:PLACEHOLDER`)
  - a custom form, e.g. `{token}` used with a custom header (header is a credential
    field, not part of the template — assert the value-only render).
- Error cases: missing `{token}`; `{user}` without username; unbalanced `base64(`.

### Success criteria
- [x] All five shapes render correctly with a placeholder; no real secret in tests.
- [x] `validateTemplate` rejects the malformed cases (table-driven).

---

## Phase 3 — Validation: local structural + cross-reference + warning tier

### Changes

**`internal/config/validate.go` (local, per-document, hard errors)**
- Extend `validateRules` (`validate.go:22-47`) per rule:
  - `Inject != "" && InCage` → `add("egress %s rule %s cannot set both inject and
    in_cage", …)`.
  - `Inject != "" && Mode == ModePassthrough` → error (passthrough tunnels raw
    bytes; the proxy can't inject). Note `in_cage` on a passthrough rule is **valid**
    (the discussion: an Anthropic API key on the passthrough host is "necessarily
    in-cage") — do not reject it.
- Add `validateCredentials(creds map[string]Credential, verr)`:
  - `source` required and non-empty; must be a **well-formed known-scheme
    reference**. Reuse a pure scheme check — add an exported pure helper in
    `internal/sysdep` (e.g. `func ValidSecretRefScheme(ref string) bool` or
    `ValidateSecretRefSyntax(ref) error`) that returns whether `ref` starts with a
    supported scheme and has a non-empty remainder, and call it from config. This
    keeps the scheme list authoritative in `sysdep` (the `ValidateHost`-reuse
    precedent) and does **not** resolve the secret.
  - `template` required; `validateTemplate(template, username)` from Phase 2.
  - `header` (post-default) must be a sane header token: non-empty, no control
    chars, no colon/whitespace (small local check; model on `ValidateHost`'s
    control-char rejection at `validate.go:151-177`).
  - `username` set but template has no `{user}` → warning (see below), not error.
- Call `validateCredentials` from `(*Config).validate` (`validate.go:14-17`).

**Cross-reference validation (post-merge) + warning tier**
- Add a warning channel to config validation. Minimal, low-ripple shape: a
  `Warnings []string` field on `Config`, plus an exported method
  ```go
  // ValidateEffective runs the cross-layer checks that only make sense on the
  // fully-merged config: every rule's inject must name a defined credential (hard
  // error), and every defined credential should be referenced by some inject
  // (warning). It returns warnings and, on a hard error, a *ValidationError.
  func (c *Config) ValidateEffective() (warnings []string, err error)
  ```
  - Hard error: an `inject` naming a credential absent from `c.Credentials`.
  - Warning: a `c.Credentials` entry never referenced by any `inject` across
    `Allow`+`DenyAlways` ("dangling credential %q is defined but never injected").
  - Also fold the "username without {user}" template warning here (or keep it in
    `validateCredentials` appending to a passed-in warning sink) — pick one warning
    accumulation path and keep it consistent.
- Introduce a tiny warning accumulator mirroring `ValidationError` but non-fatal
  (e.g. reuse a `[]string`), or add a `Warnings` slice to a shared validation
  context. Keep it simple: `ValidateEffective` returns `[]string`.

**`internal/config/load.go`**
- In `Load` (`load.go:48-71`), after `eff = merge(eff, *project)` and before
  returning, call `warnings, err := eff.ValidateEffective()`; on `err` return it
  (fail closed), else set `eff.Warnings = warnings`. This is the single place the
  effective global+project view exists.

**Surfacing warnings (make the tier visible)**
- Surface `cfg.Warnings` in the CLI where the effective config is loaded for a
  session. Primary surface: `internal/cli/doctor.go` (diagnostics) and the `run`
  startup path — print each warning to stderr with a clear `warning:` prefix before
  proceeding. Locate the exact load site during implementation (grep for
  `Loader{}`/`.Load(` in `internal/cli`). Keep it minimal: warnings never block.

### Tests
- `validate_test.go`: golden-error fixtures under `testdata/` for the hard errors —
  `inject_and_in_cage.yaml`, `passthrough_with_inject.yaml`,
  `credential_bad_source.yaml`, `credential_bad_template.yaml` — each with a
  `.golden` expected message (run `-update` to seed, review).
- A cross-layer test: credential defined in one layer, `inject` in another; assert
  `Load` accepts it (no false "undefined credential").
- A `ValidateEffective` unit test: undefined-inject → error; dangling credential →
  warning string present, no error.
- CLI: a testscript `.txtar` (or an existing doctor/run test) asserting a dangling
  credential prints the `warning:` line to stderr but the command still succeeds.

### Success criteria
- [ ] Hard errors reject: `inject`+`in_cage`; `passthrough`+`inject`; `inject` →
      undefined credential (post-merge); malformed `source`/`template`.
- [ ] `in_cage` on a passthrough rule is accepted.
- [ ] A dangling credential produces a non-fatal warning surfaced to stderr.
- [ ] Cross-layer `inject`→credential references validate correctly.

---

## Phase 4 — Policy pipeline: compile, render, golden, enforcer

### Changes

**`internal/policy/policy.go`**
- Add to `Rule` (`policy.go:78-86`), after `Reason`:
  ```go
  Inject string `json:"inject,omitempty"`
  InCage bool   `json:"in_cage,omitempty"`
  ```
  These join the annotation set **ignored by `Decide`** — extend the comment at
  `policy.go:76-77` to name them (they don't affect matching).
- `RuleFromConfig` (`policy.go:216-229`): copy `Inject`/`InCage` across.
- Add a compiled credential type + a `Credentials` field on `Compiled`
  (`policy.go:104-108`) so the artifact carries the block:
  ```go
  type Credential struct {
      Source   string `json:"source"`
      Header   string `json:"header,omitempty"`
      Template string `json:"template"`
      Username string `json:"username,omitempty"`
  }
  // Compiled gains:
  Credentials map[string]Credential `json:"credentials,omitempty"`
  ```
  A helper `CredentialsFromConfig(map[string]config.Credential) map[string]Credential`
  mirrors `RuleFromConfig`. **No resolved value** — only the reference/name.

**`internal/policy/compile/compile.go`**
- Thread the merged config `Credentials` into compilation. The compiler resolves
  layers via the Loader; collect `Credentials` from the same global+project layers
  (mirror how rules are gathered), merge them (reuse `config.mergeCredentials` or a
  local key-wise union), and set `compiled.Credentials`.
- Before `write`, run the cross-reference validation (reuse
  `config.Config.ValidateEffective`, or a shared helper) over the fused rule set +
  merged credentials so **policy generation fails closed** on an `inject` →
  undefined credential. (This is the compile-path counterpart to `Load`'s check.)
- `ruleKey`/`dedupe` (`compile.go:579-606`): add `Inject` and `InCage` to the key —
  two rules identical except for the auth axis are behaviorally distinct and must
  not collapse (unlike `Source`/`LowerTrust`).

**`internal/policy/render/render.go`**
- Human view: add an auth marker so operators see it — e.g. `renderAllow`/`markers`
  (`render.go:73-124, 328-337`) append `inject:<name>` or `in-cage` next to
  `mode`/passthrough markers. `ShowJSON`/`ExplainJSON` pick the fields up
  automatically via the embedded `policy.Rule`. Decide whether the top-level
  `credentials` map is shown in the human view (a short "Credentials:" section) or
  only in JSON — keep the human view minimal, JSON authoritative.

**Python enforcer (`internal/proxy/enforcer/`)**
- No behavior change needed: `Rule.from_dict`/`RuleSet.from_dict` ignore unknown
  keys (`policy.py:73-96`). Add a **load test** (`test_policy.py` or the existing
  policy test module) asserting a `policy.json` containing per-rule
  `inject`/`in_cage` and a top-level `credentials` map loads without error and does
  not alter `decide` outcomes (parity preserved). This locks in the
  "carries-through, ignored-by-matcher" contract.

### Tests / goldens
- Update the compile fixture `representativeFiles()`
  (`compile_test.go:77-88`) to include a `credentials:` block and an `inject` rule +
  an `in_cage` rule; run `make golden`; review `policy.golden` — assert it shows the
  reference (never a value) and the new per-rule fields.
- Update the render fixture (`render_test.go:24-43`) and regenerate the render
  goldens (plain + `_color`); review.
- `make test-enforcer` includes the new Python load test.

### Success criteria
- [ ] `policy.json` carries the top-level `credentials` map (references only) and
      per-rule `inject`/`in_cage`; **no secret value present** (asserted by golden).
- [ ] Compile fails closed when an `inject` names an undefined credential.
- [ ] `Decide` outcomes unchanged; the decision-vector corpus and matcher parity
      tests still pass (Go + Python).
- [ ] `make golden` diff reviewed and intentional; `make test` and
      `make test-enforcer` green.

---

## Testing strategy

- **Pure logic (value-template, reference-syntax, per-rule/credential validation)**
  → table-driven tests (testify), placeholder tokens only.
- **Config error messages** → golden-file tests (`testdata/*.yaml` + `*.golden`,
  `-update`).
- **Compile/render artifacts** → golden-file tests (`policy.golden`, render
  goldens), regenerated via `make golden`, diff reviewed.
- **Cross-language contract** → a Python enforcer load test + the existing Go/Python
  decision-vector parity (unaffected, must stay green).
- **CLI warning surface** → a hermetic testscript `.txtar` (stub tools on PATH,
  `$CREANCE_BIN`) asserting the `warning:` line on stderr with a clean exit.
- No external tools invoked in unit tests; enforcer tests run under `make
  test-enforcer` (repo-local venv), not the integration tag.

## Success criteria (ticket acceptance)

- [ ] Config types model the auth axis + `credentials:` block; round-trip
      parse/serialize (parse→struct→merge→compile) covered.
- [ ] Value-template supports Bearer / `token` / bare / Basic(+username) / custom;
      rendering unit-tested with no real secret.
- [ ] Validation rejects `passthrough`+`inject` and `inject`→undefined credential;
      flags a dangling credential via the warning tier.
- [ ] Policy compile/render carries the new fields through; goldens updated/reviewed.
- [ ] No secret value in compiled `policy.json` (only the reference/name).
- [ ] `make test` green; schema changes table-driven + golden; `make build` run at
      the end.

## References

- Ticket: `thoughts/shared/tickets/AC-0068b-config-injection-model.md`
- Research: `thoughts/shared/research/2026-07-02-AC-0068b-config-injection-model.md`
- Epic: `thoughts/shared/tickets/AC-0068-credential-injection-phase1-epic.md`
- Discussion: `thoughts/shared/discussions/2026-06-28-credential-injection.md`
- Pair ticket (references): AC-0068a — `internal/sysdep/secretresolver.go`
