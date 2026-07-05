---
date: 2026-07-05
ticket: AC-0068d
title: "CLI — credential management (add/list/rm) + --inject / in-cage binding"
status: ready
git_commit: 62f40c9
branch: main
epic: AC-0068
depends_on: [AC-0068a, AC-0068b]
research: thoughts/shared/research/2026-07-05-AC-0068d-cli-credential-management.md
---

# AC-0068d — CLI credential management and `--inject` binding Implementation Plan

## Overview

Add the authoring surface for the AC-0068b credential-injection config model: a
`credential` command group (`add` / `list` / `remove`, `rm` alias) that manages
the top-level `credentials:` block, and an `--inject <name>` / `--in-cage`
binding on `allow` that writes the per-rule auth axis. Every mutation reuses the
existing comment-preserving text-splice writers and the `internal/cli/mutate.go`
recompile + hot-reload skeleton, so edits apply to a running cage without a
restart. Every new command carries AC-0064-style `Long:` + `Example:` help.

No secret is ever resolved or injected here — that is AC-0068c (already Done).
`credential add` writes only a *reference* (`op://…`) and validates its **syntax**
(not by resolving); resolution happens host-side at proxy spawn.

### Decisions locked at the planning checkpoint

- **Shape-selection flags:** convenience flags `--bearer` / `--token` / `--raw` /
  `--basic` (mutually exclusive), orthogonal `--header NAME` and `--username
  SENTINEL`, plus a `--template STRING` escape hatch for the full value-template
  DSL. `--bearer` is the default when no shape flag and no `--template` is given.
- **Binding to an already-allowed host:** **update in place.** `allow <host/path>
  --inject x` updates a matching rule's auth axis if the rule already exists, else
  appends a new rule. This needs a new `SetRuleAuth` splice writer because the
  existing `AppendRule` treats an identity match (host+paths+methods) as a no-op.
- **Add-time source check:** **syntax only** via `sysdep.ValidateSecretRefSyntax`;
  no live resolve at add-time (it would need host-side `op`/keychain and break the
  dogfooding cage; spawn-time fail-closed + 472 handle unavailability).

### Decisions taken in-plan (not needing checkpoint input)

- **Verbs:** `credential add` / `list` / `remove`, matching the codebase's
  `domain`/`service`/`mount` groups; `rm` is a cobra `Aliases` for `remove`
  (AC-0067 kept legacy verbs as aliases). `list` is new but harmless.
- **`--json`:** deferred. `credential list` prints a human table only in v1
  (AC-0066 scoped `--json` to named commands; adding it here is a later,
  deliberate step). Never prints a resolved value.
- **Targets:** `credential add`/`remove` and `allow --inject`/`--in-cage` support
  `--global` (credentials are commonly team-shared) and the project default. No
  `--once` for credentials (an ephemeral credential has no use).

## Current State Analysis

- The **config model is shipped** (AC-0068b, Done): `Config.Credentials
  map[string]Credential`, `Credential{Source,Header,Template,Username}`,
  `Rule.Inject`/`Rule.InCage`, `DefaultCredentialHeader = "Authorization"`
  (`internal/config/config.go:35,104-112,120-123,137-142`).
- **Validation is shipped**: `validateCredentials` + `validateRuleAuth` (per-doc)
  and `ValidateEffective` (cross-layer: undefined `inject` is a hard error,
  dangling credential is a warning) — `internal/config/validate.go:61-158`. The
  compiler runs the same via `validateInjectRefs` (`compile.go:399-418`), so a
  dangling `inject` fails compile closed.
- **Source syntax check is shipped**: `sysdep.ValidateSecretRefSyntax`
  (`internal/sysdep/secretresolver.go:65-88`) — pure, non-resolving.
- **The mutation skeleton is shipped**: `applyAndRecompile` (`mutate.go:93-129`,
  ReadFile→apply→atomic-write→`recompile`), `mutationTarget` (`mutate.go:58-75`,
  `--global`/project), `withConfigLock` (`mutate.go:198-212`), `recompile`
  (`mutate.go:217-224`). `mutateAndRecompile` (`mutate.go:81-84`) is the
  rule-append wrapper. Recompile writes `policy.json` atomically
  (`compile.go:664-686`); the enforcer hot-reloads on its 1s mtime poll.
