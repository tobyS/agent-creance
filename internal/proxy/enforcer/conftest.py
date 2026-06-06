"""Shared pytest fixtures + the --update golden flag for the enforcer suite.

Mirrors the Go ``-update`` golden convention (internal/policy/render/render_test.go):
pass ``--update`` to regenerate the golden 403 bodies under testdata/ instead of
comparing against them.
"""

import pathlib

import pytest

# internal/proxy/enforcer/ -> internal/proxy -> internal -> <repo root>
_HERE = pathlib.Path(__file__).resolve().parent
REPO_ROOT = _HERE.parents[2]
# The canonical, shared C1 corpus. The Python suite reads it directly (no fork);
# a Go anti-fork guard asserts there is only one such directory in the tree.
CORPUS_DIR = REPO_ROOT / "internal" / "policy" / "testdata" / "decision-vectors"


def pytest_addoption(parser):
    parser.addoption(
        "--update",
        action="store_true",
        default=False,
        help="regenerate golden files",
    )


def pytest_configure(config):
    config.addinivalue_line(
        "markers",
        "integration: live mitmproxy + curl probes (slow; gated on spike S1)",
    )


@pytest.fixture
def update_golden(request) -> bool:
    return bool(request.config.getoption("--update"))


@pytest.fixture(scope="session")
def corpus_dir() -> pathlib.Path:
    return CORPUS_DIR
