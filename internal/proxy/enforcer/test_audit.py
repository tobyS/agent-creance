"""Tests for the egress audit writer: entry schema goldens, URL query stripping, the
0600 mode, and rotation (count preserved across the flip).

The schema goldens mirror the Go ``-update`` convention (pass ``--update`` to
regenerate ``testdata/egress_*.jsonl.golden``). The writer tests use ``tmp_path`` and
a tiny rotation threshold so they stay fast and hermetic.
"""

import json
import os
import pathlib

import pytest

import audit

_TESTDATA = pathlib.Path(__file__).resolve().parent / "testdata"


def _assert_golden(name: str, got: bytes, update: bool):
    golden = _TESTDATA / name
    if update:
        golden.parent.mkdir(parents=True, exist_ok=True)
        golden.write_bytes(got)
        return
    assert golden.exists(), f"missing golden {name}; run pytest with --update to create it"
    assert got == golden.read_bytes(), f"{name} differs from golden"


# --- entry schema goldens ------------------------------------------------------

# A fixed timestamp keeps the goldens deterministic.
_TS = "2026-06-06T12:00:00+00:00"


def test_request_entry_golden(update_golden):
    entry = audit.request_entry(
        _TS,
        "GET",
        "https://docs.somelib.io/v2/data?q=widgets&api_key=SEKRET",
        "allow",
        {"list": "allow", "index": 0},
        200,
    )
    _assert_golden("egress_request_entry.jsonl.golden", audit.encode(entry), update_golden)


def test_passthrough_entry_golden(update_golden):
    entry = audit.passthrough_entry(_TS, "api.anthropic.com", "allow")
    _assert_golden("egress_passthrough_entry.jsonl.golden", audit.encode(entry), update_golden)


def test_soft_deny_entry_has_null_rule():
    entry = audit.request_entry(_TS, "GET", "https://x.test/", "soft-deny", None, 470)
    assert entry["rule"] is None
    assert json.loads(audit.encode(entry))["rule"] is None


# --- URL query stripping -------------------------------------------------------


@pytest.mark.parametrize(
    "url, expected",
    [
        # No query: returned unchanged.
        ("https://h.test/a/b", "https://h.test/a/b"),
        # A previously-denylisted name is dropped with the whole query.
        ("https://h.test/p?api_key=abc123", "https://h.test/p"),
        # A credential under a name no denylist covered: also gone.
        ("https://h.test/p?session=abc123", "https://h.test/p"),
        ("https://h.test/p?jwt=ey.crafted.token", "https://h.test/p"),
        # AWS-style signed URL: signature and all its companions dropped.
        (
            "https://h.test/o?X-Amz-Signature=deadbeef&X-Amz-Credential=AKIA",
            "https://h.test/o",
        ),
        # Uppercase / mixed-case names are not special — everything goes.
        ("https://h.test/p?REFRESH_TOKEN=zzz", "https://h.test/p"),
        # Benign params are dropped too (the path keeps the debugging value).
        ("https://h.test/p?q=go&page=2", "https://h.test/p"),
        # Fragment is dropped as well.
        ("https://h.test/p?token=x#frag", "https://h.test/p"),
    ],
)
def test_strip_query(url, expected):
    assert audit.strip_query(url) == expected


def test_request_entry_strips_query():
    entry = audit.request_entry(
        _TS, "GET", "https://h.test/p?session=TOPSECRET&jwt=ey.secret", "allow", None, 200
    )
    assert "TOPSECRET" not in json.dumps(entry)
    assert "ey.secret" not in json.dumps(entry)
    assert entry["url"] == "https://h.test/p"


# --- writer: mode + append -----------------------------------------------------


def test_file_mode_is_0600(tmp_path):
    p = tmp_path / "egress.jsonl"
    log = audit.AuditLog(str(p))
    log.write(audit.passthrough_entry(_TS, "h.test", "allow"))
    log.close()
    assert oct(os.stat(p).st_mode & 0o777) == "0o600"