- **The text-splice writers are shipped** but incomplete for this ticket:
  `AppendRule`/`renderRuleItem` (`edit.go:52-81,177-193`), `RemoveRule`
  (`remove.go:28-64`), with reusable helpers `egressListNode`, `spliceLines`,
  `maxLine`, the `validate*`-gate pattern, and `ruleIdentity`.
- **Command-group + help precedent is shipped**: `domain`/`service`/`mount`/
  `policy` parents (`domain.go:36-47` etc.), registered via `addCmd(cmd, group)`
  in `newRootCmd` (`cli.go:171-195`); `Long:`/`Example:` inline style
  (`allow.go:21-33`, `run.go:57-68`). Interactive-or-flag with non-TTY guard
  `requireInteractive` (`prompt.go:23-28`).

### What's missing (the actual work)

1. `renderRuleItem` does **not** emit `inject`/`in_cage` (`edit.go:177-193`).
2. No config writer for the top-level `credentials:` **map** block
   (append/remove) — all existing writers target lists/scalars.
3. No `SetRuleAuth` (upsert the auth axis on a possibly-existing rule).
4. No `credential` command group; no `--inject`/`--in-cage` flags on `allow`.

## Desired End State

- `agent-creance credential add github --source op://Private/GitHub/token
  --bearer` writes a `credentials.github` entry (reference + `Bearer {token}`
  template, `Authorization` header) into the project (or `--global`) config,
  preserving comments, and recompiles.
- `agent-creance credential list` prints configured credentials by
  name/source/shape (header + template), never a resolved value.
- `agent-creance credential remove github` (or `rm`) deletes the entry; removing
  an absent one errors non-zero; removing one still bound by an `inject` rule
  errors with a clear "unbind first" message.
- `agent-creance allow api.github.com/graphql --inject github` writes an inject
  binding — appending a new rule, or updating the matching rule's auth axis if
  `/graphql` is already allowed. `--in-cage` marks a host in-cage. Binding to an
  undefined credential is rejected before the write with a clear error.
- Every new command has `Long:` + `Example:` help; behavior is covered by
  hermetic `.txtar` scripts (stubbed/absent tools, no real `op`/network) and Go
  unit tests. `make test`, `make lint`, `make golden`, `make build` green.

### Verification (automated, from profile.md)

