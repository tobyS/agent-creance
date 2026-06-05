---
date: 2026-06-05
ticket: AC-0006
topic: "State directory & project identity (WP-1.1) — internal/state"
status: complete
branch: main
git_commit: b9ce051e0d8819157ef1e9bf023a8a52880d4fda
researcher: Claude (tce:work)
---

# AC-0006 — State directory & project identity (WP-1.1): Research

## Research question

Design and place a pure `internal/state` package that, given a project directory,
returns (a) a stable identity **hash** derived from the canonical absolute path and
(b) the fully-resolved out-of-tree **directory layout** under
`~/.cache/agent-creance/projects/<hash>/`, with symlinked aliases collapsing to one
identity. All filesystem access must go through a `sysdep` seam (no direct `os` in
logic). What are the existing patterns to follow, the open decisions, and the
constraints from the design and dependent tickets?

## Summary of findings

- This is a **pure-logic foundation ticket**: it computes paths and a hash; it does
  **not** create, read, or write any artifact (the compiler/proxy/audit own those —
  Out of Scope in the ticket).
- The identity scheme is fixed by the design: **canonical absolute path
  (`realpath`/`EvalSymlinks`) → deterministic `<hash>`**, shared by both the lock
  file and the state directory. Symlinked aliases must collapse to one hash; renamed/
  moved dirs are intentionally a different identity (`docs/design.md:396`).
- The state-dir layout and every artifact name are enumerated by the design:
  `policy.json`, `network.sb`, `proxy.lock`, `egress.jsonl`, `claude/` (a directory),
  and the **session-overlay** file (the design names the concept but not a filename —
  see Open Questions).
- **Its declared dependency, AC-0009 (the `sysdep` seam, WP-1.4), has NOT landed** —
  only `internal/sysdep/commander.go` exists. Critically, AC-0009's planned
  `FileSystem` interface is scoped to file **I/O** ("read/write/stat/mkdir/remove/
  rename") — **not** the path-canonicalization + cache-root derivation that
  `internal/state` needs. So the seam this ticket needs is a *different* concern from
  AC-0009's `FileSystem`. This is the main design decision (see Open Questions / the
  checkpoint).
- The two ticket open questions (hash choice/length; iCloud/SMB behaviour) have clear
  answers: the iCloud/SMB warning is explicitly **deferred to `doctor` (AC-0031)** by
  the ticket itself, so `internal/state` just resolves; the hash choice is a real
  decision with a recommended default below.

## Detailed findings

### 1. What the design mandates (identity + layout)

From `docs/design.md`:

- **Config compilation** (`docs/design.md:291`): compiled artifacts live in the
  project **state directory** `~/.cache/agent-creance/projects/<hash>/`, "where
  `<hash>` derives from the canonical project path (the same identity scheme the lock
  file uses)."
- **`network.sb`** (`docs/design.md:295`): a Seatbelt profile; **exempt from the
  input-hash cache** — regenerated each launch from the live proxy port. (Relevant to
  WP-2.5, but it confirms `network.sb` is a state-dir artifact this package must
  expose an accessor for.)
- **Multi-agent lifecycle / Project identity** (`docs/design.md:394`, `:396`): the
  lock file is `proxy.lock` in the state dir, **out-of-tree so the caged agent cannot
  corrupt the refcount**. "The 'project' a lock file belongs to is identified by the
  canonical absolute path of the project directory (`realpath` resolution). …
  symlinked aliases resolve to the same project … renamed/moved projects are seen as
  different projects." `doctor` warns about unreliable `flock` filesystems (iCloud/
  SMB) — i.e. **not this package's job**.
- **Audit log** (`docs/design.md:417`): `egress.jsonl`, mode `0600`, kept out-of-tree
  for integrity (the agent runs with `./` writable).
- **Ephemeral config redirect** (`docs/design.md:435`): `CLAUDE_CONFIG_DIR` →
  `~/.cache/agent-creance/projects/<hash>/claude/` (a directory, seeded + mounted RW).
- **Session-scoped allows** (`docs/design.md:302`–`:304`, `:423`): `allow --once`
  writes a **session-overlay file in the state directory**, unioned by the compiler
  like an `include:`d file, and **purged on last-agent-exit teardown**. The design
  does **not** give it a filename.
