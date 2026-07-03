"""Parity tests for inject.render_credential_value against the Go spec.

The cases mirror internal/config/template_test.go 1:1 (same templates, same
placeholder token, same expected strings) so the two implementations of the
value-template cannot drift.
"""

import base64

import inject
import pytest

TOKEN = "PLACEHOLDER-TOKEN"

_BASIC_WANT = "Basic " + base64.standard_b64encode(
    b"x-access-token:" + TOKEN.encode()
).decode("ascii")


@pytest.mark.parametrize(
    "template,username,want",
    [
        ("Bearer {token}", "", "Bearer " + TOKEN),
        ("token {token}", "", "token " + TOKEN),
        ("{token}", "", TOKEN),
        ("Basic base64({user}:{token})", "x-access-token", _BASIC_WANT),
        ("{token}", "", TOKEN),  # custom header value; header name is separate
        ("SSWS {token}", "", "SSWS " + TOKEN),
    ],
)
def test_render_matches_go_spec(template, username, want):
    got = inject.render_credential_value(template, username, TOKEN)
    assert got == want
    # A rendered value must never still carry a placeholder.
    assert "{token}" not in got and "{user}" not in got


@pytest.mark.parametrize(
    "template,username",
    [
        ("Bearer static", ""),  # no {token}
        ("Basic base64({user}:{token})", ""),  # {user} without username
        ("Basic base64({user}:{token}", "x"),  # unbalanced base64(
        ("base64({token}) base64({token})", ""),  # double base64(
        ("Bearer {token} {scope}", ""),  # unknown placeholder
    ],
)
def test_validate_rejects_malformed(template, username):
    with pytest.raises(ValueError):
        inject.validate_template(template, username)
    # render validates first, so it must reject too.
    with pytest.raises(ValueError):
        inject.render_credential_value(template, username, "T")


@pytest.mark.parametrize(
    "template,username",
    [
        ("Bearer {token}", ""),
        ("token {token}", ""),
        ("{token}", ""),
        ("Basic base64({user}:{token})", "__token__"),
        ("PRIVATE-TOKEN {token}", ""),
    ],
)
def test_validate_accepts_wellformed(template, username):
    inject.validate_template(template, username)  # must not raise
