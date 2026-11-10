"""The Python validator must accept and reject exactly what the Go one does.

Both run against testdata/projects (see pkg/projects/projects_test.go).
"""

from pathlib import Path

import pytest

from ki_kb import projects
from ki_kb.brains import ConfigError

FIXTURES = Path(__file__).resolve().parents[2] / "testdata" / "projects"


@pytest.mark.parametrize("path", sorted((FIXTURES / "valid").glob("*.yaml")), ids=lambda p: p.name)
def test_valid_fixture(path):
    projects.load(path)


@pytest.mark.parametrize(
    "path", sorted((FIXTURES / "invalid").glob("*.yaml")), ids=lambda p: p.name
)
def test_invalid_fixture(path):
    with pytest.raises(ConfigError):
        projects.load(path)


def test_fixtures_present():
    assert len(list((FIXTURES / "valid").glob("*.yaml"))) >= 2
    assert len(list((FIXTURES / "invalid").glob("*.yaml"))) >= 10


def test_families():
    cfg = projects.load(FIXTURES / "valid" / "families.yaml")
    assert cfg.is_sensitive("client") and cfg.is_sensitive("client-acme")
    assert not cfg.is_sensitive("clientx") and not cfg.is_sensitive("cortex")
    assert all(p.sensitive for p in cfg.projects)


def test_workspaces():
    cfg = projects.load(FIXTURES / "valid" / "workspaces.yaml")
    assert [workspace.name for workspace in cfg.workspaces] == ["work", "demo"]
    assert cfg.workspaces[0].brains == ("alpha",)
    assert cfg.workspaces[1].vault == ("diagrams/projects/demo/",)


def test_bulk():
    cfg = projects.load(FIXTURES / "valid" / "bulk.yaml")
    assert cfg.projects[0].name == "archive" and cfg.projects[0].bulk is True
    assert cfg.projects[1].name == "normal" and cfg.projects[1].bulk is False


def test_vault_folders():
    cfg = projects.load(FIXTURES / "valid" / "vault-folders.yaml")
    assert cfg.projects[0].vault_folders == ("acme", "acme-platform")
    assert cfg.projects[1].vault_folders == ()
