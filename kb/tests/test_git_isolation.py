"""The git isolation fixture keeps a test's git away from a repository named by GIT_DIR."""

import subprocess
from pathlib import Path


def _git(*args: str, cwd: Path) -> str:
    done = subprocess.run(  # noqa: S603
        ["git", *args],  # noqa: S607
        cwd=cwd,
        capture_output=True,
        text=True,
        check=True,
    )
    return done.stdout


def _state(repo: Path) -> tuple[str, str, str]:
    return (
        (repo / ".git" / "config").read_text(),
        _git("branch", "-a", cwd=repo),
        _git("worktree", "list", cwd=repo),
    )


def test_git_cannot_touch_the_repository_named_by_git_dir(tmp_path, monkeypatch, isolate):
    sentinel = tmp_path / "sentinel"
    _git("init", "-q", str(sentinel), cwd=tmp_path)
    before = _state(sentinel)

    monkeypatch.setenv("GIT_DIR", str(sentinel / ".git"))
    monkeypatch.setenv("GIT_WORK_TREE", str(sentinel))
    (tmp_path / "home").mkdir()
    isolate(tmp_path / "home")

    work = tmp_path / "work"
    work.mkdir()
    _git("init", "-q", "repo", cwd=work)
    _git("config", "user.email", "alice@work.example", cwd=work / "repo")
    _git("config", "core.bare", "true", cwd=work / "repo")

    assert _state(sentinel) == before
