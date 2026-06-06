"""Pure egress matcher: a faithful Python port of internal/policy (the Go matcher).

Given an in-memory rule set and a request (host, path, method), ``decide`` returns
the cage's verdict -- allow, soft-deny, or hard-deny -- plus the carried enforcement
mode and the rule the decision is attributed to. It is deliberately pure: no
filesystem in the matcher itself (``load_policy`` is the one disk touch, kept
separate), no mitmproxy import. That keeps the C1 decision-vector corpus and the
golden tests runnable without mitmproxy installed.

This logic exists twice -- here in Python (for the mitmproxy enforcer, AC-0017) and
in Go (for ``policy explain``, internal/policy). They must never disagree. The
guardrail is the language-neutral corpus under internal/policy/testdata/
decision-vectors/ (cross-cutting C1), which both implementations replay. No change
to this matcher is complete without the corpus still passing.

Matching semantics (kept byte-for-byte with internal/policy/match.go + glob.go):

  - Host: case-insensitive. "*" matches any host; "*.suffix" matches any host with
    at least one label before ".suffix" (the bare apex is NOT matched); else exact.
  - Path: prefix-by-default. Pattern and path are trimmed of leading/trailing "/"
    and split into segments; the pattern must match a *prefix* of the path's
    segments. Within a segment "*" matches any run of characters; "?" and
    everything else are literal. A whole-segment "**" matches zero or more segments
    (the only token that crosses "/"); "**" glued to other characters degrades to
    "*". A rule with no paths (None) matches any path; a rule with paths matches if
    any one pattern matches.
  - Method: a rule with no methods (None) matches any method; otherwise the request
    method must be a verbatim member of the list.
  - Most-specific-wins: ordered, deterministic tie-break, independent of rule order.
  - Passthrough blind spot: a path-scoped deny_always is suppressed when the
    request's most-specific matching allow is mode "passthrough"; a host-level
    deny_always (no paths) still hard-denies a passthrough host.

IMPORTANT -- None vs empty list: an absent ``paths``/``methods`` key (or JSON null)
decodes to ``None`` and means "any" (host-wide / any method), exactly like Go's nil
slice. A present empty list ``[]`` is NOT the same: it matches no path / no method.
"""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from typing import Optional

# Decision constants -- exhaustive, matching internal/policy/policy.go.
DECISION_ALLOW = "allow"
DECISION_SOFT_DENY = "soft-deny"
DECISION_HARD_DENY = "hard-deny"

# Enforcement modes a rule may carry.
MODE_INTERCEPT = "intercept"
MODE_PASSTHROUGH = "passthrough"

# policy.json schema version -- the cross-language contract with the Go compiler.
COMPILED_VERSION = 1


@dataclass(frozen=True)
class Rule:
    """One egress allow/deny entry as the matcher sees it.

    ``paths``/``methods`` are None when the key is omitted ("any"), mirroring Go's
    nil slice. ``source``/``lower_trust`` are stamped by the compiler for the
    on-disk artifact and are ignored by ``decide`` (absent from the corpus).
    """

    host: str
    paths: Optional[list[str]] = None
    methods: Optional[list[str]] = None
    mode: str = ""
    reason: str = ""

    @staticmethod
    def from_dict(d: dict) -> "Rule":
        return Rule(
            host=d["host"],
            paths=d.get("paths"),  # None when absent or JSON null
            methods=d.get("methods"),
            mode=d.get("mode", "") or "",
            reason=d.get("reason", "") or "",
        )


@dataclass(frozen=True)
class RuleSet:
    """The compiled, in-memory policy: the unioned allow and deny_always lists."""

    allow: list[Rule] = field(default_factory=list)
    deny_always: list[Rule] = field(default_factory=list)

    @staticmethod
    def from_dict(d: dict) -> "RuleSet":
        return RuleSet(
            allow=[Rule.from_dict(r) for r in (d.get("allow") or [])],
            deny_always=[Rule.from_dict(r) for r in (d.get("deny_always") or [])],
        )


@dataclass(frozen=True)
class Request:
    """The single egress attempt being decided."""

    host: str
    path: str
    method: str

    @staticmethod
    def from_dict(d: dict) -> "Request":
        return Request(host=d["host"], path=d["path"], method=d["method"])


@dataclass(frozen=True)
class MatchedRule:
    """Which list a decision is attributed to, and the rule's index within it.

    None for a soft-deny. The list/index form is language-neutral -- it survives two
    rules sharing a host, where a host string alone would be ambiguous.
    """

    list: str  # "allow" or "deny_always"
    index: int

    def to_dict(self) -> dict:
        return {"list": self.list, "index": self.index}


@dataclass(frozen=True)
class Result:
    """The matcher's verdict: decision, carried mode ("" for soft-deny), matched."""

    decision: str
    mode: str = ""
    matched: Optional[MatchedRule] = None


