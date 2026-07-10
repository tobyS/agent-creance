"""End-to-end integration probes: a real mitmdump running the addon, driven by curl.

Gated on spike S1 (CA trust) and marked ``integration`` so it runs only under
``make test-integration`` / ``make test-enforcer-integration``, never in the fast
suite. Verifies the four wire outcomes against a live proxy:

  - allow       -> upstream response, no X-Cage-Reason.
  - soft-deny   -> 470 + X-Cage-Reason: soft-deny + agent_cage_not_allowlisted body.
  - hard-deny   -> 471 + X-Cage-Reason: hard-deny + the deny reason.
  - passthrough -> tunnelled; TLS validates against the REAL upstream cert (the
                   client trusting only the system CA, NOT mitmproxy's CA).

Plus hot reload: a previously soft-denied host becomes allowed within ~1s of the
policy file changing, with no restart.

Skips cleanly when prerequisites are unavailable (curl missing, or no network
egress), mirroring the Go integration-test convention.
"""

import json
import os
import socket
import subprocess
import sys
import tempfile
import threading
import time
from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest

pytestmark = pytest.mark.integration

_CURL = "/usr/bin/curl" if os.path.exists("/usr/bin/curl") else None
_MITMDUMP = os.path.join(os.path.dirname(sys.executable), "mitmdump")
_ENFORCER = os.path.join(os.path.dirname(os.path.abspath(__file__)), "enforcer.py")

# Real hosts used for the egress-requiring outcomes (allow / passthrough / reload).
_ALLOW_HOST = "example.com"
_PASSTHROUGH_HOST = "example.org"


def _require_tooling():
    if _CURL is None:
        pytest.skip("curl not available")
    if not os.path.exists(_MITMDUMP):
        pytest.skip(f"mitmdump not found at {_MITMDUMP} (run `make enforcer-venv`)")


def _egress_ok() -> bool:
    try:
        subprocess.run(
            [_CURL, "-sS", "-o", os.devnull, "--max-time", "8", f"https://{_ALLOW_HOST}/"],
            check=True,
            capture_output=True,
        )
        return True
    except Exception:
        return False


def _free_port() -> int:
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def _write_policy(path, obj):
    with open(path, "w", encoding="utf-8") as f:
        json.dump(obj, f)


class _Proxy:
    def __init__(self, port, ca_cert, policy_path, audit_path):
        self.port = port
        self.ca_cert = ca_cert
        self.policy_path = policy_path
        self.audit_path = audit_path


@contextmanager
def running_proxy(policy_obj):
    _require_tooling()
    tmp = tempfile.mkdtemp(prefix="creance-enforcer-it-")
    confdir = os.path.join(tmp, "conf")
    policy_path = os.path.join(tmp, "policy.json")
    audit_path = os.path.join(tmp, "egress.jsonl")
    _write_policy(policy_path, policy_obj)
    port = _free_port()

    proc = subprocess.Popen(
        [
            _MITMDUMP,
            "--set", f"confdir={confdir}",
            "-p", str(port),
            "-s", _ENFORCER,
            "--set", f"creance_policy={policy_path}",
            "--set", f"creance_audit_log={audit_path}",
        ],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    ca_cert = os.path.join(confdir, "mitmproxy-ca-cert.pem")
    try:
        _wait_until_ready(proc, port, ca_cert)
        yield _Proxy(port, ca_cert, policy_path, audit_path)
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
        import shutil

        shutil.rmtree(tmp, ignore_errors=True)


def _wait_until_ready(proc, port, ca_cert, timeout=20.0):
    deadline = time.time() + timeout
    while time.time() < deadline:
        if proc.poll() is not None:
            raise RuntimeError(f"mitmdump exited early with code {proc.returncode}")
        listening = False
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.3):
                listening = True
        except OSError:
            listening = False
        if listening and os.path.exists(ca_cert):
            return
        time.sleep(0.2)
    raise RuntimeError("mitmdump did not become ready in time")


