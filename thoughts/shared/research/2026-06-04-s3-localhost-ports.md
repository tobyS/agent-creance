---
date: 2026-06-04
topic: "Spike S3 — Seatbelt port-level localhost filtering across address families (WP-0.3 / AC-0003)"
status: complete
kind: spike-findings
branch: main
git_commit: 95743778d8362b01d7fb234684ba801c73bca64a
ticket: AC-0003
spike: S3
gates: [AC-0014 (WP-2.5 Seatbelt profile compiler), AC-0020 (WP-3.4)]
source_design: docs/design.md
---

# Spike S3 — Seatbelt port-level localhost filtering across address families

**Decision: the port-level localhost guarantee HOLDS — a non-allowlisted localhost
port is refused over BOTH IPv4 and IPv6 — but NOT by the mechanism the design
assumed. On macOS 26.5 the Seatbelt compiler REJECTS literal-IP host rules
(`(remote tcp "127.0.0.1:N")` → compile error "host must be `*` or `localhost`").
The only buildable per-port rule is `(remote tcp "localhost:N")`, and `localhost`
inherently spans both `127.0.0.1` and `::1`. So the "force IPv4 in the rule"
mitigation in `docs/design.md` is both impossible and unnecessary: there is no
family-specific rule to write, and the single `localhost:N` token already covers
both families, with the PORT strictly enforced (a non-allowlisted port returns
`EPERM` on v4 and v6 alike). One nuance: `localhost` matches every address assigned
to THIS machine (loopback v4/v6 PLUS interface IPs like `192.168.0.65`), but never
external hosts — so the guarantee is "allowed port reachable on any local-machine
address; everything else, and all non-allowlisted ports, refused; no external host
reachable except through an explicit `*:N` rule we must never emit."**

## Question

The "even on localhost" guarantee rests on `(remote tcp "127.0.0.1:N")` allow rules
genuinely refusing a non-allowlisted localhost port over **both** IPv4 and IPv6 when
proxy and services are pinned to one family. If a `::1` path slips past an IPv4-only
rule, host-service isolation is an illusion. Confirm (or disprove) the guarantee and
capture the self-test AC-0014 will ship. See `docs/design.md` "What the cage
prevents — address family caveat" (line 53) and "Open spikes" (S3, line 21).

## Environment

| Tool | Version |
|---|---|
| macOS | 26.5 (build 25F71), Darwin 25.5.0, arm64 |
| sandbox-exec | system `/usr/bin/sandbox-exec` (Seatbelt / SBPL v1) |
| nc | system `/usr/bin/nc` (Apple netcat) |

Loopback `lo0` carries both families (`inet 127.0.0.1`, `inet6 ::1`). The machine's
LAN interface (`en0`) address was `192.168.0.65` during the test.

## Method

Four trivial TCP listeners were bound so that a probe failure can only be Seatbelt
(something IS listening), never "nothing there":

- `127.0.0.1:18431` (v4, the *allowlisted* port) and `127.0.0.1:18432` (v4, *other*);
- `[::1]:18431` (v6, allowlisted) and `[::1]:18432` (v6, other);
- later, `192.168.0.65:18431` (the non-loopback LAN IP) to test host scope.

Probes ran `sandbox-exec -f <profile> nc -v -z -G2 -w2 <-4|-6> <addr> <port>`. The
key signal is the failure *kind*: a Seatbelt denial returns `connectx … Operation
not permitted` (**EPERM**), distinct from `Connection refused` (**ECONNREFUSED**,
nothing listening). A control profile (`(allow default)`) confirmed all listeners are
reachable from a sandbox before the restrictive runs, so every restrictive-profile
failure is attributable to the network filter alone (all non-network operations are
granted broadly in the test profile).

## Results

### 1. The design's literal-IP rule does not compile — **FAIL (compile)**

```
$ sandbox-exec -f v4allow.sb true
sandbox-exec: host must be * or localhost in network address
	(remote tcp "127.0.0.1:18431")
```

Sweeping the host token (rule `(allow network-outbound (remote tcp "<host>:18431"))`):

| host token | compiles? |
|---|---|
| `*:18431` | **yes** |
| `localhost:18431` | **yes** |
| `127.0.0.1:18431` | **no** — "host must be `*` or `localhost`" |
| `[::1]:18431` | **no** — "host must be `*` or `localhost`" |

So the literal-address rule form the design specifies (line 53, line 102-103) is
**unbuildable** on macOS 26.5. The only per-port host tokens are `*` and `localhost`.

### 2. `localhost:N` — port guarantee holds across BOTH families — **PASS**

Control (`(allow default)`) — all four listeners reachable from a sandbox:

| target | rc | message |
|---|---|---|
| v4 `127.0.0.1:18431` | 0 | succeeded |
| v4 `127.0.0.1:18432` | 0 | succeeded |
| v6 `[::1]:18431` | 0 | succeeded |
| v6 `[::1]:18432` | 0 | succeeded |