# --- the decision algorithm (internal/policy/match.go) -------------------------


def decide(rs: RuleSet, req: Request) -> Result:
    """Evaluate ``req`` against ``rs`` and return the cage's verdict.

    1. Find the most-specific matching allow; the host is "passthrough" iff that
       allow carries mode passthrough.
    2. Evaluate deny_always. A host-level deny (no paths) hard-denies even a
       passthrough host; a path-scoped deny hard-denies only on a non-passthrough
       host. The most specific *eligible* matching deny is reported.
    3. Otherwise an allow match is an allow (carrying its mode); no match is the
       implicit soft-deny.
    """
    allow_idx, allow_ok = _best_match(rs.allow, req, None)
    is_passthrough = allow_ok and rs.allow[allow_idx].mode == MODE_PASSTHROUGH

    # deny_always shadows allow. On a passthrough host only host-level denies are
    # eligible, because a path-scoped deny cannot be enforced without the path.
    eligible = None
    if is_passthrough:
        eligible = lambda r: r.paths is None  # noqa: E731

    deny_idx, deny_ok = _best_match(rs.deny_always, req, eligible)
    if deny_ok:
        r = rs.deny_always[deny_idx]
        return Result(
            decision=DECISION_HARD_DENY,
            mode=r.mode,
            matched=MatchedRule("deny_always", deny_idx),
        )

    if allow_ok:
        return Result(
            decision=DECISION_ALLOW,
            mode=rs.allow[allow_idx].mode,
            matched=MatchedRule("allow", allow_idx),
        )
    return Result(decision=DECISION_SOFT_DENY)


def _best_match(rules, req, eligible):
    """Index of the most-specific rule that matches req and passes ``eligible``.

    ``eligible`` of None means all rules are eligible. Returns (index, found). On an
    exact specificity tie the earlier index is kept (strict more-specific only).
    """
    best = -1
    best_spec = None
    for i, r in enumerate(rules):
        if eligible is not None and not eligible(r):
            continue
        pattern, ok = _rule_matches(r, req)
        if not ok:
            continue
        spec = _rule_specificity(r, pattern)
        if best == -1 or _more_specific(spec, best_spec):
            best, best_spec = i, spec
    return best, best != -1


def _rule_matches(r: Rule, req: Request):
    """Whether r matches req on host, method, and path.

    Returns (matched_pattern, ok); the matched pattern is the most specific one when
    the rule lists several, "" when the rule is host-wide.
    """
    if not _match_host(r.host, req.host):
        return "", False
    if not _match_method(r.methods, req.method):
        return "", False
    if r.paths is None:
        return "", True  # host-wide: matches any path

    matched = ""
    found = False
    for p in r.paths:
        if not _match_path(p, req.path):
            continue
        if not found or _more_specific(
            _rule_specificity(r, p), _rule_specificity(r, matched)
        ):
            matched, found = p, True
    return matched, found


# --- host / path / method matching (internal/policy/glob.go) -------------------


def _match_host(pattern: str, host: str) -> bool:
    pattern = pattern.lower()
    host = host.lower()
    if pattern == "*":
        return True
    if pattern.startswith("*."):
        # suffix includes the leading dot, e.g. ".medium.com"; endswith then
        # requires at least one label before it, so the apex does not match.
        return host.endswith(pattern[1:])
    return pattern == host


def _match_path(pattern: str, path: str) -> bool:
    return _match_segments(_split_path(pattern), _split_path(path))


def _split_path(p: str) -> list[str]:
    """Trim leading/trailing "/" then split on "/". Root ("/") yields no segments."""
    p = p.strip("/")
    if p == "":
        return []
    return p.split("/")


def _match_segments(pat: list[str], path: list[str]) -> bool:
    """Whether pat matches a prefix of path. "**" consumes zero or more segments."""
    if len(pat) == 0:
        return True
    if pat[0] == "**":
        # Consume zero segments, or consume one and retry the same "**".
        if _match_segments(pat[1:], path):
            return True
        return len(path) > 0 and _match_segments(pat, path[1:])
    if len(path) == 0:
        return False
    return _match_segment_glob(pat[0], path[0]) and _match_segments(pat[1:], path[1:])


def _match_segment_glob(pat: str, seg: str) -> bool:
    """Single-segment glob. "*" matches any run; "?" and all else are literal.

    Classic two-pointer wildcard match with backtracking on the last "*". A segment
    that is not exactly "**" but contains "**" treats each "*" independently, so
    glued "**" degrades to "*".
    """
    pi = si = 0
    star = -1
    star_match_end = 0
    while si < len(seg):
        if pi < len(pat) and pat[pi] == "*":
            star = pi
            star_match_end = si
            pi += 1
        elif pi < len(pat) and pat[pi] == seg[si]:
            pi += 1
            si += 1
        elif star >= 0:
            pi = star + 1
            star_match_end += 1
            si = star_match_end
        else:
            return False
    while pi < len(pat) and pat[pi] == "*":
        pi += 1
    return pi == len(pat)