def _curl(proxy, url, *, use_mitm_ca, http1=False, timeout=20, extra=()):
    """Drive curl through the proxy. Returns (returncode, http_code, headers, body).

    http1=True forces HTTP/1.1 to the origin so the status line carries the reason
    phrase (HTTP/2 drops reason phrases entirely). ``extra`` appends verbatim curl
    args (e.g. a client-supplied ``-H`` header for the overwrite test).

    ``--noproxy ""`` empties curl's bypass list, which otherwise defaults to the
    ambient NO_PROXY. That matters when the suite runs inside an agent-creance cage:
    the cage exports NO_PROXY=localhost,127.0.0.1,::1 so in-cage tooling can reach
    host services directly, and curl honours it even when -x is given — silently
    bypassing the proxy under test and reducing these assertions to nothing."""
    with tempfile.TemporaryDirectory() as d:
        body_path = os.path.join(d, "body")
        hdr_path = os.path.join(d, "hdr")
        cmd = [
            _CURL, "-sS",
            "-x", f"http://127.0.0.1:{proxy.port}",
            "--noproxy", "",
            "--max-time", str(timeout),
            "-o", body_path, "-D", hdr_path,
            "-w", "%{http_code}",
            url,
        ]
        if http1:
            cmd.append("--http1.1")
        if use_mitm_ca:
            cmd += ["--cacert", proxy.ca_cert]
        cmd += list(extra)
        res = subprocess.run(cmd, capture_output=True, text=True)
        http_code = res.stdout.strip()
        headers = _read(hdr_path)
        body = _read(body_path)
    return res.returncode, http_code, headers, body


def _read(path):
    try:
        with open(path, encoding="utf-8", errors="replace") as f:
            return f.read()
    except FileNotFoundError:
        return ""


def _wait_for_audit(path, predicate, timeout=5.0):
    """Poll the JSONL audit file until an entry satisfies ``predicate`` (the proxy
    writes it from a hook just after the curl returns). Returns the matching entry,
    or None on timeout."""
    deadline = time.time() + timeout
    while time.time() < deadline:
        for line in _read(path).splitlines():
            try:
                entry = json.loads(line)
            except ValueError:
                continue
            if predicate(entry):
                return entry
        time.sleep(0.2)
    return None


# --- outcomes that need no egress (mitmproxy terminates TLS locally) -----------

_DENY_POLICY = {
    "version": 1,
    "allow": [{"host": _ALLOW_HOST, "mode": "intercept"}],
    "deny_always": [
        {"host": "blocked.test", "mode": "intercept", "reason": "Blocked for testing."}
    ],
}


def test_soft_deny_470():
    with running_proxy(_DENY_POLICY) as p:
        rc, code, headers, body = _curl(
            p, "https://not-allowlisted.test/v2/auth/", use_mitm_ca=True
        )
    assert rc == 0, f"curl failed: {headers}{body}"
    assert code == "470"
    assert "x-cage-reason: soft-deny" in headers.lower()
    assert json.loads(body)["error"] == "agent_cage_not_allowlisted"


def test_hard_deny_471():
    with running_proxy(_DENY_POLICY) as p:
        rc, code, headers, body = _curl(p, "https://blocked.test/anything", use_mitm_ca=True)
    assert rc == 0, f"curl failed: {headers}{body}"
    assert code == "471"
    assert "x-cage-reason: hard-deny" in headers.lower()
    assert json.loads(body)["reason"] == "Blocked for testing."


def test_refusal_reason_phrase_on_the_wire_http1():
    # Over HTTP/1.1 the status line carries the reason phrase (AC-0050); HTTP/2
    # drops it, so force h1.1 to observe what an echoing client/log would see.
    with running_proxy(_DENY_POLICY) as p:
        _, _, soft_headers, _ = _curl(
            p, "https://not-allowlisted.test/v2/auth/", use_mitm_ca=True, http1=True
        )
        _, _, hard_headers, _ = _curl(
            p, "https://blocked.test/anything", use_mitm_ca=True, http1=True
        )
    assert "470 agent-creance soft-deny (not allowlisted)" in soft_headers
    assert "471 agent-creance hard-deny (blocked)" in hard_headers


