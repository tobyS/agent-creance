---
date: 2026-06-05
topic: "Spike S5 — Appended-profile network narrowing (WP-0.5 / AC-0005)"
status: complete
kind: spike-findings
branch: main
git_commit: 582ee155e8ef5ae1924e9f465211088180e22898
ticket: AC-0005
spike: S5
gates: [AC-0014 (WP-2.5 Seatbelt profile compiler), AC-0023 (WP-4.2 Safehouse invocation)]
source_design: docs/design.md
---

# Spike S5 — Appended-profile network narrowing

**Decision: the append-profile narrowing HOLDS — v0.1 ships `--append-profile`, NOT a
fully-generated profile. A fragment of `(deny network*)` followed by per-port
`(allow network* (remote tcp "localhost:<port>"))` rules, fed to
`safehouse --append-profile`, lands AFTER Safehouse's base `(allow network*)` and
narrows it: arbitrary egress is refused with EPERM while the proxy port and a named
host-service port stay reachable, and a real request flows end-to-end through the
proxy (HTTP 200). All three stacked assumptions are confirmed on macOS 26.5 + Agent
Safehouse 0.10.1 — the fragment is appended after the generated rules (it compiles at
source line 1098, after the base's 1094 lines), Seatbelt's last-match-wins precedence
lets the fragment's `(deny network*)` override the earlier base allow, and the
subsequent specific allows reopen only the intended ports. ONE correction carries
over from S3: the ticket's literal-IP rule form `(remote tcp "127.0.0.1:P")` does NOT
compile (even inside the appended fragment) — AC-0014 must emit the `localhost:<port>`
form. The fallback (a wholesale generated profile) is NOT needed, but Safehouse does
expose it (`--stdout`/`--output` print the full policy) should it ever be.**

## Question

The entire network model assumes a `--append-profile` fragment can *narrow*
Safehouse's "network: open by default" base: the fragment denies `network*` then
re-allows only the proxy + host-service ports. This rests on three stacked
assumptions (`docs/design.md` line 23): (1) the fragment is concatenated *after*
Safehouse's base `(allow network*)`; (2) Seatbelt's last-match-wins precedence lets
the fragment's `(deny network*)` override that base allow; (3) the subsequent
specific `(allow …)` rules reopen only the intended ports. S3 tested the *precision*
of those allow rules but assumed the deny baseline was already in effect; **this spike
validates that the deny baseline takes effect at all through `--append-profile`.** If
it doesn't compose this way, the network model needs a different strategy (a fully
generated profile instead of an append), which reshapes AC-0014 (WP-2.5).

Ticket open questions: (a) What is Safehouse's documented ordering for
`--append-profile` relative to its base rules? (b) If a fully-generated profile is
needed, does Safehouse expose a way to supply one wholesale?

## Environment

| Tool | Version |
|---|---|
| macOS | 26.5 (build 25F71), Darwin 25.5.0, arm64 |
| Agent Safehouse | 0.10.1 (`safehouse`, Homebrew at `/opt/homebrew/bin/safehouse`) |
| sandbox-exec | system `/usr/bin/sandbox-exec` (Seatbelt / SBPL v1) |
| mitmproxy / mitmdump | 12.2.3 (the host-side proxy under test) |
| nc / curl | system `/usr/bin/nc`, `/usr/bin/curl` 8.7.1 |

