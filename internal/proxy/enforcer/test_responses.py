"""Golden tests for the wire responses (the 470/471 refusal bodies + X-Cage-Reason).

Mirrors the Go ``-update`` golden pattern: pass ``--update`` to regenerate the
byte-for-byte body fixtures under testdata/. The inputs match the docs/design.md
examples so the design doc and the goldens stay in lockstep.
"""

import pathlib

import responses

_TESTDATA = pathlib.Path(__file__).resolve().parent / "testdata"

# Inputs mirror the docs/design.md "Network refusal handling" examples.
_SOFT = dict(
    url="https://docs.somelib.io/v2/auth/",
    host="docs.somelib.io",
    path="/v2/auth/",
    method="GET",
)
_HARD = dict(
    url="https://w3schools.com/html/default.asp",
    reason="Known low-quality source. Use MDN or official docs instead.",
)


def _assert_golden(name: str, got: bytes, update: bool):
    golden = _TESTDATA / name
    if update:
        golden.parent.mkdir(parents=True, exist_ok=True)
        golden.write_bytes(got)
        return
    assert golden.exists(), f"missing golden {name}; run pytest with --update to create it"
    assert got == golden.read_bytes(), f"{name} differs from golden"


def test_soft_deny_body_golden(update_golden):
    r = responses.soft_deny(**_SOFT)
    _assert_golden("soft_deny_body.json.golden", r.body, update_golden)


def test_hard_deny_body_golden(update_golden):
    r = responses.hard_deny(**_HARD)
    _assert_golden("hard_deny_body.json.golden", r.body, update_golden)


def test_soft_deny_envelope():
    r = responses.soft_deny(**_SOFT)
    assert r.status == 470
    assert r.headers[responses.X_CAGE_REASON] == "soft-deny"
    assert r.headers["Content-Type"] == "application/json"
    assert r.reason_phrase == "agent-creance soft-deny (not allowlisted)"


def test_hard_deny_envelope():
    r = responses.hard_deny(**_HARD)
    assert r.status == 471
    assert r.headers[responses.X_CAGE_REASON] == "hard-deny"
    assert r.headers["Content-Type"] == "application/json"
    assert r.reason_phrase == "agent-creance hard-deny (blocked)"


def test_allow_command_suggestion_uses_host_and_path():
    r = responses.soft_deny(**_SOFT)
    assert b"agent-creance allow 'docs.somelib.io/v2/auth/'" in r.body