# --- outcomes that need real egress -------------------------------------------


@pytest.fixture(scope="module")
def egress():
    _require_tooling()
    if not _egress_ok():
        pytest.skip("no network egress to the test hosts")


def test_allow_forwards_upstream(egress):
    with running_proxy(_DENY_POLICY) as p:
        rc, code, headers, body = _curl(p, f"https://{_ALLOW_HOST}/", use_mitm_ca=True)
    assert rc == 0, f"curl failed: {headers}{body}"
    assert code == "200"
    assert "x-cage-reason" not in headers.lower()


def test_passthrough_validates_real_cert(egress):
    policy_obj = {
        "version": 1,
        "allow": [{"host": _PASSTHROUGH_HOST, "mode": "passthrough"}],
    }
    with running_proxy(policy_obj) as p:
        # System CA only (NOT mitmproxy's): succeeds only if TLS was NOT terminated.
        rc, code, headers, body = _curl(
            p, f"https://{_PASSTHROUGH_HOST}/", use_mitm_ca=False
        )
    assert rc == 0, f"passthrough should validate the real upstream cert: {headers}{body}"
    assert code == "200"
    assert "x-cage-reason" not in headers.lower()


def test_intercept_host_fails_without_mitm_ca(egress):
    # The mirror of the passthrough check: an intercepted host presents mitmproxy's
    # CA-signed leaf, so a client trusting only the system CA must FAIL verification.
    with running_proxy(_DENY_POLICY) as p:
        rc, code, _, _ = _curl(p, f"https://{_ALLOW_HOST}/", use_mitm_ca=False)
    assert rc != 0, "intercepted host unexpectedly validated against the system CA"


def test_hot_reload(egress):
    # Start with example.com NOT allowed -> soft-deny; then allow it and expect the
    # proxy to pick up the change within ~1s without restart.
    start_policy = {"version": 1, "allow": [{"host": "placeholder.test", "mode": "intercept"}]}
    with running_proxy(start_policy) as p:
        rc, code, headers, _ = _curl(p, f"https://{_ALLOW_HOST}/", use_mitm_ca=True)
        assert code == "470", "expected soft-deny before reload"
        assert "x-cage-reason: soft-deny" in headers.lower()

        _write_policy(p.policy_path, {"version": 1, "allow": [{"host": _ALLOW_HOST, "mode": "intercept"}]})
        os.utime(p.policy_path, None)  # bump mtime to now

        deadline = time.time() + 5
        last = None
        while time.time() < deadline:
            time.sleep(1.0)
            rc, code, headers, _ = _curl(p, f"https://{_ALLOW_HOST}/", use_mitm_ca=True)
            last = code
            if code == "200":
                break
        assert last == "200", f"policy reload did not take effect (last code {last})"


# --- audit log (AC-0018) ------------------------------------------------------


def test_audit_logs_soft_deny():
    with running_proxy(_DENY_POLICY) as p:
        _curl(p, "https://not-allowlisted.test/v2/auth/", use_mitm_ca=True)
        entry = _wait_for_audit(
            p.audit_path, lambda e: e.get("decision") == "soft-deny"
        )
    assert entry is not None, "no soft-deny audit entry appeared"
    assert entry["status"] == 470
    assert entry["method"] == "GET"
    assert entry["rule"] is None


def test_audit_logs_hard_deny():
    with running_proxy(_DENY_POLICY) as p:
        _curl(p, "https://blocked.test/anything", use_mitm_ca=True)
        entry = _wait_for_audit(
            p.audit_path, lambda e: e.get("decision") == "hard-deny"
        )
    assert entry is not None, "no hard-deny audit entry appeared"
    assert entry["status"] == 471
    assert entry["rule"]["list"] == "deny_always"