def _match_method(methods: Optional[list[str]], method: str) -> bool:
    """None matches any method; otherwise verbatim membership ([] matches none)."""
    if methods is None:
        return True
    return method in methods


# --- specificity / tie-breaking (internal/policy/glob.go) ----------------------


@dataclass(frozen=True)
class _Specificity:
    host_rank: int  # exact 2, *.suffix 1, * 0
    path_scoped: int  # 1 if the rule constrains paths
    literal_segs: int  # count of non-wildcard segments in the matched pattern
    total_segs: int  # segment count of the matched pattern
    method_scoped: int  # 1 if the rule constrains methods
    canonical: str  # deterministic fallback for exact ties


def _rule_specificity(r: Rule, matched_pattern: str) -> _Specificity:
    canonical = (
        r.host
        + "|"
        + ",".join(r.paths or [])
        + "|"
        + ",".join(r.methods or [])
        + "|"
        + r.mode
    )
    path_scoped = 0
    literal_segs = 0
    total_segs = 0
    if r.paths is not None:
        path_scoped = 1
        segs = _split_path(matched_pattern)
        total_segs = len(segs)
        for seg in segs:
            if "*" not in seg:
                literal_segs += 1
    method_scoped = 1 if r.methods is not None else 0
    return _Specificity(
        host_rank=_host_rank(r.host),
        path_scoped=path_scoped,
        literal_segs=literal_segs,
        total_segs=total_segs,
        method_scoped=method_scoped,
        canonical=canonical,
    )


def _host_rank(host: str) -> int:
    if host == "*":
        return 0
    if host.startswith("*."):
        return 1
    return 2


def _more_specific(a: _Specificity, b: _Specificity) -> bool:
    """Whether a is strictly more specific than b (canonical: smaller wins)."""
    if a.host_rank != b.host_rank:
        return a.host_rank > b.host_rank
    if a.path_scoped != b.path_scoped:
        return a.path_scoped > b.path_scoped
    if a.literal_segs != b.literal_segs:
        return a.literal_segs > b.literal_segs
    if a.total_segs != b.total_segs:
        return a.total_segs > b.total_segs
    if a.method_scoped != b.method_scoped:
        return a.method_scoped > b.method_scoped
    return a.canonical < b.canonical


# --- connect-stage disposition (host only; no path/method visible) -------------


@dataclass(frozen=True)
class HostDisposition:
    """What the proxy can decide about a host at CONNECT / TLS-ClientHello time.

    At that stage only the host (CONNECT authority / SNI) is known -- there is no
    path or method yet -- so this is a deliberately host-granular view, consistent
    with "passthrough is host-granularity only by construction" (docs/design.md).

    - ``passthrough``: tunnel without terminating TLS. True iff the most
      host-specific matching allow is passthrough AND no equally-host-specific allow
      asks to intercept (an intercept rule forces termination so path/method rules
      can be applied; an ambiguous/mixed host resolves to intercept).
    - ``deny_reason``: set when a host-level deny_always (no paths) matches. Such a
      deny is still enforced on a passthrough host -- the tunnel is refused at
      CONNECT, since it cannot be enforced once tunnelled. (Path-scoped denies are
      invisible at this stage and handled later, per-request, on intercept hosts.)
    """

    passthrough: bool
    deny_reason: Optional[str]


def host_disposition(rs: RuleSet, host: str) -> HostDisposition:
    # Host-level deny (paths is None): the most host-specific matching one wins.
    deny_reason = None
    best_deny_rank = -1
    for r in rs.deny_always:
        if r.paths is None and _match_host(r.host, host):
            rank = _host_rank(r.host)
            if rank > best_deny_rank:
                best_deny_rank, deny_reason = rank, r.reason

    # Passthrough iff the top host-rank tier of matching allows is all passthrough.
    matching = [r for r in rs.allow if _match_host(r.host, host)]
    passthrough = False
    if matching:
        top_rank = max(_host_rank(r.host) for r in matching)
        top = [r for r in matching if _host_rank(r.host) == top_rank]
        passthrough = all(r.mode == MODE_PASSTHROUGH for r in top)

    return HostDisposition(passthrough=passthrough, deny_reason=deny_reason)


# --- policy.json loading -------------------------------------------------------


def load_policy(path: str) -> RuleSet:
    """Load a compiled policy.json ({version, input_hash, allow, deny_always}).

    ``input_hash`` is ignored (it keys the compiler's cache). Missing allow/
    deny_always lists tolerate as empty.
    """
    with open(path, "rb") as f:
        data = json.load(f)
    return RuleSet.from_dict(data)