- **enforcer.py extraction** (`docs/design.md:450`): extracted to the state dir, but
  to a **constant** location (not per-project) — out of scope here.

The required accessor set (Acceptance Criteria) is therefore: `policy.json`,
`network.sb`, `proxy.lock`, `egress.jsonl`, `claude/` (dir), and the session-overlay
file — all rooted at `projects/<hash>/`.

### 2. The spec's framing (WP-1.1)

`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md:135-139`
describes WP-1.1 as: "Canonical absolute-path resolution (`realpath`), the
path→`<hash>` scheme shared by the lock file and state dir, and the directory layout
… Pure + filesystem seam. *Done when:* given a path, returns a stable hash + resolved
layout; symlinked aliases collapse to one identity; table-tested."

Cross-cutting concern **C4** (spec §4, `:108-110`): "Any WP that writes runtime state
asserts it lands under `~/.cache/agent-creance/projects/<hash>/` … never added to the
cage's mounted dirs." `internal/state` is the single source of truth that makes C4
checkable everywhere downstream (it is consumed by AC-0013, AC-0014, AC-0020,
AC-0021).

### 3. The seam situation (the crux)

- Only `internal/sysdep/commander.go` (the `Commander` interface) and its fake
  (`internal/sysdep/sysdeptest/fake.go`) exist today. `internal/state` does not exist.
- **AC-0009 (`sysdep` seam extensions, WP-1.4)** is the declared dependency. Its
  `FileSystem` AC reads "read/write/stat/mkdir/remove/rename as needed"
  (`AC-0009:25`) — pure **file I/O**. It also carries an unresolved open question:
  "Keep one fat `FileSystem` or several narrow interfaces defined at point of use (Go
  idiom favors the latter)?" (`AC-0009:49`).
