"""The egress audit log: a 0600 JSONL record of every decision, with rotation.

Every egress attempt the enforcer decides is appended here as one compact JSON line
(``egress.jsonl`` in the project's out-of-tree state dir). Keeping it out-of-tree is
deliberate: the agent runs with ``./`` writable, so an in-tree log could be doctored
by the very process it records (docs/design.md, "Audit log"). ``agent-creance logs``
(AC-0021) is the reader; this module is only the writer.

Two entry shapes, matching what the proxy can actually see:

  - intercepted (TLS-terminated) requests -> full entry:
        {ts, method, url, decision, rule, status}
  - passthrough hosts -> host-only entry: {ts, host, decision}. Without TLS
    termination the proxy sees only the CONNECT/SNI host -- no path, method, status,
    and (for an ignored connection) no byte counts at all.

Redaction: the listed fields carry no headers at all, and the logged URL has its
query string stripped entirely (scheme/host/path kept for debugging). A query is
where credentials ride -- bearer tokens, signed-URL signatures, session ids -- under
arbitrary parameter names a denylist can't anticipate, so dropping the whole query is
the safe shape and keeps the audit trail from being itself a credential leak.

Rotation: a write that would push ``egress.jsonl`` past ``DEFAULT_MAX_BYTES`` rotates
first -- the current file is renamed to ``egress.jsonl.1`` (atomically replacing any
prior backup) and a fresh current file is started -- capping disk at ~2x the
threshold per project and never splitting or dropping an entry.

This module imports no mitmproxy: the builders and ``strip_query`` are pure (golden/
table tested), and ``AuditLog`` is plain filesystem I/O, so the suite runs without
mitmproxy installed (mirrors policy.py / responses.py). enforcer.py is the glue that
calls ``write`` from the request/response/connect hooks.
"""

from __future__ import annotations

import json
import os
from datetime import datetime, timezone
from typing import Optional
from urllib.parse import urlsplit, urlunsplit

# Rotate when a write would push the current file past this many bytes. Caps disk at
# roughly 2x this per project (current + one .1 backup). Injectable so tests can set
# a tiny threshold.
DEFAULT_MAX_BYTES = 500 * 1024 * 1024

# The single rotated backup's suffix. The reader (AC-0021) reads ".1" then current as
# one logical stream, so this name is part of that contract.
ROTATED_SUFFIX = ".1"

def now_iso() -> str:
    """Current UTC timestamp, ISO 8601. Hooks call this; the pure builders take the
    timestamp as an argument so golden tests stay deterministic."""
    return datetime.now(timezone.utc).isoformat()


def strip_query(url: str) -> str:
    """Return ``url`` with its query string and fragment removed.

    The query is where credentials ride -- bearer tokens, signed-URL signatures,
    session ids -- under arbitrary parameter names, so it is dropped entirely rather
    than scrubbed against a denylist that can't anticipate every name. The scheme,
    host, and path are kept for debugging value. A URL with no query or fragment is
    returned unchanged.
    """
    parts = urlsplit(url)
    if not parts.query and not parts.fragment:
        return url
    return urlunsplit(parts._replace(query="", fragment=""))


def request_entry(
    ts: str,
    method: str,
    url: str,
    decision: str,
    rule: Optional[dict],
    status: int,
) -> dict:
    """A full entry for an intercepted (TLS-terminated) request. ``rule`` is the
    matched rule as ``{"list","index"}`` or None (soft-deny). ``url`` has its query
    string stripped (see ``strip_query``)."""
    return {
        "ts": ts,
        "method": method,
        "url": strip_query(url),
        "decision": decision,
        "rule": rule,
        "status": status,
    }


def passthrough_entry(ts: str, host: str, decision: str) -> dict:
    """A host-only entry for a passthrough host -- no path/method/status, by
    construction (the proxy never terminates TLS, so it sees only the host)."""
    return {"ts": ts, "host": host, "decision": decision}


def encode(entry: dict) -> bytes:
    """One compact JSONL line: minimal separators, insertion-ordered keys, trailing
    newline, UTF-8."""
    return (
        json.dumps(entry, ensure_ascii=False, separators=(",", ":")) + "\n"
    ).encode("utf-8")


class AuditLog:
    """Append-only 0600 JSONL writer with size-based rotation.

    Single-writer by design: the enforcer's hooks run on one asyncio event loop, so
    writes are serialized and need no lock. The handle is opened lazily on the first
    write and reopened across a rotation.
    """

    def __init__(self, path: str, max_bytes: int = DEFAULT_MAX_BYTES) -> None:
        self._path = path
        self._rotated = path + ROTATED_SUFFIX
        self._max_bytes = max_bytes
        self._fh = None
        self._size = 0

    def write(self, entry: dict) -> None:
        line = encode(entry)
        self._ensure_open()
        # Rotate before a write that would exceed the cap -- but never rotate a fresh
        # (empty) file just because one line alone is over the threshold; write it
        # rather than split or drop it.
        if self._size > 0 and self._size + len(line) > self._max_bytes:
            self._rotate()
        self._fh.write(line)
        self._fh.flush()
        self._size += len(line)

    def close(self) -> None:
        if self._fh is not None:
            self._fh.close()
            self._fh = None

    def _ensure_open(self) -> None:
        if self._fh is not None:
            return
        directory = os.path.dirname(self._path)
        if directory:
            os.makedirs(directory, exist_ok=True)
        # O_APPEND so concurrent host-side readers never see a partial line; mode 0600
        # on create, then fchmod to guarantee the bits regardless of umask.
        fd = os.open(self._path, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
        os.fchmod(fd, 0o600)
        self._fh = os.fdopen(fd, "ab", buffering=0)
        self._size = os.path.getsize(self._path)

    def _rotate(self) -> None:
        # os.replace is atomic and overwrites any existing .1, which is exactly
        # "delete .1, rename current -> .1". Then a fresh current file is opened.
        self.close()
        os.replace(self._path, self._rotated)
        self._ensure_open()
        self._size = 0
