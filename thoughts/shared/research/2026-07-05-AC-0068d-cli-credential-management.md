---
date: 2026-07-05T00:00:00Z
git_commit: d3c4bc1612b179f79a6ef4f808050b1ed0097803
branch: main
repository: agent-creance
topic: "AC-0068d — CLI: credential management (add/list/rm) + --inject binding"
tags: [research, codebase, cli, credentials, config-mutation, hot-reload, help]
status: complete
last_updated: 2026-07-05
---

# Research: AC-0068d — CLI credential management and `--inject` binding

**Date**: 2026-07-05T00:00:00Z
**Git Commit**: d3c4bc1612b179f79a6ef4f808050b1ed0097803
**Branch**: main
**Repository**: agent-creance

## Research Question

How do we add an authoring surface for the AC-0068b credential-injection config
model — a `credential add/list/rm` command group and an `--inject`/`in-cage`
binding on `allow` — reusing the existing config-mutation, comment-preserving
write, and recompile + hot-reload machinery, with AC-0064-style help? And what
are the right answers to the two `Questions for Research/Planning` on the ticket:
(a) does `credential add` resolve the source at add-time or defer to spawn, and
(b) what is the flag surface for shape selection and the Basic username sentinel?

## Summary

AC-0068d is a **thin CLI layer over machinery that already exists**. Every piece
it needs — the config schema, the comment-preserving text-splice writers, the
shared read→edit→atomic-write→recompile skeleton, the config lock, live-cage
detection, cobra command groups, and the help conventions — is already in the
tree and well-tested. The work is authoring new cobra commands that build typed
values and route them through the established `internal/cli/mutate.go` skeleton,
plus **two genuinely new config-package writers** (for the `credentials:` block
and for rendering the auth axis on rules), and their tests.

Key findings:

- **The config model is fully in place (AC-0068b, Done).** `Config.Credentials
  map[string]Credential` and `Rule.Inject string` / `Rule.InCage bool` are
  shipped (`internal/config/config.go:35,104-112,137-142`). `credential add`
  writes a `Credential{Source,Header,Template,Username}` entry; `allow --inject
  <name>` writes `inject:` on a rule; "mark in-cage" writes `in_cage: true`.
  `DefaultCredentialHeader = "Authorization"` (`config.go:120-123`) is what lets
  `--bearer`/`--token` skip `--header`.

- **The shape flags are UX sugar over a `template:` string** (+ `header:` /
  `username:`). The value-template DSL (`internal/config/template.go`) fixes five
  forms with `{token}` and (Basic-only) `{user}` substitutions. So `--bearer` →
  `Bearer {token}`, `--token` → `token {token}`, bare → `{token}`, `--basic` →
  `Basic base64({user}:{token})`; `--header NAME` sets the target header
  orthogonally; `--username SENTINEL` sets the Basic sentinel. Config validation
  (`validateTemplate`, `validate.go:94`) already enforces "{user} ⟹ username
  non-empty", so the CLI leans on it rather than re-validating.

- **Add-time is a syntax check, not a resolve (resolves Question a).** AC-0068a
  shipped `sysdep.ValidateSecretRefSyntax` (`secretresolver.go:65-88`) — a pure,
  non-resolving scheme/shape check the config loader already uses. The discussion
  is unambiguous that *resolution* happens once, host-side, at proxy spawn, and
  explicitly rejected earlier/per-request resolution. So `credential add` should
  reuse `ValidateSecretRefSyntax` for a fast-fail on a malformed `--source` and
  defer any real `op read`/keychain resolve to spawn (where fail-closed + 472
  handle unavailability). A live add-time resolve would need host-side `op`,
  which breaks the dogfooding cage — at most an opt-in flag, not the default.

- **Undefined-binding rejection is nearly free.** `config.ValidateEffective`
  (`validate.go:119-158`) already makes an `inject` naming an undefined
  credential a hard `*ValidationError`, run by both the Loader and the compiler
  (`compile.go` `validateInjectRefs`). So `allow --inject bogus` fails at
  recompile with a clear message; the CLI can additionally pre-check for a
  friendlier error.