- `make test` (= `go test -race ./...`) green.
- `make lint` (= `go vet ./...` + `golangci-lint run`) clean; `make fmt` applied.
- `make golden` (= `go test ./... -update`) — review the diff (none expected
  from `renderRuleItem`, which the JSON compile/render goldens don't use).
- `make build` at the end so `bin/agent-creance` reflects the final commit.

## What We're NOT Doing

- No secret resolution, header injection, overwrite/fail-closed, 472, phantom
  priming — that is AC-0068c (Done).
- No opening of `/graphql`, no real GitHub end-to-end, no `docs/design.md`
  credential-injection section — that is AC-0068e.
- No minted/rotating tokens or broker config — Phase 2 (AC-0069).
- No `credential list --json` (deferred per AC-0066 scoping).
- No opt-in add-time `--verify`/resolve (would break the dogfooding cage; a later
  add if wanted).
- No `--once` (session-overlay) target for credentials.

## Implementation Approach

Bottom-up, each phase independently testable and committed: the pure config-package
writers first (Phase 1), then the read/write `credential` group (Phase 2), then the
`allow` binding that depends on the Phase 1 `SetRuleAuth` (Phase 3), then docs/help
polish + full verification (Phase 4). Every mutation routes through the existing
`applyAndRecompile` skeleton; the only new plumbing is the config-package writers
and the cobra commands.

---

## Phase 1: Config-package writers for credentials and the rule auth axis

### Overview

Extend the comment-preserving splice layer so it can render the auth axis on a
rule, upsert that axis on an existing rule, and append/remove entries in the
top-level `credentials:` map. Pure `internal/config` work, unit-tested; no CLI.

### Changes Required:

#### 1. Render the auth axis on a rule

**File**: `internal/config/edit.go`
**Changes**: Extend `renderRuleItem` (`:177-193`) to emit `inject:` and `in_cage:`
between `mode:` and `reason:`, matching the struct field order
(`config.go:104-112`). Empty/false are omitted.

```go
if rule.Mode != "" && rule.Mode != ModeIntercept {
    out = append(out, pad+"  mode: "+scalar(rule.Mode))
}
if rule.Inject != "" {
    out = append(out, pad+"  inject: "+scalar(rule.Inject))
}
if rule.InCage {
    out = append(out, pad+"  in_cage: true")
}
if rule.Reason != "" {
    out = append(out, pad+"  reason: "+strconv.Quote(rule.Reason))
}
```

Strengthen the `AppendRule` gate `validateAppend` (`:232-257`) to also assert the
appended rule carries the expected `Inject`/`InCage` (today it only checks
`Reason`), so a render bug can't silently drop a binding on the append path.

#### 2. Upsert the auth axis on a (possibly existing) rule

**File**: `internal/config/edit.go` (or a new `internal/config/setauth.go`)
**Changes**: Add `SetRuleAuth`. If no identity-matching rule exists it delegates to
`AppendRule` (which now renders the auth axis); if one exists, it re-renders that
rule's item in place (span-bounded splice, mirroring `RemoveRule`
`remove.go:48-63`), preserving host/paths/methods/mode/reason and overwriting the
auth axis. A no-op (existing auth axis already equals the requested one) returns
`(src, false, nil)`.

```go
// SetRuleAuth ensures the rule identified by host+paths+methods carries the auth
// axis (Inject/InCage) from rule. If no such rule exists it is appended; if it
// exists its inject/in_cage are updated in place (comment-preserving), and its
// reason is set when rule.Reason is non-empty. changed is false (out == src) when
// the target rule already has exactly this auth axis. Used by `allow --inject` /
// `--in-cage` so binding to an already-allowed host/path is not a silent no-op.
func SetRuleAuth(src []byte, list RuleList, rule Rule) (out []byte, changed bool, err error) {
    before, err := Parse(src)
    if err != nil {
        return nil, false, fmt.Errorf("read existing config: %w", err)
    }
    rules := listRules(before, list)
    idx := indexOfIdentity(rules, ruleIdentity(rule))
    if idx < 0 {
        return AppendRule(src, list, rule) // renders inject/in_cage now
    }

    existing := rules[idx]
    updated := existing
    updated.Inject, updated.InCage = rule.Inject, rule.InCage
    if rule.Reason != "" {
        updated.Reason = rule.Reason
    }
    if updated.Inject == existing.Inject && updated.InCage == existing.InCage &&
        updated.Reason == existing.Reason {
        return src, false, nil
    }

    var doc yaml.Node
    if err := yaml.Unmarshal(src, &doc); err != nil {
        return nil, false, fmt.Errorf("parse config for editing: %w", err)
    }
    listVal := egressListNode(&doc, list)
    if listVal == nil || listVal.Kind != yaml.SequenceNode || idx >= len(listVal.Content) {
        return nil, false, fmt.Errorf("config: internal: rule list node mismatch")
    }
    lines := strings.Split(string(src), "\n")
    item := listVal.Content[idx]
    start := item.Line - 1
    replacement := renderRuleItem(updated, leadingSpaces(lines[start]))
    candidate := []byte(strings.Join(spliceLines(lines, start, maxLine(item), replacement), "\n"))

    if err := validateSetRuleAuth(before, candidate, list, idx, updated); err != nil {
        return nil, false, err
    }
    return candidate, true, nil
}
```

