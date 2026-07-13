"""Addon hook tests for the mitmproxy enforcer.

These exercise the mitmproxy glue (the request / http_connect / tls_clienthello
hooks + hot reload). The decision logic itself is covered by the corpus
(test_vectors.py); here we assert the addon wires decisions to mitmproxy responses
correctly. Requires mitmproxy; the module skips cleanly if it is not installed so
the pure parity/golden suites still run.
"""

import json
import logging
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
def audit_path(tmp_path):
    return tmp_path / "egress.jsonl"


@pytest.fixture
def addon(tmp_path, audit_path):
    path = tmp_path / "policy.json"
    _write_policy(path, _POLICY)
    a = enforcer.Enforcer()
    with taddons.context(a) as tctx:
        tctx.configure(a, creance_policy=str(path), creance_audit_log=str(audit_path))
        yield a


def _read_audit(path):
    if not path.exists():
        return []
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()]


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


async def test_allow_forwards_untouched(addon):
    flow = _https_flow("react.dev", "/learn/anything")
    await addon.request(flow)
    assert flow.response is None


async def test_soft_deny_returns_470(addon):
    flow = _https_flow("not-allowlisted.example", "/v2/auth/")
    await addon.request(flow)
    assert flow.response is not None
    assert flow.response.status_code == 470
    assert flow.response.reason == "agent-creance soft-deny (not allowlisted)"
    assert flow.response.headers[responses.X_CAGE_REASON] == "soft-deny"
    body = json.loads(flow.response.content)
    assert body["error"] == "agent_cage_not_allowlisted"
    assert body["host"] == "not-allowlisted.example"
    assert body["allow_command_suggestion"] == (
        "agent-creance allow 'not-allowlisted.example/v2/auth/'"
    )


async def test_hard_deny_returns_471_with_reason(addon):
    flow = _https_flow("w3schools.com", "/html/default.asp")
    await addon.request(flow)
    assert flow.response is not None
    assert flow.response.status_code == 471
    assert flow.response.reason == "agent-creance hard-deny (blocked)"
    assert flow.response.headers[responses.X_CAGE_REASON] == "hard-deny"
    body = json.loads(flow.response.content)
    assert body["error"] == "agent_cage_hard_deny"
    assert body["reason"] == "Known low-quality source."


async def test_request_does_not_overwrite_existing_response(addon):
    flow = _https_flow("w3schools.com", "/html")
    flow.response = tutils.tresp(content=b"preset")
    await addon.request(flow)
    assert flow.response.content == b"preset"


# --- http_connect: deny-at-CONNECT for passthrough hosts ----------------------


def test_connect_refused_for_denied_passthrough_host(addon):
    flow = _https_flow("tunnel-blocked.example", "/")
    addon.http_connect(flow)
    assert flow.response is not None
    assert flow.response.status_code == 471
    assert flow.response.reason == "agent-creance hard-deny (blocked)"
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


# --- fail closed (AC-0058 / B1, B2, B3) ---------------------------------------


def _raise(*_args, **_kwargs):
    raise RuntimeError("boom")


async def test_request_hook_fails_closed_on_exception(addon, monkeypatch):
    # An exception anywhere in the decision path must hard-deny, never forward upstream.
    monkeypatch.setattr(policy, "decide", _raise)
    flow = _https_flow("react.dev", "/learn")
    await addon.request(flow)
    assert flow.response is not None, "a raising decision must still set a response"
    assert flow.response.status_code == 471
    assert flow.response.headers[responses.X_CAGE_REASON] == "hard-deny"


def test_http_connect_fails_closed_on_exception(addon, monkeypatch):
    monkeypatch.setattr(policy, "host_disposition", _raise)
    flow = _https_flow("api.anthropic.com", "/")
    addon.http_connect(flow)
    assert flow.response is not None
    assert flow.response.status_code == 471


def test_tls_clienthello_fails_closed_on_exception(addon, monkeypatch):
    monkeypatch.setattr(policy, "host_disposition", _raise)
    ch = _clienthello("api.anthropic.com")  # a passthrough host that would normally tunnel
    addon.tls_clienthello(ch)
    # Fail closed = do not tunnel: TLS terminates and the (hard-closed) request hook decides.
    assert ch.ignore_connection is False


def test_initial_load_failure_logs_error(tmp_path, caplog):
    # A corrupt initial policy.json is fatal: it logs at ERROR (which trips mitmproxy's
    # ErrorCheck → non-zero exit) and falls back to the empty deny-all ruleset, never a
    # partial one.
    path = tmp_path / "policy.json"
    path.write_text("{ not valid json", encoding="utf-8")
    a = enforcer.Enforcer()
    with taddons.context(a) as tctx:
        with caplog.at_level(logging.ERROR):
            tctx.configure(a, creance_policy=str(path))
    assert any("initial policy" in r.getMessage() for r in caplog.records), caplog.text
    assert a._ruleset.allow == []
    assert a._ruleset.deny_always == []


