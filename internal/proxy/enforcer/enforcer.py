"""The agent-creance mitmproxy enforcer addon (WP-3.1 / AC-0017).

This is the runtime enforcer: a mitmproxy addon that reads the compiled policy.json
and turns each egress attempt into one of the three outcomes the agent understands.

  - allow      -> forward upstream untouched.
  - soft-deny  -> 403 + X-Cage-Reason: soft-deny + the agent_cage_not_allowlisted body.
  - hard-deny  -> 403 + X-Cage-Reason: hard-deny + the agent_cage_hard_deny body.

Per-host modes:

  - intercept  -> TLS is terminated; the full host+path+method matcher runs in the
                  ``request`` hook (this is also how soft/hard-deny *bodies* reach the
                  agent over HTTPS).
  - passthrough-> the connection is tunnelled without terminating TLS (the agent's
                  client validates the real upstream certificate). Host-granularity
                  only; a host-level deny_always is still enforced by refusing the
                  CONNECT.

The policy is hot-reloaded by polling policy.json's mtime, so `agent-creance allow`
from another terminal takes effect within ~1s without restarting the cage.

The decision logic itself lives in policy.py (a pure port of the Go matcher), and
the wire bodies in responses.py -- both kept free of any mitmproxy import so the C1
corpus and the golden tests run without it. This file is the thin mitmproxy glue.

Out of scope here (separate tickets): audit logging (AC-0018), go:embed/extraction
(AC-0019), proxy lifecycle/lock/port (AC-0020). The policy path is supplied as a
mitmproxy option (`--set creance_policy=<path>`), which AC-0020 will wire up.
"""

from __future__ import annotations

import asyncio
import logging
import os
from typing import Optional

from mitmproxy import ctx, http, tls

import policy
import responses

logger = logging.getLogger(__name__)

# How often the hot-reload loop checks policy.json's mtime.
_POLL_INTERVAL_SECONDS = 1.0


class Enforcer:
    """The mitmproxy addon. Holds the live RuleSet and the policy file's mtime."""

    def __init__(self) -> None:
        self._ruleset = policy.RuleSet()
        self._policy_path: str = ""
        self._mtime: Optional[float] = None
        self._task: Optional[asyncio.Task] = None

    # --- addon lifecycle -------------------------------------------------------

    def load(self, loader) -> None:
        loader.add_option(
            name="creance_policy",
            typespec=str,
            default="",
            help="Path to the compiled agent-creance policy.json the enforcer reads.",
        )

    def configure(self, updated) -> None:
        if "creance_policy" in updated:
            self._policy_path = ctx.options.creance_policy
            self._load()

    def running(self) -> None:
        # Background mtime poll for hot reload. Hold the task reference so it is not
        # garbage-collected; cancel it on shutdown.
        if self._task is None:
            self._task = asyncio.create_task(self._poll_loop())

    def done(self) -> None:
        if self._task is not None:
            self._task.cancel()
            self._task = None

    # --- policy loading / hot reload ------------------------------------------

    def _load(self) -> None:
        if not self._policy_path:
            return
        try:
            self._ruleset = policy.load_policy(self._policy_path)
            self._mtime = os.path.getmtime(self._policy_path)
            logger.info("loaded policy from %s", self._policy_path)
        except FileNotFoundError:
            logger.warning("policy file not found: %s", self._policy_path)
        except (OSError, ValueError) as exc:  # malformed JSON, unreadable file
            logger.error("failed to load policy %s: %s", self._policy_path, exc)

    def _maybe_reload(self) -> None:
        """Reload the policy if the file's mtime changed since the last load."""
        if not self._policy_path:
            return
        try:
            mtime = os.path.getmtime(self._policy_path)
        except FileNotFoundError:
            return
        if mtime != self._mtime:
            self._load()

    async def _poll_loop(self) -> None:
        while True:
            await asyncio.sleep(_POLL_INTERVAL_SECONDS)
            self._maybe_reload()

    # --- enforcement hooks -----------------------------------------------------

    def http_connect(self, flow: http.HTTPFlow) -> None:
        """Refuse a CONNECT only for a passthrough host that is host-level denied.

        A passthrough host is never TLS-terminated, so a host-level deny on it can
        only be enforced here, by refusing the tunnel. Intercept hosts (incl.
        not-allowlisted and path/host-denied ones) are allowed to CONNECT so TLS
        terminates and the ``request`` hook can return the structured 403 body.
        """
        host = flow.request.host
        disp = policy.host_disposition(self._ruleset, host)
        if disp.passthrough and disp.deny_reason is not None:
            url = f"https://{host}/"
            r = responses.hard_deny(url, disp.deny_reason)
            flow.response = http.Response.make(r.status, r.body, r.headers)

    def tls_clienthello(self, data: tls.ClientHelloData) -> None:
        """Tunnel passthrough hosts without terminating TLS (real upstream cert)."""
        sni = data.client_hello.sni
        if not sni:
            return
        disp = policy.host_disposition(self._ruleset, sni)
        if disp.passthrough and disp.deny_reason is None:
            data.ignore_connection = True

    def request(self, flow: http.HTTPFlow) -> None:
        """Decide an intercepted (TLS-terminated) request: allow / soft / hard."""
        if flow.response is not None:
            return  # already answered (e.g. at http_connect)

        req = policy.Request(
            host=flow.request.pretty_host,
            path=flow.request.path,
            method=flow.request.method,
        )
        result = policy.decide(self._ruleset, req)
        if result.decision == policy.DECISION_ALLOW:
            return  # forward upstream untouched

        url = flow.request.pretty_url
        if result.decision == policy.DECISION_SOFT_DENY:
            r = responses.soft_deny(url, req.host, req.path, req.method)
        else:  # hard-deny
            reason = ""
            if result.matched is not None:
                reason = self._ruleset.deny_always[result.matched.index].reason
            r = responses.hard_deny(url, reason)

        flow.response = http.Response.make(r.status, r.body, r.headers)


addons = [Enforcer()]
