# AC-0069b: Unix-socket secret broker — Go-side custody + rotation channel

## Overview

Replace the one-shot inherited-fd secret delivery to the Python enforcer with a
**host-side Go broker daemon** that custodies every injected credential in
`mlock`-ed, zeroizable memory and serves it to the addon over a **unix socket**
that the cage provably cannot reach. The broker is the rotation-capable channel
AC-0069a's refresh loop will write into: its wire protocol carries `expires_at`
from day one, and its in-memory store is swappable at runtime without touching
the proxy.

## Current State Analysis

Phase 1 (AC-0068) delivers secrets exactly once, at exec time, and both ends are
structurally one-shot:

- Go writes the payload to a pipe and **closes it (EOF)**
  (`internal/sysdep/processmanager.go:87-117`), passing only the fd *number* in
  argv (`--set creance_secret_fd=3`, `internal/proxy/lifecycle.go:495-507`).
- The addon reads fd 3 to EOF in `configure()` behind a `_secrets_read` latch
  that is set **before** the read (`internal/proxy/enforcer/enforcer.py:127-143`,
  `215-245`), then closes the fd. There is no re-read path.
- Consequently the two halves of an injection refresh on different clocks: the
  credential *shape* every ~1s from `policy.json` (`enforcer.py:197-211`), the
  *token* never (`docs/design.md:631`).

The proxy itself is a **detached daemon** (`Setsid: true` + `Process.Release()`)
refcounted across CLI invocations through the flocked `proxy.lock`
(`internal/proxy/lifecycle.go:47-76`, `137-221`), and it deliberately outlives
its spawning CLI whenever a second agent is attached. Secret *resolution* is
spawn-only because `op://` can prompt for Touch ID
(`internal/proxy/lifecycle.go:108-116`).

Nothing in the repo has any unix-socket precedent: no `net.Listen("unix", …)`,
no `os.Chmod` (and `sysdep.FileSystem` has no `Chmod` method,
`internal/sysdep/filesystem.go:19-44`), no SBPL rule mentioning unix sockets, and
no `internal/verify` vector for one.

## Desired End State

`agent-creance run` in a project with `inject` rules spawns **two** host-side
daemons: the broker and `mitmdump`. The CLI resolves the secrets on the spawn
path (Touch ID intact) and hands them to the broker over the existing
`SpawnWithSecret` pipe. The broker `mlock`s them, serves them on
`~/.cache/agent-creance/projects/<hash>/broker.sock` (mode `0600` in a `0700`
dir), and wipes them on SIGTERM. The addon fetches the token from the socket
**per injected request**; a broker that is missing, unreachable, or holding no
valid credential yields the existing per-request **472**. The caged agent cannot
connect to the socket, and that is asserted by an SBPL deny fragment *and* an
adversarial `internal/verify` vector.

Verify: `make test` + `make test-enforcer` green; `make test-integration` and
`make test-enforcer-integration` green (out-of-cage); `agent-creance doctor`
reports broker health; the verify battery's new `broker-socket` vector reports
BLOCKED.

### Key Discoveries:

- **The broker cannot live in the run session.** The proxy outlives the spawning
  CLI when a second agent is attached (`internal/proxy/lifecycle.go:157-163`), so
  a run-session-owned broker would vanish under a live proxy and 472 forever.
  Decision: **detached sibling daemon**, PID in `proxy.lock`, SIGTERMed on
  last-out `Detach`.
- **`_apply_injection` runs inside `request`'s `except Exception` → 471**
  (`internal/proxy/enforcer/enforcer.py:294-337`, `339-373`). A broker client that
  raises would *hard-deny*, not 472. The 472 path must be entered deliberately.
- **The `request` hook is sync today** (`enforcer.py:294`) and there is **no
  `pytest-asyncio`** in `internal/proxy/enforcer/requirements.txt`. Per-request
  fetch forces `async def` on `request` (mitmproxy supports it and calls it the
  preferred style; a blocking hook stalls the whole proxy loop) and a harness
  change.
- **`Layout.Root`'s mode is not reliably `0700`**: `proxy/lifecycle.go:138` does
  `MkdirAll(root, 0o755)` while `cage/cage.go:239` does `0o700` — whichever runs
  first wins. The socket's parent dir mode must be set explicitly.
