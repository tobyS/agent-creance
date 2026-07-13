"""Client for the host-side credential broker (AC-0069b).

The broker is a Go daemon, a sibling of this mitmproxy process, that custodies the
injected credentials and serves them over a unix socket. The addon fetches a token
from it once per injected request, rather than holding one for the proxy's lifetime
as it did when the payload arrived over an inherited fd (AC-0068c).

Two things follow from fetching per request. The token lives in this process only
for as long as one request needs it — Python's memory hygiene is weak, which is why
custody moved to Go. And a rotated credential (AC-0069a's refresh loop) takes effect
on the very next request, with no proxy restart and no way to tear a request that is
already in flight.

Protocol: newline-delimited JSON, one request and one response per connection.

    -> {"credential": "gh"}
    <- {"token": "ghs_...", "expires_at": "2026-07-13T11:00:00Z"}
    <- {"error": "unknown_credential"}
    <- {"error": "expired"}

``expires_at`` is absent for a credential that does not expire.

Like policy.py / inject.py / responses.py, this module does not import mitmproxy —
it stays unit-testable without it.
"""

import asyncio
import json
from typing import Optional, Tuple

# Errors the broker reports (broker/protocol.go). Both mean the same thing to the
# addon — answer 472 — but they are distinguished on the wire so the operator can
# tell "never configured" from "the refresh loop fell behind".
ERROR_UNKNOWN_CREDENTIAL = "unknown_credential"
ERROR_EXPIRED = "expired"


async def fetch(
    sock_path: str, credential: str, timeout: float
) -> Tuple[Optional[str], Optional[str]]:
    """Fetch the current token for ``credential`` from the broker at ``sock_path``.

    Returns ``(token, None)`` on success and ``(None, error)`` otherwise, where
    ``error`` is a short, secret-free description — a broker error kind, or the type
    of the transport failure.

    It never raises. That is deliberate and load-bearing: the addon's request hook
    wraps everything in an ``except Exception`` that fail-closes to a *hard-deny*
    (471), which is the wrong answer here. A credential that cannot be fetched is a
    472 — "human-recoverable, unlock the secret store; do NOT allow" — so this
    function must report failure by returning it, never by raising it.
    """
    if not sock_path:
        return None, "no broker socket configured"

    writer = None
    try:
        reader, writer = await asyncio.wait_for(
            asyncio.open_unix_connection(sock_path), timeout
        )
        writer.write(json.dumps({"credential": credential}).encode() + b"\n")
        await asyncio.wait_for(writer.drain(), timeout)

        line = await asyncio.wait_for(reader.readline(), timeout)
        if not line:
            return None, "broker closed the connection without answering"

        resp = json.loads(line)
        error = resp.get("error")
        if error:
            return None, str(error)
        token = resp.get("token")
        if not token:
            return None, "broker returned no token"
        return str(token), None
    except asyncio.TimeoutError:
        return None, "broker timed out"
    except (OSError, ValueError, TypeError, AttributeError) as exc:
        # Only the exception *type* — an error string ends up in a log, which is not
        # where a token (or an attacker-chosen byte sequence) belongs.
        return None, f"broker unreachable ({type(exc).__name__})"
    finally:
        if writer is not None:
            try:
                writer.close()
                await writer.wait_closed()
            except (OSError, RuntimeError):
                pass  # already gone; nothing to clean up
