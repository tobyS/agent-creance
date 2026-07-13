---
date: 2026-07-13T10:09:22Z
git_commit: 8f4b0a12a904d540c9adcb2ef9ac1dd41afa8a84
branch: main
repository: agent-creance
topic: "AC-0069b: Unix-socket secret broker — Go-side custody + rotation channel"
tags: [research, codebase, credential-injection, broker, unix-socket, mlock, proxy, enforcer, rotation, seatbelt]
status: complete
last_updated: 2026-07-13
---

# Research: AC-0069b — Unix-socket secret broker (Go-side custody + rotation channel)

**Date**: 2026-07-13T10:09:22Z
**Git Commit**: 8f4b0a12a904d540c9adcb2ef9ac1dd41afa8a84
**Branch**: main
**Repository**: agent-creance

## Research Question

AC-0069b (Phase 2 of credential injection, sub-ticket of epic AC-0069): replace
the one-shot inherited-fd secret delivery with a **host-side Go broker** that
holds the resolved/minted secret in `mlock`-able, zeroizable memory and serves
it to the mitmproxy addon over a **unix socket** with restrictive permissions —
a channel that supports **rotation** (AC-0069a's refresh loop updates the served
credential without a proxy restart and without racing in-flight requests), stays
**fail-closed (472)** when the broker is unavailable, and is **never reachable
from inside the cage** (the IMDS/SSRF lesson). Depends on AC-0068 (Phase 1),
which is Done.

## Summary

Phase 1's delivery channel is structurally one-shot on both ends: the Go side
writes the payload once and closes the pipe (`internal/sysdep/processmanager.go:110-113`),
and the addon reads fd 3 to EOF exactly once behind a `_secrets_read` latch
(`internal/proxy/enforcer/enforcer.py:138-143`). Nothing in the system can
re-deliver a secret to a running proxy. AC-0069a's planning round (paused
2026-07-12) already fixed the resolution: **the broker comes first**, the fd-3
stream extension and per-refresh respawn are rejected, and the broker protocol
must carry **expiry metadata** so the addon can implement stale-then-472-at-expiry.

Six load-bearing facts came out of this round:

1. **The broker has no host process to live in.** The proxy is a detached
   `mitmdump` daemon (`Setsid: true` + `Process.Release()`,
   `internal/sysdep/processmanager.go:74-83`), refcounted across CLI invocations
   through a flocked `proxy.lock` (`internal/proxy/lifecycle.go:47-76`,
   `137-221`), and is deliberately allowed to **outlive any single CLI process**.
   A broker that lives inside the run-session CLI would die while the proxy it
   serves is still up (second agent attached) — every subsequent request would
   then 472. So the broker needs its own detached daemon with the proxy's
   lifetime and refcount, or the design has to change the proxy-sharing model.
   This is the single biggest structural question the ticket does not answer.

2. **Secret *resolution* must stay on the CLI's spawn path, not move into the
   broker.** `op://` can prompt for Touch ID, which is exactly why resolution is
   spawn-only today (`internal/proxy/lifecycle.go:108-116`, `docs/design.md:631`);
   a detached daemon cannot prompt. So the CLI still resolves and hands the value
   to the broker at spawn — plausibly over the **existing** `SpawnWithSecret`
   pipe, which is already the "never argv/env/disk" channel. The fd channel does
   not disappear; it changes cargo (Go→broker instead of Go→Python).

3. **The addon can do this cleanly, but only if the request hook goes `async`.**
   Every enforcer hook is a sync `def` today (`enforcer.py:294`), and the addon
   already runs one `asyncio.Task` on mitmproxy's loop (`enforcer.py:161`).
   mitmproxy officially supports `async def` hooks and states a blocking hook
   stalls the entire proxy event loop; `asyncio.open_unix_connection` is stdlib,
   so a broker client adds **no new Python dependency** (`requirements.txt` pins
   only `mitmproxy==12.2.3` + `pytest`).

4. **The cage cannot reach the socket — by two independent mechanisms — but
   neither is currently *asserted*.** `~/.cache/agent-creance/` is never mounted
   into the cage (`internal/cage/testdata/invocation.golden.json:3-24`;
   `docs/design.md:374`), and the generated SBPL is `(deny network*)` re-opened
   only by `(allow network-outbound (remote tcp "localhost:<port>"))`
   (`internal/profile/profile.go:47`, `75-77`) — which does not match a
   unix-socket remote. But **no rule, golden, or verify-matrix probe anywhere in
   the repo mentions unix sockets**: there is no `(deny network-outbound (literal
   …))`, and `internal/verify/matrix.go:51-178` has no socket vector. The
   guarantee currently holds by construction, not by test.

5. **The memory-hygiene half of the ticket is weaker than it looks — and the
   research says so plainly.** `unix.Mlock` *does* work on darwin (XNU implements
   `mlock`; only `mlockall` returns `ENOSYS`, which is why HashiCorp lists darwin
   as mlock-unavailable), but macOS swap is encrypted by default on T2/Apple
   Silicon, the attacker in this model shares the user's uid, and Go's stack
   copying means a `[]byte` wipe cannot chase every derived copy (Go's GC is
   non-moving — the common "GC copies your secret" claim is false; the stack is
   the real leak). Go 1.26's `runtime/secret` is **a no-op on darwin**
   (linux/amd64+arm64 only). Verdict from the research: **skip `memguard`**;
   best-effort `unix.Mlock` + explicit wipe + `RLIMIT_CORE=0` is honest hygiene,
   and the real lever is AC-0069a's short TTL. This should be written down as a
   bounded claim rather than sold as a control.

