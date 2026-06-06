"""Addon hook tests for the mitmproxy enforcer.

These exercise the mitmproxy glue (the request / http_connect / tls_clienthello
hooks + hot reload). The decision logic itself is covered by the corpus
(test_vectors.py); here we assert the addon wires decisions to mitmproxy responses
correctly. Requires mitmproxy; the module skips cleanly if it is not installed so
the pure parity/golden suites still run.
"""

import json
import os
import types

import pytest

pytest.importorskip("mitmproxy")

from mitmproxy.test import taddons, tflow, tutils  # noqa: E402

import enforcer  # noqa: E402
import policy  # noqa: E402
import responses  # noqa: E402

# A fixture policy spanning every outcome the hooks must produce.
_POLICY = {
    "version": 1,
    "input_hash": "test",
    "allow": [
        {"host": "react.dev", "mode": "intercept"},
        {"host": "api.anthropic.com", "mode": "passthrough"},
        {"host": "tunnel-blocked.example", "mode": "passthrough"},
    ],
    "deny_always": [
        {"host": "w3schools.com", "mode": "intercept", "reason": "Known low-quality source."},
        {"host": "tunnel-blocked.example", "mode": "intercept", "reason": "Blocked tunnel host."},
    ],
}


def _write_policy(path, obj):
    with open(path, "w", encoding="utf-8") as f:
        json.dump(obj, f)


@pytest.fixture
def addon(tmp_path):
    path = tmp_path / "policy.json"
    _write_policy(path, _POLICY)
    a = enforcer.Enforcer()
    with taddons.context(a) as tctx:
        tctx.configure(a, creance_policy=str(path))
        yield a


def _https_flow(host, path, method="GET"):
    return tflow.tflow(
        req=tutils.treq(host=host, port=443, scheme=b"https", path=path, method=method)
    )


def _clienthello(sni):
    """Duck-typed stand-in for tls.ClientHelloData (the addon reads sni / sets ignore)."""
    return types.SimpleNamespace(
        client_hello=types.SimpleNamespace(sni=sni),
        ignore_connection=False,
    )


# --- request hook: the three outcomes -----------------------------------------


def test_allow_forwards_untouched(addon):
    flow = _https_flow("react.dev", "/learn/anything")
    addon.request(flow)
    assert flow.response is None


def test_soft_deny_returns_403(addon):
    flow = _https_flow("not-allowlisted.example", "/v2/auth/")
    addon.request(flow)
    assert flow.response is not None
    assert flow.response.status_code == 403
    assert flow.response.headers[responses.X_CAGE_REASON] == "soft-deny"
    body = json.loads(flow.response.content)
    assert body["error"] == "agent_cage_not_allowlisted"
    assert body["host"] == "not-allowlisted.example"
    assert body["allow_command_suggestion"] == (
        "agent-creance allow 'not-allowlisted.example/v2/auth/'"
    )


def test_hard_deny_returns_403_with_reason(addon):
    flow = _https_flow("w3schools.com", "/html/default.asp")
    addon.request(flow)
    assert flow.response is not None
    assert flow.response.status_code == 403
    assert flow.response.headers[responses.X_CAGE_REASON] == "hard-deny"
    body = json.loads(flow.response.content)
    assert body["error"] == "agent_cage_hard_deny"
    assert body["reason"] == "Known low-quality source."


def test_request_does_not_overwrite_existing_response(addon):
    flow = _https_flow("w3schools.com", "/html")
    flow.response = tutils.tresp(content=b"preset")
    addon.request(flow)
    assert flow.response.content == b"preset"


# --- http_connect: deny-at-CONNECT for passthrough hosts ----------------------


def test_connect_refused_for_denied_passthrough_host(addon):
    flow = _https_flow("tunnel-blocked.example", "/")
    addon.http_connect(flow)
    assert flow.response is not None
    assert flow.response.status_code == 403
    assert flow.response.headers[responses.X_CAGE_REASON] == "hard-deny"
    body = json.loads(flow.response.content)
    assert body["reason"] == "Blocked tunnel host."


def test_connect_allowed_for_intercept_host(addon):
    # A not-allowlisted (intercept) host must be allowed to CONNECT so TLS
    # terminates and the request hook can return the structured soft-deny body.
    flow = _https_flow("not-allowlisted.example", "/")
    addon.http_connect(flow)
    assert flow.response is None


def test_connect_allowed_for_clean_passthrough_host(addon):
    flow = _https_flow("api.anthropic.com", "/")
    addon.http_connect(flow)
    assert flow.response is None


# --- tls_clienthello: passthrough tunnelling ----------------------------------


def test_clienthello_tunnels_passthrough_host(addon):
    data = _clienthello("api.anthropic.com")
    addon.tls_clienthello(data)
    assert data.ignore_connection is True


def test_clienthello_terminates_intercept_host(addon):
    data = _clienthello("react.dev")
    addon.tls_clienthello(data)
    assert data.ignore_connection is False


def test_clienthello_does_not_tunnel_denied_passthrough_host(addon):
    # Belt-and-braces: even though http_connect already refused it, the clienthello
    # hook must not tunnel a host-level-denied passthrough host.
    data = _clienthello("tunnel-blocked.example")
    addon.tls_clienthello(data)
    assert data.ignore_connection is False


def test_clienthello_ignores_missing_sni(addon):
    data = _clienthello(None)
    addon.tls_clienthello(data)
    assert data.ignore_connection is False


# --- hot reload ---------------------------------------------------------------


def test_hot_reload_picks_up_new_allow(tmp_path):
    path = tmp_path / "policy.json"
    _write_policy(path, {"version": 1, "allow": [{"host": "react.dev", "mode": "intercept"}]})

    a = enforcer.Enforcer()
    with taddons.context(a) as tctx:
        tctx.configure(a, creance_policy=str(path))

        before = policy.decide(a._ruleset, policy.Request("flip.example", "/", "GET"))
        assert before.decision == policy.DECISION_SOFT_DENY

        # Rewrite the policy to allow the previously-denied host, bump the mtime.
        _write_policy(path, {"version": 1, "allow": [{"host": "flip.example", "mode": "intercept"}]})
        os.utime(path, (a._mtime + 10, a._mtime + 10))
        a._maybe_reload()

        after = policy.decide(a._ruleset, policy.Request("flip.example", "/", "GET"))
        assert after.decision == policy.DECISION_ALLOW
