"""Addon hook tests for credential injection (AC-0068c).

Exercises the request-hook overwrite / in-cage / 472 branches, the responseheaders
X-Cage-Injected annotation, and the inherited-fd secret intake. Requires mitmproxy;
skips cleanly if it is absent so the pure suites still run.
"""

import json
import os

import pytest

pytest.importorskip("mitmproxy")

from mitmproxy.test import taddons, tflow, tutils  # noqa: E402

import enforcer  # noqa: E402
import responses  # noqa: E402

_INJECT_POLICY = {
    "version": 1,
    "input_hash": "test",
    "credentials": {
        "gh": {
            "source": "op://vault/gh",
            "header": "Authorization",
            "template": "Bearer {token}",
        }
    },
    "allow": [
        {"host": "api.github.com", "mode": "intercept", "inject": "gh"},
        {"host": "s3.example.com", "mode": "intercept", "in_cage": True},
        {"host": "plain.example.com", "mode": "intercept"},
    ],
    "deny_always": [],
}


def _https_flow(host, path="/graphql", method="POST"):
    return tflow.tflow(
        req=tutils.treq(host=host, port=443, scheme=b"https", path=path, method=method)
    )


@pytest.fixture
def addon(tmp_path):
    path = tmp_path / "policy.json"
    with open(path, "w", encoding="utf-8") as f:
        json.dump(_INJECT_POLICY, f)
    a = enforcer.Enforcer()
    with taddons.context(a) as tctx:
        tctx.configure(a, creance_policy=str(path), creance_audit_log=str(tmp_path / "e.jsonl"))
        yield a


# --- request hook: overwrite / in-cage / 472 ----------------------------------


def test_inject_overwrites_client_supplied_header(addon):
    addon._secrets = {"gh": "ghs_real"}
    flow = _https_flow("api.github.com")
    flow.request.headers["Authorization"] = "token gho_PHANTOM"  # what the phantom sets
    addon.request(flow)
    assert flow.response is None, "an injected request is forwarded, not refused"
    assert flow.request.headers["Authorization"] == "Bearer ghs_real"
    assert flow.metadata.get("creance_injected") == "gh"


def test_inject_missing_secret_returns_472(addon):
    addon._secrets = {}  # nothing resolved host-side
    flow = _https_flow("api.github.com")
    addon.request(flow)
    assert flow.response is not None
    assert flow.response.status_code == 472
    assert flow.response.reason == responses.REASON_PHRASE_INJECTION_UNAVAILABLE
    assert flow.response.headers[responses.X_CAGE_REASON] == "injection-unavailable"
    assert flow.response.headers[responses.X_CAGE_INJECTED] == "gh"
    body = json.loads(flow.response.content)
    assert body["error"] == "agent_cage_injection_unavailable"
    assert body["credential"] == "gh"
    assert "unlock the secret store" in body["how_to_proceed"]


def test_in_cage_leaves_auth_header_untouched(addon):
    addon._secrets = {"gh": "ghs_real"}  # present, but must not be used here
    flow = _https_flow("s3.example.com", path="/bucket/key", method="GET")
    flow.request.headers["Authorization"] = "AWS4-HMAC-SHA256 client-signature"
    addon.request(flow)
    assert flow.response is None
    assert flow.request.headers["Authorization"] == "AWS4-HMAC-SHA256 client-signature"
    assert "creance_injected" not in flow.metadata


def test_plain_allow_does_not_touch_auth(addon):
    addon._secrets = {"gh": "ghs_real"}
    flow = _https_flow("plain.example.com", path="/", method="GET")
    flow.request.headers["Authorization"] = "Bearer client-token"
    addon.request(flow)
    assert flow.response is None
    assert flow.request.headers["Authorization"] == "Bearer client-token"
    assert "creance_injected" not in flow.metadata


def test_null_byte_host_does_not_match_inject_rule(addon):
    # The SOCKS5 null-byte lesson: an embedded null must not let a host masquerade as
    # an inject host. The existing matcher canonicalizes + exact-matches, so the
    # variant fails to match the api.github.com inject rule and soft-denies — no
    # injection, no header set.
    addon._secrets = {"gh": "ghs_real"}
    flow = _https_flow("api.github.com\x00.evil.example")
    addon.request(flow)
    assert flow.response is not None
    assert flow.response.status_code == 470  # soft-deny, not injected
    assert "Authorization" not in flow.request.headers


# --- responseheaders: X-Cage-Injected on a real upstream 401/403 --------------


def test_upstream_401_for_injected_request_gets_x_cage_injected(addon):
    flow = _https_flow("api.github.com")
    flow.metadata["creance_injected"] = "gh"
    flow.response = tutils.tresp(status_code=401)
    addon.responseheaders(flow)
    assert flow.response.headers[responses.X_CAGE_INJECTED] == "gh"
    assert flow.response.stream is True


def test_upstream_403_for_injected_request_gets_x_cage_injected(addon):
    flow = _https_flow("api.github.com")
    flow.metadata["creance_injected"] = "gh"
    flow.response = tutils.tresp(status_code=403)
    addon.responseheaders(flow)
    assert flow.response.headers[responses.X_CAGE_INJECTED] == "gh"


def test_upstream_200_for_injected_request_not_annotated(addon):
    flow = _https_flow("api.github.com")
    flow.metadata["creance_injected"] = "gh"
    flow.response = tutils.tresp(status_code=200)
    addon.responseheaders(flow)
    assert responses.X_CAGE_INJECTED not in flow.response.headers


def test_upstream_401_without_injection_not_annotated(addon):
    flow = _https_flow("plain.example.com")
    flow.response = tutils.tresp(status_code=401)
    addon.responseheaders(flow)
    assert responses.X_CAGE_INJECTED not in flow.response.headers


# --- inherited-fd secret intake -----------------------------------------------


def test_secret_fd_intake_populates_secrets(tmp_path):
    a = enforcer.Enforcer()
    r, w = os.pipe()
    os.write(w, json.dumps({"gh": "tok3n", "deploy": "d3ploy"}).encode())
    os.close(w)
    with taddons.context(a) as tctx:
        tctx.configure(a, creance_secret_fd=r)
    assert a._secrets == {"gh": "tok3n", "deploy": "d3ploy"}


def test_secret_fd_intake_tolerates_empty_payload(tmp_path):
    a = enforcer.Enforcer()
    r, w = os.pipe()
    os.close(w)  # no bytes, immediate EOF
    with taddons.context(a) as tctx:
        tctx.configure(a, creance_secret_fd=r)
    assert a._secrets == {}


def test_secret_fd_intake_tolerates_malformed_payload(tmp_path):
    a = enforcer.Enforcer()
    r, w = os.pipe()
    os.write(w, b"not json{{{")
    os.close(w)
    with taddons.context(a) as tctx:
        tctx.configure(a, creance_secret_fd=r)
    assert a._secrets == {}  # fail closed: inject-hosts then 472
