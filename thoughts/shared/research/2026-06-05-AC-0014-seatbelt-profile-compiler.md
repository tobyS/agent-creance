---
date: 2026-06-05
researcher: Tobias Schlitt
ticket: AC-0014
topic: "Seatbelt profile compiler → network.sb (WP-2.5)"
status: complete
branch: main
git_commit: 25a447cb17ac6f8238871cc758aa30c4a9d1e830
source_design: docs/design.md
spec: thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md
gates: [AC-0003 (S3, resolved), AC-0005 (S5, resolved)]
---

# Research — AC-0014: Seatbelt profile compiler → `network.sb` (WP-2.5)

## Research question

Build `internal/profile`: a compiler that generates the cage's network Seatbelt
profile — a `(deny network*)` baseline plus narrow per-port allow rules for
`network.host_services`, **plus** a separately-generated launch-time proxy-port
fragment supplied with the live (ephemeral) port. What exactly must the generated
SBPL look like, how does it compose with Safehouse, how do we mirror the AC-0013
compiler/cache/golden patterns, and what does the shipped self-test entail?

## Summary / headline findings

1. **Both spike gates are RESOLVED and clear to build.** S3 (AC-0003, resolved
   2026-06-04) and S5 (AC-0005, resolved 2026-06-05) both passed. The ticket's
   "do not merge enforcement before both resolve" gate is satisfied.

2. **The ticket's central premise is OVERTURNED by the spikes — use `localhost:<port>`,
   not `127.0.0.1:<port>`.** The literal-IP rule form the ticket/design/spec all
   assume (`(remote tcp "127.0.0.1:<port>")`) **does not compile** on macOS 26.5:
   `sandbox-exec: host must be * or localhost in network address`. The only buildable
   host tokens are `*` and `localhost`. `localhost` inherently spans IPv4 (`127.0.0.1`)
   **and** IPv6 (`::1`), and the **port** is the discriminator. So:
   - The compiler MUST emit `(remote tcp "localhost:<port>")`.
   - It MUST translate any loopback literal in config (`127.0.0.1`, `::1`, `localhost`)
     to the `localhost` token keyed on the port.
   - It MUST **never** emit `*:<port>` for a host service (that permits external egress
     on that port — `localhost` ≠ `*`; external hosts stay refused, verified in S3 §3).
   - "Force IPv4" is both impossible (no family-specific rule compiles) and unnecessary
     (the port guarantee is family-agnostic; a non-allowlisted port is EPERM-refused
     over both v4 and v6). See S3 §1–2.

3. **`network.sb` is an `--append-profile` FRAGMENT, not a standalone whole profile.**
   S5 validated end-to-end that a fragment of `(deny network*)` + per-port
   `(allow network* (remote tcp "localhost:N"))`, fed to `safehouse --append-profile`,
   lands *after* Safehouse's base `(allow network*)` and narrows it (arbitrary egress
   EPERM-refused, DNS denied, proxy + host-service ports reachable, real request HTTP
   200 through the proxy). Consequences for the artifact:
   - **No `(version 1)` / `(deny default)` header** — Safehouse's base already declares
     those; the fragment is appended text.
   - Order is load-bearing: `(deny network*)` **first**, then the specific allows.
   - **Seatbelt on this compile path is last-match-wins** (not first-match — see the
     "Conflicting evidence" note below), which is *why* a late `(deny network*)`
     overrides the base allow and the subsequent specific allows reopen only the named
     ports.
   - We do **not** need to re-state Safehouse's own `network-outbound` denies
     (docker/ssh sockets) — they're `unix-socket` remotes, disjoint from our
     `tcp localhost:N` allows.
   - The wholesale-generated-profile fallback is **not** built (S5 PASS).

4. **The proxy-port allow is a separate, uncached, launch-time fragment.** The proxy
   port is ephemeral (`:0`-allocated per run; see design.md:295, :411), so it cannot be
   baked into the config-hash-cached artifact. AC-0014 provides a pure function that
   renders the proxy fragment from a supplied port. The cached `network.sb` carries the
   deny baseline + host-service allows; the proxy allow is generated fresh each launch.

5. **The closest template is `internal/policy/compile` (AC-0013).** Mirror its
   `Compiler`/`New`/`Compile`/`Result`, SHA-256 `inputHash` cache, tolerant
   `readCompiled`, atomic out-of-tree `write` (tmp+rename under `layout.Root`), and
   golden-test harness (`-update` flag with the exact string `"regenerate golden files"`
   that the Makefile greps for). The output path already exists: `state.Layout.NetworkSB()`
   → `<state-root>/network.sb`.