- **macOS caps `sun_path` at 104 bytes.** `~/.cache/agent-creance/projects/<16-hex>/broker.sock`
  is ~70 chars for a short `$HOME`, but a long username or `XDG_CACHE_HOME` can
  overflow it. `$TMPDIR` is **not** a fallback — it is writable in-cage.
- **A peer-uid check is theatre**: the caged agent runs as the same uid as
  `mitmdump`. Filesystem permissions plus unreachability are the control (the
  ssh-agent model, chosen by the user).
- **`mlock` works on darwin** (only `mlockall` is `ENOSYS`) and
  `golang.org/x/sys` is already a direct dep (`go.mod:5-12`) — no new module for
  `unix.Mlock`/`unix.Setrlimit`.
- **`internal/proxy/extract.go` lists the addon modules twice** — the `//go:embed`
  directive (`:30`) and `enforcerModules` (`:47`). A new `broker.py` must go in
  both.

## What We're NOT Doing

- **No minting, no refresh loop, no token expiry logic** — that is AC-0069a. This
  ticket ships the *channel*: the wire protocol carries `expires_at` and the
  store has a `Set` method, but nothing calls it yet and every credential
  delivered at spawn is non-expiring.
- **No revocation on teardown** (AC-0069a decision 4).
- **No `memguard`** — best-effort `unix.Mlock` + explicit wipe + `RLIMIT_CORE=0`,
  documented as hygiene rather than a control.
- **No bearer-token handshake** — the user chose filesystem permissions as the
  sole control. The socket path is therefore *not* a secret and rides argv.
- **No peer-uid / peer-pid check** (theatre at same uid).
- **No change to the 470/471/472 taxonomy** — the broker reuses 472 verbatim, so
  the AC-0047 pinned-literal surface is untouched.
- **No change to the `/graphql` public-read posture** (AC-0068 review; lifetime
  bounds the leak window, not scope).
- **No automatic broker restart** — fail closed, surface in doctor/status.

## Implementation Approach

Build the broker bottom-up behind `internal/sysdep` seams so every phase is
hermetically testable, then cut the delivery channel over in one move (Phase 3)
rather than running two channels in parallel: the fd payload stops going to
`mitmdump` and starts going to the broker, and `mitmdump` learns the socket path
from argv. Because the socket path is not a secret, the handshake needs no
protection — `--set creance_broker_sock=<path>` replaces `--set creance_secret_fd=3`.

Wire protocol — newline-delimited JSON, one request per connection (the addon
opens a connection per injected request, so there is no framing state to get
wrong and no in-flight rotation race by construction):

```
→ {"credential":"gh"}\n
← {"token":"ghs_…","expires_at":"2026-07-13T11:00:00Z"}\n     (expires_at omitted ⇒ no expiry)
← {"error":"unknown_credential"}\n                             (⇒ addon 472)
← {"error":"expired"}\n                                        (⇒ addon 472; AC-0069a will exercise this)
```

---

## Phase 1: Broker core + sysdep seams

### Overview

The broker's in-memory store, wire protocol, and the two new OS seams it needs —
all pure/unit-testable, nothing wired into the CLI yet.

### Changes Required:

#### 1. Memory-hygiene seam

**File**: `internal/sysdep/memory.go` (new) + `internal/sysdep/sysdeptest/memory.go` (new)
**Changes**: A `Memory` interface with `Lock(b []byte) error`, `Unlock(b []byte) error`,
`DisableCoreDumps() error`. `OSMemory` implements them with `unix.Mlock`,
`unix.Munlock`, and `unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{Cur: 0, Max: 0})`.
Compile-time assertion + a `sysdeptest.FakeMemory` recording locked buffers and
returning scripted errors. Follows the existing seam trio pattern
(`internal/sysdep/clock.go`, `sysdeptest/clock.go`).

Package doc states the bounded claim explicitly: mlock is best-effort hygiene on
macOS (encrypted swap, same-uid adversary, Go stack copying, `runtime/secret` is
a darwin no-op); the blast radius is bounded by TTL and scope, not by memory
protection. A `Lock` failure is logged and tolerated — it never fails the spawn.

#### 2. Unix-socket seam