def test_audit_logs_allow(egress):
    with running_proxy(_DENY_POLICY) as p:
        _curl(p, f"https://{_ALLOW_HOST}/", use_mitm_ca=True)
        entry = _wait_for_audit(
            p.audit_path,
            lambda e: e.get("decision") == "allow" and "url" in e,
        )
    assert entry is not None, "no allow audit entry appeared"
    assert entry["status"] == 200
    assert _ALLOW_HOST in entry["url"]


def test_audit_passthrough_logs_host_only(egress):
    policy_obj = {
        "version": 1,
        "allow": [{"host": _PASSTHROUGH_HOST, "mode": "passthrough"}],
    }
    with running_proxy(policy_obj) as p:
        _curl(p, f"https://{_PASSTHROUGH_HOST}/", use_mitm_ca=False)
        entry = _wait_for_audit(
            p.audit_path, lambda e: e.get("host") == _PASSTHROUGH_HOST
        )
    assert entry is not None, "no passthrough audit entry appeared"
    assert entry["decision"] == "allow"
    # Host-only: TLS was never terminated, so no path/method/status is recorded.
    for absent in ("method", "path", "url", "status"):
        assert absent not in entry


# --- response streaming (AC-0057) ---------------------------------------------


class _StreamingHandler(BaseHTTPRequestHandler):
    # HTTP/1.0 => the body is close-delimited (no Content-Length / chunked framing
    # needed); the connection closes when do_GET returns, signalling end of body.
    # The handler emits text/event-stream events with a delay between each, flushing
    # after every write, so a streaming proxy relays them incrementally while a
    # buffering proxy would hold them all until the connection closes.
    protocol_version = "HTTP/1.0"
    events = 4
    delay = 0.3

    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for i in range(self.events):
            try:
                self.wfile.write(f"data: chunk{i}\n\n".encode())
                self.wfile.flush()
            except (BrokenPipeError, ConnectionResetError):
                return
            time.sleep(self.delay)

    def log_message(self, *args):  # silence the default stderr access log
        pass


@contextmanager
def _streaming_origin():
    """A local plaintext-HTTP origin that streams SSE events with delays. Plain HTTP
    (no TLS) so the proxy need not verify an upstream cert; the cage's streaming
    behaviour is transport-agnostic."""
    srv = HTTPServer(("127.0.0.1", 0), _StreamingHandler)
    thread = threading.Thread(target=srv.serve_forever, daemon=True)
    thread.start()
    try:
        yield srv.server_address[1]
    finally:
        srv.shutdown()
        srv.server_close()
        thread.join(timeout=5)


def _curl_stream(proxy, url, timeout=20):
    """Drive ``curl -N`` through the proxy, returning [(elapsed, line), ...] for each
    non-empty body line as it arrives (elapsed is seconds since the first line).

    ``--noproxy ""`` for the same reason as in _curl: an ambient NO_PROXY would make
    curl bypass the proxy under test."""
    proc = subprocess.Popen(
        [
            _CURL, "-sS", "-N", "--no-buffer",
            "-x", f"http://127.0.0.1:{proxy.port}",
            "--noproxy", "",
            "--max-time", str(timeout),
            url,
        ],
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
        text=True,
        bufsize=1,
    )
    lines = []
    start = None
    for raw in proc.stdout:
        line = raw.strip()
        if not line:
            continue
        now = time.monotonic()
        if start is None:
            start = now
        lines.append((now - start, line))
    proc.wait(timeout=5)
    return lines


def test_response_streams_incrementally():
    # A streaming SSE origin behind the proxy: the client must receive the early
    # events well before the stream closes. If the enforcer buffered the body (the
    # pre-fix behaviour), every event would arrive together at the end. Uses a local
    # plaintext origin, so it needs no external egress.
    with _streaming_origin() as origin_port:
        policy_obj = {
            "version": 1,
            "allow": [{"host": "127.0.0.1", "mode": "intercept"}],
        }
        with running_proxy(policy_obj) as p:
            lines = _curl_stream(p, f"http://127.0.0.1:{origin_port}/")
            entry = _wait_for_audit(
                p.audit_path, lambda e: e.get("decision") == "allow" and "url" in e
            )

    data = [(t, line) for t, line in lines if line.startswith("data:")]
    assert len(data) == _StreamingHandler.events, f"unexpected events: {lines}"
    spread = data[-1][0] - data[0][0]
    # Buffered delivery => spread ~0 (all events flushed at once); streamed delivery
    # => ~ (events-1) * delay (~0.9s here). 0.3s sits safely between the two regimes.
    assert spread >= 0.3, f"events arrived together (spread={spread:.3f}s); not streamed"

    # The audit must still fire for the streamed response, with its status.
    assert entry is not None, "no allow audit entry appeared for the streamed response"
    assert entry["status"] == 200