- **Two new config-package writers are required.** The existing text-splice
  writers (`internal/config/edit.go`, `remove.go`) cover `allow`/`deny_always`,
  `host_services`, `add_dirs_*`, `include:` — but **not** the `credentials:`
  map, and `renderRuleItem` (`edit.go:177-193`) does **not** emit `inject`/
  `in_cage`. AC-0068d must add a comment-preserving `AppendCredential`/
  `RemoveCredential` (a top-level *map* block, a new shape vs. the existing list
  writers) and extend rule rendering to carry the auth axis.

- **Binding `--inject` to an already-allowed host is the one real design
  gap.** Duplicate detection keys on host+sorted paths+sorted methods
  (`ruleIdentity`, `edit.go:271-273`) — mode/reason/auth excluded — so
  `allow api.github.com/graphql --inject github` on an *already-allowed*
  `/graphql` rule is reported as a no-op and the binding is silently dropped.
  The flagship use case (allowing `/graphql` *and* binding in one shot) works via
  the append path, but binding to a pre-existing rule needs a decision (see Open
  Questions).

- **Command groups and help have direct precedent.** `domain`/`service`/`mount`/
  `policy` are already parent-command-with-subcommands (`domain.go:36-47`, etc.),
  registered under groups in `newRootCmd` (`cli.go:171-195`). `credential
  add/list/rm` mirrors them exactly and joins the "Configure Egress & Cage"
  group. The one divergence: the codebase uses the verbs `add`/`remove` (never
  `rm`) and has no `list` verb inside a config-editing group — the ticket's `rm`
  and `list` are new (see Open Questions / Architecture).

- **Testing is a solved shape.** `allow_deny.txtar` / `domain.txtar` are the
  direct testscript templates (seed config → mutate → `grep` the changed file +
  `grep '# keep this comment'` for preservation → `policy show`/`explain` to
  confirm recompile). Go unit tests (`allow_test.go` `mutateFixture`,
  `service_test.go` seeded running-cage lock, `inject_test.go`
  `FakeSecretResolver`) complement for policy.json assertions and warning-hygiene.

## Detailed Findings

### 1. The config model the CLI authors (`internal/config`, AC-0068b — Done)

The schema the commands write is fully shipped:

- `Config.Credentials map[string]Credential` (`config.go:35`), mirrored in
  `rawConfig` with `yaml:"credentials"` (`config.go:153`), strict-decoded
  (`KnownFields(true)`), merged key-wise by `mergeCredentials` (over-wins), copied
  through `Parse` (`config.go:211`).
- `Credential{Source, Header, Template, Username string}` (`config.go:137-142`);
  an empty `Header` defaults to `DefaultCredentialHeader = "Authorization"` in
  `defaultCredentialHeaders` (`config.go:120-123, 262-269`).
- `Rule.Inject string` (`yaml:"inject"`) and `Rule.InCage bool`
  (`yaml:"in_cage"`) — the auth axis, orthogonal to `Mode` (`config.go:104-112`).
- The value-template (`template.go`): `RenderCredentialValue` and the unexported
  `validateTemplate` (`template.go:34,64`). Substitutions are exactly `{token}`
  and `{user}`; an optional single non-nested `base64(...)` wrapper. Five forms:
  `Bearer {token}`, `token {token}`, `{token}`, `Basic base64({user}:{token})`,
  and any custom string with `{token}` (custom header carried in `Header`, not the
  template).

### 2. Validation the CLI relies on (`internal/config/validate.go`)

- `validateCredentials` (`validate.go:74-101`): per-entry structural checks — a
  non-empty `source` that passes `sysdep.ValidateSecretRefSyntax` (`validate.go:89`),
  a `template` that passes `validateTemplate` (`validate.go:94`), and a sane
  `header` via `validateHeaderName` (`validate.go:97,106-117`).