**File**: `internal/sysdep/unixsocket.go` (new) + `internal/sysdep/sysdeptest/unixsocket.go` (new)
**Changes**: A `UnixSocket` interface with
`Listen(path string, perm os.FileMode) (net.Listener, error)` and
`Probe(path string) bool`. `OSUnixSocket.Listen` removes a stale socket file,
`net.Listen("unix", path)`, then `os.Chmod(path, perm)` (the listener creates it
`0755`-ish under umask; the chmod is what makes it `0600`). It rejects a path
whose length would overflow darwin's 104-byte `sun_path` with a named sentinel
error `ErrSocketPathTooLong`. `Probe` dials with a short timeout (mirroring
`sysdep.PortAllocator.Probe`, `internal/sysdep/portallocator.go:68-75`).

#### 3. Broker store

**File**: `internal/broker/store.go` (new)
**Changes**: `Store` holds `map[string]entry` under an `sync.RWMutex`, where
`entry` is `{token []byte, expiresAt time.Time}`. API: `NewStore(mem sysdep.Memory)`,
`Set(name string, token []byte, expiresAt time.Time)` (the rotation entry point
AC-0069a will call; `mlock`s the new buffer, wipes and unlocks the old one),
`Get(name string) (token []byte, expiresAt time.Time, ok bool)`, `Wipe()` (zero
every buffer, `Munlock`, drop the map). Tokens are `[]byte` throughout — never
`string`, which cannot be wiped.

#### 4. Wire protocol

**File**: `internal/broker/protocol.go` (new)
**Changes**: `Request{Credential string}` / `Response{Token string, ExpiresAt string, Error string}`
with JSON tags `credential` / `token` / `expires_at` / `error`, and the error
constants `ErrUnknownCredential = "unknown_credential"`, `ErrExpired = "expired"`.
`ExpiresAt` is RFC3339, omitted when zero. Encoding/decoding is
newline-delimited (`json.Decoder` / `json.Encoder` over the conn).

#### 5. Broker server

**File**: `internal/broker/server.go` (new)
**Changes**: `Server{store *Store, clock sysdep.Clock}` with
`Serve(ctx context.Context, ln net.Listener) error` — accept loop, one goroutine
per connection, decode one request, answer, close. Lookup misses answer
`unknown_credential`; a non-zero `expiresAt` at or before `clock.Now()` answers
`expired` (the AC-0069a hook — unexercised until minting lands). `Serve` returns
on context cancellation, closing the listener.

The server never logs a token value — only credential names and error kinds.

### Success Criteria:

#### Automated Verification:

- [x] `make test` passes (`go test -race ./...`)
- [ ] `make lint` passes (`go vet` + golangci-lint)
- [ ] `go build ./...` succeeds
- [ ] Table tests cover `Store.Set`/`Get`/`Wipe` including overwrite-wipes-old and
      Wipe-zeroes-the-buffer (assert the underlying `[]byte` is all zeros)
- [ ] Table tests cover the protocol round-trip: hit, unknown credential, expired
      (with a `sysdeptest.FakeClock` advanced past `expires_at`), omitted
      `expires_at` ⇒ no expiry
- [ ] `Server.Serve` is tested over a `net.Pipe()`-backed or `t.TempDir()` unix
      listener (a real socket in a temp dir is hermetic — no external tool)
- [ ] `sysdeptest.FakeMemory` records `Lock`/`Unlock` calls; a `Lock` failure is
      tolerated (store still serves)
- [ ] `ErrSocketPathTooLong` is returned for a path ≥104 bytes

#### Manual Verification:

- [x] None for this phase (no user-visible surface yet)

### Implementation log

**Status:** complete
**Base commit:** 0d00e23
**Phase commit:** (this commit)

Added `internal/sysdep/memory.go` + `unixsocket.go` (with `sysdeptest` fakes) and
the `internal/broker` package: `Store` (mlock + wipe-on-replace + wipe-on-shutdown,
tokens as `[]byte` throughout), the newline-delimited JSON protocol, and `Server`.

Two things the tests caught:

- The `sun_path` guard fires on macOS `t.TempDir()` paths — they embed the test
  name on top of an already-long `/var/folders/…` TMPDIR and overshoot 104 bytes.
  Tests use a short `os.MkdirTemp("", "ac")` dir instead. This is *not* only a test
  artifact: it confirms the production path-length check is load-bearing, and that
  a long `$HOME` or `XDG_CACHE_HOME` can genuinely overflow it.