6. **Peer-uid authentication is security theatre here.** macOS has
   `LOCAL_PEERCRED` (`unix.GetsockoptXucred`) and `LOCAL_PEERPID`, but the caged
   agent and `mitmdump` **run as the same uid**, so a uid check distinguishes
   nothing. What has teeth: socket `0600` in a `0700` dir outside the cage's
   filesystem, an explicit SBPL `network-outbound` deny on the socket path
   (belt-and-braces, and it survives a future broader mount), a **bearer token**
   handed to the addon over the existing fd (defence against path leakage), and
   optionally **PID pinning** against the pid the broker itself spawned (not a
   generic pid check — PID-reuse attacks are a documented macOS class).

## Detailed Findings

### 1. The delivery channel as shipped (what is being replaced)

**Go side** — `SpawnWithSecret` (`internal/sysdep/processmanager.go:87-117`):
`os.Pipe()`, read end handed to the child as `cmd.ExtraFiles[0]` → fd 3
(`SecretFD = 3`, `processmanager.go:59-63`), `SysProcAttr{Setsid: true}`,
stdin/stdout/stderr discarded, `cmd.Process.Release()`; a goroutine writes the
payload and **closes the write end (EOF)**. Interface (`processmanager.go:28-57`):

```go
type ProcessManager interface {
	Spawn(ctx context.Context, name string, args ...string) (pid int, err error)
	SpawnWithSecret(ctx context.Context, secret []byte, name string, args ...string) (pid int, err error)
	Alive(pid int) bool
	Signal(pid int, sig os.Signal) error
	StartTime(pid int) (int64, error)
}
```

The fd *number* rides argv as `--set creance_secret_fd=3`, appended only when a
payload exists (`internal/proxy/lifecycle.go:495-507`); the payload never does.

**Payload**: flat JSON `{credential-name: raw-token}`, built by
`resolveInjectionSecrets` (`internal/cli/inject.go:26-54`) over the compiled
policy's distinct `inject` names (`injectedCredentialNames`, `inject.go:58-70`).
An individually unresolvable credential is **warned and omitted** (`inject.go:43-47`);
a whole-resolver failure warns and spawns the proxy plain
(`lifecycle.go:177-185`). Either way the addon fails closed per request.

**Python side** — `configure()` reads fd 3 once, guarded by `_secrets_read`
(`enforcer.py:127-143`); `_read_secrets` (`enforcer.py:215-245`) loops `os.read`
to EOF, `json.loads`, coerces to `dict[str, str]`, and closes the fd in
`finally`. Any `OSError`/`ValueError`/`TypeError` → `self._secrets = {}` →
per-request 472. The guard sets `_secrets_read = True` **before** the read, so
even a failed read latches. `done()` (`enforcer.py:163-169`) cancels the poll
task and closes the audit log — it does **not** clear `_secrets`.

**Consequence**: the two halves of an injection refresh on different clocks —
the credential *shape* every ~1s from `policy.json`
(`_poll_loop`, `enforcer.py:197-211`, `_POLL_INTERVAL_SECONDS = 1.0`), the
*token* never after startup. `docs/design.md:631`: "adding an `inject` rule to a
*running* cage hot-reloads the rule but not the secret map, so that host answers
472 until the proxy respawns."

### 2. Proxy process model — the constraint that shapes the broker

- **Detached daemon.** `Attach` spawns `mitmdump` under the flock
  (`lifecycle.go:186-191`); `OSProcessManager` sets `Setsid: true` and calls
  `Process.Release()`, so the daemon is reparented to launchd and the `Manager`
  holds no `*os.Process` — only a PID in `proxy.lock`.
- **Refcount.** `lockState` (`lifecycle.go:47-67`) carries
  `{proxy_pid, port, policy_hash, agents[], canonical_path}`, `agentRef` is
  `{pid, start}` (start time as the anti-PID-recycling factor, `:73-76`). All
  lock I/O happens **in place on the flocked descriptor** (`readLock` `:512-525`,
  `writeLock` `:528-538`) — temp+rename is deliberately avoided so the inode
  cannot be swapped under the flock (`internal/sysdep/flock.go:11-25`). Real
  flock is `O_RDWR|O_CREAT` at `lockPerm = 0o600` + `unix.Flock(LOCK_EX)`
  (`flock.go:49-67`).
- **Reuse vs spawn.** `proxyUp := cur.ProxyPID != 0 && m.proc.Alive(cur.ProxyPID) && m.ports.Probe(cur.Port)`
  (`lifecycle.go:157`). On reuse, **`cfg.Secrets` is never called** — the Touch-ID
  guard (`lifecycle.go:108-116`).
- **Teardown.** `Detach` (`:247-277`) removes the agent ref; on last-out it
  SIGTERMs `ProxyPID` (`:263`), removes the session overlay, and writes back a
  cleared `lockState` keeping only `PolicyHash`. `CleanOrphan` (`:361-388`) and
  `Clean` (`:398-435`) repeat the shape.