- `validateRuleAuth` (`validate.go:61-68`): `inject`+`in_cage` mutually exclusive;
  `inject` on a `passthrough` rule is an error (a tunnel can't be injected).
  Note: `in_cage` on a `passthrough` rule is *valid*.
- `ValidateEffective` (`validate.go:119-158`): the cross-layer check — every
  `inject` must name a defined credential (hard error), and a defined-but-never-
  injected credential (or a username with no `{user}`) is a **non-fatal warning**
  (`Config.Warnings`, surfaced by the Loader). This is what makes
  `allow --inject <undefined>` fail closed at recompile.

### 3. The config-mutation command family and the shared skeleton (`internal/cli`)

The path a mutation follows, using `allow` as the exemplar:

- `newAllowCmd` (`allow.go:16-44`): `Use: "allow URL"`, `ExactArgs(1)`, bool flags
  `--once`/`--global`; `RunE` → `runAllow(ctx, app, ".", url, once, global)`.
- `runAllow` (`allow.go:50-58`): `splitURL` → `(host,path)`, builds
  `domainAddOpts`, delegates to `runDomainAdd` (`domain.go:106-185`), which
  validates flags/host/mode, builds the `config.Rule`, resolves the target file
  via `mutationTarget`, picks `AllowList`/`DenyList`, and calls
  `mutateAndRecompile` (`domain.go:184`).
- `mutateAndRecompile` (`mutate.go:81-84`) → `applyAndRecompile` (`mutate.go:93-129`),
  the core skeleton, run inside `withConfigLock` (`mutate.go:198-212`):
  ReadFile (absent → create) → `apply(data)` returning `(out,changed,err)` →
  unchanged prints "already … nothing to do" and returns → `MkdirAll` →
  `writeFileAtomic` (temp+rename, `init.go:362-372`) → `recompile(ctx,app,dir)`
  (`mutate.go:122,217-224`) → success line.
- **Target resolution** (`mutationTarget`, `mutate.go:58-75`): `--once` →
  out-of-tree session overlay (`layout.SessionOverlay()`); `--global` →
  `~/.config/agent-creance.yaml` (`load.go` `GlobalPath`); default → project
  `./.agent-creance.yaml`. Flag availability differs per command (`deny` has
  neither; `service`/`mount` are project-only).
- `recompile` (`mutate.go:217-224`) builds a silent `compile.New(...)` and calls
  `Compile(ctx, dir)` directly and synchronously — no reliance on the file
  watcher for command-driven edits.
- **Sibling skeleton** `applyAndWarn` (`mutate.go:138-171`): for edits that do
  *not* feed `policy.json` (host_services, mounts), it writes then *warns* if a
  cage is running (via `cageRunning`, `mutate.go:177-188`) that the change takes
  effect next `run`. Credential/inject edits feed `policy.json`, so they use
  `applyAndRecompile` (they hot-reload), not `applyAndWarn`.

### 4. Comment-preserving text-splice writers (`internal/config/edit.go`, `remove.go`)

- The design is a **surgical text splice, not a struct round-trip** (`edit.go:3-19`):
  parse only to *locate* an insertion/deletion point (yaml.Node carries
  Line/Column), splice rendered lines in as text, leave every other byte
  untouched. Every write is gated by a re-parse + byte-diff so a splice bug can
  never reach disk (`validateAppend`, `edit.go:232-257`; `validateRemoveRule`,
  `remove.go:104-123`).
- `AppendRule(src, list, rule)` (`edit.go:52-81`): duplicate check via
  `containsRule`/`ruleIdentity` (host + sorted paths + sorted methods — **mode,
  reason, and the auth axis excluded**, `edit.go:271-273`) → `planInsert`
  (`edit.go:86-129`) walks `network → egress → <list>` and synthesizes only the
  missing suffix at the right indent → splice → validate.
- `renderRuleItem` (`edit.go:177-193`) renders one rule as YAML: `- host:`,
  optional `paths:`/`methods:` (flow style), `mode:`, `reason:`. **It does NOT
  render `inject`/`in_cage`** — AC-0068d must extend it.
- `RemoveRule` (`remove.go:28-64`) is the reverse splice; removing an absent entry
  returns `config.ErrNotFound` (`errors.go:29-33`), never a silent no-op.
- **No writer exists for the top-level `credentials:` map** — the existing
  writers all target lists (`allow`/`deny_always`) or scalar-list sections. A
  map-of-objects block is a new rendering shape.

### 5. Recompile + hot-reload contract (AC-0053 — Done)

- Command mutations recompile **directly and synchronously** (`recompile`,
  `mutate.go:217-224`); the run-session `fsnotify` watcher
  (`internal/configwatch`) is only the *hand-edit* trigger and is not on the
  command path.
- Compiled `policy.json` lives out-of-tree at `<cache>/agent-creance/projects/
  <hash>/policy.json` (`state.go:246`, `PolicyJSON()`); `Compiler.write` writes
  temp+rename atomically (`compile.go:664-686`), and only rewrites on a real hash
  change (cache gate, `compile.go:261-286`).
- A running cage's Python enforcer hot-reloads by **polling `policy.json`'s mtime
  every 1s** (`enforcer.py:63,197-211`) — nothing signals it. Invalid edits keep
  last-good (compile only overwrites on success).
- The compiler already understands the auth axis: `mergeCredentials`
  (`compile.go:338`), `inject`/`in_cage` in `ruleKey` (`compile.go:646-662`),
  `validateInjectRefs` failing compile closed on a dangling `inject`
  (`compile.go:399-418`).
- Security note (AC-0053): a `config-ro.sb` Seatbelt fragment denies **in-cage
  write** of every resolved config file, so a prompt-injected agent cannot add or
  rebind credentials from inside the cage. The `credentials:` block (references
  only) is covered by this already.

### 6. cobra command groups + AC-0064 help-as-doc-surface (Done)

- Parent-with-subcommand precedent: `domain`/`service`/`mount` (each `add`/
  `remove`) and `policy` (`show`/`explain`/`refresh`). The parent has no `RunE`
  — just `Use`/`Short`/`Long` + `AddCommand` (`domain.go:36-47`, `service.go:21-32`,
  `mount.go:28`, `policy.go:27`).
- Registration in `newRootCmd` (`cli.go:109-200`): five group consts
  (`cli.go:94-100`), `cobra.EnableCommandSorting = false` (`cli.go:160`),
  `root.AddGroup(...)` titles (`cli.go:162-168`), a local `addCmd(cmd, group)`
  helper (`cli.go:171-174`). A top-level command **must** be added via `addCmd`
  with a valid `GroupID` or cobra panics. The Configure group holds `allow`,
  `deny`, `domain`, `service`, `mount`, `include`, `import` (`cli.go:182-188`) —
  where `credential` joins.
- Help convention: each leaf command sets, inline in its `cobra.Command` literal,
  a `Long:` (\n-joined string fragments, ~88 cols, explains behavior +
  preconditions/side effects) and an `Example:` (\n-joined; each invocation
  preceded by a `# comment`, two-space indented, blank line between examples,
  full `agent-creance <cmd> …` invocations). No shared helper, no heredoc — see
  `run.go:57-68`, `doctor.go:31-35`, `allow.go:21-33` for the exact style.
  Parent commands get a framing `Long:` and no `Example:`.
- Help is asserted by testscript (not golden): each mutation `.txtar` opens with a
  `--help` block asserting a distinctive `Long:` line, the literal `Examples:`
  heading, and one example invocation (`allow_deny.txtar:13-19`).

### 7. Interactive-or-flag convention (AC-0067 — Done)

- Explicit-or-prompt: a value supplied by flag runs non-interactively; omitted →
  an interactive prompt collects it, via the **hand-rolled** `internal/cli/prompt.go`
  (not bubbletea/survey). Non-TTY safety is mandatory: when a prompt is needed but
  stdin/stdout is not a terminal, the command fails with a clear hint naming the
  flags, and never hangs (`requireInteractive`, `prompt.go:23-28`;
  `domain.go:141-151` is the precedent for the `allow` paths-prompt).
- Removing a non-existent entry → clear error, non-zero exit (applies to
  `credential rm`).

### 8. `--json` output (AC-0066 — Done)

`--json` is scoped to named commands only (not automatic) and uses dedicated
string-valued output structs (int status → `ok/warn/problem`, empty arrays as
`[]`) — `internal/cli/policy.go` is the precedent. `credential list --json` would
be a deliberate add, not required by the ticket AC.

## Code References

- `internal/config/config.go:35,104-112,120-123,137-142,262-269` — `Credentials`
  map, `Rule.Inject/InCage`, `Credential`, `DefaultCredentialHeader`, header default.
- `internal/config/template.go:34,64` — `RenderCredentialValue`/`validateTemplate`
  (the shape spec the flags select).
- `internal/config/validate.go:61-68,74-101,119-158` — `validateRuleAuth`,
  `validateCredentials`, `ValidateEffective` (undefined-inject hard error,
  dangling-credential warning).
- `internal/sysdep/secretresolver.go:30-38,59-63,65-88` — `Resolve`,
  scheme constants, `ValidateSecretRefSyntax` (add-time syntax check).
- `internal/cli/allow.go:16-58`, `deny.go:14-50`, `domain.go:36-47,106-185` —
  command structure + shared `runDomainAdd`.
- `internal/cli/mutate.go:58-75,81-84,93-129,138-171,177-188,198-224` —
  `mutationTarget`, `mutateAndRecompile`, `applyAndRecompile`, `applyAndWarn`,
  `cageRunning`, `withConfigLock`, `recompile`.
- `internal/config/edit.go:3-19,52-81,86-129,177-193,232-257,271-273` — text-splice
  design, `AppendRule`, `planInsert`, `renderRuleItem` (no auth axis today),
  validation gate, `ruleIdentity`.
- `internal/config/remove.go:28-64,104-123,244-249` — `RemoveRule`, validation gate,
  splice.
- `internal/cli/cli.go:38-50,94-100,109-200,219-223` — `App` deps (incl. reserved
  `SecretResolver`), group consts, `newRootCmd` registration, prod wiring.
- `internal/cli/prompt.go:23-28` — `requireInteractive` (non-TTY guard).
- `internal/proxy/enforcer/enforcer.py:63,197-211` — 1s mtime-poll hot-reload.
- `internal/policy/compile/compile.go:261-286,338,399-418,646-662,664-686` —
  cache gate, `mergeCredentials`, `validateInjectRefs`, `ruleKey`, atomic write.
- `internal/state/state.go:246` — `PolicyJSON()` out-of-tree path.

### Test references

- `internal/cli/testdata/script/allow_deny.txtar`, `domain.txtar`,
  `import.txtar`, `include.txtar` — mutation `.txtar` templates (seed, mutate,
  `grep` file + comment preservation, `policy show`/`explain`, `--help` block).
- `internal/cli/script_test.go:26-57,66-132` — testscript harness, `$CREANCE_BIN`
  Setup, `seedaudit`/`seedlock` custom commands.
- `internal/cli/allow_test.go:22-108` — `mutateFixture`/`newMutateFixture`,
  `policyJSON` assertion (rule reaches `policy.json`).
- `internal/cli/service_test.go:61-73` — seeded running-cage lock (alive PID +
  listening port) for "write + warn" assertions.
- `internal/cli/inject_test.go` — `sysdeptest.NewFakeSecretResolver().WithSecret(...)`,
  warning-hygiene (token never leaks into warnings).
- `internal/config/edit_test.go`, `remove_test.go`, `credentials_test.go` — config
  writer + credential model tests.

## Architecture Documentation

- **Two orthogonal axes, one flat rule.** Transport (`Mode`) and auth
  (`Inject`/`InCage`) are independent flat fields; the CLI authors each with a
  distinct flag family.
- **Reference ≠ secret.** `credential add` only ever writes a *reference*
  (`op://…`), never a value. Resolution is host-side at spawn (AC-0068c). This is
  why add-time is a syntax check, not a resolve, and why the block is safe in a
  cage-mounted project config.
- **The mutation skeleton is the contract.** New commands should build an
  `apply func(src []byte) (out []byte, changed bool, err error)` closure and route
  it through `applyAndRecompile` (or a rule-append via `mutateAndRecompile`), for
  free config-lock serialization, comment-preserving atomic write, and direct
  recompile that the enforcer's mtime poll hot-reloads.
- **Verb naming divergence.** The codebase's config-editing groups use `add`/
  `remove` and have no `list` verb (read surface lives under `policy`/`status`).
  The ticket asks for `rm` and `list`. Codebase convention (AC-0067 kept legacy
  verbs as aliases) suggests `remove` primary with `rm` alias; `list` is
  genuinely new but harmless. This is a naming call, not a structural one.

## Impact Analysis

### Existing Usages Found
- `internal/config/edit.go:177-193` (`renderRuleItem`) — used by `AppendRule` for
  every `allow`/`deny` write. Extending it to emit `inject`/`in_cage` changes the
  rendered output for *any* rule that sets them (none today), so existing goldens
  are unaffected unless a fixture sets the auth axis.
- `internal/config/edit.go:271-273` (`ruleIdentity`) — used by duplicate detection
  in `AppendRule`. Auth fields are excluded, which is the root of the
  bind-to-existing-host gap.
- `internal/cli/mutate.go:93-129` (`applyAndRecompile`) — reused by `allow`/`deny`/
  `domain`/`include`; new commands become additional callers. No change to the
  skeleton itself is required.
- `internal/config/validate.go:119-158` (`ValidateEffective`) — already run by
  Loader + compiler; `allow --inject` inherits undefined-credential rejection.

### Current Contract
- `AppendRule(src, list, rule) (out, changed, err)` — appends one rule to a list,
  no-op if an identically-identified rule exists (identity excludes mode/reason/
  auth). Comment/format preserving, validated before return.
- `renderRuleItem(rule) []string` — renders host/paths/methods/mode/reason only.
- `mutationTarget/applyAndRecompile` — resolve target file, lock, splice, atomic
  write, recompile.

### Adaptation Requirements
- `internal/config/edit.go` — extend `renderRuleItem` to emit `inject:`/`in_cage:`
  when set; add `AppendCredential(src, name, cred)` (new top-level *map* block
  render) with its own validation gate.
- `internal/config/remove.go` — add `RemoveCredential(src, name)` mirroring
  `RemoveRule` (span-bounded splice, `ErrNotFound` when absent).
- `internal/cli/` — new `credential.go` (parent + `add`/`list`/`rm` subcommands)
  and `--inject`/`--in-cage` flags on `allow` (`allow.go`/`domain.go`).
- Possibly a decision on binding-to-existing-host (see Open Questions) that may
  require an update-in-place splice (`SetRuleAuth`) beyond append-only.

### Backward Compatibility Options
- Extending `renderRuleItem` is additive (fields omitted when empty) — no golden
  churn for existing fixtures.
- `AppendCredential`/`RemoveCredential` are new functions — no existing caller
  impact.

## Historical Context (from thoughts/)

- `thoughts/shared/discussions/2026-06-28-credential-injection.md:127-142,148-159,
  186-193,268` — the five value-template shapes and their real consumers; the
  "resolve host-side at spawn, never per-request" decision; the high-level CLI
  sketch (`credential` group + `allow --inject`, reuse recompile + hot-reload);
  the help/docs convention.
- `thoughts/shared/tickets/AC-0068d-cli-credential-management.md:66-72` — the two
  open questions (add-time resolve vs defer; shape-selection flag surface).
- `thoughts/shared/research/2026-07-02-AC-0068b-config-injection-model.md` and
  `thoughts/shared/plans/2026-07-02-AC-0068b-config-injection-model.md` — the
  config model the CLI authors (implemented).
- `thoughts/shared/plans/2026-07-03-AC-0068c-proxy-injection-engine.md` — what the
  injection engine consumes from the config the CLI writes (references at spawn,
  overwrite/fail-closed, 472).
- `thoughts/shared/tickets/AC-0064-help-as-doc-surface.md`,
  `AC-0053-config-hot-reload.md`, `AC-0067-config-editing-commands-tui.md`,
  `AC-0066-cli-ergonomics-bundle.md` — the help, hot-reload, interactive-or-flag,
  and `--json` conventions this ticket inherits.

## Related Research

- `thoughts/shared/research/2026-07-02-AC-0068b-config-injection-model.md`
- `thoughts/shared/research/2026-07-02-AC-0068c-proxy-injection-engine.md`
- `thoughts/shared/research/2026-06-29-AC-0068a-secretresolver-seam.md`
- `thoughts/shared/research/2026-06-28-AC-0064-help-as-doc-surface.md`

## Open Questions

These need a human call before planning; the first two are the ticket's own
`Questions for Research/Planning`.

1. **Shape-selection flag surface (ticket Question b).** The config fields are
   fixed, so this is purely the CLI UX. Recommendation: mutually-exclusive value
   flags `--bearer` (default header) / `--token` / `--raw` (bare `{token}`) /
   `--basic`, plus orthogonal `--header NAME` (custom header) and `--username
   SENTINEL` (Basic sentinel), with a `--template STRING` escape hatch for the
   full DSL. Alternative: only `--template STRING` (+ `--header`/`--username`),
   maximally general but less ergonomic and further from the user story's
   `--bearer`. Needs confirmation because it is user-facing API.

2. **Binding `--inject`/`--in-cage` to an already-allowed host.** Duplicate
   detection excludes the auth axis, so binding to a pre-existing identical
   host/path rule is currently a silent no-op. Options: (A) update-in-place —
   `allow <host/path> --inject x` updates the matching rule's auth axis if it
   exists, else appends (needs a new `SetRuleAuth` splice; best UX, matches the
   ticket's "writes an inject binding"); (B) append-only, and error if the rule
   already exists telling the user to remove+re-add (simplest, but poor for the
   flagship when the path is already allowed); (C) include the auth axis in rule
   identity (rejected — yields two conflicting rules for one host/path). Needs a
   call because it drives whether a new writer is required.

3. **Add-time source verification (ticket Question a).** Recommendation: always
   syntax-check `--source` via `ValidateSecretRefSyntax`; do **not** live-resolve
   at add-time in v1 (needs host-side `op`/keychain, breaks the dogfooding cage;
   spawn-time fail-closed + 472 already handle unavailability). An opt-in
   `--verify`/`--check` that actually resolves could be a later add. The docs
   favor defer-to-spawn; confirming because the ticket explicitly lists it.

Not blocking (proposed in-plan, no user input needed unless they want to weigh
in): verb naming (`remove` primary + `rm` alias, `list` new); whether
`credential list --json` ships in v1 (default: human table only, `--json`
deferred per AC-0066 scoping); whether `credential list` shows the resolved
header/template/shape (yes — name/source/shape, never a value); the exact
`credential add` interactive-prompt fallback for an omitted `--source`.

## tce Config Drift

None observed. `profile.md` and `tickets.md` match the codebase as encountered
during this research (Go 1.26 module, `internal/cli` composition root with `App`
+ per-command files, `internal/config` text-splice writers, `make test`/`make
golden` commands, tmt `AC-NNNN` tickets under `thoughts/shared/tickets/`).