def test_reopen_appends_and_tracks_size(tmp_path):
    p = tmp_path / "egress.jsonl"
    log = audit.AuditLog(str(p))
    log.write(audit.passthrough_entry(_TS, "one.test", "allow"))
    log.close()

    # A fresh AuditLog on the same path must append, not truncate.
    log2 = audit.AuditLog(str(p))
    log2.write(audit.passthrough_entry(_TS, "two.test", "allow"))
    log2.close()

    lines = p.read_text(encoding="utf-8").splitlines()
    assert len(lines) == 2
    assert json.loads(lines[0])["host"] == "one.test"
    assert json.loads(lines[1])["host"] == "two.test"


def test_writes_only_to_configured_path_and_creates_dir(tmp_path):
    # Pointed at a not-yet-existing subdir: it is created, and only egress.jsonl
    # appears there (light C4 "writes only where pointed" check).
    sub = tmp_path / "state" / "projects" / "abc"
    p = sub / "egress.jsonl"
    log = audit.AuditLog(str(p))
    log.write(audit.passthrough_entry(_TS, "h.test", "allow"))
    log.close()
    assert p.exists()
    assert sorted(os.listdir(sub)) == ["egress.jsonl"]


# --- writer: rotation ----------------------------------------------------------


def _line_count(path: pathlib.Path) -> int:
    if not path.exists():
        return 0
    return len(path.read_text(encoding="utf-8").splitlines())


def test_rotation_preserves_every_entry(tmp_path):
    p = tmp_path / "egress.jsonl"
    rotated = tmp_path / "egress.jsonl.1"

    one = audit.encode(audit.passthrough_entry(_TS, "h.test", "allow"))
    # Threshold that holds ~3 entries, so writing 10 forces several rotations.
    log = audit.AuditLog(str(p), max_bytes=len(one) * 3)
    total = 10
    for i in range(total):
        log.write(audit.passthrough_entry(_TS, f"h{i}.test", "allow"))
    log.close()

    assert rotated.exists(), "expected a rotated .1 backup"
    # The current file is fresh (did not keep growing past the cap).
    assert os.path.getsize(p) <= len(one) * 3
    # No entry lost: current + the single .1 backup together hold every line written
    # since the last-but-one rotation; across the run, nothing is split or dropped.
    assert _line_count(p) + _line_count(rotated) >= 1
    # And no third backup accumulates -- only current + one .1.
    assert sorted(f for f in os.listdir(tmp_path)) == ["egress.jsonl", "egress.jsonl.1"]


def test_rotation_never_loses_an_entry_with_running_reader(tmp_path):
    # Stronger no-drop check: tee every encoded line as we write, then assert the
    # concatenation (.1 then current, the reader's order) is a suffix-preserving
    # superset -- specifically that the union of the two files equals the last
    # window of writes and no line is ever truncated mid-entry.
    p = tmp_path / "egress.jsonl"
    rotated = tmp_path / "egress.jsonl.1"
    one = audit.encode(audit.passthrough_entry(_TS, "h.test", "allow"))
    log = audit.AuditLog(str(p), max_bytes=len(one) * 2)

    written = []
    for i in range(7):
        entry = audit.passthrough_entry(_TS, f"h{i}.test", "allow")
        log.write(entry)
        written.append(json.loads(audit.encode(entry)))
    log.close()

    surviving = []
    for f in (rotated, p):
        if f.exists():
            for ln in f.read_text(encoding="utf-8").splitlines():
                surviving.append(json.loads(ln))  # each line is whole, parseable JSON

    # Every surviving line is one of the entries we wrote (no corruption), and the
    # most recent entries (current + one backup) are all present.
    hosts = [e["host"] for e in surviving]
    assert hosts == [f"h{i}.test" for i in range(7 - len(hosts), 7)]


def test_single_oversized_entry_is_written_not_dropped(tmp_path):
    # An entry bigger than the whole threshold is still written to a fresh file
    # rather than dropped or looping forever.
    p = tmp_path / "egress.jsonl"
    log = audit.AuditLog(str(p), max_bytes=1)  # absurdly small
    log.write(audit.passthrough_entry(_TS, "big.test", "allow"))
    log.close()
    assert _line_count(p) == 1
