from pathlib import Path

import pytest

from ki_kb import brains, kinds, plan, projects, scanner
from ki_kb.activities import collect_docs
from ki_kb.aliases import Aliases
from ki_kb.brains import ConfigError

REPO_ROOT = Path(__file__).resolve().parents[2]

BASE = """
sensitive_brains: [client]
identities:
  personal: {github_user: alice, email: me@example.com, credential: personal, ingest: true}
  org-a: {github_user: alice-orga, email: alice@org-a.com, credential: org-a, ingest: true}
projects:
"""


def cfg(projects_yaml: str) -> projects.Config:
    return projects.parse(BASE + projects_yaml)


# --- projects file -----------------------------------------------------------


def test_shipped_projects_file_is_valid():
    projects.load(REPO_ROOT / "deploy" / "config" / "projects.yaml")


def test_project_routing_and_rules():
    c = cfg(
        """
  - name: acme
    repo: git@github.com:Org-A/Acme.git
    identity: org-a
    brains: [acme]
    ingest:
      - {path: docs/, brain: acme}
      - {path: docs/patterns/, brain: cortex}
  - name: payments-client
    repo: https://github.com/org-a/payments
    identity: org-a
    brains: [client]
    ingest: [{path: docs/, brain: client}]
  - name: scope-only
    repo: https://github.com/alice/notes
    identity: personal
    brains: [voice]
"""
    )
    co = c.project("acme")
    assert co.repo == "https://github.com/org-a/acme"
    assert not co.sensitive
    assert co.brain_for("docs/api.md") == "acme"
    assert co.brain_for("docs/patterns/retry.md") == "cortex"
    assert co.brain_for("src/main.go") is None
    assert c.project("payments-client").sensitive
    assert c.project("scope-only").ingest == ()


@pytest.mark.parametrize(
    "bad",
    [
        # ingest brain not in scope
        "  - {name: x, repo: https://github.com/a/b, identity: personal, brains: [voice],"
        " ingest: [{path: a/, brain: client}]}",
        # unknown identity
        "  - {name: x, repo: https://github.com/a/b, identity: nobody, brains: [voice]}",
        # path escaping the repo
        "  - {name: x, repo: https://github.com/a/b, identity: personal, brains: [voice],"
        " ingest: [{path: ../etc, brain: voice}]}",
        # colon in brain name
        "  - {name: x, repo: https://github.com/a/b, identity: personal, brains: ['brain:client']}",
        # not GitHub
        "  - {name: x, repo: https://gitlab.com/a/b, identity: personal, brains: [voice]}",
        # same repo under two identities
        "  - {name: x, repo: https://github.com/a/b, identity: personal, brains: [voice]}\n"
        "  - {name: y, repo: https://github.com/a/b, identity: org-a, brains: [voice]}",
    ],
)
def test_projects_file_rejects(bad):
    with pytest.raises(ConfigError):
        cfg(bad)


@pytest.mark.parametrize(
    "url",
    [
        "git@github.com:Org/Repo.git",
        "ssh://git@github.com/org/repo.git",
        "git@github.com-work:org/repo.git",
        "ssh://git@github.com-work.2/org/repo",
    ],
)
def test_normalize_repo_forms(url):
    assert projects.normalize_repo(url) == "https://github.com/org/repo"


@pytest.mark.parametrize(
    "url",
    [
        "git@gitlab.com:org/repo.git",
        "git@github.com-:org/repo.git",
        "https://github.com-work/org/repo",
    ],
)
def test_normalize_repo_refuses_other_hosts(url):
    with pytest.raises(ConfigError):
        projects.normalize_repo(url)


def test_graph_names_are_graphiti_safe():
    assert brains.graph_for("cortex") == "cortex"
    assert brains.graph_for("acme") == "brain-acme"


# --- scanner -----------------------------------------------------------------