6. **The shipped self-test (per S3) — scope is a checkpoint question.** S3's decision
   text says AC-0014 ships the localhost-refusal self-test "run on `setup` and every
   `doctor`, uncached." But the ticket's own verification step 5 frames it as a *gated
   integration test* (`make test-integration` drives the compiled profile through
   `sandbox-exec`, reusing AC-0003's probes). Wiring a live self-test into `doctor`
   pulls in App seams and a real-tool invocation that overlap with AC-0023/AC-0033.
   See Open Questions.

7. **AC-0014 owns design.md + spec corrections.** Both spikes explicitly defer the
   literal-IP wording corrections to "the gated build tickets" (= AC-0014). The
   affected spots are catalogued below.

## Spike decisions that constrain this ticket (authoritative)

### S3 / AC-0003 — localhost port filtering across address families
`thoughts/shared/research/2026-06-04-s3-localhost-ports.md`

- Literal IPs don't compile; only `*` and `localhost` host tokens are valid.
- `(deny network*)` + `(allow network-outbound (remote tcp "localhost:<allowed>"))`:
  allowed port reachable over v4 **and** v6; every other port EPERM-refused over both.
- `localhost` ≠ `*`: external hosts stay refused; never emit `*:N`.
- `localhost` scope = all of this machine's addresses (loopback + LAN interface IPs),
  not loopback-only — intra-machine widening, never external. Flag in the threat-model
  note. (The only way to scope strictly to loopback is at the app layer — bind the
  service to `127.0.0.1`.)
- **Self-test design** (S3 §"Reusable self-test"): bind a *non-allowlisted* loopback
  port on both families; inside the cage, connecting to it must EPERM-fail on v4 and v6,
  while the allowlisted proxy port succeeds. Distinguish EPERM (enforcement) from
  ECONNREFUSED (closed port) — only EPERM is a true PASS.
- Decision on cadence: run on `setup` and every `doctor`, uncached (no v0.1 build-version
  cache). TCP only; UDP/bind out of scope.

### S5 / AC-0005 — appended-profile network narrowing
`thoughts/shared/research/2026-06-05-s5-append-profile.md`

- **v0.1 ships `--append-profile`, not a fully-generated profile.** No reshape of AC-0014.
- Validated fragment form:
  ```scheme
  (deny network*)
  (allow network* (remote tcp "localhost:18081"))   ;; proxy port
  (allow network* (remote tcp "localhost:13306"))   ;; host-service port
  ```
- The binary is `safehouse` (self-named "Agent Safehouse 0.10.1"); flag is
  `--append-profile PATH`, "appended after generated rules; repeatable; in argument
  order." Repeatable ⇒ the cached `network.sb` and the launch-time proxy fragment can be
  two separate files passed in order.
- Confirmed last-match-wins: the fragment's late `(deny network*)` overrides the base
  `(allow network*)` at line 313, and specific allows reopen named ports. Safehouse's own
  base already relies on deny-after-allow.
- No need to re-state Safehouse's docker/ssh socket denies.
- Fallback escape hatch exists if append ordering ever breaks: `safehouse --output/--stdout`
  + `sandbox-exec -f` — left unbuilt.
- Inbound/`network-bind` not exercised; the fragment's `(deny network*)` closes inbound.
  If v0.1 ever needs the caged agent to *listen*, that's a separate allow (flagged here,
  out of scope now).

