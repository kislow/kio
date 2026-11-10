"""Keeps tests that run real git away from the repository pytest runs in."""

from pathlib import Path

import pytest

REPO_VARS = (
    "GIT_DIR",
    "GIT_WORK_TREE",
    "GIT_INDEX_FILE",
    "GIT_PREFIX",
    "GIT_COMMON_DIR",
    "GIT_OBJECT_DIRECTORY",
    "GIT_CONFIG_PARAMETERS",
    "GIT_ALTERNATE_OBJECT_DIRECTORIES",
    "GIT_NAMESPACE",
)


def isolate_git(monkeypatch: pytest.MonkeyPatch, root: Path) -> None:
    """Point git at an empty config and stop it walking up into a real repository."""
    for var in REPO_VARS:
        monkeypatch.delenv(var, raising=False)
    empty = root / "empty.gitconfig"
    empty.write_text("")
    monkeypatch.setenv("HOME", str(root))
    monkeypatch.setenv("XDG_CONFIG_HOME", str(root / ".config"))
    monkeypatch.setenv("GIT_CONFIG_GLOBAL", str(empty))
    monkeypatch.setenv("GIT_CONFIG_SYSTEM", str(empty))
    monkeypatch.setenv("GIT_CONFIG_NOSYSTEM", "1")
    monkeypatch.setenv("GIT_CEILING_DIRECTORIES", str(root))


@pytest.fixture(autouse=True)
def _isolated_git(
    monkeypatch: pytest.MonkeyPatch, tmp_path_factory: pytest.TempPathFactory
) -> None:
    isolate_git(monkeypatch, tmp_path_factory.mktemp("git-home").resolve())


@pytest.fixture
def isolate(monkeypatch: pytest.MonkeyPatch):
    """The isolation function itself, for tests that re-apply it after setting git variables."""
    return lambda root: isolate_git(monkeypatch, root)
