"""Ingest guards: no symlinks, no exception text in Temporal history, no stranded graphs."""

import pytest
from temporalio.exceptions import ApplicationError

from ki_kb import activities, plan, projects

PROJECTS = """
sensitive_brains: [client]
identities:
  personal: {github_user: alice, email: me@example.com, credential: personal, ingest: true}
projects:
  - {name: notes, repo: https://github.com/alice/notes, identity: personal, brains: [voice],
     ingest: [{path: docs/, brain: voice}]}
  - {name: payments, repo: https://github.com/alice/payments, identity: personal, brains: [client],
     ingest: [{path: docs/, brain: client}]}
"""


@pytest.fixture
def cfg() -> projects.Config:
    return projects.parse(PROJECTS)


def test_collect_docs_skips_symlinks_out_of_the_clone(tmp_path, cfg):
    outside = tmp_path / "outside.md"
    outside.write_text("FALKORDB_PASSWORD=hunter2hunter2\n")
    root = tmp_path / "repo"
    (root / "docs").mkdir(parents=True)
    (root / "docs" / "real.md").write_text("# real\n")
    (root / "docs" / "leak.md").symlink_to(outside)
    (root / "docs" / "environ.md").symlink_to("/proc/self/environ")
    (root / "docs" / "linked").symlink_to(tmp_path, target_is_directory=True)

    docs = activities.collect_docs(root, cfg.project("notes"))

    assert list(docs) == ["docs/real.md"]


def test_collect_docs_skips_symlink_to_a_file_inside_the_clone(tmp_path, cfg):
    root = tmp_path / "repo"
    (root / "docs").mkdir(parents=True)
    (root / "docs" / "a.md").write_text("a\n")
    (root / "docs" / "b.md").symlink_to("a.md")
    assert list(activities.collect_docs(root, cfg.project("notes"))) == ["docs/a.md"]


@pytest.mark.parametrize("name", ["notes", "payments"])
def test_safe_error_carries_no_exception_text(cfg, name):
    project = cfg.project(name)
    secret = "model said: card 4111 1111 1111 1111 and the request body"
    err = activities._safe_error(project, ValueError(secret), "docs/design.md")

    assert isinstance(err, ApplicationError)
    assert secret not in str(err) and err.__cause__ is None
    assert err.type == "ValueError"
    if project.sensitive:
        assert "docs/design.md" not in str(err)
    else:
        assert "docs/design.md" in str(err)


def test_git_error_message_has_no_git_output(cfg):
    err = activities._safe_error(
        cfg.project("notes"), activities.GitError("git clone failed with exit code 128")
    )
    assert "exit code 128" in str(err)


def test_stranded_graphs():
    rec = lambda g: plan.Recorded(hash="h", episode="e", graph=g)  # noqa: E731
    recorded = {
        "notes": {"docs/a.md": rec("brain-voice")},
        "payments": {"docs/b.md": rec("brain-client"), "docs/c.md": rec("brain-client-acme")},
    }
    assert activities.stranded_graphs(recorded, frozenset({"cortex", "brain-voice"})) == {
        "brain-client",
        "brain-client-acme",
    }
    assert (
        activities.stranded_graphs(
            recorded, frozenset({"brain-voice", "brain-client", "brain-client-*"})
        )
        == set()
    )


def test_forget_stranded_selection():
    import json

    from ki_kb.admin import stranded_in

    state = {
        "docs/a.md": json.dumps({"hash": "1", "episode": "e1", "graph": "brain-voice"}),
        "docs/b.md": json.dumps({"hash": "2", "episode": "e2", "graph": "brain-client"}),
        "docs/c.md": json.dumps({"hash": "3", "episode": "e3", "graph": "bad name"}),
    }
    assert stranded_in(state, frozenset({"brain-voice"})) == {"brain-client": ["docs/b.md"]}
    assert stranded_in(state, frozenset({"brain-voice", "brain-client"})) == {}


def test_identity_without_ingest_cannot_feed_a_brain():
    with pytest.raises(projects.ConfigError, match="ingest: false"):
        projects.parse(PROJECTS.replace("ingest: true}", "ingest: false}"))
