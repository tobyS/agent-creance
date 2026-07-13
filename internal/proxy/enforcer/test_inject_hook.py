"""Addon hook tests for credential injection (AC-0068c, AC-0069b).

Exercises the request-hook overwrite / in-cage / 472 branches and the
responseheaders X-Cage-Injected annotation. Since AC-0069b the addon holds no
secrets of its own: it fetches each token from the host-side broker over a unix
socket, so these tests stand up a stub broker rather than writing a private field.
Requires mitmproxy; skips cleanly if it is absent so the pure suites still run.
"""

import json

import pytest

pytest.importorskip("mitmproxy")

from mitmproxy.test import taddons, tflow, tutils  # noqa: E402

import enforcer  # noqa: E402
import responses  # noqa: E402
from stub_broker import stub_broker  # noqa: E402

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
def policy_file(tmp_path):
    path = tmp_path / "policy.json"
    with open(path, "w", encoding="utf-8") as f:
        json.dump(_INJECT_POLICY, f)
    return path


@pytest.fixture
async def make_addon(policy_file, tmp_path):
    """Build an addon wired to a stub broker serving ``tokens``.

    Passing tokens={} models a broker that holds nothing (every lookup misses);
    passing sock=None models no broker at all.
    """

    async def _make(tokens=None, sock=None):
        a = enforcer.Enforcer()
        with taddons.context(a) as tctx:
            tctx.configure(
                a,
                creance_policy=str(policy_file),
                creance_audit_log=str(tmp_path / "e.jsonl"),
                creance_broker_sock=sock or "",
            )
            return a

    return _make


# --- request hook: overwrite / in-cage / 472 ----------------------------------


async def test_inject_overwrites_client_supplied_header(make_addon):
    async with stub_broker({"gh": "ghs_real"}) as b:
        addon = await make_addon(sock=b.path)
        flow = _https_flow("api.github.com")
        flow.request.headers["Authorization"] = "token gho_PHANTOM"  # what the phantom sets

        await addon.request(flow)

        assert flow.response is None, "an injected request is forwarded, not refused"
        assert flow.request.headers["Authorization"] == "Bearer ghs_real"
        assert flow.metadata.get("creance_injected") == "gh"


async def test_inject_rotated_token_takes_effect_without_restart(make_addon):
    """The property the fd channel could not offer, and the reason for AC-0069b."""
    async with stub_broker({"gh": "ghs_old"}) as b:
        addon = await make_addon(sock=b.path)

        first = _https_flow("api.github.com")
        await addon.request(first)
        assert first.request.headers["Authorization"] == "Bearer ghs_old"

        b.rotate("gh", "ghs_new")

        second = _https_flow("api.github.com")
        await addon.request(second)
        assert second.request.headers["Authorization"] == "Bearer ghs_new"


async def test_inject_unknown_credential_returns_472(make_addon):
    async with stub_broker({}) as b:  # broker holds nothing
        addon = await make_addon(sock=b.path)
        flow = _https_flow("api.github.com")

        await addon.request(flow)

        _assert_472(flow)


async def test_inject_expired_token_returns_472(make_addon):
    """A dead token must not go upstream: a 472 tells the human what to do; a 401
    from GitHub does not."""
    async with stub_broker({}, error="expired") as b:
        addon = await make_addon(sock=b.path)
        flow = _https_flow("api.github.com")

        await addon.request(flow)

        _assert_472(flow)


async def test_inject_without_broker_returns_472(make_addon):
    addon = await make_addon(sock=None)  # no broker configured at all
    flow = _https_flow("api.github.com")

    await addon.request(flow)

    _assert_472(flow)


async def test_inject_unreachable_broker_returns_472(make_addon, tmp_path):
    """A dead broker is a 472, NOT the 471 that the hook's except-Exception would
    produce if broker.fetch raised. This is the distinction AC-0069b turns on."""
    addon = await make_addon(sock=str(tmp_path / "nonexistent.sock"))
    flow = _https_flow("api.github.com")

    await addon.request(flow)

    _assert_472(flow)
    assert flow.response.status_code != 471, "a missing broker is human-recoverable"