# --- credential injection end-to-end (AC-0068c) --------------------------------


class _EchoHandler(BaseHTTPRequestHandler):
    """A local plaintext-HTTP origin for the injection tests: /echo returns the
    Authorization header it received (so the test sees what the proxy forwarded);
    /reject401 and /reject403 return those statuses (to drive upstream rejection)."""

    def do_GET(self):  # noqa: N802 (BaseHTTPRequestHandler API)
        if self.path.startswith("/reject401"):
            self.send_response(401)
            self.end_headers()
            return
        if self.path.startswith("/reject403"):
            self.send_response(403)
            self.end_headers()
            return
        body = json.dumps(
            {"authorization": self.headers.get("Authorization", "")}
        ).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):  # silence the default stderr access log
        pass


@contextmanager
def _echo_origin():
    srv = HTTPServer(("127.0.0.1", 0), _EchoHandler)
    thread = threading.Thread(target=srv.serve_forever, daemon=True)
    thread.start()
    try:
        yield srv.server_address[1]
    finally:
        srv.shutdown()
        srv.server_close()
        thread.join(timeout=5)


@contextmanager
def running_proxy_with_secret(policy_obj, secret_obj):
    """Like running_proxy, but also delivers secret_obj (a {name: token} dict) to the
    addon over an inherited fd — the real end-to-end delivery path. The child inherits
    the pipe read end (kept open via pass_fds) and is told its number via
    creance_secret_fd, mirroring the Go launcher's SpawnWithSecret (which uses fd 3)."""
    _require_tooling()
    tmp = tempfile.mkdtemp(prefix="creance-enforcer-inj-it-")
    confdir = os.path.join(tmp, "conf")
    policy_path = os.path.join(tmp, "policy.json")
    audit_path = os.path.join(tmp, "egress.jsonl")
    _write_policy(policy_path, policy_obj)
    port = _free_port()

    r, w = os.pipe()
    os.write(w, json.dumps(secret_obj).encode())
    os.close(w)  # EOF for the child's read

    proc = subprocess.Popen(
        [
            _MITMDUMP,
            "--set", f"confdir={confdir}",
            "-p", str(port),
            "-s", _ENFORCER,
            "--set", f"creance_policy={policy_path}",
            "--set", f"creance_audit_log={audit_path}",
            "--set", f"creance_secret_fd={r}",
        ],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        pass_fds=(r,),
    )
    os.close(r)  # the child holds its own copy
    ca_cert = os.path.join(confdir, "mitmproxy-ca-cert.pem")
    try:
        _wait_until_ready(proc, port, ca_cert)
        yield _Proxy(port, ca_cert, policy_path, audit_path)
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
        import shutil

        shutil.rmtree(tmp, ignore_errors=True)


_INJECT_POLICY = {
    "version": 1,
    "credentials": {
        # source is resolved host-side by Go; here the token arrives over the fd, so
        # source is a placeholder and only header/template matter to the addon.
        "gh": {"source": "env://IGNORED", "header": "Authorization", "template": "Bearer {token}"},
    },
    "allow": [{"host": "127.0.0.1", "mode": "intercept", "inject": "gh"}],
    "deny_always": [],
}