- The readiness `Probe` (dial-and-hang-up) was reaching `handle` and logging
  "malformed request". An `io.EOF` on the first decode is now a silent drop.

`make test` and `make lint` green.

---

## Phase 2: Broker daemon + proxy lifecycle integration

### Overview

Spawn the broker as a detached sibling of `mitmdump`, refcount it in `proxy.lock`,
and tear it down on last-out `Detach`.

### Changes Required:

#### 1. State layout

**File**: `internal/state/state.go`
**Changes**: Add `brokerSock = "broker.sock"` to the artifact-name constants
(`:31-58`) and a `Layout.BrokerSock()` accessor alongside `ProxyLock()` (`:284`).
Path-only, no I/O (the package does none).

#### 2. The `broker` command

**File**: `internal/cli/broker.go` (new)
**Changes**: A **hidden** cobra command `agent-creance broker --socket <path>`.
It is the daemon entrypoint, never run by a user directly. It:

1. calls `app.Memory.DisableCoreDumps()` (best-effort, warn on failure);
2. reads the secret payload from `sysdep.SecretFD` (fd 3) to EOF — the same
   one-shot contract as today, now Go→Go — and parses the flat
   `{name: raw-token}` JSON;
3. builds a `broker.Store`, `Set`s every credential with a zero `expiresAt`
   (static tokens do not expire);
4. `Listen`s on `--socket` with mode `0600` via the `UnixSocket` seam;
5. installs a SIGTERM/SIGINT handler that cancels the serve context; on return,
   `Wipe()`s the store and removes the socket file.

An empty or malformed payload is **not** fatal: the broker serves an empty store
(every lookup ⇒ `unknown_credential` ⇒ 472), consistent with Phase-1's
fail-closed-per-request rule. It logs the exception kind only, never the payload.

#### 3. Lifecycle: spawn, refcount, teardown

**File**: `internal/proxy/lifecycle.go`
**Changes**:

- `lockState` (`:47-67`) gains `BrokerPID int \`json:"broker_pid"\``.
- `StartConfig` gains `SelfExe string` (the `agent-creance` binary path, from
  `os.Executable()` in the composition root) and `Memory`/`UnixSocket` are *not*
  needed here — only the probe: the manager takes the existing `sysdep.UnixSocket`
  for readiness.
- On the **spawn branch** (`:163-205`), when `len(secret) > 0`:
  1. compute `sock := cfg.Layout.BrokerSock()`; `MkdirAll(Layout.Root, 0o700)`
     (tighten from the current `0o755` at `:138`) and, if the socket path is too
     long, **warn and skip the broker** — the proxy still spawns and the addon
     472s (never fail-to-spawn);
  2. `SpawnWithSecret(ctx, secret, cfg.SelfExe, "broker", "--socket", sock)` →
     `brokerPID`;
  3. `waitBrokerReady` — the `waitProxyReady` shape (`:229-242`): ≤100 ×
     `sock.Probe(path)` with a 50ms `Sleeper` gap, hard error if the broker exits
     during startup;
  4. spawn `mitmdump` with **`Spawn`** (no secret) and the new arg.
- `mitmArgs` (`:495-507`): replace `--set creance_secret_fd=3` with
  `--set creance_broker_sock=<path>`, appended iff a broker was started.
- `Detach` last-out (`:263`): SIGTERM the broker **after** the proxy, and remove
  the socket file best-effort. Same in `CleanOrphan` (`:361-388`) and `Clean`
  (`:398-435`); `Inspect` (`:312-339`) reports the broker PID.
- Reuse branch unchanged — `cfg.Secrets` is still never called (Touch-ID guard).

#### 4. Composition root

**File**: `internal/cli/cli.go`, `internal/cli/run.go`
**Changes**: `App` gains `Memory sysdep.Memory` and `UnixSocket sysdep.UnixSocket`,
wired in `cli.Main()` (`:207-245`). `runRun` passes `SelfExe` into `StartConfig`.
Register the hidden `broker` command in the command tree.

### Success Criteria:

#### Automated Verification:

- [ ] `make test` passes
- [ ] `make lint` passes
- [ ] Lifecycle test: with secrets present, the fake ProcessManager records a
      `SpawnWithSecret` for `broker` (argv contains `--socket …`) **and** a plain
      `Spawn` for `mitmdump` whose argv contains `--set creance_broker_sock=…`
      and **no** `creance_secret_fd`
- [ ] Lifecycle test: the secret appears in the fake's recorded `Secrets`, and
      **not** in any recorded argv (the existing hygiene assertion, retargeted)
- [ ] Lifecycle test: with no secrets, no broker is spawned and no
      `creance_broker_sock` arg is emitted
- [ ] Lifecycle test: reuse branch spawns neither daemon and never calls `Secrets`
- [ ] Lifecycle test: last-out `Detach` SIGTERMs both PIDs; a non-last `Detach`
      SIGTERMs neither
- [ ] Lifecycle test: an over-long socket path warns, skips the broker, and still
      spawns the proxy (no `creance_broker_sock` arg)
- [ ] `broker` command testscript (`internal/cli/testdata/script/broker.txtar`):
      hidden from `--help`; refuses to start without `--socket`
- [ ] Golden update for `proxy.lock` shape if one exists; `make golden` diff reviewed

#### Manual Verification:

- [ ] `agent-creance run` in a project with an `inject` rule starts a broker
      process (`ps` shows `agent-creance broker --socket …`) and the socket exists
      with mode `0600` in a `0700` dir
- [ ] Exiting the last agent leaves no broker process and no socket file behind

### Implementation log

**Status:** complete (automated criteria; manual items pending user verification)
**Phase commit:** (this commit)

`Layout.BrokerSock()`, the hidden `broker` command, and the lifecycle integration:
`lockState.BrokerPID`, broker-before-proxy spawn, `waitBrokerReady` (the
`waitProxyReady` shape), and teardown in `Detach`/`CleanOrphan`/`Clean`.
`Inspect` gained `BrokerPID`/`BrokerUp`/`BrokerDown` for Phase 4's doctor surface.

Deviations from the plan, both forced by the code:

- **`sysdep.FileSystem` gained `Chmod`.** The plan said "the socket dir mode must
  be set explicitly" without saying how; `MkdirAll` only applies its mode to dirs
  it *creates*, so a state dir left at `0755` by an earlier binary would have
  stayed that way. The seam had no `Chmod` at all.
- **`sysdeptest.FakeProcessManager` gained a `SpawnPIDs` queue.** With two daemons
  spawned per attach, a single `SpawnPID` cannot distinguish them.

The old `TestAttachDeliversInjectionSecretOnSpawn` was rewritten rather than
deleted: it now asserts the secret reaches the *broker* over the fd, that
`mitmdump` gets only `creance_broker_sock` (and no `creance_secret_fd`), and — the
Phase-1 hygiene assertion, retained verbatim in spirit — that the token appears in
no argv.

`make test` and `make lint` green.

---

## Phase 3: Enforcer cut-over to the broker

### Overview

Replace the addon's fd intake with a per-request unix-socket fetch, and make the
`request` hook a coroutine.

### Changes Required:

#### 1. Broker client

**File**: `internal/proxy/enforcer/broker.py` (new)
**Changes**: A stdlib-only (`asyncio`, `json`) client:
`async def fetch(sock_path: str, credential: str, timeout: float) -> tuple[str | None, str | None]`
returning `(token, error)`. Opens `asyncio.open_unix_connection(sock_path)`,
writes the newline-delimited request, reads one line, closes the writer and
`await writer.wait_closed()` (a leaked writer per request exhausts fds). Any
`OSError`/`asyncio.TimeoutError`/`ValueError` becomes an `error` string — the
client **never raises**, so it can never be mistaken for the 471 fail-closed
path. It never logs the token.

Mirrors the module discipline of `policy.py`/`inject.py`/`responses.py`: no
mitmproxy import, so it is unit-testable without mitmproxy installed.

#### 2. Addon

**File**: `internal/proxy/enforcer/enforcer.py`
**Changes**:

- Options (`:107-125`): drop `creance_secret_fd`, add `creance_broker_sock` (str,
  default `""`).