async def test_in_cage_leaves_auth_header_untouched(make_addon):
    async with stub_broker({"gh": "ghs_real"}) as b:
        addon = await make_addon(sock=b.path)
        flow = _https_flow("s3.example.com", path="/bucket/key", method="GET")
        flow.request.headers["Authorization"] = "AWS4-HMAC-SHA256 client-signature"

        await addon.request(flow)

        assert flow.response is None
        assert flow.request.headers["Authorization"] == "AWS4-HMAC-SHA256 client-signature"
        assert "creance_injected" not in flow.metadata
        assert b.requests == [], "an in-cage host never reaches the broker"


async def test_plain_allow_does_not_touch_auth(make_addon):
    async with stub_broker({"gh": "ghs_real"}) as b:
        addon = await make_addon(sock=b.path)
        flow = _https_flow("plain.example.com", path="/", method="GET")
        flow.request.headers["Authorization"] = "Bearer client-token"

        await addon.request(flow)

        assert flow.response is None
        assert flow.request.headers["Authorization"] == "Bearer client-token"
        assert "creance_injected" not in flow.metadata
        assert b.requests == [], "a non-inject host never reaches the broker"


async def test_null_byte_host_does_not_match_inject_rule(make_addon):
    # The SOCKS5 null-byte lesson: an embedded null must not let a host masquerade as
    # an inject host. The existing matcher canonicalizes + exact-matches, so the
    # variant fails to match the api.github.com inject rule and soft-denies — no
    # injection, no header set, and the broker is never asked for the token.
    async with stub_broker({"gh": "ghs_real"}) as b:
        addon = await make_addon(sock=b.path)
        flow = _https_flow("api.github.com\x00.evil.example")

        await addon.request(flow)

        assert flow.response is not None
        assert flow.response.status_code == 470  # soft-deny, not injected
        assert "Authorization" not in flow.request.headers
        assert b.requests == []


def _assert_472(flow):
    assert flow.response is not None
    assert flow.response.status_code == 472
    assert flow.response.reason == responses.REASON_PHRASE_INJECTION_UNAVAILABLE
    assert flow.response.headers[responses.X_CAGE_REASON] == "injection-unavailable"
    assert flow.response.headers[responses.X_CAGE_INJECTED] == "gh"
    body = json.loads(flow.response.content)
    assert body["error"] == "agent_cage_injection_unavailable"
    assert body["credential"] == "gh"
    assert "unlock the secret store" in body["how_to_proceed"]


# --- responseheaders: X-Cage-Injected on a real upstream 401/403 --------------


@pytest.fixture
async def addon(make_addon):
    return await make_addon(sock=None)


async def test_upstream_401_for_injected_request_gets_x_cage_injected(addon):
    flow = _https_flow("api.github.com")
    flow.metadata["creance_injected"] = "gh"
    flow.response = tutils.tresp(status_code=401)
    addon.responseheaders(flow)
    assert flow.response.headers[responses.X_CAGE_INJECTED] == "gh"
    assert flow.response.stream is True


async def test_upstream_403_for_injected_request_gets_x_cage_injected(addon):
    flow = _https_flow("api.github.com")
    flow.metadata["creance_injected"] = "gh"
    flow.response = tutils.tresp(status_code=403)
    addon.responseheaders(flow)
    assert flow.response.headers[responses.X_CAGE_INJECTED] == "gh"


async def test_upstream_200_for_injected_request_not_annotated(addon):
    flow = _https_flow("api.github.com")
    flow.metadata["creance_injected"] = "gh"
    flow.response = tutils.tresp(status_code=200)
    addon.responseheaders(flow)
    assert responses.X_CAGE_INJECTED not in flow.response.headers


async def test_upstream_401_without_injection_not_annotated(addon):
    flow = _https_flow("plain.example.com")
    flow.response = tutils.tresp(status_code=401)
    addon.responseheaders(flow)
    assert responses.X_CAGE_INJECTED not in flow.response.headers
