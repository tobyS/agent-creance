"""The agent-creance mitmproxy enforcer addon (WP-3.1 / AC-0017).

This is the runtime enforcer: a mitmproxy addon that reads the compiled policy.json
and turns each egress attempt into one of the three outcomes the agent understands.

  - allow      -> forward upstream untouched.
  - soft-deny  -> 470 + X-Cage-Reason: soft-deny + the agent_cage_not_allowlisted body.
  - hard-deny  -> 471 + X-Cage-Reason: hard-deny + the agent_cage_hard_deny body.

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

The decision logic itself lives in policy.py (a pure port of the Go matcher), the
wire bodies in responses.py, and the egress audit writer in audit.py -- all kept
free of any mitmproxy import so the C1 corpus and the golden tests run without it.
This file is the thin mitmproxy glue.

Every decision is recorded to the JSONL audit log (audit.py): intercepted requests
get a full entry from the ``response`` hook (which fires for our synthesized
refusals too), and passthrough hosts get a host-only entry at the connect/clienthello stage,
since an ignored tunnel exposes no path/method/status to the addon. The audit log
path arrives as a mitmproxy option (`--set creance_audit_log=<path>`; empty disables
it), exactly like the policy path -- both are wired from the Go launcher by AC-0020.

Out of scope here (separate tickets): go:embed/extraction (AC-0019), proxy
lifecycle/lock/port (AC-0020). The policy path is supplied as a mitmproxy option
(`--set creance_policy=<path>`), which AC-0020 will wire up.
"""

from __future__ import annotations

import asyncio
import logging
import os
from typing import Optional

from mitmproxy import ctx, http, tls

import audit
import policy
import responses

logger = logging.getLogger(__name__)

# How often the hot-reload loop checks policy.json's mtime.
_POLL_INTERVAL_SECONDS = 1.0


def _make_response(r: responses.CageResponse) -> http.Response:
    """Build the mitmproxy response from a CageResponse, including the reason phrase.

    Response.make defaults the reason to "" for unregistered codes (470/471), so we
    set it explicitly — see responses.REASON_PHRASE_* for why (AC-0050).
    """
    resp = http.Response.make(r.status, r.body, r.headers)
    resp.reason = r.reason_phrase
    return resp


class Enforcer:
    """The mitmproxy addon. Holds the live RuleSet and the policy file's mtime."""

    def __init__(self) -> None:
        self._ruleset = policy.RuleSet()
        self._policy_path: str = ""
        self._mtime: Optional[float] = None
        self._task: Optional[asyncio.Task] = None
        self._audit_path: str = ""
        self._audit: Optional[audit.AuditLog] = None

    # --- addon lifecycle -------------------------------------------------------

    def load(self, loader) -> None:
        loader.add_option(
            name="creance_policy",
            typespec=str,
            default="",
            help="Path to the compiled agent-creance policy.json the enforcer reads.",
        )
        loader.add_option(
            name="creance_audit_log",
            typespec=str,
            default="",
            help="Path to the egress.jsonl audit log the enforcer appends to ('' disables).",
        )

    def configure(self, updated) -> None:
        if "creance_policy" in updated:
            self._policy_path = ctx.options.creance_policy
            self._load()
        if "creance_audit_log" in updated:
            self._audit_path = ctx.options.creance_audit_log
            if self._audit is not None:
                self._audit.close()
            self._audit = (
                audit.AuditLog(self._audit_path) if self._audit_path else None
            )

    def running(self) -> None:
        # Two settings make the enforcer a true egress gate — the proxy must never
        # touch a denied host:
        #   - connection_strategy=lazy: don't open the upstream connection until an
        #     ALLOWED request is actually forwarded. A soft/hard-denied request is
        #     answered from the request hook, so its host is never connected to.
        #   - upstream_cert=False: generate the client-facing leaf cert from the SNI
        #     alone, never reach out to copy the upstream's certificate.
        # Without these, mitmproxy connects eagerly at CONNECT (and sniffs the
        # upstream cert), leaking a connection to every host — including denied ones
        # — and returning a spurious 502 for hosts that don't resolve.
        ctx.options.update(connection_strategy="lazy", upstream_cert=False)

        # Background mtime poll for hot reload. Hold the task reference so it is not
        # garbage-collected; cancel it on shutdown.
        if self._task is None:
            self._task = asyncio.create_task(self._poll_loop())

    def done(self) -> None:
        if self._task is not None:
            self._task.cancel()
            self._task = None
        if self._audit is not None:
            self._audit.close()
            self._audit = None

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
        terminates and the ``request`` hook can return the structured refusal body.
        """
        host = flow.request.host
        disp = policy.host_disposition(self._ruleset, host)
        if disp.passthrough and disp.deny_reason is not None:
            url = f"https://{host}/"
            r = responses.hard_deny(url, disp.deny_reason)
            flow.response = _make_response(r)
            # Host-only: TLS never terminates here, so there is no path/method/status
            # to record. This is the one place a denied passthrough host is audited
            # (the request/response hooks never run for it).
            self._audit_passthrough(host, policy.DECISION_HARD_DENY)

    def tls_clienthello(self, data: tls.ClientHelloData) -> None:
        """Tunnel passthrough hosts without terminating TLS (real upstream cert)."""
        sni = data.client_hello.sni
        if not sni:
            return
        disp = policy.host_disposition(self._ruleset, sni)
        if disp.passthrough and disp.deny_reason is None:
            data.ignore_connection = True
            # Host-only allow: the tunnel is about to be relayed raw (the addon sees
            # no flow, path, or byte counts for it), so this is where it is audited.
            self._audit_passthrough(sni, policy.DECISION_ALLOW)

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
        # Stash the verdict for the response hook (which logs the entry once the
        # status is known). Done for every intercepted request, incl. allows, and
        # *before* the allow early-return below. The presence of this key is also how
        # the response hook tells an intercepted flow from a CONNECT/passthrough one.
        flow.metadata["creance_audit"] = {
            "decision": result.decision,
            "rule": result.matched.to_dict() if result.matched is not None else None,
        }

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

        flow.response = _make_response(r)

    def response(self, flow: http.HTTPFlow) -> None:
        """Audit an intercepted request once its response status is known.

        Fires for real upstream responses AND the refusals we synthesize in ``request``
        (mitmproxy emulates the response hook for addon-set responses), so this is
        the single logging point for allow / soft-deny / hard-deny alike. Flows
        without a stashed verdict (CONNECT / passthrough) are skipped -- they are
        audited host-only at the connect/clienthello stage instead.
        """
        if self._audit is None:
            return
        rec = flow.metadata.get("creance_audit")
        if rec is None:
            return
        self._audit.write(
            audit.request_entry(
                audit.now_iso(),
                flow.request.method,
                flow.request.pretty_url,
                rec["decision"],
                rec["rule"],
                flow.response.status_code,
            )
        )

    # --- audit helpers ---------------------------------------------------------

    def _audit_passthrough(self, host: str, decision: str) -> None:
        """Write a host-only audit entry for a passthrough host (no path/method/
        status visible without TLS termination)."""
        if self._audit is not None:
            self._audit.write(audit.passthrough_entry(audit.now_iso(), host, decision))


addons = [Enforcer()]