`validateSetRuleAuth` re-parses and asserts: the other list unchanged
(`sameIdentities`), this list's identities unchanged, and the rule at `idx` now has
the expected `Inject`/`InCage`/`Reason`. Add a small `indexOfIdentity(rules,
id) int` helper (mirrors `containsRule`, `edit.go:284-292`).

#### 3. Append/remove entries in the top-level `credentials:` map

**File**: `internal/config/credentials_edit.go` (new)
**Changes**: `AppendCredential(src, name, cred)` and `RemoveCredential(src, name)`,
comment-preserving splices for a **mapping** block (a new shape — existing writers
target sequences). Reuse `rootMapping`/`mappingChild`/`endOfRegion`/`renderNested`
navigation and the re-parse validation-gate pattern.

- `AppendCredential`: if `name` already exists → `(src, false, nil)` (a no-op, or
  an error — pick no-op to match `AppendRule`; the CLI reports it). Render the
  entry under a top-level `credentials:` mapping (synthesize the `credentials:` key
  at column 0 if absent, at end of file, hugging content):

  ```yaml
  credentials:
    github:
      source: "op://Private/GitHub/token"
      template: "Bearer {token}"
  ```

  Emit `header:`/`username:` only when non-default/non-empty (an empty `Header`
  defaults to `Authorization` at parse). Gate: re-parse, assert
  `after.Credentials[name]` equals `cred` (post-default) and no other credential or
  either egress list changed.
- `RemoveCredential`: locate `credentials.<name>`'s key+value node span, splice it
  out (like `RemoveHostService` `remove.go:127-163`); `ErrNotFound` when absent.
  Add a `credentialsNode(doc)` navigator (mirrors `hostServicesNode`).

### Success Criteria:

#### Automated Verification:
- [x] `go test ./internal/config/...` green.
- [x] `make lint` clean; `go build ./...`.
- [x] `make golden` produces no unexpected diff (renderRuleItem is not used by the
      JSON compile/render goldens).

#### Manual Verification:
- [x] A round-trip test on a comment-rich fixture shows comments/blank lines
      preserved across `AppendCredential`, `RemoveCredential`, and `SetRuleAuth`
      (append + update-in-place branches).
- [x] `SetRuleAuth` on an already-allowed host updates the rule's `inject:` rather
      than no-op'ing; on a fresh host it appends with `inject:` rendered.

### Tests (Phase 1)
- `internal/config/edit_test.go`: `renderRuleItem`/`AppendRule` now render
  `inject`/`in_cage`; the strengthened gate rejects a dropped binding.
- New `internal/config/setauth_test.go`: append branch, update-in-place branch,
  no-op branch, comment preservation, validation-gate failure paths.
- New `internal/config/credentials_edit_test.go`: append into an absent vs present
  `credentials:` block, header/username omission, remove present/absent
  (`ErrNotFound`), comment preservation, cross-section no-change assertions.

---

## Phase 2: `credential` command group (add / list / remove)

### Overview

A cobra parent `credential` with `add`, `list`, and `remove` (`rm` alias)
subcommands, registered in the Configure group. `add`/`remove` route through the
mutate skeleton; `list` is read-only.

### Changes Required:

#### 1. The command group

**File**: `internal/cli/credential.go` (new)
**Changes**: `newCredentialCmd(app)` parent (no `RunE`, framing `Long:`,
`AddCommand(newCredentialAddCmd, newCredentialListCmd, newCredentialRemoveCmd)`),
mirroring `newDomainCmd` (`domain.go:36-47`). Register in `newRootCmd`:
`addCmd(newCredentialCmd(app), groupConfigure)` (`cli.go:182-188`).

#### 2. `credential add NAME`

**File**: `internal/cli/credential.go`
**Changes**: `Use: "add NAME"`, `ExactArgs(1)`. Flags: `--source REF` (string),
shape selectors `--bearer`/`--token`/`--raw`/`--basic` (bool, mutually exclusive),
`--header NAME` (string), `--username SENTINEL` (string), `--template STRING`
(escape hatch), `--global` (bool). Body `runCredentialAdd(ctx, app, dir, name,
opts)`:

1. Resolve the template from the shape flags (exactly one of the shape bools or
   `--template`; none → default `Bearer {token}`; more than one → error):
   `--bearer`→`Bearer {token}`, `--token`→`token {token}`, `--raw`→`{token}`,
   `--basic`→`Basic base64({user}:{token})`, `--template X`→`X`.
2. `--source` required (prompt via `requireInteractive`/`prompt.go` when omitted on
   a TTY; non-TTY → clear error naming `--source`). Validate syntax via
   `sysdep.ValidateSecretRefSyntax`; on failure, a clear error naming the accepted
   schemes (`op://`/`keychain://`/`env://`).
3. `--basic` requires `--username` (error early with a hint; `validateTemplate`
   also enforces `{user}` ⇒ username, but a CLI-level message is friendlier).
4. Build `config.Credential{Source, Header, Template, Username}` (leave `Header`
   empty to inherit `Authorization`; set it only for `--header`).
5. `mutationTarget(app, dir, false, opts.global)` for the file; route through
   `applyAndRecompile` with `apply = func(src) { return config.AppendCredential(src,
   name, cred) }`. `changed == false` → "credential %q already defined" (no-op).
   Success line via `app.OutStyle.OK`.

`Long:`/`Example:` per AC-0064 (examples: `--bearer`, `--basic --username
__token__`, `--header x-api-key --raw`, `--template` escape hatch).

#### 3. `credential list`

**File**: `internal/cli/credential.go`
**Changes**: `Use: "list"`, `NoArgs`. Body loads the effective merged config
(`config.NewLoader(app.FS, app.Paths).Load(dir)`) so it reflects global+project,
and prints a table: NAME, SOURCE (the reference, safe), HEADER (post-default),
SHAPE (the template, e.g. `Bearer {token}`), and USERNAME when set. **Never
resolves or prints a value.** Empty → a friendly "no credentials configured" line.
No `--json` in v1.

#### 4. `credential remove NAME` (alias `rm`)

**File**: `internal/cli/credential.go`
**Changes**: `Use: "remove NAME"`, `Aliases: []string{"rm"}`, `ExactArgs(1)`,
`--global`. Body `runCredentialRemove`:
1. Load the effective config; if any `allow`/`deny_always` rule injects `name`,
   error: "credential %q is still injected by <host>; unbind it first (allow
   <host> without --inject, or domain remove <host>)" — avoids a cryptic
   compile-time `validateInjectRefs` failure after the write.
2. Route through `applyAndRecompile` with `apply = RemoveCredential(src, name)`;
   `ErrNotFound` → "credential %q is not defined; nothing to remove" and exit
   non-zero (AC-0067).

### Success Criteria:

#### Automated Verification:
- [x] `make test` green (new `.txtar` + Go tests).
- [x] `make lint` clean; `go build ./...`.
- [x] `credential add/list/remove --help` assert `Examples:` + a distinctive
      `Long:` line (testscript).

#### Manual Verification:
- [x] `credential add … --bearer` then `credential list` shows the entry
      (name/source/shape), never a value; the config file gained a
      comment-preserved `credentials:` block; "policy recompiled".
- [x] `credential remove` of a bound credential is blocked with the unbind hint;
      of an absent one exits non-zero with a clear message.

### Tests (Phase 2)
- `internal/cli/testdata/script/credential.txtar`: seed a comment-rich config;
  `add --bearer` → `grep` the `credentials:` block + `# comment` preserved +
  `stdout` recompiled; `list` → `stdout` name/source/shape, `! stdout` any value;
  duplicate `add` → no-op message; `remove` → gone; `remove` absent → non-zero +
  message; `--global` lands in `$WORK/home/.config/agent-creance.yaml`; shape/flag
  rejections (`--bearer --basic` together, bad `--source`, `--basic` without
  `--username`) via `!` + `stderr`; a `--help` block. Env: `PATH=$CREANCE_BIN`
  (tools absent — no real `op`).
