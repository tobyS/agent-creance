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
import time
from contextlib import contextmanager

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


def _curl(proxy, url, *, use_mitm_ca, timeout=20):
    """Drive curl through the proxy. Returns (returncode, http_code, headers, body)."""
    with tempfile.TemporaryDirectory() as d:
        body_path = os.path.join(d, "body")
        hdr_path = os.path.join(d, "hdr")
        cmd = [
            _CURL, "-sS",
            "-x", f"http://127.0.0.1:{proxy.port}",
            "--max-time", str(timeout),
            "-o", body_path, "-D", hdr_path,
            "-w", "%{http_code}",
            url,
        ]
        if use_mitm_ca:
            cmd += ["--cacert", proxy.ca_cert]
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