- **Readiness.** `waitProxyReady` (`:229-242`): ≤100 × `ports.Probe` with a 50ms
  `Sleeper` gap (5s ceiling); a proxy that exits during startup is a hard error.
- **CLI lifetime.** `runRun` blocks in `cage.NewRunner(...).Run(...)`
  (`internal/cli/run.go:320`) for the caged agent's whole life, then runs its
  deferred `Detach`. So the *spawning* CLI outlives its own agent — but the
  **proxy outlives the CLI** whenever another agent is still attached. That is
  precisely the case a run-session-owned broker breaks.
- **Only state file written by the lifecycle is `proxy.lock`.** No PID file, no
  port file, no socket path anywhere.

### 3. Cage reachability — where a socket may safely live

- **Mounts** (`internal/cage/cage.go:121-128`): `--add-dirs` = `safehouse.add_dirs_rw`
  (conventionally `.`) **plus `$HOME/.claude`**; `--add-dirs-ro` =
  `safehouse.add_dirs_ro` + detected plugin-marketplace dirs. `~/.cache/agent-creance/`
  is **never mounted** — it appears in argv only as `--append-profile` *path
  arguments*, read host-side by `sandbox-exec` before the sandbox applies
  (golden: `internal/cage/testdata/invocation.golden.json:3-24`).
  `docs/design.md:374`: "none of these files are mounted into the cage at all."
- **Network SBPL** (`internal/profile/profile.go:47`, `75-77`): `(deny network*)`
  re-opened only by `(allow network-outbound (remote tcp "localhost:<port>"))`
  per host-service port + the live proxy port. Golden:
  `internal/profile/testdata/network.golden`. A `remote tcp` allow is disjoint
  from a unix-socket remote, so `(deny network*)` still covers AF_UNIX connects
  — the project already observed `(deny network*)` biting socket `bind()`
  in-cage (`thoughts/shared/plans/2026-06-29-AC-0068a-secretresolver-seam.status.md:27-32`).