def test_scanner_flags_card_numbers_and_secrets():
    text = (
        "Test card 4111 1111 1111 1111 in sandbox\n"
        "aws key AKIAIOSFODNN7EXAMPLE\n"
        "-----BEGIN OPENSSH PRIVATE KEY-----\n"
        'password = "hunter2hunter2"\n'
    )
    found = {f.kind for f in scanner.scan(text)}
    assert {"card-number", "aws-access-key", "private-key", "assigned-secret"} <= found


def test_scanner_ignores_ordinary_numbers():
    text = "Order 1234567890123 shipped. Port 6379. Version 2026-10-16. 0000000000000000\n"
    assert scanner.scan(text) == []


# --- aliases -----------------------------------------------------------------


def test_aliases_rewrite_outside_code_blocks():
    a = Aliases({"Kubernetes": ["k8s"], "Azure Kubernetes Service": ["AKS"]})
    text = "We run k8s on AKS.\n```\nkubectl --context k8s\n```\n"
    out = a.apply(text)
    assert out.startswith("We run Kubernetes on Azure Kubernetes Service.")
    assert "kubectl --context k8s" in out


# --- kinds -------------------------------------------------------------------


@pytest.mark.parametrize(
    "path,kind",
    [
        ("decisions/2026-10-13-graphiti.md", kinds.DECISION),
        ("docs/adr/0001-chi.md", kinds.DECISION),
        ("CONVENTIONS.md", kinds.CONVENTION),
        ("docs/runbooks/rotate-keys.md", kinds.STATE),
        ("README.md", kinds.POINTER),
    ],
)
def test_kind_for(path, kind):
    assert kinds.kind_for(path) == kind


def test_source_description_roundtrip():
    sd = kinds.source_description(kinds.DECISION, "acme", "decisions/x.md")
    assert kinds.parse_source_description(sd) == (kinds.DECISION, "acme/decisions/x.md")
    assert kinds.parse_source_description("garbage") == (kinds.POINTER, "")


def test_parsed_source_drops_the_section_context():
    sd = kinds.source_description(
        kinds.DECISION, "acme", "decisions/x.md#Use chi > Why", "Use chi > Why"
    )
    assert sd.endswith(" | Use chi > Why")
    assert kinds.parse_source_description(sd) == (
        kinds.DECISION,
        "acme/decisions/x.md#Use chi > Why",
    )


# --- diffing -----------------------------------------------------------------


def test_diff_only_touches_changed_docs():
    h = plan.content_hash
    recorded = {
        "a.md": plan.Recorded(hash=h("a"), episode="e1", graph="brain-acme"),
        "b.md": plan.Recorded(hash=h("old"), episode="e2", graph="brain-acme"),
        "gone.md": plan.Recorded(hash=h("x"), episode="e3", graph="brain-acme"),
    }
    current = [
        plan.Current("a.md", h("a"), "brain-acme"),
        plan.Current("b.md", h("new"), "brain-acme"),
        plan.Current("c.md", h("c"), "brain-acme"),
    ]
    p = plan.diff(current, recorded)
    assert [d.path for d in p.add] == ["c.md"]
    assert [d.path for d, _ in p.replace] == ["b.md"]
    assert [path for path, _ in p.remove] == ["gone.md"]
    assert p.unchanged == 1


def test_diff_moves_doc_when_brain_changes():
    h = plan.content_hash("same")
    p = plan.diff(
        [plan.Current("d.md", h, "brain-client")],
        {"d.md": plan.Recorded(hash=h, episode="e", graph="brain-acme")},
    )
    assert len(p.replace) == 1


# --- document collection -----------------------------------------------------


