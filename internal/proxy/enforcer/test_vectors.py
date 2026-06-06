"""C1 parity: replay the shared decision-vector corpus from the Python side.

This is the cross-language guardrail. The same JSON files under
internal/policy/testdata/decision-vectors/ are consumed by the Go table test
(internal/policy/vectors_test.go) and by this suite. The Python matcher
(``policy.decide``) must produce the identical {decision, mode, matched_rule} for
every vector. Mirrors the Go test's discipline: strict decode (reject unknown keys)
and fail if the corpus is empty.
"""

import json
import pathlib

import pytest

import policy

# Computed the same way as conftest.CORPUS_DIR, but locally so it is available at
# collection time for parametrization.
_HERE = pathlib.Path(__file__).resolve().parent
CORPUS_DIR = _HERE.parents[2] / "internal" / "policy" / "testdata" / "decision-vectors"

_VECTOR_KEYS = {"name", "ruleset", "request", "expected"}
_EXPECTED_KEYS = {"decision", "mode", "matched_rule"}


def _vector_files():
    if not CORPUS_DIR.is_dir():
        return []
    return sorted(p for p in CORPUS_DIR.iterdir() if p.suffix == ".json" and p.is_file())


def test_corpus_not_empty():
    """A deleted/missing corpus must fail loudly, never pass silently."""
    assert _vector_files(), f"no decision vectors found in {CORPUS_DIR}"


@pytest.mark.parametrize(
    "vector_path", _vector_files(), ids=lambda p: p.stem
)
def test_decision_vector(vector_path):
    with open(vector_path, "rb") as f:
        v = json.load(f)

    # Strict decode: mirror Go's DisallowUnknownFields at the top level.
    unknown = set(v) - _VECTOR_KEYS
    assert not unknown, f"{vector_path.name}: unknown top-level keys {unknown}"
    unknown_expected = set(v["expected"]) - _EXPECTED_KEYS
    assert not unknown_expected, (
        f"{vector_path.name}: unknown expected keys {unknown_expected}"
    )

    rs = policy.RuleSet.from_dict(v["ruleset"])
    req = policy.Request.from_dict(v["request"])
    got = policy.decide(rs, req)

    exp = v["expected"]
    got_matched = got.matched.to_dict() if got.matched is not None else None
    assert got.decision == exp["decision"], f"{vector_path.name}: decision"
    assert got.mode == exp["mode"], f"{vector_path.name}: mode"
    assert got_matched == exp["matched_rule"], f"{vector_path.name}: matched_rule"
