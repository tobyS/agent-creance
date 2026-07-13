"""A stub of the Go credential broker, for tests (AC-0069b).

Speaks the same newline-delimited JSON protocol as internal/broker over a real unix
socket, so the addon's client code is exercised end to end — connect, request,
response, close — without building or running the Go binary. The Go side has its own
tests for the server; this is here so the *Python* side can be tested hermetically.

    async with stub_broker(tmp_path, {"gh": "ghs_real"}) as b:
        ...           # b.path is the socket
        b.rotate("gh", "ghs_new")   # serve a different token from now on
        assert b.requests == ["gh"]
"""

import asyncio
import json
import os
import shutil
import tempfile
from contextlib import asynccontextmanager


def short_sock_dir():
    """A temp dir short enough that a socket inside it fits in sun_path.

    AF_UNIX paths are capped at 104 bytes on darwin, and pytest's ``tmp_path``
    embeds the test's name on top of an already-long TMPDIR — which overflows it for
    most of these test names. The Go side guards the production path explicitly
    (sysdep.MaxSocketPathLen); here we just keep the path short. Callers are
    responsible for removing the directory (or use ``stub_broker``, which does).
    """
    return tempfile.mkdtemp(prefix="ac")


class StubBroker:
    """The handle yielded by stub_broker()."""

    def __init__(self, path, tokens, error):
        self.path = path
        self.requests = []  # credential names asked for, in order
        self._tokens = dict(tokens)
        self._error = error

    def rotate(self, credential, token):
        """Replace the served token, as AC-0069a's refresh loop will."""
        self._tokens[credential] = token

    def _answer(self, credential):
        if self._error:
            return {"error": self._error}
        token = self._tokens.get(credential)
        if token is None:
            return {"error": "unknown_credential"}
        return {"token": token}

    async def _handle(self, reader, writer):
        try:
            line = await reader.readline()
            if not line:
                return  # a readiness probe: connect, hang up
            credential = json.loads(line).get("credential", "")
            self.requests.append(credential)
            writer.write(json.dumps(self._answer(credential)).encode() + b"\n")
            await writer.drain()
        finally:
            writer.close()


@asynccontextmanager
async def stub_broker(tokens, error=None):
    """Serve ``tokens`` on a unix socket until the block exits.

    ``error``, when set, makes every lookup answer with that error kind instead —
    the way to model an expired token, which only a minted credential (AC-0069a) can
    produce for real.
    """
    tmp = short_sock_dir()
    path = os.path.join(tmp, "b.sock")
    broker = StubBroker(path, tokens, error)
    server = await asyncio.start_unix_server(broker._handle, path)
    try:
        yield broker
    finally:
        server.close()
        await server.wait_closed()
        shutil.rmtree(tmp, ignore_errors=True)