- `configure` (`:127-143`): drop the fd branch and the `_secrets_read` latch;
  store `self._broker_sock`.
- Delete `_secrets`, `_read_secrets` (`:215-245`) and the `_secrets` field
  (`:99-103`).
- `request` (`:294`) becomes `async def`; `_apply_injection` becomes
  `async def` and is `await`ed. The `except Exception` fail-closed envelope
  (`:330-337`) stays exactly as it is — it is the 471 net for genuine bugs.
- `_apply_injection` (`:339-373`): after the `in_cage` / no-`inject` / missing-shape
  checks, `token, err = await broker.fetch(self._broker_sock, rule.inject, _BROKER_TIMEOUT)`.
  No socket configured, an `err`, or a `None` token ⇒ the **existing**
  `responses.injection_unavailable` 472, unchanged. Otherwise render + overwrite +
  stash `flow.metadata["creance_injected"]` exactly as today.
- `_BROKER_TIMEOUT = 2.0` seconds — a local socket that does not answer in 2s is a
  dead broker, and the caged agent gets a 472 rather than a hang.

#### 3. Shipping surface

**File**: `internal/proxy/extract.go`
**Changes**: add `enforcer/broker.py` to the `//go:embed` directive (`:30`) **and**
`broker.py` to `enforcerModules` (`:47`).

#### 4. Python test harness