def test_collect_docs(tmp_path: Path):
    (tmp_path / "docs").mkdir()
    (tmp_path / "docs" / "api.md").write_text("api")
    (tmp_path / "docs" / "TEMPLATE.md").write_text("skip")
    (tmp_path / "node_modules").mkdir()
    (tmp_path / "node_modules" / "x.md").write_text("skip")
    (tmp_path / "main.go").write_text("package main")
    project = cfg(
        "  - {name: c, repo: https://github.com/a/c, identity: personal, brains: [acme],"
        " ingest: [{path: docs/, brain: acme}]}"
    ).project("c")
    assert collect_docs(tmp_path, project) == {"docs/api.md": ("acme", "api")}


@pytest.mark.parametrize(
    "line",
    [
        "FALKORDB_PASSWORD=hunter2hunter2",
        "api_key: 9f8e7d6c5b4a3210",
        "export GH_TOKEN=abcdef123456789",
        "token: ghp_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789",
        "secret=dGhpcyBpcyBhIHNlY3JldA==",
    ],
)
def test_scanner_flags_unquoted_assignments(line):
    assert "assigned-secret-unquoted" in {f.kind for f in scanner.scan(line)}


@pytest.mark.parametrize(
    "line",
    [
        "password: <your password>",
        "TOKEN=$KI_TOKEN",
        "token: ${{ secrets.GITHUB_TOKEN }}",
        "the token is short",
        "api_key: {{ .Values.key }}",
        "fine-grained read-only token: *Contents*, plus *Pull requests*",
        "api_key: required for every call",
        "password: alphabetic-only-value",
    ],
)
def test_scanner_skips_placeholders(line):
    assert scanner.scan(line) == []


# Synthetic values only: the card numbers are published test numbers or made-up
# Luhn-valid runs, the phone numbers and the Emirates ID are not assigned to anyone.
@pytest.mark.parametrize(
    "line",
    [
        "Mobile: +9715012345678",
        "Tel +491512345678908 (office)",
        "Subject: Re: [CASE 123456789012347] your request",
        "https://example.com/event?eventId=123456789012347",
        "ami-version 0.4.20260512143021",
        "image 0.4.20260512-143021-main",
        "secretsManagerName: acme_service_a1b2c3d4",
        "newrelicRoSecretId: acme-prod/newrelic-ro-12345",
        "db_secret_arn: arn:aws:secretsmanager:eu-west-1:000000000000:secret:x-AbCdEf",
        "token_path = /var/run/secrets/token-12345678",
        "SECRET_REF: team-secret-12345678",
        "export TOKEN='[your-token-12345]'",
        'password = "<your-password>"',
        'api_key: "${API_KEY_VALUE}"',
    ],
)
def test_scanner_skips_known_false_positives(line):
    assert scanner.scan(line) == []


@pytest.mark.parametrize(
    "line",
    [
        "card 4111111111111111",
        "card 4111 1111 1111 1111",
        "card 4111-1111-1111-1111",
        "amex 3782 822463 10005",
        "amex 378282246310005",
        "card 1234 5678 9012 3456 785",
        "card 4000000000000002",
    ],
)
def test_scanner_flags_card_groupings(line):
    assert "card-number" in {f.kind for f in scanner.scan(line)}


@pytest.mark.parametrize(
    "line",
    [
        "Emirates ID 784-1990-1234567-1",
        "id: 784-2001-7654321-0",
    ],
)
def test_scanner_flags_emirates_id(line):
    assert "national-id" in {f.kind for f in scanner.scan(line)}


def test_scanner_flags_signed_jwt_url():
    url = (
        "https://files.example.com/a.pdf?jwt=eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.c2lnbmF0dXJl"
    )
    assert "jwt" in {f.kind for f in scanner.scan(url)}


@pytest.mark.parametrize(
    "line,kind",
    [
        ("secretsManagerKey: mal_service_a1b2c3d4", "assigned-secret-unquoted"),
        ("password = 'hunter2hunter2'", "assigned-secret"),
    ],
)
def test_scanner_still_flags_values_next_to_skipped_shapes(line, kind):
    assert kind in {f.kind for f in scanner.scan(line)}