Note: the binary is `safehouse`, not `agent-safehouse` (the design's prose name); it
self-identifies as "Agent Safehouse 0.10.1". The narrowing flag is **`--append-profile
PATH`** — help text: *"Append an additional sandbox profile file after generated
rules. Repeatable; files are appended in argument order."* That documented ordering
("after generated rules") is exactly assumption (1).

## Method

Mirrors the S3 control-then-restrict methodology so any test-run failure is
attributable to the fragment alone, and so a Seatbelt denial (`EPERM` —
`connectx … Operation not permitted`) is cleanly distinguished from `ECONNREFUSED`
(nothing listening).

Three host-side listeners were started (outside the cage), then the cage was driven
twice — once on the bare Safehouse base (control), once with the append fragment:

- **Proxy port P = 18081** — a real `mitmdump -p 18081` (so the proxy path can be
  exercised end-to-end, not just at TCP).
- **Host-service port H = 13306** — a throwaway `nc -k -l 127.0.0.1 13306` (stands in
  for an allowlisted host service, e.g. MySQL).
- **Other port O = 19090** — a throwaway `nc -k -l 127.0.0.1 19090` (a NON-allowlisted
  local port; its listener is up, so a denial can only be the sandbox).

The append fragment under test (`fragment.sb`):

```scheme
;; S5 append fragment — narrows safehouse's base (allow network*) to deny-all
;; then re-open ONLY the proxy port + one host-service port.
(deny network*)
(allow network* (remote tcp "localhost:18081"))   ;; proxy port P
(allow network* (remote tcp "localhost:13306"))   ;; host-service port H
```

Invocation: `safehouse --append-profile fragment.sb -- /bin/bash probe.sh`. The probe
script ran, inside the cage: an external `nc 1.1.1.1:443` and `curl http://example.com`
(no proxy); a TCP connect to the proxy port and a `curl -x http://127.0.0.1:18081
http://example.com/`; and `nc` to the host-service port and to the other port.

## Results

### 0. The base policy IS network-open, and Safehouse itself proves last-match-wins

`safehouse --stdout` (the generated base, 1094 lines) opens with `(version 1)` then
`(deny default)` (line 34), and at **line 313** carries the broad
`(allow network*)  ;; Allow outbound + inbound traffic …`. Crucially, Safehouse's own
policy then places **`(deny network-outbound …)` blocks AFTER that allow** — the
Docker-socket deny (line 909) and the SSH-agent-socket deny (line 1008) — with
load-bearing comments: *"Without this deny block, the file-level deny above is bypassed
entirely"* and *"open network policy in 20-network.sb allows agent-backed
authentication even when --enable=ssh is not set."* So Safehouse already depends on
**deny-after-allow narrowing via last-match-wins on this exact compile path** — strong
prior evidence before our own fragment is even appended. Our `--append-profile`
content lands after all of this (line 1098+), i.e. after the line-313 base allow.

### 1. Fragment ordering & compilation — assumption (1) confirmed; literal-IP FAILS

| fragment form | result |
|---|---|
| `(allow network* (remote tcp "localhost:P"))` | **compiles + runs** (rc=0); appears at source line 1098 (after the 1094-line base) |
| `(allow network* (remote tcp "127.0.0.1:P"))` (ticket's literal form) | **does NOT compile** — `sandbox-exec: host must be * or localhost in network address`, backtrace at line 1098 |

The append lands after the generated rules (the literal-IP error's backtrace pins it
to line 1098). And the S3 finding reproduces *inside the appended fragment*: literal
IPs are unbuildable; only `*` and `localhost` host tokens compile.

### 2. Control run (base policy, no fragment) — all four targets reachable

`safehouse -- /bin/bash control.sh`:

| target | rc | result |
|---|---|---|
| external `1.1.1.1:443` | 0 | `Connection … succeeded!` |
| proxy `127.0.0.1:18081` | 0 | `succeeded!` |
| host-service `127.0.0.1:13306` | 0 | `succeeded!` |
| other `127.0.0.1:19090` | 0 | `succeeded!` |

Every listener is genuinely up and external egress is open under the base — so any
refusal in the test run is the fragment's doing, not a closed port.

### 3. Test run (base + append fragment) — narrowing HOLDS — **PASS**

`safehouse --append-profile fragment.sb -- /bin/bash probe.sh`:

| probe | expect | rc | result | verdict |
|---|---|---|---|---|
| `nc 1.1.1.1:443` (external, no proxy) | blocked | 1 | `connectx … Operation not permitted` (**EPERM**) | **egress denied** ✓ |
| `curl http://example.com/` (no proxy) | blocked | 6 | `Could not resolve host: example.com` (DNS denied) | **egress denied** ✓ |
| `nc 127.0.0.1:18081` (proxy port) | success | 0 | `succeeded!` | **reachable** ✓ |
| `curl -x http://127.0.0.1:18081 http://example.com/` | success | 0 | `HTTP 200` | **proxy egress works end-to-end** ✓ |
| `nc 127.0.0.1:13306` (host service) | success | 0 | `succeeded!` | **reachable** ✓ |
| `nc 127.0.0.1:19090` (other port) | blocked | 1 | `connectx … Operation not permitted` (**EPERM**) | **refused** ✓ |

The deny baseline takes effect (external EPERM, DNS denied) while the proxy and
host-service ports stay open, and a request flows the full path through the proxy
(HTTP 200). The non-allowlisted local port 19090 is **EPERM-refused** even though the
control proved its listener is up — decisive proof the refusal is Seatbelt, not a
closed port. Assumptions (2) and (3) confirmed.

### 4. Proxy path corroborated in the flow log

`mitmdump`'s log independently confirms the cage routed through the proxy:

```
[127.0.0.1:61344] client connect
[127.0.0.1:61344] server connect example.com:80 ([2606:4700:10::ac42:93f3]:80)
127.0.0.1:61344: GET http://example.com/
              << 200 OK 528b
```

mitmproxy opened the upstream connection to `example.com:80` **on the cage's behalf** —
the only egress the cage has is the allowlisted proxy port.

### 5. Side note — DNS is also denied by the baseline

The no-proxy `curl` failed at name resolution (`Could not resolve host`), not at
connect: `(deny network*)` blocks outbound UDP 53 too. Egress is blocked either way,
and the cage does not even leak DNS queries for blocked hosts — a small bonus property.
(Caged tooling must therefore reach allowlisted host services by **IP/`localhost`**,
which matches the S4 `NO_PROXY=localhost,127.0.0.1,::1` decision; name-based egress
goes through the proxy, which does its own resolution host-side.)

## Answers to the ticket's acceptance criteria

- Research note exists at `thoughts/shared/research/2026-06-05-s5-append-profile.md` — **yes**.
- PASS/FAIL recorded — **PASS**: with the append fragment, arbitrary egress is denied
  (EPERM) while the proxy port + a host-service port are reachable (§3).
- Exact working `.sb` fragment captured — **yes** (§Method; the `localhost:<port>`
  form). The ticket's literal `127.0.0.1:<port>` form is captured as the *failure*
  case (§1).
- `Decision:` line states append vs fully-generated — **yes** (below; `--append-profile`).

## Answers to the ticket's open questions

- **"Safehouse's documented ordering for `--append-profile`?"** — The CLI help states
  the file is appended *"after generated rules … in argument order."* Empirically the
  fragment compiles at source line 1098, after the 1094-line base (§1), so it is
  evaluated last and wins under last-match-wins.
- **"If a fully-generated profile is needed, does Safehouse expose a way to supply one
  wholesale?"** — Not needed (append works), but **yes, a path exists**: `safehouse
  --stdout` / `--output PATH` print the full policy text, and that file can be fed to a
  standalone `sandbox-exec -f` invocation (Safehouse's help documents exactly this:
  `sandbox-exec -f "$(safehouse)" -- /usr/bin/true`). So the fallback is available if a
  future Safehouse change ever breaks append ordering.

## Decision

Decision: **v0.1 uses `--append-profile`, NOT a fully-generated profile.** The
deny-all-then-reopen fragment narrows Safehouse's base `(allow network*)` exactly as
the design assumes. Specifics for the gated WPs:

- **AC-0014 (WP-2.5, Seatbelt profile compiler)** is NOT reshaped — it keeps emitting a
  `--append-profile` fragment, not a wholesale profile. The fragment MUST be:
  `(deny network*)` first, then one `(allow network* (remote tcp "localhost:<port>"))`
  per allowlisted port (proxy + each host service), in that order — the broad deny
  before the specific allows, so last-match-wins reopens only the named ports. It MUST
  use the **`localhost:<port>`** host token: the literal `127.0.0.1:<port>` /
  `[::1]:<port>` forms do not compile (§1, confirming S3 inside the append path). The
  port is the discriminator; `localhost` covers both families (per S3).
- **AC-0014** does NOT need `(deny network*)` to also re-state Safehouse's own
  network-outbound denies (docker/ssh sockets) — those live earlier in the base and a
  later broad `(allow network* (remote tcp "localhost:…"))` does not match a
  `unix-socket` remote, so they are not reopened. (Verified by construction: our allows
  are `remote tcp "localhost:N"`, disjoint from the base's `remote unix-socket` denies.)
- **AC-0023 (WP-4.2, Safehouse invocation)** is NOT reshaped — it passes the compiled
  fragment via `--append-profile <path>`. The flag name and "append after generated
  rules" semantics are confirmed for Safehouse 0.10.1.
- The wholesale-profile fallback stays unbuilt; if ever needed, `safehouse --output`/
  `--stdout` + `sandbox-exec -f` is the documented escape hatch.

## Limitations / scope of this result

- **TCP outbound only.** S5's charter is the egress-narrowing case (proxy + host
  services). `network-bind` / `network-inbound` (caged dev servers accepting
  connections) were not exercised here; Safehouse's base leaves inbound open and the
  fragment's `(deny network*)` would close it — if v0.1 ever needs the agent to *listen*,
  that is a separate allow (out of scope for this spike, flagged for AC-0014).
- **Single Safehouse version.** Confirmed on 0.10.1. The append ordering is documented
  behavior, but a major Safehouse change could reorder it; the AC-0014 self-test (the
  S3 EPERM probe, run on `setup`/`doctor`) will catch a regression, and the wholesale
  fallback (§Decision) is the contingency.
- **Real proxy, real upstream.** The proxy path was exercised against live
  `example.com` over plain HTTP (port 80) to keep the test focused on the
  egress-narrowing question; TLS interception + CA trust is S1/S4's domain and is not
  re-litigated here. The HTTP 200 only proves the proxy port is reachable and egress
  flows through it.
- **`localhost` host-widening** (the S3 nuance: `localhost` matches all of this
  machine's addresses, not loopback-only) carries over unchanged; it is intra-machine,
  never external (S3 §3).

## Design impact

The design's S5 premise is **validated** — no architectural change. `docs/design.md`
needs the same family of corrections already flagged by S3 (to be made by the gated
build tickets, not this spike):

1. **Rule form.** The fragment's allow rules are `(remote tcp "localhost:<port>")`,
   not the literal `(remote tcp "127.0.0.1:N")` the design/ticket show (lines 53,
   99–114) — the literal form does not compile, here too (§1).
2. **Binary/flag names.** The tool is invoked as `safehouse` (self-named "Agent
   Safehouse"); the narrowing flag is `--append-profile PATH` ("append after generated
   rules"), as the design assumes (lines 23, 295, 403). No change needed beyond noting
   the `safehouse` vs `agent-safehouse` prose spelling.
3. **M0 milestone.** S5 was the last open spike on the M3 critical path (spec §12);
   with this PASS, all five spikes (S1–S5) are resolved and the dependent phases
   (WP-2.5, WP-4.2) are unblocked with no architectural fallback triggered.
</content>