**File**: `internal/proxy/enforcer/requirements.txt`, `conftest.py`,
`test_inject_hook.py`, `test_broker.py` (new)
**Changes**: add `pytest-asyncio` (pinned) and configure `asyncio_mode = auto` in
`conftest.py`. `test_inject_hook.py`'s direct hook calls become `await
addon.request(flow)` and its tests `async def`; the fd-intake tests
(`:155-181`) are replaced by broker-fetch tests. Secrets are no longer injected
by writing `addon._secrets` — tests stand up a **stub asyncio unix server** in
`tmp_path` and point `creance_broker_sock` at it. `test_broker.py` covers the
client against that stub: hit, `unknown_credential`, `expired`, no socket file,
server that never answers (timeout), truncated reply.

### Success Criteria:

#### Automated Verification:

- [ ] `make test-enforcer` passes (pinned-mitmproxy venv)
- [ ] `make test` passes (the Go embed/extract tests see the new module)
- [ ] `broker.py` imports without mitmproxy installed (pure-module discipline)
- [ ] Hook tests: a broker hit **overwrites** a client-supplied header (the
      existing overwrite assertion, now sourced from the socket)
- [ ] Hook tests: `unknown_credential`, `expired`, missing socket file, and a
      broker timeout each produce a **472** with the unchanged body/headers — and
      **not** a 471
- [ ] Hook tests: an `in_cage` rule still never touches the auth header, and no
      broker fetch is attempted for it
- [ ] Hook tests: upstream 401/403 on an injected flow still carries
      `X-Cage-Injected`
- [ ] 472 golden (`testdata/injection_unavailable_body.json.golden`) is byte-identical
- [ ] `grep -r creance_secret_fd internal/` returns nothing outside history

#### Manual Verification:

- [ ] With the cage running, an injected host still authenticates end-to-end
- [ ] `kill` the broker mid-session: the next injected request returns 472 with
      the human-recoverable body, and non-injected hosts keep working

---

## Phase 4: Cage unreachability, asserted

### Overview

Make "the cage cannot reach the socket" an executable property rather than an
argument from construction — an SBPL deny **and** an adversarial probe.

### Changes Required:

#### 1. SBPL fragment

**File**: `internal/profile/profile.go`, `internal/profile/testdata/broker.golden` (new)
**Changes**: `RenderBrokerDenyFragment(sockPath string) string` emitting

```
;; agent-creance broker.sb — the credential broker's socket is host-side only (AC-0069b).
(deny network-outbound (literal "<sockPath>"))
(deny file-read* file-write* (literal "<sockPath>"))
```

`network-outbound` with a path literal is the operation macOS uses for a unix-socket
`connect(2)` (the Chromium `common.sb` form). This is belt-and-braces over
`(deny network*)` (`profile.go:47`) — the value is that it survives a future
change that mounts a broader path into the cage. Golden test as for the other
renderers; the path goes through the same `sanitizeLabel` control-char defence.

#### 2. Cage wiring

**File**: `internal/state/state.go`, `internal/cage/cage.go`
**Changes**: `Layout.BrokerProfileSB()` (→ `broker.sb`) alongside `ProxyProfileSB()`;
`Builder.Prepare` (`:230-298`) writes it `0600` like its siblings; `Build`
(`:143-150`) appends a seventh `--append-profile`. Update
`internal/cage/testdata/invocation.golden.json`.

#### 3. Adversarial vector

**File**: `internal/verify/matrix.go`, `internal/verify/testdata/fake-agent.sh`
**Changes**: a new **BLOCKED** vector `broker-socket` — the fake agent attempts
`nc -U <sock>` (falling back to a python one-liner if `nc -U` is unavailable) and
reports `connect-ok` on success. `Evaluate`/`isLeak` (`internal/verify/battery.go:61-109`)
already treat `connect-ok` on a BLOCKED vector as an escape, so no evaluator
change is needed. The socket path is passed to the battery the same way the proxy
port is.

#### 4. Doctor / status

**File**: `internal/doctor/`, `internal/status/`
**Changes**: report broker health for a project with injected credentials —
socket present, dialable (`UnixSocket.Probe`), broker PID alive. A live proxy with
a dead broker is a **warning** with the actionable text ("the cage is degraded:
injected hosts answer 472; restart the session"), not an error, since it is the
correct fail-closed state.

### Success Criteria:

#### Automated Verification:

- [ ] `make test` passes; `make golden` diff reviewed (broker.sb golden, cage
      invocation golden)
- [ ] `RenderBrokerDenyFragment` golden test passes; a control character in the
      path cannot terminate the comment or smuggle a live SBPL form
- [ ] Doctor/status tests: dead broker + live proxy ⇒ warning, not error; no
      broker configured (no inject rules) ⇒ no report at all
- [ ] The verify matrix lists `broker-socket` as BLOCKED and the battery treats
      `connect-ok` on it as an escape (unit test over the evaluator)

#### Manual Verification:

- [ ] `agent-creance verify` (live battery, out-of-cage) reports `broker-socket`
      BLOCKED
- [ ] Inside a cage, `nc -U ~/.cache/agent-creance/projects/*/broker.sock` fails

---

## Phase 5: Integration, docs, and the honest write-up

### Overview

Real broker + real `mitmdump` + real socket, end to end; then correct every doc
that describes the inherited-fd channel.

**This phase must run out-of-cage** (real `mitmdump`, real `agent-safehouse`) —
batch it as one breakout per the project's dogfooding rule.

### Changes Required:

#### 1. Go integration test

**File**: `internal/proxy/broker_integration_test.go` (new, `//go:build integration`)
**Changes**: build the real binary, spawn a real broker over a real socket, spawn a
real `mitmdump` with the enforcer pointed at it, and assert: an injected request
carries the header; killing the broker turns the next request into a 472; the
socket is `0600` and its dir `0700`; SIGTERM removes the socket file.

#### 2. Python integration test

**File**: `internal/proxy/enforcer/test_integration.py`
**Changes**: `running_proxy_with_secret` (`:512-557`) is replaced by
`running_proxy_with_broker`, which starts a **stub asyncio unix broker** (not the
Go binary — the Python suite stays self-contained) and passes
`--set creance_broker_sock=…`. The four injection e2e tests carry over unchanged
in intent; **`test_concurrent_proxies_hold_distinct_secrets_e2e` (`:614-655`) must
survive** — two proxies, two brokers, two sockets, distinct tokens under the same
credential name. That is the per-project isolation invariant.

#### 3. Docs

**File**: `docs/design.md`
**Changes**: rewrite the "Credential injection" delivery paragraph (`:631`) — the
inherited fd now delivers Go→broker, and the addon fetches per request over the
unix socket. State the custody claim honestly (mlock is hygiene, not a control;
same-uid adversary; TTL and scope are the real bounds). Note that the socket is
host-side only and unreachable from the cage, and that this is now asserted by
`broker.sb` + the `broker-socket` verify vector. Record that the Phase-1 Open
Decision on delivery-channel evolution is **resolved**.

**File**: `internal/cage/briefing.md`, `internal/setup/skill.go` — no change: the
472 semantics and text are unchanged (deliberately, to avoid the AC-0047 surface).

### Success Criteria:

#### Automated Verification:

- [ ] `make test-integration` passes (out-of-cage)
- [ ] `make test-enforcer-integration` passes (out-of-cage)
- [ ] `make test` and `make lint` still green
- [ ] `make build` produces `bin/agent-creance` at the final commit
- [ ] The concurrent-proxy isolation e2e still passes with two brokers

#### Manual Verification:

- [ ] A real dogfood run against GitHub still authenticates through the injected
      credential
- [ ] `docs/design.md` describes the shipped channel accurately, including the
      bounded memory-custody claim

---

## Testing Strategy

### Unit Tests:

- **Go**: table tests for the store (set/get/wipe/overwrite, zeroization asserted
  on the underlying buffer), the protocol (hit / unknown / expired / no-expiry),
  the server over a hermetic temp-dir socket, the socket-path-length guard, and
  the lifecycle's spawn/refcount/teardown branches with the fake ProcessManager.
- **Python**: the broker client against a stub asyncio unix server (hit, unknown,
  expired, absent socket, timeout, truncated reply) and the async hook (472 vs
  471 discrimination, overwrite, in-cage untouched, upstream-401 annotation).

### Integration Tests:

- Real broker + real `mitmdump` + real socket (Go, `integration` tag).
- Two concurrent proxies with two brokers holding distinct tokens under the same
  credential name (Python, `pytest -m integration`).
- The live verify battery's `broker-socket` BLOCKED vector, inside a real cage.

### Manual Testing Steps:

1. `agent-creance run` in a project with an `inject` rule; confirm two daemons and
   a `0600` socket in a `0700` dir.
2. Make an injected request; confirm it authenticates.
3. `kill` the broker; confirm the next injected request is a 472 with the
   human-recoverable body, and that non-injected hosts still work.
4. `agent-creance doctor`; confirm the dead broker is surfaced as a warning.
5. Exit the last agent; confirm no broker process and no socket file remain.
6. Inside the cage, try to connect to the socket; confirm refusal.

## Performance Considerations

One unix-socket round-trip per **injected** request (not per request — passthrough
and non-inject hosts never touch the broker). A local AF_UNIX round-trip is
sub-millisecond against an upstream HTTPS call that is orders of magnitude slower,
so the cost is not measurable in practice. The per-request fetch is what buys
instant rotation and no in-flight race, and it keeps the token out of Python
memory except for the moment a request needs it. The 2s client timeout bounds the
worst case (a hung broker) at a 472 rather than a stall.

## Migration Notes

No persisted state changes shape in a breaking way: `proxy.lock` gains a
`broker_pid` field, and the existing corrupt/empty-lock tolerance
(`internal/proxy/lifecycle.go:517-523`) means a lock written by an older binary
simply deserializes with `BrokerPID == 0` — treated as "no broker", so the next
`Attach` spawns one. A proxy left running by an older binary keeps its fd-delivered
secrets until it is torn down; the new addon and the old proxy never meet, because
the enforcer is extracted fresh per run and a running proxy is only reused with
its own already-loaded addon.

## References

- Original ticket: `thoughts/shared/tickets/AC-0069b-secret-broker.md`
- Epic: `thoughts/shared/tickets/AC-0069-credential-injection-phase2-epic.md`
- Research: `thoughts/shared/research/2026-07-13-AC-0069b-secret-broker.md`
- Sibling (consumes this channel): `thoughts/shared/tickets/AC-0069a-minted-short-lived-tokens.md`
  — its 2026-07-12 decisions bind this plan (broker first; the protocol carries
  `expires_at`).
- Phase-1 delivery as shipped: `thoughts/shared/plans/2026-07-03-AC-0068c-proxy-injection-engine.md`
- Founding discussion (the Open Decision this resolves):
  `thoughts/shared/discussions/2026-06-28-credential-injection.md:317-321`
- Patterns to follow: `internal/proxy/lifecycle.go:229-242` (readiness poll),
  `internal/configwatch/configwatch.go:55-188` (background goroutine),
  `internal/sysdep/clock.go` + `sysdeptest/clock.go` (seam trio),
  `internal/proxy/enforcer/audit.py:147-149` (the one existing `0600` hardening)