def test_injection_overwrites_client_header_e2e():
    with _echo_origin() as origin_port:
        with running_proxy_with_secret(_INJECT_POLICY, {"gh": "REALTOKEN"}) as p:
            rc, code, headers, body = _curl(
                p,
                f"http://127.0.0.1:{origin_port}/echo",
                use_mitm_ca=False,
                extra=["-H", "Authorization: token gho_PHANTOM"],
            )
    assert rc == 0, f"curl failed: {headers}{body}"
    assert code == "200"
    # The origin saw the injected value, not the client-supplied phantom.
    assert json.loads(body)["authorization"] == "Bearer REALTOKEN"


def test_injection_unavailable_472_e2e():
    with _echo_origin() as origin_port:
        with running_proxy_with_secret(_INJECT_POLICY, {}) as p:  # nothing resolved
            rc, code, headers, body = _curl(
                p, f"http://127.0.0.1:{origin_port}/echo", use_mitm_ca=False
            )
    assert rc == 0, f"curl failed: {headers}{body}"
    assert code == "472"
    assert "x-cage-reason: injection-unavailable" in headers.lower()
    assert "x-cage-injected: gh" in headers.lower()
    assert json.loads(body)["error"] == "agent_cage_injection_unavailable"


def test_upstream_401_annotated_with_x_cage_injected_e2e():
    with _echo_origin() as origin_port:
        with running_proxy_with_secret(_INJECT_POLICY, {"gh": "REALTOKEN"}) as p:
            rc, code, headers, body = _curl(
                p, f"http://127.0.0.1:{origin_port}/reject401", use_mitm_ca=False
            )
    assert rc == 0, f"curl failed: {headers}{body}"
    assert code == "401"  # the upstream owns the status
    assert "x-cage-injected: gh" in headers.lower()


# --- concurrent per-project scoping (AC-0068e) ---------------------------------


def test_concurrent_proxies_hold_distinct_secrets_e2e():
    """Two simultaneous proxies each inject their own token, with no shared state.

    The multi-project claim ("four repos = four proxies = four scoped tokens"), reduced
    to its mechanism: the proxy is refcounted per project, and each spawn reads its own
    payload off its own inherited fd. So N concurrent cages hold N independently scoped
    tokens, which is what neutralizes the cage-wide keychain read.

    Both proxies are alive at once (nested context managers) and share one policy — the
    same credential NAME, deliberately, since a per-name collision is exactly the shared
    state this test must rule out.
    """
    with _echo_origin() as origin_a, _echo_origin() as origin_b:
        with running_proxy_with_secret(_INJECT_POLICY, {"gh": "TOKEN_PROJECT_A"}) as pa, \
             running_proxy_with_secret(_INJECT_POLICY, {"gh": "TOKEN_PROJECT_B"}) as pb:
            assert pa.port != pb.port, "the two proxies must be distinct processes"

            rc_a, code_a, hdr_a, body_a = _curl(
                pa, f"http://127.0.0.1:{origin_a}/echo", use_mitm_ca=False
            )
            rc_b, code_b, hdr_b, body_b = _curl(
                pb, f"http://127.0.0.1:{origin_b}/echo", use_mitm_ca=False
            )
            # The secret is bound to the PROXY, not to the origin: origin B reached
            # through proxy A must see A's token. Without this, the two assertions above
            # would also pass if each origin somehow selected its own credential.
            rc_x, code_x, hdr_x, body_x = _curl(
                pa, f"http://127.0.0.1:{origin_b}/echo", use_mitm_ca=False
            )

    assert rc_a == 0, f"curl failed: {hdr_a}{body_a}"
    assert rc_b == 0, f"curl failed: {hdr_b}{body_b}"
    assert rc_x == 0, f"curl failed: {hdr_x}{body_x}"
    assert code_a == code_b == code_x == "200"

    assert json.loads(body_a)["authorization"] == "Bearer TOKEN_PROJECT_A"
    assert json.loads(body_b)["authorization"] == "Bearer TOKEN_PROJECT_B"
    assert json.loads(body_x)["authorization"] == "Bearer TOKEN_PROJECT_A"

    # No cross-talk: neither proxy's secret map leaked into the other.
    assert "TOKEN_PROJECT_B" not in body_a
    assert "TOKEN_PROJECT_A" not in body_b