def test_malformed_reload_keeps_last_good(tmp_path):
    path = tmp_path / "policy.json"
    _write_policy(path, {"version": 1, "allow": [{"host": "react.dev", "mode": "intercept"}]})

    a = enforcer.Enforcer()
    with taddons.context(a) as tctx:
        tctx.configure(a, creance_policy=str(path))
        good_mtime = a._mtime
        assert (
            policy.decide(a._ruleset, policy.Request("react.dev", "/", "GET")).decision
            == policy.DECISION_ALLOW
        )

        # Corrupt the file and bump the mtime so the poll attempts a reload.
        path.write_text("{ corrupt", encoding="utf-8")
        os.utime(path, (good_mtime + 10, good_mtime + 10))
        a._maybe_reload()

        # Last-good ruleset is still in force (the failed assignment never completed).
        assert (
            policy.decide(a._ruleset, policy.Request("react.dev", "/", "GET")).decision
            == policy.DECISION_ALLOW
        )

        # And it recovers when a valid policy lands.
        _write_policy(path, {"version": 1, "allow": [{"host": "flip.example", "mode": "intercept"}]})
        os.utime(path, (good_mtime + 20, good_mtime + 20))
        a._maybe_reload()
        assert (
            policy.decide(a._ruleset, policy.Request("flip.example", "/", "GET")).decision
            == policy.DECISION_ALLOW
        )


# --- audit logging (AC-0018) --------------------------------------------------


async def test_intercept_allow_logs_entry_and_strips_query(addon, audit_path):
    # An allowed request with a token in the query string: the response hook logs a
    # full entry, and the whole query (so the token) must not reach the log.
    flow = _https_flow("react.dev", "/learn?api_key=SEKRET")
    await addon.request(flow)
    assert flow.response is None  # allow forwards untouched
    flow.response = tutils.tresp(content=b"ok")  # upstream 200
    addon.response(flow)

    entries = _read_audit(audit_path)
    assert len(entries) == 1
    e = entries[0]
    assert e["decision"] == "allow"
    assert e["method"] == "GET"
    assert e["status"] == 200
    assert e["rule"] == {"list": "allow", "index": 0}
    assert e["url"] == "https://react.dev/learn"  # query dropped entirely
    assert "SEKRET" not in audit_path.read_text(encoding="utf-8")


async def test_intercept_soft_deny_logs_entry(addon, audit_path):
    flow = _https_flow("not-allowlisted.example", "/v2/auth/")
    await addon.request(flow)
    addon.response(flow)  # status comes from the synthesized refusal

    entries = _read_audit(audit_path)
    assert len(entries) == 1
    e = entries[0]
    assert e["decision"] == "soft-deny"
    assert e["rule"] is None
    assert e["status"] == 470


async def test_intercept_hard_deny_logs_entry_with_rule(addon, audit_path):
    flow = _https_flow("w3schools.com", "/html/default.asp")
    await addon.request(flow)
    addon.response(flow)

    entries = _read_audit(audit_path)
    assert len(entries) == 1
    e = entries[0]
    assert e["decision"] == "hard-deny"
    assert e["rule"]["list"] == "deny_always"
    assert e["status"] == 471


def test_passthrough_clean_logs_host_only(addon, audit_path):
    addon.tls_clienthello(_clienthello("api.anthropic.com"))

    entries = _read_audit(audit_path)
    assert len(entries) == 1
    e = entries[0]
    assert e == {"ts": e["ts"], "host": "api.anthropic.com", "decision": "allow"}
    # Host-only: no path/method/status/url leaks.
    for absent in ("method", "path", "url", "status"):
        assert absent not in e


def test_passthrough_denied_logs_host_only(addon, audit_path):
    flow = _https_flow("tunnel-blocked.example", "/secret/path")
    addon.http_connect(flow)

    entries = _read_audit(audit_path)
    assert len(entries) == 1
    e = entries[0]
    assert e["host"] == "tunnel-blocked.example"
    assert e["decision"] == "hard-deny"
    for absent in ("method", "path", "url", "status"):
        assert absent not in e


# --- response streaming (AC-0057) ---------------------------------------------


async def test_responseheaders_marks_response_for_streaming(addon):
    # The responseheaders hook must flag every upstream response for incremental
    # streaming so long SSE bodies are not buffered (which would time out the client).
    flow = _https_flow("react.dev", "/learn")
    await addon.request(flow)
    assert flow.response is None  # allow forwards untouched
    flow.response = tutils.tresp(content=b"data")
    assert flow.response.stream is False  # mitmproxy default: buffered
    addon.responseheaders(flow)
    assert flow.response.stream is True


async def test_response_still_audits_after_streaming(addon, audit_path):
    # Streaming the body must not break the single audit point: the response hook
    # still fires with status_code available.
    flow = _https_flow("react.dev", "/learn")
    await addon.request(flow)
    flow.response = tutils.tresp(content=b"data")
    addon.responseheaders(flow)
    addon.response(flow)

    entries = _read_audit(audit_path)
    assert len(entries) == 1
    e = entries[0]
    assert e["decision"] == "allow"
    assert e["status"] == 200


async def test_audit_disabled_when_option_empty(tmp_path):
    # No creance_audit_log -> the addon writes nothing and creates no file.
    path = tmp_path / "policy.json"
    _write_policy(path, _POLICY)
    log = tmp_path / "egress.jsonl"
    a = enforcer.Enforcer()
    with taddons.context(a) as tctx:
        tctx.configure(a, creance_policy=str(path))  # audit option left at default ""
        flow = _https_flow("w3schools.com", "/x")
        await a.request(flow)
        a.response(flow)
        a.tls_clienthello(_clienthello("api.anthropic.com"))
    assert not log.exists()