Restrictive: `(deny network*)` + `(allow network-outbound (remote tcp "localhost:18431"))`:

| target | rc | result | verdict |
|---|---|---|---|
| v4 `127.0.0.1:18431` (allowlisted port) | 0 | `Connection … succeeded!` | **reachable** ✓ |
| v4 `127.0.0.1:18432` (other port) | 1 | `connectx … Operation not permitted` (EPERM) | **refused** ✓ |
| v6 `[::1]:18431` (allowlisted port) | 0 | `Connection … succeeded!` | **reachable** ✓ |
| v6 `[::1]:18432` (other port) | 1 | `connectx … Operation not permitted` (EPERM) | **refused** ✓ |

The allowlisted port is reachable over **both** v4 and v6; the non-allowlisted port
is **refused over both** — and the refusal is **EPERM** (Seatbelt), not
ECONNREFUSED, while the controls prove the listener is genuinely up. **The headline
guarantee holds.** Crucially, it holds *because* `localhost` covers both families:
there is no IPv4-only rule that could let `::1` slip past, because no family-specific
rule can be written at all.

### 3. `localhost` is NOT `*` — external hosts stay refused — **PASS**

Does `localhost:N` secretly mean "any host on port N" (which would be an egress
hole)? No. With rule `(remote tcp "localhost:443")` vs `(remote tcp "*:443")`,
probing an external host:

| rule | target | rc | result |
|---|---|---|---|
| `localhost:443` | `1.1.1.1:443` (external) | 1 | EPERM — **refused** |
| `*:443` (control) | `1.1.1.1:443` (external) | 0 | succeeded |

So `localhost` ≠ `*`. `localhost` matches the local machine's own addresses only.

### 4. `localhost` scope = all local-machine addresses (loopback + interface IPs)

`localhost` is broader than the loopback pair. Under the `localhost:18431` rule, the
machine's **non-loopback LAN address** was reachable on the allowlisted port, while a
non-allowlisted port on that same LAN address was still refused:

| target | rc | result |
|---|---|---|
| `192.168.0.65:18431` (LAN IP, allowlisted port) | 0 | succeeded — **reachable** |
| `192.168.0.65:18432` (LAN IP, other port) | 1 | EPERM — **refused** |

So `localhost` = "any address assigned to this host" (loopback `127.0.0.1`/`::1`
**and** interface IPs such as `192.168.0.65`), never external hosts. The **port** is
strictly enforced on all of them.

## Answers to the ticket's acceptance criteria

- allowlisted `127.0.0.1:<allowed>` reachable — **PASS** (§2).
- non-allowlisted `127.0.0.1:<other>` refused — **PASS**, EPERM (§2).
- non-allowlisted `[::1]:<other>` refused — **PASS**, EPERM (§2). This is the core
  "does `::1` slip past?" check: it does **not**.
- `[::1]:<allowed>` refused "when only v4 is allowed" — **MOOT / overturned**. The
  premise (a v4-only rule) is **unbuildable**: literal IPs do not compile, and the
  only writable rule (`localhost:<allowed>`) deliberately covers `::1` too, so
  `[::1]:<allowed>` is **reachable**, not refused. The spike's expectation here was
  based on the design's literal-IP assumption, which §1 disproves. The guarantee the
  AC was protecting — *non-allowlisted* ports refused on both families — still holds.

## Answer to the ticket's open question

**"Should the shipped self-test run on every `run`, only on `setup`/`doctor`, or
once-and-cache?"** — Decided with the maintainer: **run it on `setup` and on every
`doctor`, uncached.** Always fresh, simplest to reason about, and it never adds
latency to a normal `agent-creance run` (the hot path). `doctor` is the natural home
for a kernel-enforcement health check (sibling to the CA live-verify in `docs/design.md`
line 444). No build-version cache in v0.1.

## Reusable self-test (for AC-0014 / WP-2.5)

Ship this as the cage's network self-test, run on `setup` and `doctor`. It binds a
**non-allowlisted** loopback port on both families and asserts the cage is refused on
it (EPERM), while the allowlisted proxy port works. Pseudocode mirrors the probes
above:

1. Pick a port `OTHER` that is **not** in the compiled allowlist; bind a throwaway
   listener on `127.0.0.1:OTHER` **and** `[::1]:OTHER` (so a failure is provably the
   sandbox, not a closed port).
2. Compile a probe profile = the real `network.sb` (deny baseline + `localhost:<proxy>`
   allow), or a minimal stand-in with the same deny baseline.
3. Inside the cage (`sandbox-exec -f <profile> …`):
   - connect to `127.0.0.1:OTHER` → **must fail with EPERM** ("Operation not permitted").
   - connect to `[::1]:OTHER` → **must fail with EPERM**.
   - connect to the allowlisted proxy port on `127.0.0.1` → **must succeed**.