- **Nothing in the repo mentions unix sockets.** No `local unix-socket`, no
  `system-socket` in any renderer or golden (the one occurrence is inside a
  test's own standalone SBPL header, `internal/profile/live_integration_test.go:61-64`).
  `internal/verify/matrix.go:51-178` has vectors for `fs-outside`, `fs-home-write`,
  `net-raw-tcp`, `net-localhost-v4/v6`, `net-child`, `net-dns`, the three proxy
  refusals — and **no unix-socket vector**.
- **Sun_path limit.** macOS caps `sockaddr_un.sun_path` at 104 bytes. The natural
  path `~/.cache/agent-creance/projects/<16-hex>/broker.sock` is ~70 chars for a
  short `$HOME`, but a long username or an `XDG_CACHE_HOME` override can push it
  over — a real failure mode worth an explicit check (`$TMPDIR`-based fallbacks
  are *not* an option: `$TMPDIR` **is** writable in-cage per the project's own
  recorded model).

### 4. Enforcer internals a broker client must fit into

- **Concurrency model**: mitmproxy's single asyncio event loop. The addon's only
  coroutine is `_poll_loop` (`enforcer.py:208-211`), created in `running()`
  (`:161`). All hooks — `http_connect` (`:249`), `tls_clienthello` (`:276`),
  `request` (`:294`), `responseheaders` (`:375`), `response` (`:406`) — are sync
  `def`. mitmproxy *does* support `async def` hooks and calls them the preferred
  style for new addons; a blocking hook stalls the whole proxy loop.
- **Injection point**: `_apply_injection` (`enforcer.py:339-373`) — matched allow
  rule → skip if `in_cage` → skip if no `inject` → two dict lookups
  (`self._ruleset.credentials[name]` for the shape, `self._secrets[name]` for the
  token) → **either missing ⇒ 472** → else `inject.render_credential_value(...)`
  and **overwrite** `flow.request.headers[cred.header]`, stashing
  `flow.metadata["creance_injected"]` for the response-side annotation.
- **Fail-closed envelope**: `_apply_injection` runs inside `request`'s
  `try/except Exception` (`enforcer.py:330-337`), so an *exception* becomes a
  **471 hard-deny**, not a 472. A broker client that raises (socket missing,
  timeout) would therefore currently hard-deny — the 472 path must be entered
  deliberately.
- **472 shape** (`responses.py:146-170`): status 472, `X-Cage-Reason:
  injection-unavailable`, `X-Cage-Injected: <name>`, body
  `{error: "agent_cage_injection_unavailable", url, credential, how_to_proceed}`;
  golden `testdata/injection_unavailable_body.json.golden`. It carries **no**
  distinction between "token missing" and "token expired" and no retry hint.
- **Shipping surface**: the five runtime modules are enumerated twice in
  `internal/proxy/extract.go` — the `//go:embed` directive (`:30`) and
  `enforcerModules` (`:47`); `Extract()` writes them atomically into
  `<cache>/agent-creance/enforcer/`. A new `broker.py` module must be added to
  **both** lists.
- **Deps**: `requirements.txt` pins `mitmproxy==12.2.3` + `pytest==8.3.4` only.
  `asyncio` and `socket` are stdlib — a broker client adds nothing.
- **Test harness**: unit tests drive hooks by direct synchronous call
  (`addon.request(flow)`, `test_inject_hook.py:63`) under `taddons.context`, and
  set secrets by writing the private field (`addon._secrets = {...}`, `:60`).
  There is **no `pytest-asyncio`** in `requirements.txt` and no async test in the
  suite — making `request` a coroutine forces a harness change (add
  `pytest-asyncio`, or drive hooks through `asyncio.run`/the taddons loop).
  Integration spawns real `mitmdump` with `pass_fds=(r,)`
  (`test_integration.py:512-557`), and pins per-proxy isolation with two
  concurrent proxies holding distinct tokens under the same credential name
  (`test_integration.py:614-655`).

### 5. Existing Go seams, patterns, and what is missing

- **No unix-socket precedent at all.** No `net.Listen("unix", …)`, no `AF_UNIX`,
  no `net.UnixConn` anywhere. The only production `net.Listen` is TCP port
  allocation (`internal/sysdep/portallocator.go:47-66`). (`internal/sysdep/listener.go`
  is a name collision — it is an `lsof`-shelling scanner for doctor.)
- **No `os.Chmod` anywhere, and `sysdep.FileSystem` has no `Chmod` method**
  (`internal/sysdep/filesystem.go:19-44`). Permissions are set at creation only
  (`0o600` for the flock and the `.sb` fragments, `0o700` for `~/.claude` and
  `Layout.Root`, `0o755` for `dirPerm`). Note `proxy.lifecycle` does
  `MkdirAll(Layout.Root, 0o755)` (`lifecycle.go:138`) while `cage.Prepare` does
  `MkdirAll(Layout.Root, 0o700)` (`cage.go:239`) — whichever runs first wins, so
  the state root's mode is **not** reliably `0700` today. A `0700` socket dir
  must be created explicitly, not assumed.
- **The only mode-hardening in the tree is Python-side**: the audit writer's
  `os.open(..., 0o600)` + `os.fchmod(fd, 0o600)` (`internal/proxy/enforcer/audit.py:147-149`).
- **Seams that do exist**: `sysdep.Clock` (`Now`/`Since`, no ticker),
  `sysdep.Sleeper` (`Sleep(ctx, d)`, cancellable, fake records durations),
  `sysdep.Flock`, `sysdep.ProcessManager`, `sysdep.FileSystem` — every one an
  `App` field wired in `cli.Main()` (`internal/cli/cli.go:207-245`), with the
  trio pattern `sysdep/<name>.go` (interface + `OS*` impl) / `sysdeptest/<name>.go`
  (fake) / consumer-takes-interface.
- **Background-goroutine pattern**: `internal/configwatch` (`configwatch.go:55-188`)
  — `stop`/`done` channels + `sync.Once`, `Start` spawns `go w.loop(ctx)`, `Stop`
  closes and joins; wired advisory-on-failure with a deferred stop in `run`
  (`internal/cli/run.go:296-315`).
- **`golang.org/x/sys` is already a direct dep** (`unix.Flock`, `unix.SysctlRaw`),
  so `unix.Mlock` / `unix.GetsockoptXucred` / `unix.Setrlimit` need no new module.

### 6. Memory hygiene on macOS — what `mlock`-able/zeroizable actually buys

- **`mlock` works on darwin; `mlockall` does not.** XNU implements `mlock`/`munlock`
  via `vm_map_wire_kernel`; `mlockall` literally `return ENOSYS`. HashiCorp's
  `go-secure-stdlib/mlock` no-ops on darwin — but that is about `mlockall`
  (whole address space), which is the thing darwin lacks. `unix.Mlock(b []byte)`
  is available in `x/sys/unix` on darwin and requires no privileges (subject to a
  documented `RLIMIT_MEMLOCK`, which the XNU code path does not visibly enforce —
  worth an empirical check, moot for one page).
- **`memguard`** (v0.23.0, Aug 2025) does work on darwin (`memcall_darwin.go`
  calls `unix.Mlock`/`Mprotect`/`Mmap`) and offers guard pages, canaries, and
  XSalsa20Poly1305 encryption-at-rest of the buffer. Its README has **no
  limitations section** — it markets guarantees without bounding them, and the
  encryption key lives in the same address space as the ciphertext.
- **Zeroization in Go is partial by construction.** Go's GC is **non-moving**
  ("A Guide to the Go Garbage Collector") — the "GC copies your secret around the
  heap" claim is false. The real leak is **stack copying + register spills**
  (Filippo Valsorda: derived values are "scattered throughout registers and
  memory"; chasing them manually "won't work"). Go 1.26 shipped `runtime/secret`
  (`secret.Do` erases registers/stack/heap used by `f`) — but it is
  **linux/amd64 + linux/arm64 only, and a silent no-op elsewhere**, behind
  `GOEXPERIMENT=runtimesecret`. There is no `crypto/subtle` wipe.
- **macOS undercuts the swap argument**: T2/Apple-Silicon SSDs are hardware
  encrypted by default and `vm.swapusage` reports swap as `(encrypted)`. What
  `mlock` still does not stop: hibernation `sleepimage`, `task_for_pid`
  inspection by root/debugger, and core dumps (`RLIMIT_CORE=0` is the cheap fix).
- **Research verdict**: skip `memguard`; do best-effort `unix.Mlock` + explicit
  wipe (`[]byte`, never `string` — strings cannot be wiped and every conversion
  copies) + `RLIMIT_CORE=0`, and **document the residual risk honestly**: an
  attacker who can read the broker's memory has the token; the blast radius is
  bounded by TTL and scope (AC-0069a), not by memory protection.

### 7. Socket authentication on macOS — what works and what is theatre

- **APIs**: darwin uses `SOL_LOCAL` options, not `SO_PEERCRED`.
  `unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)` → `*unix.Xucred`
  (uid/gids, **no pid**); `unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)`
  → pid. Both reachable from a `*net.UnixConn` via `SyscallConn().Control(...)`.
- **A uid check buys nothing here**: the caged agent and `mitmdump` run as the
  **same uid**. `LOCAL_PEERCRED` reports the same value for both.
- **PID caveats**: both xucred and `LOCAL_PEERPID` are connect-time snapshots
  (`getpeereid(3)`: "the credentials … are those of its peer at the time it
  called connect(2)"), and PID-reuse is a documented, exploited macOS class
  (CVE-2020-14977; Apple's own XPC guidance is "use the audit token, not the
  pid"). **PID *pinning*** — comparing `LOCAL_PEERPID` against the pid the broker
  itself spawned — dodges most of that critique, since reuse requires the pinned
  process to have exited (at which point the broker should be tearing down).
  `LOCAL_PEERTOKEN` (the audit-token analogue) is undocumented and has no
  `x/sys/unix` wrapper — do not build on it.
- **Ecosystem baseline**: ssh-agent has *no* protocol auth at all
  (draft-miller-ssh-agent-17 §7: "ability to communicate with an agent is usually
  sufficient to invoke it"; mode 0600 in a 0700 dir is the entire control);
  gpg-agent adds an optional nonce cookie; `docker.sock` is the cautionary tale.
  For local sockets, **filesystem permissions are the primary control** —
  agent-creance is unusual only in that the adversary shares the uid, which is why
  the **sandbox profile**, not the file mode, is the real boundary.
- **A bearer token** handed to the addon over the existing fd-3 channel is cheap
  defence-in-depth against socket-path leakage (a future refactor mounting a
  broader path, a symlink trick). It must never land in argv, in `policy.json`,
  or in a repo file.

## Code References

- `internal/proxy/lifecycle.go:47-76` — `lockState` / `agentRef` (refcount shape).
- `internal/proxy/lifecycle.go:98-116` — `StartConfig.Secrets` contract + the
  spawn-only / Touch-ID rationale.
- `internal/proxy/lifecycle.go:137-221` — `Attach`: flock, prune, reuse-vs-spawn,
  secret resolution, spawn, readiness, refcount write.
- `internal/proxy/lifecycle.go:229-242` — `waitProxyReady` (Sleeper-driven poll).
- `internal/proxy/lifecycle.go:247-277` — `Detach`: last-out SIGTERM + cleared lock.
- `internal/proxy/lifecycle.go:495-507` — `mitmArgs` (`--set creance_secret_fd=3`).
- `internal/sysdep/processmanager.go:28-63` — `ProcessManager` interface, `SecretFD = 3`.
- `internal/sysdep/processmanager.go:87-117` — `SpawnWithSecret` (pipe, Setsid,
  Release, write-then-EOF goroutine).
- `internal/sysdep/flock.go:11-25`, `:49-67` — in-place flock discipline, `0o600`.
- `internal/sysdep/filesystem.go:19-44` — FileSystem seam (**no `Chmod`**).
- `internal/sysdep/portallocator.go:47-75` — the only production `net.Listen`.
- `internal/state/state.go:31-58`, `:246-298` — artifact names + `Layout` accessors
  (where a `broker.sock` accessor would go); `:116-128` — project hash → root.
- `internal/cli/inject.go:26-70` — `resolveInjectionSecrets` + `injectedCredentialNames`.
- `internal/cli/run.go:220-255`, `:296-320` — attach + lazy `Secrets` closure,
  configwatch wiring, blocking `cage.Run`.
- `internal/cage/cage.go:121-150` — mounts + the six `--append-profile` fragments.
- `internal/cage/cage.go:230-298` — `Prepare` (`MkdirAll(Layout.Root, 0o700)`).
- `internal/profile/profile.go:47`, `:75-77` — `(deny network*)` + the only allow form.
- `internal/verify/matrix.go:51-178` — the adversarial vector matrix (no socket vector).
- `internal/proxy/enforcer/enforcer.py:92-103` — `_secrets` dict + `_secrets_read`.
- `internal/proxy/enforcer/enforcer.py:107-143` — options (`creance_secret_fd`) + `configure`.
- `internal/proxy/enforcer/enforcer.py:145-169` — `running()` (asyncio task) / `done()`.
- `internal/proxy/enforcer/enforcer.py:197-211` — the 1s mtime poll loop.
- `internal/proxy/enforcer/enforcer.py:215-245` — `_read_secrets` (blocking read to EOF).
- `internal/proxy/enforcer/enforcer.py:294-337` — sync `request` hook + fail-closed
  `except` (→ **471**, not 472).
- `internal/proxy/enforcer/enforcer.py:339-373` — `_apply_injection` (shape+token
  lookup, 472, overwrite).
- `internal/proxy/enforcer/responses.py:146-170` — the 472 response.
- `internal/proxy/enforcer/policy.py:92-137` — `Credential` / `RuleSet` parse (reload).
- `internal/proxy/enforcer/audit.py:147-149` — the one `fchmod(0o600)` in the tree.
- `internal/proxy/extract.go:30`, `:47` — embed directive + `enforcerModules` (both
  need a new module).
- `internal/proxy/enforcer/test_inject_hook.py:45-53`, `:155-181` — `taddons`
  harness + fd-intake tests.
- `internal/proxy/enforcer/test_integration.py:512-557`, `:614-655` — real
  `mitmdump` + `pass_fds`; concurrent-proxy isolation.
- `internal/configwatch/configwatch.go:55-188` — the background-goroutine pattern.
- `go.mod:5-12` — `golang.org/x/sys` already a direct dep.

## Architecture Documentation

- **Reference-only artifacts.** `policy.json` carries `{source, header, template,
  username}` and rule `inject`/`in_cage` — never values (`policy.py:92-116`;
  compile fails closed on a dangling `inject`). A socket path or bearer token
  must not turn `policy.json` into a secret-bearing artifact.
- **Secrets never on disk / argv / env / logs** (`docs/design.md:631`) — pinned by
  fakes and resolver tests, not left to inspection
  (`thoughts/shared/reviews/2026-07-11-AC-0068-review.md:120-126`).
- **472 is per-request, never fail-to-spawn**
  (`thoughts/shared/plans/2026-07-03-AC-0068c-proxy-injection-engine.md:56-58`).
  A broker that is down must not stop the proxy from starting.
- **Python renders the header value; Go delivers the raw token** (same plan,
  `:50-54`). The broker changes *who holds* the token, not *who renders*.
- **Per-project isolation falls out of the architecture** (`docs/design.md:637`):
  one proxy, one secret set, one state dir per project hash — pinned e2e by
  `test_integration.py:614-655`. A broker must be per-project too; a
  cross-project broker would break the invariant that neutralizes the shared
  keychain.
- **Never an in-cage token endpoint** (the IMDS/SSRF lesson), stated in the
  discussion, the excluded-options list, and `docs/design.md:685`. A unix socket
  the cage cannot reach is not such an endpoint; a socket the cage *could* reach
  would be exactly the hole the whole design avoids — which is why the SBPL/mount
  argument in finding 3 has to become an asserted property, not an assumed one.
- **Status-code surface is pinned by literal**: any new/changed refusal status
  touches SKILL.md, `internal/cage/briefing.md`, `docs/design.md` and their marker
  tests (the AC-0047 checklist). Reusing 472 unchanged avoids that surface
  entirely.
- **Testing conventions**: new side effects (socket listen/dial, mlock, peer-cred
  getsockopt) belong behind `internal/sysdep` interfaces with `sysdeptest` fakes;
  real socket + real `mitmdump` paths go behind `//go:build integration` /
  `pytest -m integration`. External tools are never invoked in unit tests.
- **Dogfooding split**: real `mitmdump`-over-real-socket work cannot run inside
  the dogfooding cage — batch it as an out-of-cage phase.

## Historical Context (from thoughts/)

- `thoughts/shared/discussions/2026-06-28-credential-injection.md:317-321` — the
  one recorded Open Decision: "v1 uses inherited-fd (recorded as chosen); the
  unix-socket broker is the Phase-2 hardening for rotation and Go-side memory
  hygiene. Confirm at Phase-2 planning." Also `:84-85` (IMDS lesson), `:264-275`
  (excluded options: plaintext file, encrypted-at-rest file as "theater",
  per-request `op read`; broker deferred because "the inherited-fd channel is
  simpler and sufficient for static tokens"), `:73-79` (prior art: nono holds the
  secret in zeroizable memory; Envoy's overwrite/fail-closed vocabulary).
- `thoughts/shared/plans/2026-07-03-AC-0068c-proxy-injection-engine.md:50-58` —
  the locked decisions the broker inherits: Python renders / Go delivers; 472 is
  per-request; "no unix-socket broker … — Phase 2 (AC-0069b)".
- `thoughts/shared/reviews/2026-07-11-AC-0068-review.md:42-44`, `:84-87`,
  `:120-126` — the broker does **not** fix the `/graphql` public-read posture
  (lifetime bounds the leak window, not scope); the secret-hygiene and
  concurrent-proxy invariants are asserted by tests and must survive.
- `thoughts/shared/research/2026-07-11-AC-0069a-minted-short-lived-tokens.md:41-49`,
  `:601-611`, `:656-699` — the one-shot fd contract; delivery-channel options A–D;
  the eight open questions, of which "who owns the refresh loop" is the one a
  broker "would answer structurally".
- `thoughts/shared/tickets/AC-0069a-minted-short-lived-tokens.md:83-97`
  (uncommitted, 2026-07-12) — **the binding decisions from the paused AC-0069a
  planning round**: (1) broker first, fd-stream extension and per-refresh respawn
  rejected; (3) **stale-then-472-at-expiry**, which "requires expiry metadata at
  the injection side — an input to the AC-0069b broker protocol design"; (4)
  best-effort revocation on last-out `Detach`.
- `thoughts/shared/plans/2026-06-29-AC-0068a-secretresolver-seam.status.md:27-32`
  — empirical evidence that `(deny network*)` blocks socket `bind()` in-cage.
- `thoughts/shared/research/2026-06-05-s5-append-profile.md:218-222` — safehouse's
  base denies `unix-socket` remotes and a later `remote tcp` allow does not
  re-open them ("verified by construction").

## Related Research

- `thoughts/shared/research/2026-07-11-AC-0069a-minted-short-lived-tokens.md`
- `thoughts/shared/research/2026-07-02-AC-0068c-proxy-injection-engine.md`
- `thoughts/shared/research/2026-07-10-AC-0068e-github-flagship.md`
- `thoughts/shared/research/2026-06-29-AC-0068a-secretresolver-seam.md`

## Impact Analysis

The broker replaces the *delivery* half of Phase 1 while leaving *resolution*
(CLI, spawn-only) and *rendering* (Python, per request) where they are.

### Existing Usages Found

- `internal/proxy/lifecycle.go:186-191` — sole caller of `SpawnWithSecret`;
  chooses it iff `len(secret) > 0`.
- `internal/cli/run.go:241-245` — sole producer of the `Secrets` closure.
- `internal/cli/inject.go:26-54` (+ `inject_test.go`) — payload building;
  warn-and-omit semantics pinned by tests, including the hygiene assertion that
  the token never appears in the warning text.
- `internal/proxy/enforcer/enforcer.py:138-143`, `215-245` (+
  `test_inject_hook.py:155-181`) — the fd intake and its `_secrets_read` latch.
- `internal/proxy/enforcer/enforcer.py:339-373` (+ overwrite/472/annotation tests)
  — the per-request consumer of `self._secrets`.
- `internal/proxy/enforcer/test_integration.py:512-557`, `:614-655` — real-fd
  delivery and per-proxy isolation, both e2e.
- `internal/proxy/lifecycle_test.go:464-466` — asserts `SpawnWithSecret` was used
  and `creance_secret_fd=3` is in argv.
- `internal/sysdep/sysdeptest/processmanager.go:68-77` — the fake records
  `Spawned` + `Secrets`, which is how "the secret is never in argv" is asserted.

### Current Contract

- **Input**: compiled policy (reference-only credentials + rule `inject` names) +
  a `SecretResolver`.
- **Output**: flat JSON `{name: raw-token}`, written once to fd 3 at exec; the
  addon renders headers from token + shape per request.
- **Assumptions consumers make**: (a) secret values are fixed for the proxy's
  lifetime; (b) resolution happens only at spawn (interactive prompts allowed
  there, nowhere else); (c) values never touch disk/argv/env/logs; (d) a missing
  token is a per-request 472, never a spawn failure; (e) tokens are opaque
  strings; (f) each proxy holds only its own project's payload; (g) enforcer
  hooks are synchronous.

### Adaptation Requirements

- **A host process for the broker.** The proxy outlives the spawning CLI whenever
  a second agent is attached (`lifecycle.go:157-163`), so the broker cannot be a
  goroutine in the run session without introducing a "socket died under a live
  proxy → permanent 472" failure mode. Either a detached broker daemon with its
  own PID in `proxy.lock` and last-out SIGTERM alongside the proxy, or a change
  to how proxies are shared.
- **`internal/state`** — a `Layout.BrokerSock()` accessor (+ the `0700` dir it
  lives in; note `Layout.Root` is created `0755` by `lifecycle.go:138` and `0700`
  by `cage.go:239`, whichever runs first — the socket dir must be created
  explicitly). Plus the 104-byte `sun_path` ceiling.
- **`internal/sysdep`** — new seams: a unix-socket listener/dialer (nothing
  exists), file-mode control (`FileSystem` has no `Chmod`), and optionally
  `Mlock`/`Setrlimit` and `GetsockoptXucred`/`LOCAL_PEERPID`. Each needs a
  `sysdeptest` fake.
- **`internal/proxy/lifecycle.go`** — spawn/refcount/teardown the broker
  alongside the proxy; `lockState` grows a `broker_pid` (and the lock is the
  natural place, since it is already the flocked coordination point).
- **`SpawnWithSecret`** — its cargo changes: instead of `{name: token}` to
  Python, it plausibly carries `{socket_path, auth_token}` to Python and the raw
  secrets to the *broker*. The one-shot EOF contract survives; the payload shape
  and both its tests change.
- **`internal/proxy/enforcer/`** — a new `broker.py` (stdlib `asyncio`), added to
  the `//go:embed` list **and** `enforcerModules` (`extract.go:30`, `:47`); the
  `request` hook becomes `async def` (or the fetch moves to a background task);
  `_read_secrets`/`_secrets_read` are replaced; a broker error must map to **472**,
  not fall into the `except Exception` → 471 path.
- **Python test harness** — `pytest-asyncio` (or an explicit loop) once hooks are
  coroutines; `test_integration.py` gains a real-socket fixture.
- **`internal/verify/matrix.go`** — a BLOCKED vector proving the cage cannot
  connect to the broker socket, which is the ticket's "the cage cannot reach the
  socket" acceptance criterion made executable.
- **`docs/design.md`** — the "Credential injection" section describes the
  inherited-fd channel verbatim (`:631`); it becomes wrong the day the broker
  ships.

### Backward Compatibility Options

- **Option A: broker as a detached sibling daemon, refcounted in `proxy.lock`.**
  Lifetime matches the proxy exactly; the CLI resolves at spawn (Touch ID intact)
  and hands the secrets to the broker over the existing pipe. Pros: the only
  option whose lifetime is correct under proxy sharing; gives AC-0069a a home for
  the refresh loop (answering its open question structurally). Cons: a second
  daemon to spawn, health-check, and reap; a second PID to keep honest against
  crashes; more surface in `Attach`/`Detach`/`Clean`/`CleanOrphan`/doctor.
- **Option B: broker inside the run-session CLI process.** Pros: no new daemon;
  reuses the configwatch goroutine pattern; dies naturally with the session.
  Cons: **breaks under proxy sharing** — when the first agent exits while a second
  is attached, the proxy lives on but the socket vanishes → permanent 472 for the
  survivor. Would need broker-ownership handoff between CLI processes (a
  materially harder concurrency problem than the flocked refcount).
- **Option C: broker inside the `mitmdump` process (a Go helper spawned by the
  addon).** Cons: custody moves *into* the process whose memory hygiene the
  ticket exists to escape; no gain.
- **Option D (orthogonal): keep the fd path for static credentials, use the
  broker only for minted ones.** Sanctioned by the ticket's Out of Scope ("static
  tokens may continue to use the simpler path if justified at planning"). Pros:
  smaller blast radius; Phase-1 behavior untouched. Cons: two delivery channels,
  two Python intake paths, two sets of tests, and the memory-hygiene claim
  (secrets out of Python) holds for only half the credentials — the acceptance
  criterion "the value is wiped on shutdown" would be true of minted tokens only.

### Addon fetch models (all compatible with Option A)

- **A1: fetch per request, no cache.** Strongest reading of "never handing the
  raw secret to the addon for longer than a request needs"; rotation is instant;
  no in-flight race by construction. Costs one local socket round-trip per
  injected request (sub-millisecond, against an upstream HTTPS call) and forces
  the `request` hook to `async def`.
- **A2: cache `(token, expires_at)` in the addon, refresh on expiry with an
  `asyncio.Lock` (single-flight).** Fastest hot path; but the token then lives in
  Python memory for its whole TTL — i.e. the custody gain shrinks to "Python
  holds a short-lived token instead of a long-lived one" (which is real, but is
  AC-0069a's win, not the broker's).
- **A3: background prefetch task holding the current token in an attribute; the
  hook reads it with zero await.** Lowest latency, no async hook needed; same
  Python-residency property as A2, plus a supervisor requirement (a silently dead
  task serves a stale token forever).

## Open Questions

1. **Broker process model** — Option A (detached sibling daemon, refcounted in
   `proxy.lock`) vs Option B (run-session-owned). The proxy-sharing case makes B
   incorrect as stated; is A accepted, with the extra daemon it implies?
2. **Addon fetch model** — A1 (per-request fetch), A2 (cached with expiry), or A3
   (background prefetch)? This decides both how much custody the broker actually
   buys and whether the `request` hook becomes a coroutine (with the
   `pytest-asyncio` harness change that follows).
3. **Memory-hygiene ambition** — best-effort `unix.Mlock` + explicit wipe +
   `RLIMIT_CORE=0` with an honest "this is hygiene, not a control" note, vs
   pulling in `memguard`, vs wipe-only. The research verdict favours the first;
   the acceptance criterion says "`mlock`-able, zeroizable … wiped on shutdown",
   which the first satisfies literally.
4. **Scope of the migration** — do static credentials move to the broker too (one
   channel, one intake path, the fd carries only the handshake), or does Phase 1's
   fd-secret path survive for static tokens (Option D)?
5. **Authentication** — the uid check is theatre (same uid). Proposed:
   socket `0600` in a `0700` dir outside the cage + a bearer token delivered over
   the existing fd + optional PID pinning against the pid the broker spawned. Is
   the bearer token worth the surface, or are filesystem permissions + the SBPL
   deny (the ssh-agent model) sufficient?
6. **Asserting cage unreachability** — should the plan add an explicit SBPL
   `(deny network-outbound (literal "<socket>"))` fragment and an
   `internal/verify` BLOCKED vector, or rely on `(deny network*)` + the socket
   living outside every mount (true today, but untested)?
7. **Rotation semantics on the wire** — the protocol must carry `expires_at`
   (AC-0069a decision 3). Does it also carry a version/generation counter so the
   addon can detect a rotation mid-flight, and what does the broker answer when
   it holds no valid token (an explicit "unavailable" reply → 472, distinct from
   a socket error)?
8. **Broker crash semantics** — if the broker dies while the proxy lives, every
   injected request 472s (correct, fail-closed) but the cage is now permanently
   degraded. Does `doctor`/`status` surface it, and does anything restart it?

## tce Config Drift

None found. The two mismatches recorded in the AC-0069a research
(`internal/configwatch/` missing from the code map, and a stale `internal/cli`
command list) are **already fixed in the working tree** — `.claude/tce/profile.md`
carries uncommitted edits adding the `configwatch`, `gitremote`, and `style` rows
and the full command list. The profile now matches the codebase; the edits just
need committing.
