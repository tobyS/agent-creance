"""Focused matcher unit tests, ported from internal/policy/match_test.go.

These exercise the host/path/method glob primitives at finer granularity than the
decision-vector corpus does. The corpus (test_vectors.py) is the parity contract;
these guard the porting of the individual matching rules.
"""

import pytest

import policy
from policy import _match_host, _match_method, _match_path  # internal, on purpose


@pytest.mark.parametrize(
    "pattern,host,want",
    [
        ("api.github.com", "api.github.com", True),
        ("api.github.com", "github.com", False),
        ("API.GitHub.com", "api.github.com", True),  # case-insensitive
        ("*", "anything.example", True),
        ("*.medium.com", "foo.medium.com", True),
        ("*.medium.com", "a.b.medium.com", True),
        ("*.medium.com", "medium.com", False),  # apex excluded
        ("*.medium.com", "amedium.com", False),  # near miss, no dot boundary
        ("*.medium.com", "foo.example.com", False),
    ],
)
def test_match_host(pattern, host, want):
    assert _match_host(pattern, host) is want


@pytest.mark.parametrize(
    "host,want",
    [
        ("api.example.com", "api.example.com"),
        ("API.EXAMPLE.COM", "api.example.com"),  # lowercased
        ("api.example.com.", "api.example.com"),  # trailing dot stripped
        ("api.example.com:443", "api.example.com"),  # port stripped
        ("api.example.com.:443", "api.example.com"),  # port then dot
        ("api.example.com:", "api.example.com:"),  # empty port not stripped
        ("api.example.com:abc", "api.example.com:abc"),  # non-numeric port kept
        ("::1", "::1"),  # ipv6 literal untouched
        ("127.0.0.1:8080", "127.0.0.1"),  # ipv4 with port
    ],
)
def test_canonical_host(host, want):
    """Must stay byte-identical to internal/policy/glob.go canonicalHost (AC-0058 / C1)."""
    assert policy.canonical_host(host) == want


@pytest.mark.parametrize(
    "pattern,path,want",
    [
        ("/repos/org/repo/", "/repos/org/repo/blob/main", True),  # prefix covers subtree
        ("/repos/org/repo/", "/repos/org/repo", True),  # prefix exact
        ("/repos/org/repo/", "/repos/org", False),  # not a parent match
        ("/repos/org/repo/", "/repos/org/other", False),  # wrong branch
        ("/@*", "/@scope", True),  # star within segment
        ("/@*", "/@scope/article", True),  # star + prefix coverage
        ("/a*/b", "/a/c/b", False),  # star does not cross slash
        ("/foo*", "/foo", True),  # star matches empty run
        ("**/.env", "/.env", True),  # doublestar at root
        ("**/.env", "/a/b/.env", True),  # doublestar at depth
        ("**/.env", "/a/b/c", False),  # doublestar non-match
        ("**/.git/config", "/x/y/.git/config", True),
        ("/a/**/b", "/a/b", True),  # doublestar middle, zero segments
        ("/a/**/b", "/a/x/y/b", True),  # doublestar middle, many segments
        ("**", "/", True),  # bare doublestar matches root
        ("**", "/a/b/c", True),  # bare doublestar matches anything
        ("/v1", "/v1/", True),  # trailing slash normalized
        ("/a**b", "/axyzb", True),  # glued doublestar degrades to star
        ("/a**b", "/a/x/b", False),  # glued doublestar does not cross slash
    ],
)
def test_match_path(pattern, path, want):
    assert _match_path(pattern, path) is want


@pytest.mark.parametrize(
    "methods,method,want",
    [
        (None, "DELETE", True),  # nil matches any
        (["GET", "POST"], "POST", True),
        (["GET", "POST"], "DELETE", False),
        (["GET"], "get", False),  # case-sensitive
        ([], "GET", False),  # empty list matches nothing
    ],
)
def test_match_method(methods, method, want):
    assert _match_method(methods, method) is want


def test_none_paths_methods_preserved_on_decode():
    """A host-wide rule must keep paths/methods None ("key omitted"), not []."""
    rs = policy.RuleSet.from_dict(
        {"allow": [{"host": "react.dev", "mode": "intercept"}]}
    )
    assert rs.allow[0].paths is None
    assert rs.allow[0].methods is None


def test_empty_paths_list_matches_nothing():
    """A present empty paths list is distinct from None: it matches no path."""
    rs = policy.RuleSet.from_dict(
        {"allow": [{"host": "react.dev", "paths": [], "mode": "intercept"}]}
    )
    got = policy.decide(rs, policy.Request("react.dev", "/learn", "GET"))
    assert got.decision == policy.DECISION_SOFT_DENY

def test_auth_fields_and_credentials_carried_but_ignored_by_matcher():
    """AC-0068b: per-rule inject/in_cage and the top-level credentials block are
    carried in policy.json but are annotations the matcher ignores. Rule.from_dict /
    RuleSet.from_dict must load a policy carrying them without error, and decide must be
    unchanged (no secret value is present — only references)."""
    data = {
        "version": 1,
        "input_hash": "x",
        "credentials": {
            "github-token": {
                "source": "op://Private/GitHub PAT/token",
                "header": "Authorization",
                "template": "Bearer {token}",
            }
        },
        "allow": [
            {
                "host": "api.github.com",
                "paths": ["/graphql"],
                "methods": ["POST"],
                "mode": "intercept",
                "inject": "github-token",
            },
            {"host": "s3.eu-central-1.amazonaws.com", "mode": "intercept", "in_cage": True},
        ],
        "deny_always": [],
    }

    rs = policy.RuleSet.from_dict(data)

    # The unknown top-level 'credentials' key and the inject/in_cage rule keys are
    # ignored on load; the rules still decide purely by host/path/method.
    allowed = policy.decide(rs, policy.Request("api.github.com", "/graphql", "POST"))
    assert allowed.decision == policy.DECISION_ALLOW

    in_cage = policy.decide(
        rs, policy.Request("s3.eu-central-1.amazonaws.com", "/bucket/key", "GET")
    )
    assert in_cage.decision == policy.DECISION_ALLOW
