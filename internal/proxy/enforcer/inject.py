"""The credential value-template: render an auth-header value from a token.

This is the runtime injector's port of internal/config/template.go (the Go spec).
Like the matcher (policy.py <-> internal/policy), the value-template exists twice --
in Go (validated at config-compile time) and here in Python (applied at inject time)
-- and the two must never disagree. The parity table in test_inject.py replays the
same shapes as internal/config/template_test.go.

DSL (kept byte-for-byte with template.go):

  - ``{token}`` is replaced by the resolved secret, ``{user}`` by the credential's
    username sentinel. Both are literal, replace-all substitutions.
  - An optional single ``base64( ... )`` wrapper base64-encodes (standard alphabet,
    padded) the substituted inner expression and splices it back in place; the
    prefix/suffix around it are substituted but not encoded.
  - Supported shapes: ``Bearer {token}``, ``token {token}``, ``{token}``,
    ``Basic base64({user}:{token})``, and any custom value containing ``{token}``.

The module is pure (no mitmproxy import) so the parity test runs without it.
"""

from __future__ import annotations

import base64

TOKEN_PLACEHOLDER = "{token}"
USER_PLACEHOLDER = "{user}"
BASE64_OPEN = "base64("


def render_credential_value(template: str, username: str, token: str) -> str:
    """Render ``template`` into a header value with ``username``/``token`` substituted.

    Raises ValueError on a malformed template (the same cases validate_template
    rejects); the compiler validates templates up front, so at inject time this is a
    guard, not the primary check.
    """
    validate_template(template, username)

    open_idx = template.find(BASE64_OPEN)
    if open_idx < 0:
        return _substitute(template, username, token)

    # validate_template guarantees exactly one base64( with a matching ), non-nested.
    rest = template[open_idx + len(BASE64_OPEN):]
    close_idx = rest.find(")")
    inner = rest[:close_idx]
    prefix = template[:open_idx]
    suffix = rest[close_idx + 1:]

    encoded = base64.standard_b64encode(
        _substitute(inner, username, token).encode("utf-8")
    ).decode("ascii")
    return (
        _substitute(prefix, username, token)
        + encoded
        + _substitute(suffix, username, token)
    )


def _substitute(s: str, username: str, token: str) -> str:
    # {user} first, then {token} -- the secret is substituted last, matching
    # template.go's substitutePlaceholders ordering.
    return s.replace(USER_PLACEHOLDER, username).replace(TOKEN_PLACEHOLDER, token)


def validate_template(template: str, username: str) -> None:
    """Raise ValueError unless ``template`` is a well-formed value-template.

    Mirrors template.go validateTemplate: {token} required; {user} needs a username;
    at most one balanced base64(...) wrapper; no unknown {...} placeholders.
    """
    if TOKEN_PLACEHOLDER not in template:
        raise ValueError(
            f"value-template {template!r} must contain the {TOKEN_PLACEHOLDER} placeholder"
        )
    if USER_PLACEHOLDER in template and username == "":
        raise ValueError(
            f"value-template {template!r} uses {USER_PLACEHOLDER} but the credential sets no username"
        )

    open_idx = template.find(BASE64_OPEN)
    if open_idx >= 0:
        rest = template[open_idx + len(BASE64_OPEN):]
        close_idx = rest.find(")")
        if close_idx < 0:
            raise ValueError(
                f"value-template {template!r} has an unbalanced {BASE64_OPEN} (missing closing ')')"
            )
        if BASE64_OPEN in rest[:close_idx] or BASE64_OPEN in rest[close_idx + 1:]:
            raise ValueError(
                f"value-template {template!r} supports at most one {BASE64_OPEN}...) wrapper"
            )

    stripped = template.replace(USER_PLACEHOLDER, "").replace(TOKEN_PLACEHOLDER, "")
    if "{" in stripped or "}" in stripped:
        raise ValueError(
            f"value-template {template!r} contains an unknown {{...}} placeholder "
            f"(only {USER_PLACEHOLDER} and {TOKEN_PLACEHOLDER} are supported)"
        )
