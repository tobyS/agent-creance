"""Tests for the broker client (AC-0069b).

Pure: no mitmproxy. Exercises the client against a stub broker over a real unix
socket, plus every failure mode the addon depends on being *returned* rather than
raised — because a raise would be caught by the request hook's fail-closed handler
and turned into a 471 hard-deny, when the right answer is a 472.
"""

import asyncio
import os

import pytest

import broker
from stub_broker import short_sock_dir, stub_broker


async def test_fetch_returns_token():
    async with stub_broker({"gh": "ghs_real"}) as b:
        token, err = await broker.fetch(b.path, "gh", timeout=2.0)

    assert token == "ghs_real"
    assert err is None


async def test_fetch_sends_the_credential_name():
    async with stub_broker({"gh": "ghs_real", "deploy": "d3ploy"}) as b:
        token, _ = await broker.fetch(b.path, "deploy", timeout=2.0)
        assert token == "d3ploy"
        assert b.requests == ["deploy"]


async def test_fetch_unknown_credential():
    async with stub_broker({}) as b:
        token, err = await broker.fetch(b.path, "gh", timeout=2.0)

    assert token is None
    assert err == broker.ERROR_UNKNOWN_CREDENTIAL


async def test_fetch_expired_token():
    async with stub_broker({}, error=broker.ERROR_EXPIRED) as b:
        token, err = await broker.fetch(b.path, "gh", timeout=2.0)

    assert token is None
    assert err == broker.ERROR_EXPIRED


async def test_fetch_without_socket_path():
    token, err = await broker.fetch("", "gh", timeout=2.0)

    assert token is None
    assert "no broker socket" in err


async def test_fetch_missing_socket(tmp_path):
    token, err = await broker.fetch(str(tmp_path / "gone.sock"), "gh", timeout=2.0)

    assert token is None
    assert "unreachable" in err


async def test_fetch_times_out_on_a_silent_broker(sock_dir):
    """A broker that accepts but never answers must not hang the request forever —
    the caged agent gets a 472 instead."""
    # The handler blocks on an event rather than a sleep: server.wait_closed() waits
    # for in-flight handlers, so a sleeping one would stall teardown for its full
    # duration.
    release = asyncio.Event()

    async def never_answer(reader, writer):
        await release.wait()

    path = os.path.join(sock_dir, "s.sock")
    server = await asyncio.start_unix_server(never_answer, path)
    try:
        token, err = await broker.fetch(path, "gh", timeout=0.05)
    finally:
        release.set()
        server.close()
        await server.wait_closed()

    assert token is None
    assert err == "broker timed out"


async def test_fetch_handles_a_closed_connection(sock_dir):
    async def hang_up(reader, writer):
        writer.close()

    path = os.path.join(sock_dir, "s.sock")
    server = await asyncio.start_unix_server(hang_up, path)
    try:
        token, err = await broker.fetch(path, "gh", timeout=2.0)
    finally:
        server.close()
        await server.wait_closed()

    assert token is None
    assert err is not None


async def test_fetch_handles_a_garbage_reply(sock_dir):
    async def garbage(reader, writer):
        await reader.readline()
        writer.write(b"this is not json\n")
        await writer.drain()
        writer.close()

    path = os.path.join(sock_dir, "s.sock")
    server = await asyncio.start_unix_server(garbage, path)
    try:
        token, err = await broker.fetch(path, "gh", timeout=2.0)
    finally:
        server.close()
        await server.wait_closed()

    assert token is None
    assert "unreachable" in err


async def test_fetch_never_raises(tmp_path):
    """The contract the addon leans on, asserted directly: whatever is on the other
    end of the socket, fetch reports failure by returning it."""
    for path in ["", "/nonexistent/dir/x.sock", str(tmp_path)]:  # last one is a dir
        token, err = await broker.fetch(path, "gh", timeout=0.1)
        assert token is None
        assert err, f"no error reported for {path!r}"
