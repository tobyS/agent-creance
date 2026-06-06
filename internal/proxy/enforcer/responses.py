"""The cage's wire responses: the three 403 bodies + the X-Cage-Reason header.

These are what the *agent* receives over the wire when a request is refused. The
shapes are fixed by docs/design.md ("Network refusal handling"): a soft-deny ("not
allowlisted, could be added") and a hard-deny ("permanently blocked, find another
way"), each an HTTP 403 with an ``X-Cage-Reason`` header and a structured JSON body
whose ``error`` carries the ``agent_cage_`` prefix the shipped skill activates on.

This is the ONLY implementation of these bodies — the Go ``policy/render`` package
renders a different, operator-facing ``explain`` JSON and is not a template. The
module is pure (no mitmproxy import) so the golden tests run without it.
"""

from __future__ import annotations

import json
from dataclasses import dataclass

# Header name + the two values, byte-for-byte what the skill keys on.
X_CAGE_REASON = "X-Cage-Reason"
REASON_SOFT_DENY = "soft-deny"
REASON_HARD_DENY = "hard-deny"

# The agent_cage_ error enums (skill activates on the prefix).
ERROR_SOFT_DENY = "agent_cage_not_allowlisted"
ERROR_HARD_DENY = "agent_cage_hard_deny"

# Fixed how_to_proceed guidance. The soft-deny copy is the wording agreed at the
# AC-0017 planning checkpoint (deliberately NOT "route around silently"). The
# hard-deny copy is verbatim from docs/design.md.
HOW_TO_PROCEED_SOFT = (
    "Not on the project allowlist. Ignore this resource if you can find the needed "
    "information elsewhere or can work reliably without it. If you think the "
    "information is important and would contribute significantly to your success, "
    "prompt the user and ask them to add the resource to the allowlist."
)
HOW_TO_PROCEED_HARD = (
    "Permanently blocked. Do NOT ask the user to allow it. Do NOT retry. Find an "
    "alternative source."
)

_STATUS_FORBIDDEN = 403
_CONTENT_TYPE = "application/json"


@dataclass(frozen=True)
class CageResponse:
    """A synthetic refusal response the addon hands to mitmproxy."""

    status: int
    headers: dict[str, str]
    body: bytes


def _encode(obj: dict) -> bytes:
    """Deterministic JSON: insertion-ordered keys, 2-space indent, trailing newline.

    Mirrors the project's operator-JSON convention (json.MarshalIndent + "\\n").
    """
    return (json.dumps(obj, indent=2, ensure_ascii=False) + "\n").encode("utf-8")


def soft_deny(url: str, host: str, path: str, method: str) -> CageResponse:
    """403 for a host/path not on the allowlist (recoverable via `allow`)."""
    body = _encode(
        {
            "error": ERROR_SOFT_DENY,
            "url": url,
            "host": host,
            "path": path,
            "method": method,
            "how_to_proceed": HOW_TO_PROCEED_SOFT,
            "allow_command_suggestion": f"agent-creance allow '{host}{path}'",
        }
    )
    return CageResponse(
        status=_STATUS_FORBIDDEN,
        headers={"Content-Type": _CONTENT_TYPE, X_CAGE_REASON: REASON_SOFT_DENY},
        body=body,
    )


def hard_deny(url: str, reason: str) -> CageResponse:
    """403 for a permanently blocked host/path. ``reason`` is the deny rule's reason."""
    body = _encode(
        {
            "error": ERROR_HARD_DENY,
            "url": url,
            "reason": reason,
            "how_to_proceed": HOW_TO_PROCEED_HARD,
        }
    )
    return CageResponse(
        status=_STATUS_FORBIDDEN,
        headers={"Content-Type": _CONTENT_TYPE, X_CAGE_REASON: REASON_HARD_DENY},
        body=body,
    )