### Conflicting evidence — trust the spikes, not the general web reference
A web search of the classic SBPL literature (fG!'s Sandbox Guide, derived from 10.6.8)
claims (a) Seatbelt is **first-match-wins** so a later allow can't widen an earlier deny,
and (b) there is no `--append-profile` on `sandbox-exec`. **Both are stale/irrelevant
here.** `--append-profile` is a **Safehouse** flag (not `sandbox-exec`), and the S5 spike
*empirically* demonstrated **last-match-wins** on the real macOS 26.5 + Safehouse 0.10.1
compile path (the fragment's trailing `(deny network*)` overrode the base allow, and
Safehouse's own policy depends on deny-after-allow). The spikes are authoritative for
this environment; the web reference is useful only for token *syntax* background. The
web reference's one genuinely useful caveat: SBPL gives `tcp4`/`tcp6` family tokens, but
S3 already showed family-pinning is unnecessary, so we stick with `tcp` + `localhost`.

## What the generated artifacts must look like

**Cached `network.sb`** (input-hash cached; deny baseline + host services). For a
fixture `host_services: [mysql:3306, redis:6379]`:
```scheme
;; agent-creance network.sb — appended after safehouse's base via --append-profile.
;; Deny-all baseline, then re-open only allowlisted host-service ports.
(deny network*)
(allow network* (remote tcp "localhost:3306"))   ;; mysql
(allow network* (remote tcp "localhost:6379"))   ;; redis
```
- Deterministic order (config order, or sorted — a decision; AC-0013 emits in a fixed
  documented order). Golden-tested.
- Comments may carry the cosmetic label for readability; the address is always `localhost`.

**Launch-time proxy fragment** (uncached; rendered from the live port `P`):
```scheme
;; agent-creance proxy fragment — live ephemeral proxy port, regenerated per launch.
(allow network* (remote tcp "localhost:<P>"))
```
- Pure function of the port; must allow exactly `localhost:<P>` and nothing else.
- **Does not carry its own `(deny network*)`** — it relies on `network.sb`'s deny being
  appended *before* it. This imposes an **ordering contract on AC-0023**: append
  `network.sb` first, then the proxy fragment (both after Safehouse's base). Document
  this handoff. (Alternative: a single launch-time fragment containing deny + host +
  proxy — but that defeats caching the host-service body; see Open Questions on whether
  the proxy allow could instead be folded into `network.sb` at launch.)

**`network*` vs `network-outbound`:** S5's validated fragment used `network*`; S3's
decision text used `network-outbound`. Both compile and both reopen the port for the
cage's outbound use. `network-outbound` is tighter (leaves inbound on that port denied);
`network*` is the exact form proven end-to-end through Safehouse. See Open Questions.

## Codebase patterns to mirror (AC-0013 / `internal/policy/compile`)

- **Compiler/DI:** `Compiler` holds only seam-typed deps injected via `New`. A profile
  compiler needs `sysdep.FileSystem` + `sysdep.PathResolver` (+ `config.Loader`,
  `state.Resolver`); **no** HTTP/clock/generator-runner — a Seatbelt profile compiles
  purely from config (no registry fetches). `compile.go:86-118`.
- **Result:** `{ ProfilePath, InputHash, Skipped bool, AllowCount }` analog.
  `compile.go:120-128`.
- **Cache key:** `inputHash` = `hex(sha256(json.Marshal(payload)))` over the resolved
  config layers. For profiles the only inputs are `network.host_services` (and whatever
  the proxy fragment consumes — but the proxy port is *excluded* by design). `compile.go:266-283`.
- **Tolerant cache read:** missing/corrupt → recompute. But note `network.sb` is text,
  not JSON-with-embedded-hash like `policy.json`. Decision needed on how to store/compare
  the input hash for a text artifact (e.g. a header comment `;; input-hash: <hex>`, or a
  sidecar). See Open Questions. `compile.go:287-297`.
- **Atomic out-of-tree write:** marshal/render → append `'\n'` → `MkdirAll(layout.Root,
  0o755)` → write `dest+".tmp"` → `Rename(tmp, dest)` → on failure `Remove(tmp)`. Write
  to `layout.NetworkSB()`. `compile.go:400-422`.
- **Golden harness:** `var update = flag.Bool("update", false, "regenerate golden files")`
  — the description string is grepped by the Makefile `golden` target, use verbatim.
  Read artifact from the **fake FS** (`fsys.Files[path]`), read/write golden via real
  `os` under `testdata/`. `compile_test.go:19,110-124`.
- **C4 no-in-tree-write assertion:** assert `ProfilePath` is under the state root and no
  `fsys.Files`/`fsys.Dirs` entry sits under `projDir`. `compile_test.go:210-238`.
- **Integration test:** `//go:build integration`, real seams, isolated `HOME`/`XDG_CACHE_HOME`
  temp dirs. For AC-0014 the integration test is where the S3 sandbox-exec probes live.
  `live_integration_test.go`.

### Key files
- `internal/policy/compile/compile.go` — compiler/cache/write template.
- `internal/policy/policy.go` — artifact-schema + `CompiledVersion` + `FromConfig` idioms.
- `internal/policy/compile/compile_test.go` — golden + cache + C4 tests.
- `internal/policy/compile/live_integration_test.go` — integration test shape.
- `internal/config/config.go:52-63` — `Network`, `HostService{Label, Port}`.
- `internal/config/validate.go:56-73` — `parseHostService` (`label:port`, port 1–65535).
- `internal/state/state.go:158` — `Layout.NetworkSB()` (output path, already reserved).
- `internal/sysdep/filesystem.go` + `sysdep/sysdeptest/filesystem.go` — FS seam + fake
  (`.Files`, `.Dirs` for C4).
- `Makefile:69-74` — `golden` target greps `"regenerate golden files"`.
- `internal/cli/cli.go` — `App` currently injects only `Commander`/writers/`Tested`; the
  policy compiler is **not** wired into any command. Wiring `internal/profile` into the
  CLI would be establishing a new pattern, not mirroring one.

## Impact / what this ticket touches

- **New package `internal/profile`** (compiler + render functions + tests + `testdata/`).
- **No config changes** — `HostService{Label, Port}` already exists.
- **No state changes** — `NetworkSB()` already exists. (A path for the proxy fragment is
  *not* obviously needed if it's a pure render function; see Open Questions.)
- **Docs to correct (AC-0014 owns these, per S3/S5):**
  - `docs/design.md:53` — the "address family" caveat: reframe to family-agnostic,
    port-based; rule is `(remote tcp "localhost:N")`, not literal `127.0.0.1`; add the
    intra-machine widening nuance and "never emit `*:N`".
  - `docs/design.md:99-114` — the `host_services` config comment block: "address ALWAYS
    forced to 127.0.0.1" / "address as 127.0.0.1, NOT localhost" is backwards. The
    compiler emits `localhost:N`; caged tooling reaches services via `localhost` or
    `127.0.0.1` (both work; the rule covers both families). Keep the `0.0.0.0`-binding
    honesty.
  - `docs/design.md:295` — already mostly correct (network.sb exempt from cache,
    regenerated from live port); reconcile with the *split* (cached body + uncached proxy
    fragment) the ticket actually asks for.
  - Spec `WP-2.5` (lines 189-194) and `§14`: "`127.0.0.1:<port>`" / "IPv4 literal
    enforced" → `localhost:<port>`. Optionally annotate rather than rewrite (spec is a
    dated discussion artifact).
- **Out of scope (ticket):** passing the profile to Safehouse (AC-0023);
  lock-file/port allocation (AC-0020) — AC-0014 only *consumes* a port value.
- **Downstream:** AC-0023 consumes `network.sb` + the proxy fragment via `--append-profile`
  (ordering contract above). AC-0033 is the end-to-end isolation gate that AC-0014's
  localhost-refusal probe folds into.

## Open questions for the planning checkpoint

1. **Self-test scope.** Ship the S3 localhost-refusal self-test only as a gated
   *integration test* (matches ticket verification step 5; minimal, dependency-clean), or
   *also* wire a runtime self-test into `doctor` now (literal S3 "run on setup/doctor"
   decision; pulls in App seams + real sandbox-exec, overlaps AC-0023/AC-0033)?
   **Recommendation:** integration test only for AC-0014; defer doctor-wiring to a later
   ticket. (`setup` doesn't exist yet; `doctor` is hermetic today.)
2. **`network*` vs `network-outbound`** for the allow rules. `network-outbound` is tighter
   (inbound stays denied even on allowlisted ports); `network*` is the exact form S5
   proved end-to-end. **Recommendation:** `network-outbound` (tighter; matches S3's
   AC-0014 directive), unless we want to stay byte-for-byte with the S5-validated string.
3. **Proxy fragment shape.** A pure `RenderProxyFragment(port int) string` returning just
   the allow line (relies on `network.sb`'s deny appended first; AC-0023 orders them), or
   a self-contained fragment that re-states `(deny network*)` + the proxy allow (composes
   regardless of order, at the cost of a redundant deny)? **Recommendation:** pure allow
   line + documented ordering contract for AC-0023.
4. **Input-hash storage for a text artifact.** `policy.json` embeds `input_hash` in JSON.
   For `network.sb` (plain SBPL text), store the hash as a header comment
   (`;; input-hash: <hex>`) and parse it back on cache read, or keep a tiny sidecar?
   **Recommendation:** header comment — keeps one file, human-readable, easy to strip when
   comparing the body. (Note: even cached, network.sb is cheap to regenerate; design.md:295
   even calls it "free." Caching here is mostly for parity/consistency — confirm we want
   the cache at all for this artifact, or just regenerate every time and skip the
   input-hash machinery.)
5. **CLI wiring.** Leave `internal/profile` as a library-only package (like
   `internal/policy/compile` today, exercised by tests only), or add a CLI command now?
   **Recommendation:** library-only; CLI/launch wiring belongs to AC-0020/AC-0023.

## References

- Ticket: `thoughts/shared/tickets/AC-0014-seatbelt-profile-compiler.md`
- S3 findings: `thoughts/shared/research/2026-06-04-s3-localhost-ports.md`
- S5 findings: `thoughts/shared/research/2026-06-05-s5-append-profile.md`
- Spec: `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md` (WP-2.5 lines 189-194, §14)
- Design: `docs/design.md` (lines 30, 42-46, 50-68, 99-114, 295, 403, 411)
- Template: `internal/policy/compile/*`, `internal/policy/policy.go`
- AC-0013 plan/research: `thoughts/shared/plans/2026-06-05-AC-0013-policy-compiler.md`,
  `thoughts/shared/research/2026-06-05-AC-0013-policy-compiler.md`
- Downstream: AC-0020 (port lifecycle), AC-0023 (safehouse invocation), AC-0033 (isolation gate)
</content>
</invoke>
