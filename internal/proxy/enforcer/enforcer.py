"""The agent-creance mitmproxy enforcer addon (WP-3.1 / AC-0017).

This is the runtime enforcer: a mitmproxy addon that reads the compiled policy.json
and turns each egress attempt into one of the three outcomes the agent understands.

  - allow      -> forward upstream untouched, unless the matched rule carries the auth
                  axis (AC-0068c): ``inject`` overwrites the credential's header with a
                  value rendered from a host-side-resolved token (472 if it did not
                  resolve), ``in_cage`` leaves auth untouched by contract.
  - soft-deny  -> 470 + X-Cage-Reason: soft-deny + the agent_cage_not_allowlisted body.
  - hard-deny  -> 471 + X-Cage-Reason: hard-deny + the agent_cage_hard_deny body.
  - injection-unavailable -> 472 + X-Cage-Reason: injection-unavailable + the
                  agent_cage_injection_unavailable body (fail-closed, human-recoverable).

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
import json
import logging
import os
from typing import Optional

from mitmproxy import ctx, http, tls

import audit
import inject
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


def _safe_url(flow: http.HTTPFlow) -> str:
    """Best-effort request URL for a refusal body, total even on a malformed flow.

    Used from the hooks' fail-closed handlers, where reading flow.request.pretty_url
    must never itself raise (that would re-open the fail-open hole this guards).
    """
    try:
        return flow.request.pretty_url
    except Exception:  # noqa: BLE001 — any failure falls back to a placeholder
        return "https://unknown/"


class Enforcer:
    """The mitmproxy addon. Holds the live RuleSet and the policy file's mtime."""

    def __init__(self) -> None:
        self._ruleset = policy.RuleSet()
        self._policy_path: str = ""
        self._mtime: Optional[float] = None
        self._task: Optional[asyncio.Task] = None
        self._audit_path: str = ""
        self._audit: Optional[audit.AuditLog] = None
        # Injection secrets: {credential-name: raw-token}, resolved host-side and
        # delivered once over the inherited fd. Held in memory for the proxy's
        # lifetime; never logged or written to disk (AC-0068c). Read at most once.
        self._secrets: dict[str, str] = {}
        self._secrets_read = False

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
        loader.add_option(
            name="creance_secret_fd",
            typespec=int,
            default=0,
            help="Inherited fd carrying the injection payload (0 disables injection).",
        )

    def configure(self, updated) -> None:
        if "creance_policy" in updated:
            self._policy_path = ctx.options.creance_policy
            self._load(initial=True)
        if "creance_audit_log" in updated:
            self._audit_path = ctx.options.creance_audit_log
            if self._audit is not None:
                self._audit.close()
            self._audit = (
                audit.AuditLog(self._audit_path) if self._audit_path else None
            )
        if "creance_secret_fd" in updated:
            fd = ctx.options.creance_secret_fd
            if fd > 0 and not self._secrets_read:
                # Read once, at startup, before any request is served.
                self._secrets_read = True
                self._read_secrets(fd)

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

    def _load(self, initial: bool = False) -> None:
        if not self._policy_path:
            return
        try:
            self._ruleset = policy.load_policy(self._policy_path)
            self._mtime = os.path.getmtime(self._policy_path)
            logger.info("loaded policy from %s", self._policy_path)
        except (FileNotFoundError, OSError, ValueError) as exc:  # missing/malformed/unreadable
            if initial:
                # A failed *initial* load is fatal: the addon would otherwise run on the
                # empty (deny-all) ruleset silently. Logging at ERROR during configure
                # trips mitmproxy's ErrorCheck, which exits the process non-zero so the
                # Go launcher's readiness wait sees the proxy die and surfaces it, rather
                # than reporting a healthy proxy that blocks everything (AC-0058 / B2).
                logger.error("failed to load initial policy %s: %s", self._policy_path, exc)
            elif isinstance(exc, FileNotFoundError):
                # The file vanished mid-run; keep the last-good ruleset, retry next tick.
                logger.warning("policy file not found during reload: %s", self._policy_path)
            else:
                # Malformed/unreadable during a hot reload: the assignment above never
                # completed, so the last-good ruleset stays in force; retry next tick when
                # a valid write lands (AC-0058 / B3).
                logger.error("failed to reload policy %s (keeping last-good): %s", self._policy_path, exc)

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

    # --- injection secrets -----------------------------------------------------

    def _read_secrets(self, fd: int) -> None:
        """Read the injection payload the Go launcher wrote to the inherited fd.

        The payload is JSON {credential-name: raw-token}, written to the write end of
        a pipe whose read end the child inherited at ``fd`` (sysdep.SecretFD). Read to
        EOF, parse, hold in memory, then close the fd. The secret values are never
        logged. A read/parse failure leaves the map empty, so inject-hosts fail closed
        (472) rather than the secret leaking into a log or the proxy refusing to start.
        """
        try:
            chunks = []
            while True:
                b = os.read(fd, 65536)
                if not b:
                    break
                chunks.append(b)
            raw = b"".join(chunks)
            if raw:
                loaded = json.loads(raw)
                self._secrets = {str(k): str(v) for k, v in loaded.items()}
            logger.info("loaded %d injection credential(s)", len(self._secrets))
        except (OSError, ValueError, TypeError) as exc:
            # Note: never interpolate the payload — only the exception type/message,
            # which carries no secret value.
            logger.error("failed to read injection secrets (fd %d): %s", fd, exc)
            self._secrets = {}
        finally:
            try:
                os.close(fd)
            except OSError:
                pass

    # --- enforcement hooks -----------------------------------------------------

    def http_connect(self, flow: http.HTTPFlow) -> None:
        """Refuse a CONNECT only for a passthrough host that is host-level denied.

        A passthrough host is never TLS-terminated, so a host-level deny on it can
        only be enforced here, by refusing the tunnel. Intercept hosts (incl.
        not-allowlisted and path/host-denied ones) are allowed to CONNECT so TLS
        terminates and the ``request`` hook can return the structured refusal body.
        """
        try:
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
        except Exception as exc:  # noqa: BLE001 — fail closed on ANY error (AC-0058 / B1)
            # An unhandled exception here would be logged and the CONNECT allowed to
            # proceed. Refuse the tunnel instead so a bug cannot leak a denied host.
            logger.error("enforcer http_connect hook failed; refusing CONNECT: %s", exc)
            flow.response = _make_response(
                responses.hard_deny(_safe_url(flow), "internal enforcer error")
            )

    def tls_clienthello(self, data: tls.ClientHelloData) -> None:
        """Tunnel passthrough hosts without terminating TLS (real upstream cert)."""
        try:
            sni = data.client_hello.sni
            if not sni:
                return
            disp = policy.host_disposition(self._ruleset, sni)
            if disp.passthrough and disp.deny_reason is None:
                data.ignore_connection = True
                # Host-only allow: the tunnel is about to be relayed raw (the addon sees
                # no flow, path, or byte counts for it), so this is where it is audited.
                self._audit_passthrough(sni, policy.DECISION_ALLOW)
        except Exception as exc:  # noqa: BLE001 — fail closed on ANY error (AC-0058 / B1)
            # Do NOT leave the connection ignored: terminating TLS routes it to the
            # (hard-closed) request hook, which decides safely. Undo any tunnel decision.
            data.ignore_connection = False
            logger.error("enforcer tls_clienthello hook failed; not tunnelling: %s", exc)

    def request(self, flow: http.HTTPFlow) -> None:
        """Decide an intercepted (TLS-terminated) request: allow / soft / hard."""
        if flow.response is not None:
            return  # already answered (e.g. at http_connect)

        try:
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
                self._apply_injection(flow, result)
                return  # forward upstream (with the injected header, or a 472)

            url = flow.request.pretty_url
            if result.decision == policy.DECISION_SOFT_DENY:
                r = responses.soft_deny(url, req.host, req.path, req.method)
            else:  # hard-deny
                reason = ""
                if result.matched is not None:
                    reason = self._ruleset.deny_always[result.matched.index].reason
                r = responses.hard_deny(url, reason)

            flow.response = _make_response(r)
        except Exception as exc:  # noqa: BLE001 — fail closed on ANY error (AC-0058 / B1)
            # mitmproxy logs an exception raised in the request hook and FORWARDS the
            # flow upstream (no flow.response was set) — a denied egress would leak. Turn
            # any unexpected error in the decision path into a hard-deny so the proxy
            # fails closed. The cardinal rule for a fail-closed egress filter.
            logger.error("enforcer request hook failed; hard-denying: %s", exc)
            flow.response = _make_response(
                responses.hard_deny(_safe_url(flow), "internal enforcer error")
            )

    def _apply_injection(self, flow: http.HTTPFlow, result: policy.Result) -> None:
        """Apply the matched allow rule's auth axis to an allowed request (AC-0068c).

        - ``in_cage``: leave auth untouched — the proxy guarantees it never adds,
          strips, or modifies an auth header on an in-cage host.
        - ``inject``: overwrite the credential's header with the rendered value. If the
          credential is unknown or its secret did not resolve host-side, fail closed
          with 472 instead of forwarding unauthenticated or with the phantom.
        - neither: today's default — the proxy does not touch auth.

        Runs inside the ``request`` hook's try/except, so an unexpected error (e.g. a
        malformed template that slipped past compile-time validation) falls through to
        the fail-closed hard-deny there.
        """
        if result.matched is None or result.matched.list != "allow":
            return
        rule = self._ruleset.allow[result.matched.index]
        if rule.in_cage:
            return
        if not rule.inject:
            return
        cred = self._ruleset.credentials.get(rule.inject)
        token = self._secrets.get(rule.inject)
        if cred is None or token is None:
            flow.response = _make_response(
                responses.injection_unavailable(flow.request.pretty_url, rule.inject)
            )
            return
        value = inject.render_credential_value(cred.template, cred.username, token)
        # Overwrite: header assignment replaces any client-supplied value(s) of this
        # name (including the phantom), so the cage cannot exceed the injected scope
        # against this host even if a prompt-injected agent set the header itself.
        flow.request.headers[cred.header] = value
        # Remember the credential so responseheaders can annotate an upstream 401/403.
        flow.metadata["creance_injected"] = rule.inject

    def responseheaders(self, flow: http.HTTPFlow) -> None:
        """Stream every upstream response body to the client incrementally.

        mitmproxy buffers response bodies by default so addons can inspect or modify
        them; this enforcer inspects no response body (the allow/deny decision is
        request-side, and the audit reads only the status code), so the default
        buffering merely adds latency and breaks long Server-Sent-Events responses --
        the caged client sees no bytes until the upstream stream closes, so a long
        Claude inference times out. Setting ``stream`` relays each chunk as it arrives.

        ``stream`` must be set *here*, in the responseheaders hook (after the status
        line and headers are read, before the body): setting it in ``response`` is too
        late, mitmproxy has already buffered the body. The ``response`` hook still fires
        afterwards (once the body has fully streamed) with ``status_code`` intact, so
        the single audit entry is unaffected. Refusals synthesized in ``request``
        short-circuit before upstream is contacted, so this hook never runs for them.

        This is also the only place a genuine upstream response is visible, so it is
        where an injected credential rejected upstream (a real 401/403) is annotated
        with X-Cage-Injected — the upstream owns the status, we only name the
        credential so the agent blames it rather than its phantom (AC-0068c). The
        header is set before streaming (mutating headers is fine; only the body is
        off-limits once ``stream`` is on).
        """
        if flow.response is None:
            return
        injected = flow.metadata.get("creance_injected")
        if injected and flow.response.status_code in (401, 403):
            flow.response.headers[responses.X_CAGE_INJECTED] = injected
        flow.response.stream = True

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