4. PASS iff both `OTHER` probes are EPERM-refused and the proxy probe succeeds.
   Distinguish EPERM from ECONNREFUSED — only EPERM proves *enforcement* (a refused
   connection to a closed port would be a false PASS).

Concrete commands captured during this spike (reuse verbatim in the integration test):

```sh
# listeners (both families, on a NON-allowlisted port)
nc -4 -k -l 127.0.0.1 18432 &
nc -6 -k -l ::1       18432 &

# probe profile: deny baseline + allow ONLY the proxy/allowlisted port
cat > probe.sb <<'SB'
(version 1)
(deny default)
(allow process-fork) (allow process-exec*) (allow sysctl-read)
(allow file-read* file-read-metadata file-read-data)
(allow mach-lookup) (allow signal) (allow system-socket)
(deny network*)
(allow network-outbound (remote tcp "localhost:18431"))   ; <-- proxy/allowlisted port
SB

# expectations
sandbox-exec -f probe.sb nc -vz -G2 -w2 -4 127.0.0.1 18431   # PASS: succeeded
sandbox-exec -f probe.sb nc -vz -G2 -w2 -4 127.0.0.1 18432   # PASS: EPERM (Operation not permitted)
sandbox-exec -f probe.sb nc -vz -G2 -w2 -6 ::1       18432   # PASS: EPERM (Operation not permitted)
```

## Decision

Decision: **IPv4-pinning at the Seatbelt-rule level is impossible and unnecessary;
the cage emits `(remote tcp "localhost:N")` per allowlisted port, which refuses every
non-allowlisted localhost port over both IPv4 and IPv6.** Specifics for the gated WPs:

- **AC-0014 (Seatbelt profile compiler)** MUST emit per-port rules as
  `(allow network-outbound (remote tcp "localhost:<port>"))`. It MUST translate any
  loopback literal in config (`127.0.0.1`, `::1`, or `localhost`) into the `localhost`
  token keyed on the **port** — emitting `127.0.0.1:<port>` would fail to compile.
  It MUST **never** emit `*:<port>` for a host service (that would permit external
  egress on that port; see §3).
- **AC-0014** also ships the network self-test above, run on `setup` and every
  `doctor`, uncached (per the open-question decision).
- **AC-0020 (WP-3.4)** can rely on the port guarantee being family-agnostic; no
  separate IPv6 handling is needed in the host-service allow path.

## Limitations / scope of this result

- **TCP only.** The design's localhost concerns are TCP (proxy + DB/Redis). UDP and
  `(local …)`/bind rules were not exercised; out of scope for S3.
- **No DNS in the probes.** Literal addresses were used throughout, so name
  resolution played no part; this isolates the address/port filter cleanly.
- **`localhost` host-widening is intra-machine, not external.** An allowlisted
  service port is reachable on *all* of this machine's addresses (loopback + LAN
  interface IPs), not loopback-only. On a dev box the loopback and interface IPs
  generally front the same services, so this is a minor widening within the same
  host; it never reaches external hosts (§3). The only way to scope an allowlisted
  service to loopback strictly is at the application layer (bind the service to
  `127.0.0.1`), not at the Seatbelt layer. Flagged for AC-0014's threat-model note.
- **S3 assumes the deny baseline is in effect.** This spike tested the *precision* of
  the allow rules against a `(deny network*)` baseline established directly in the
  profile. Whether that deny baseline actually takes effect when delivered via
  Safehouse `--append-profile` (narrowing Safehouse's base `allow network*`) is
  **S5's** charter (AC-0005 / WP-0.5), not this one.

## Design impact

The localhost guarantee is **validated**, but `docs/design.md` needs two corrections
(to be made by the gated build tickets, not this spike):

1. **Rule form (lines 53, 102-107).** The Seatbelt rule is `(remote tcp "localhost:N")`,
   not the literal `(remote tcp "127.0.0.1:N")` — the latter does not compile on
   macOS 26.5. The "agent-creance forces IPv4 (`127.0.0.1`) for both the proxy and
   host-service rules" sentence is wrong as written: you cannot pin family in the
   rule, and you do not need to — `localhost` covers both v4 and v6 and the port is
   the discriminator. The user-facing config can still ask for `127.0.0.1` host
   services (the app binds/targets v4), but the compiler translates that to a
   port-keyed `localhost` rule.
2. **Address-family caveat (line 53).** Reframe: the guarantee is **family-agnostic
   and port-based**, not "only holds if a single family is pinned." Add the
   intra-machine host-widening nuance (`localhost` = all local addresses, not
   loopback-only) and the "never emit `*:N`" rule. The shipped self-test (run on
   `setup`/`doctor`) confirms enforcement, as the design already anticipated.