- What `internal/state` actually needs from the OS is **not** file I/O. It is:
  1. **Canonical path resolution** — `filepath.EvalSymlinks` + absolute-path
     normalisation (resolving symlinks requires the dir to exist on disk → impure →
     must be behind a seam to stay table-testable, e.g. "link and target resolve to
     one hash").
  2. **Cache-root derivation** — `~/.cache/agent-creance`. Note the design uses the
     **XDG-style `~/.cache`**, *not* macOS `~/Library/Caches` (so `os.UserCacheDir()`
     is the wrong primitive). The global config is likewise `~/.config/agent-creance.
     yaml` (spec WP-1.3). Deriving this needs the home dir and possibly
     `XDG_CACHE_HOME` — i.e. env/home access, which the grep guard forbids importing
     `"os"` for.
- The ticket's grep guard (Verification #4):
  `! grep -rnE '"os"|os\.(Open|Stat|MkdirAll|ReadFile|WriteFile)' internal/state/*.go`
  → bans importing `"os"` at all. So `os.UserHomeDir`/`os.Getenv` **cannot** be called
  from `internal/state`; they must come through the seam. `path/filepath` is not
  banned by the grep, but AC #4 ("ALL filesystem access through the seam") means
  `EvalSymlinks` should also be routed through the seam for testability.

**Conclusion:** the seam `internal/state` needs (path canonicalisation + home/env for
cache root) is a *distinct concern* from AC-0009's file-I/O `FileSystem`. They don't
overlap, so there is no genuine blocking dependency — only a decision about **where**
this narrow seam lives. See Open Questions.

### 4. Existing patterns to mirror

- **Seam interface style** (`internal/sysdep/commander.go`): tiny interface,
  production impl as an empty struct, compile-time `var _ Iface = (*Impl)(nil)`
  assertion, heavy explanatory doc-comments aimed at a PHP/TS reader.
- **Fakes** (`internal/sysdep/sysdeptest/fake.go`): a scripted struct in the separate
  `sysdeptest` package (so other packages' tests can import it), map-backed, with a
  small builder helper and `New…` constructor. Mirrors `net/http/httptest`.
- **Table tests** (`internal/prereq/version_test.go`): slice-of-struct cases +
  `t.Run(tc.name, …)`. This is exactly the shape AC-0006's tests should take
  (symlink-collapse case, distinct-paths case, per-accessor rooting case).
- **Composition root** (`internal/cli/cli.go`): `App` holds injected deps
  (`Commander`, writers, `Tested`). If `internal/state` gets a constructor taking a
  seam, the real seam is wired here only when a *consumer command* needs it (AC-0009
  out-of-scope note: "no wiring into `cli.App` for unused deps").

### 5. Hash choice (ticket Open Question #1)

The design only requires "deterministic, collision-safe, short". Paths are not
adversary-controlled for the *directory-name* purpose (the cache root is out-of-tree).
Options:

- **SHA-256 truncated to 16 hex chars (64-bit)** — `crypto/sha256` of the canonical
  path string, `hex.EncodeToString(sum[:8])`. Negligible collision probability across
  any realistic number of projects; stdlib-only; the conventional choice for
  path→dir-name identity. **Recommended.**
- FNV-1a 64-bit (`hash/fnv`) — also stdlib, slightly shorter code, non-cryptographic;
  fine here but no real advantage and "looks" less robust.
- Shorter truncations (12 hex / 48-bit) — shorter dir names, marginally higher
  collision odds; unnecessary.

The scheme is effectively a one-way commitment (changing it later orphans existing
state dirs), but there are no real users pre-v0.1, so the cost of choosing now is low.

### 6. iCloud/SMB (ticket Open Question #2)

**Already answered by the ticket and design:** defer the warning to `doctor`
(AC-0031). `internal/state` performs no filesystem-reliability checks — it just
resolves the canonical path and returns the layout. No action here.

## Code references

- `docs/design.md:291` — state dir + `<hash>` from canonical path.
- `docs/design.md:295` — `network.sb` cache-exempt (artifact still in layout).
- `docs/design.md:394`,`:396` — `proxy.lock`, project identity = canonical path,
  symlink collapse, rename = new identity, iCloud/SMB → doctor.
- `docs/design.md:417` — `egress.jsonl` `0600`, out-of-tree rationale.
- `docs/design.md:435` — `claude/` config-redirect dir.
- `docs/design.md:302`-`304`,`:423` — session-overlay file, purged on teardown.
- `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md:135-139` —
  WP-1.1 definition; `:108-110` — C4 out-of-tree invariant.
- `internal/sysdep/commander.go` — seam interface idiom to mirror.
- `internal/sysdep/sysdeptest/fake.go` — fake idiom to mirror.
- `internal/prereq/version_test.go` — table-test idiom to mirror.
- `internal/cli/cli.go` — composition root (`App`).
- `thoughts/shared/tickets/AC-0009-sysdep-seam-extensions.md:25`,`:49` — AC-0009's
  `FileSystem` is file-I/O; "fat vs narrow" still open.

## Impact analysis

Direct consumers of `internal/state` (downstream tickets): policy compiler (AC-0013/
WP-2.4), profile compiler (AC-0014/WP-2.5), proxy lifecycle/lock (AC-0020/WP-3.4),
audit (AC-0021), cage config redirect (WP-4.2), session-overlay mutation (WP-6.1).
The **accessor names and the layout chosen here become the contract** for all of them,
so naming (esp. the session-overlay file) should be deliberate. No existing code
depends on `internal/state` yet (net-new package), so there is no regression surface
beyond keeping `make test` green.

## Open questions for the checkpoint

1. **Where does the path/cache seam live?** `internal/state` needs a *narrow*
   path-canonicalisation + cache-root seam, which is a different concern from
   AC-0009's file-I/O `FileSystem`. Options: (A, recommended) add a small,
   point-of-use seam to `internal/sysdep` now (real impl + fake), honouring AC #4's
   "through the `internal/sysdep` seam" literally and pre-seeding a slice of the seam
   work; (B) define the interface locally in `internal/state` (most idiomatic
   "interface at point of use") and rewire if AC-0009 later subsumes it. Both keep
   tests hermetic.
2. **Hash algorithm/length** — recommend **SHA-256 truncated to 16 hex chars
   (64-bit)**. Confirm or pick FNV-64 / a different length.
3. **Session-overlay filename** — the design names the concept, not the file.
   Recommend `session-overlay.yaml` (it's YAML, unioned like an `include:`). Low risk;
   will decide unless you have a preference.