- `internal/cli/credential_test.go`: Go-level with `mutateFixture` —
  `AppendCredential` reaches `policy.json` (credentials map present, no value);
  remove-while-bound blocked; `list` output via a buffer; `sysdeptest`
  `FakeSecretResolver` not needed (no resolution).

---

## Phase 3: `allow --inject` / `--in-cage` binding

### Overview

Add the auth-axis flags to `allow` (and `domain add`, which shares
`runDomainAdd`), routing through the Phase 1 `SetRuleAuth` so binding updates an
existing rule or appends a new one.

### Changes Required:

#### 1. Flags + opts

**File**: `internal/cli/allow.go`, `internal/cli/domain.go`
**Changes**: Add `--inject NAME` (string) and `--in-cage` (bool) to `newAllowCmd`
(`allow.go:16-44`) and `newDomainAddCmd`. Thread onto `domainAddOpts`
(`domain.go`), set `rule.Inject`/`rule.InCage` in `runDomainAdd`
(`domain.go:157-174`).

#### 2. Validation + routing

**File**: `internal/cli/domain.go`
**Changes**: In `runDomainAdd`:
- Reject `--inject` with `--in-cage` (mutually exclusive), and `--inject`/`--in-cage`
  with `--deny` (the auth axis is meaningless on a hard-deny rule) — clear errors.
- When `--inject` is set, **pre-check the credential is defined** in the effective
  merged config (`config.NewLoader(...).Load(dir)`), because the target file may be
  project-only while the credential lives in `--global`. Undefined → error naming
  the fix ("run `agent-creance credential add <name> …`"). This satisfies the AC
  ("binding to an undefined credential is rejected with a clear error") *before*
  writing, rather than via a post-write compile failure.
- Route through `SetRuleAuth` instead of `AppendRule` **when an auth flag is set**;
  otherwise keep the existing `AppendRule` path (plain `allow` unchanged). Add a
  `mutateAuthAndRecompile` wrapper mirroring `mutateAndRecompile` (`mutate.go:81-84`)
  that plugs `SetRuleAuth` into `applyAndRecompile`. `changed == false` → an
  "already bound" style message.

#### 3. Help

**File**: `internal/cli/allow.go`
**Changes**: Extend `allow`'s `Long:`/`Example:` to document `--inject`/`--in-cage`
(the in-cage marker satisfies the AC's "documented" requirement), e.g. examples
for `allow api.github.com/graphql --inject github` and `allow <host> --in-cage`.

### Success Criteria:

#### Automated Verification:
- [x] `make test` green.
- [x] `make lint` clean; `go build ./...`.
- [x] `allow --help` asserts the `--inject`/`--in-cage` `Long:`/`Example:` lines.

#### Manual Verification:
- [x] `allow api.github.com/graphql --inject github` on a fresh path appends a rule
      with `inject:`; run again after the path is already allowed → the existing
      rule gains `inject:` (update-in-place, not a silent no-op).
- [x] `allow <host> --inject undefined` is rejected before writing with the
      "run credential add" hint; `--inject` + `--in-cage` and `--inject` + `--deny`
      are rejected.
- [x] `allow <host> --in-cage` writes `in_cage: true`; "policy recompiled".

### Tests (Phase 3)
- `internal/cli/testdata/script/allow_inject.txtar` (or fold into
  `allow_deny.txtar`): seed a config with a `credentials.github` entry; `allow
  api.github.com/graphql --inject github` → `grep 'inject: github'` + recompiled +
  `policy show` shows it; re-run on an already-allowed path → still one rule, now
  with `inject:` (update-in-place); `--inject undefined` → non-zero + hint;
  `--in-cage` → `grep 'in_cage: true'`; `--inject --in-cage` and `--inject --deny`
  rejected; `--help` asserts the new lines.
- `internal/cli/allow_test.go`: Go-level — `SetRuleAuth` append vs update reaches
  `policy.json` with the binding; undefined-credential pre-check errors without
  writing (assert the file is unchanged).

---

## Phase 4: Help/docs polish + final verification

### Overview

Ensure the new surface is consistently documented and the whole suite is green.
(No `docs/design.md` credential-injection section — that is AC-0068e.)

### Changes Required:

#### 1. Help coverage

**File**: `internal/cli/credential.go`, `internal/cli/allow.go`, testscripts
**Changes**: Confirm the `credential` parent has a framing `Long:` (no `Example:`,
per the parent-command convention) and each leaf has `Long:` + `Example:`; confirm
every new command's help is asserted by a `.txtar` (`Examples:` + a `Long:` anchor
+ one example invocation), matching `allow_deny.txtar:13-19`.

#### 2. Final verification

Run the full gate and review.

### Success Criteria:

#### Automated Verification:
- [x] `make test` green (race).
- [x] `make lint` clean; `make fmt` applied.
- [x] `make golden` diff reviewed and intentional (expected: none).
- [x] `make build` — `bin/agent-creance` reflects the final commit.

#### Manual Verification:
- [x] `agent-creance credential --help` lists `add`/`list`/`remove` under the
      Configure group; `agent-creance --help` shows `credential` in that group.
- [x] End-to-end by hand: `credential add github --source op://… --bearer` →
      `allow api.github.com/graphql --inject github` → `credential list` shows the
      binding target; config comments intact; no secret value printed anywhere.

---

## Testing Strategy

### Unit Tests:
- **Config writers (pure)** → `internal/config` tests: `renderRuleItem` auth-axis
  rendering; `SetRuleAuth` append/update/no-op + gate failures; `AppendCredential`/
  `RemoveCredential` present/absent/comment-preservation/cross-section-no-change.
  Table-driven where shapes vary; comment-rich fixtures for splice fidelity.
- **CLI (fakes)** → `internal/cli` Go tests with `mutateFixture`
  (`allow_test.go:22-108`): mutations reach `policy.json`; undefined-credential and
  remove-while-bound pre-checks error without writing; `list` output via a buffer.
  No `FakeSecretResolver` needed (no resolution in this ticket).

### Integration Tests:
- None. This ticket resolves and injects nothing; there is no real-tool path. (The
  end-to-end `gh`/GraphQL validation is AC-0068e, behind the integration tag.)

### Manual Testing Steps:
1. `credential add github --source op://Private/GitHub/token --bearer`; inspect the
   config — a comment-preserved `credentials:` block; `credential list` shows it,
   no value.
2. `allow api.github.com/graphql --inject github`; inspect — a rule with `inject:
   github`; `policy show` reflects it. Re-run → updates in place, not duplicated.
3. `credential remove github` while bound → blocked with unbind hint; unbind, then
   remove → gone, `policy recompiled`.
4. `allow x --inject nope` → rejected with the "credential add" hint; `--basic`
   without `--username` and `--inject --in-cage` → rejected.

## Performance Considerations

None. All operations are single-file splices plus one synchronous recompile,
identical in cost to the existing `allow`/`domain` commands.

## Migration Notes

None. Purely additive: new commands, new config-package functions, and additive
rendering of already-existing schema fields. Existing configs and goldens are
unaffected (fields render only when set).

## References

- Ticket: `thoughts/shared/tickets/AC-0068d-cli-credential-management.md`
- Research: `thoughts/shared/research/2026-07-05-AC-0068d-cli-credential-management.md`
- Epic: `thoughts/shared/tickets/AC-0068-credential-injection-phase1-epic.md`
- Config model (Done): `thoughts/shared/plans/2026-07-02-AC-0068b-config-injection-model.md`
- Injection engine (Done): `thoughts/shared/plans/2026-07-03-AC-0068c-proxy-injection-engine.md`
- Discussion: `thoughts/shared/discussions/2026-06-28-credential-injection.md`
- Splice writers: `internal/config/edit.go`, `internal/config/remove.go`
- Mutation skeleton: `internal/cli/mutate.go:58-224`
- Command group + help precedent: `internal/cli/domain.go`, `cli.go:171-195`,
  `allow.go:21-33`
- Source syntax check: `internal/sysdep/secretresolver.go:65-88`
- Cross-layer validation: `internal/config/validate.go:119-158`
