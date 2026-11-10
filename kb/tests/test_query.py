from dataclasses import dataclass, field
from datetime import UTC, datetime

import pytest
from fastapi.testclient import TestClient

from ki_kb import query


@dataclass
class FakeEdge:
    fact: str
    episodes: list[str]
    valid_at: datetime | None = None
    invalid_at: datetime | None = None
    expired_at: datetime | None = None
    uuid: str = "edge-1"
    source_node_uuid: str = "n-acme"
    target_node_uuid: str = "n-chi"
    name: str = "USES"


# group, name, summary, labels as Cypher returns them for each entity node
NODES = {
    "n-acme": ("brain-acme", "Acme", "A" * 500, ["Entity", "Entity_brainacme", "Service"]),
    "n-chi": ("brain-acme", "chi", "A router", ["Entity", "Entity_brainacme"]),
    "n-mux": ("brain-acme", "gorilla/mux", "", ["Entity"]),
    "n-other": ("brain-other", "Other", "secret", ["Entity"]),
}


def node_record(uuid):
    _, name, summary, labels = NODES[uuid]
    return {"uuid": uuid, "name": name, "summary": summary, "labels": labels}


def node_edges(node):
    base = {"episodes": ["ep-dec"], "valid_at": "2026-11-05T08:00:00+00:00", "invalid_at": None}
    return [
        {
            **base,
            "uuid": "edge-1",
            "source": node,
            "target": "n-chi",
            "name": "USES",
            "fact": "Acme routes with chi",
            "m_uuid": "n-chi",
            "m_name": "chi",
            "m_summary": "A router",
            "m_labels": ["Entity", "Entity_brainacme"],
        },
        {
            **base,
            "uuid": "edge-2",
            "source": "n-mux",
            "target": node,
            "name": "REPLACED_BY",
            "fact": "gorilla/mux was replaced by Acme",
            "invalid_at": "2026-01-01T00:00:00+00:00",
            "m_uuid": "n-mux",
            "m_name": "gorilla/mux",
            "m_summary": "",
            "m_labels": ["Entity"],
        },
    ]


@dataclass
class FakeGraphiti:
    edges: list[FakeEdge]
    searched: list = field(default_factory=list)
    queries: list[str] = field(default_factory=list)
    params: list[dict] = field(default_factory=list)

    @property
    def driver(self):
        return self

    async def search(self, q, group_ids, num_results):
        self.searched.append(group_ids)
        return self.edges[:num_results]

    async def execute_query(self, cypher, **kwargs):
        self.queries.append(cypher)
        self.params.append(kwargs)
        if "IN $uuids" in cypher:
            return (
                [
                    node_record(u)
                    for u in kwargs["uuids"]
                    if u in NODES and NODES[u][0] == kwargs["group_id"]
                ],
                None,
                None,
            )
        if "RELATES_TO" in cypher:
            node = kwargs["node"]
            return node_edges(node)[: kwargs["limit"]], None, None
        if "AS day" in cypher:
            return (
                [
                    {"day": "2026-11-07", "documents": 3},
                    {"day": "2026-11-08", "documents": 12},
                ],
                None,
                None,
            )
        if "documents" in cypher:
            return [{"documents": 12, "last_ingest": "2026-11-06T10:00:00+00:00"}], None, None
        if "committed_at" in cypher:
            return (
                [
                    {
                        "name": "acme/decisions/routing.md",
                        "committed_at": "2026-11-05T08:00:00+00:00",
                        "source_description": "decision:acme/decisions/routing.md | Routing",
                    },
                    {"name": "acme/README.md", "committed_at": None, "source_description": None},
                ],
                None,
                None,
            )
        known = {
            "ep-dec": "decision:acme/decisions/routing.md",
            "ep-readme": "pointer:acme/README.md",
        }
        return (
            [
                {"uuid": episode_id, "source_description": known[episode_id]}
                for episode_id in kwargs["episode_ids"]
                if episode_id in known
            ],
            None,
            None,
        )

    async def close(self):
        pass


@pytest.fixture
def client(monkeypatch):
    for k, v in {
        "KI_ALLOWED_GRAPHS": "cortex,brain-acme",
        "KI_LLM_BASE_URL": "http://llm.invalid/v1",
        "KI_LLM_MODEL": "test-model",
        "KI_EMBEDDING_BASE_URL": "http://embed.invalid/v1",
        "KI_EMBEDDING_MODEL": "test-embed",
        "KI_EMBEDDING_DIM": "1024",
        "FALKORDB_HOST": "falkordb.invalid",
    }.items():
        monkeypatch.setenv(k, v)
    old = datetime(2026, 1, 1, tzinfo=UTC)
    fake = FakeGraphiti(
        edges=[
            FakeEdge("Acme routes with chi", ["ep-dec"]),
            FakeEdge("Acme used gorilla/mux", ["ep-dec"], invalid_at=old),
            FakeEdge("README mentions make dev", ["ep-readme"]),
        ]
    )

    with TestClient(query.app) as c:
        query.app.state.kb.graphitis["brain-acme"] = fake
        yield c, fake


def test_search_labels_kinds_and_supersession(client):
    c, fake = client
    r = c.post("/search", json={"graph": "brain-acme", "query": "routing", "limit": 10})
    assert r.status_code == 200
    facts = r.json()["facts"]
    assert facts[0]["kind"] == "decision" and facts[0]["source"] == "acme/decisions/routing.md"
    assert facts[1]["invalid_at"] is not None
    assert facts[2]["kind"] == "pointer"
    assert fake.searched == [["brain-acme"]]
    assert "RETURN e.uuid AS uuid, e.source_description AS source_description" in fake.queries[0]
    assert "content" not in fake.queries[0]


def test_kind_filter(client):
    c, _ = client
    r = c.post("/search", json={"graph": "brain-acme", "query": "x", "kinds": ["pointer"]})
    assert [f["fact"] for f in r.json()["facts"]] == ["README mentions make dev"]


def test_disallowed_graph_is_refused(client):
    c, _ = client
    r = c.post("/search", json={"graph": "brain-client", "query": "cde"})
    assert r.status_code == 403


def test_metrics_are_mounted_and_requests_counted(client):
    from prometheus_client import REGISTRY

    client, _ = client

    before = (
        REGISTRY.get_sample_value("ki_query_requests_total", {"tool": "healthz", "status": "200"})
        or 0
    )
    assert client.get("/healthz").status_code == 200
    assert (
        REGISTRY.get_sample_value("ki_query_requests_total", {"tool": "healthz", "status": "200"})
        == before + 1
    )
    body = client.get("/metrics").text
    assert "ki_query_seconds" in body and "ki_query_requests_total" in body
    assert (
        REGISTRY.get_sample_value("ki_query_requests_total", {"tool": "metrics", "status": "200"})
        is None
    )


def test_stats_counts_documents_and_reports_the_last_ingest(client):
    c, fake = client
    r = c.post("/stats", json={"graph": "brain-acme"})
    assert r.status_code == 200
    assert r.json() == {"documents": 12, "last_ingest": "2026-11-06T10:00:00Z"}
    assert fake.params[-1]["group_id"] == "brain-acme"


def test_recent_lists_documents_newest_first_with_their_kind(client):
    c, fake = client
    r = c.post("/recent", json={"graph": "brain-acme", "since": "2026-11-01T00:00:00Z", "limit": 5})
    assert r.status_code == 200
    docs = r.json()["documents"]
    assert docs[0] == {
        "source": "acme/decisions/routing.md",
        "kind": "decision",
        "committed_at": "2026-11-05T08:00:00Z",
    }
    assert docs[1] == {"source": "acme/README.md", "kind": "pointer", "committed_at": None}
    assert fake.params[-1]["since"].startswith("2026-11-01T00:00:00")
    assert fake.params[-1]["limit"] == 5


def test_recent_without_since_reads_everything(client):
    c, fake = client
    assert c.post("/recent", json={"graph": "brain-acme"}).status_code == 200
    assert fake.params[-1]["since"] == ""


def test_activity_counts_documents_per_day_of_ingest_not_of_commit(client):
    c, fake = client
    r = c.post("/activity", json={"graph": "brain-acme", "since": "2026-10-09T00:00:00Z"})
    assert r.status_code == 200
    assert r.json() == {
        "days": [{"day": "2026-11-07", "documents": 3}, {"day": "2026-11-08", "documents": 12}]
    }
    # A document committed 10 days ago and ingested today has valid_at ten days back and
    # created_at today. Only created_at may bucket and filter, so it counts on today.
    cypher = fake.queries[-1]
    assert "e.created_at >= $since" in cypher
    assert "substring(e.created_at, 0, 10) AS day" in cypher
    assert "valid_at" not in cypher
    assert "count(DISTINCT split(e.name, '#')[0])" in cypher
    assert fake.params[-1]["group_id"] == "brain-acme"
    assert fake.params[-1]["since"] == "2026-10-09T00:00:00+00:00"


def test_activity_reads_since_in_utc(client):
    c, fake = client
    r = c.post("/activity", json={"graph": "brain-acme", "since": "2026-10-09T02:00:00+02:00"})
    assert r.status_code == 200
    assert fake.params[-1]["since"] == "2026-10-09T00:00:00+00:00"
    assert c.post("/activity", json={"graph": "brain-acme"}).status_code == 422


def test_stats_recent_and_activity_refuse_a_disallowed_graph(client):
    c, _ = client
    assert c.post("/stats", json={"graph": "brain-client"}).status_code == 403
    assert c.post("/recent", json={"graph": "brain-client"}).status_code == 403
    since = "2026-11-01T00:00:00Z"
    assert c.post("/activity", json={"graph": "brain-client", "since": since}).status_code == 403


def test_graph_around_a_query_returns_endpoints_for_every_edge(client):
    c, fake = client
    fake.edges = [
        FakeEdge("Acme routes with chi", ["ep-dec"], uuid="edge-1"),
        FakeEdge(
            "Acme used gorilla/mux",
            ["ep-readme"],
            uuid="edge-2",
            source_node_uuid="n-acme",
            target_node_uuid="n-mux",
            name="USED",
            invalid_at=datetime(2026, 1, 1, tzinfo=UTC),
        ),
        FakeEdge("Acme once used flask", ["ep-dec"], uuid="edge-3", expired_at=datetime.now(UTC)),
    ]
    r = c.post("/graph", json={"graph": "brain-acme", "query": "routing", "limit": 10})
    assert r.status_code == 200
    body = r.json()
    assert fake.searched == [["brain-acme"]]
    assert [e["id"] for e in body["edges"]] == ["edge-1", "edge-2"]
    first = body["edges"][0]
    assert first == {
        "id": "edge-1",
        "from": "n-acme",
        "to": "n-chi",
        "name": "USES",
        "fact": "Acme routes with chi",
        "kind": "decision",
        "source": "acme/decisions/routing.md",
        "valid_at": None,
        "invalid_at": None,
    }
    assert body["edges"][1]["kind"] == "pointer"
    assert body["edges"][1]["invalid_at"] is not None
    ids = {n["id"] for n in body["nodes"]}
    assert ids == {"n-acme", "n-chi", "n-mux"}
    assert all({e["from"], e["to"]} <= ids for e in body["edges"])
    acme = next(n for n in body["nodes"] if n["id"] == "n-acme")
    assert len(acme["summary"]) == 400 and acme["labels"] == ["Service"]
    assert fake.params[0]["group_id"] == "brain-acme"


def test_graph_around_a_query_drops_an_edge_whose_endpoint_is_not_in_the_graph(client):
    c, fake = client
    fake.edges = [FakeEdge("Leak", ["ep-dec"], target_node_uuid="n-other")]
    r = c.post("/graph", json={"graph": "brain-acme", "query": "x"})
    assert r.json() == {"nodes": [], "edges": []}


def test_graph_around_a_node_returns_its_edges_in_both_directions_with_endpoints(client):
    c, fake = client
    r = c.post("/graph", json={"graph": "brain-acme", "node": "n-acme", "limit": 5})
    assert r.status_code == 200
    body = r.json()
    assert [(e["from"], e["to"]) for e in body["edges"]] == [
        ("n-acme", "n-chi"),
        ("n-mux", "n-acme"),
    ]
    assert body["edges"][0]["kind"] == "decision"
    assert body["edges"][1]["invalid_at"] == "2026-01-01T00:00:00Z"
    assert {n["id"] for n in body["nodes"]} == {"n-acme", "n-chi", "n-mux"}
    i = next(i for i, q in enumerate(fake.queries) if "RELATES_TO" in q)
    cypher = fake.queries[i]
    assert "e.expired_at IS NULL" in cypher
    assert "ORDER BY coalesce(e.valid_at, '') DESC" in cypher
    assert "(n:Entity {uuid: $node, group_id: $group_id})-[e:RELATES_TO]-(m:Entity)" in cypher
    assert fake.params[i] == {
        "node": "n-acme",
        "group_id": "brain-acme",
        "limit": 5,
        "routing_": "r",
    }


def test_graph_refuses_a_node_of_another_graph_with_404(client):
    c, fake = client
    r = c.post("/graph", json={"graph": "brain-acme", "node": "n-other"})
    assert r.status_code == 404
    assert not any("RELATES_TO" in q for q in fake.queries)
    assert c.post("/graph", json={"graph": "brain-acme", "node": "nope"}).status_code == 404


def test_graph_wants_exactly_one_of_query_and_node_and_a_limit_in_range(client):
    c, _ = client
    g = {"graph": "brain-acme"}
    assert c.post("/graph", json=g).status_code == 422
    assert c.post("/graph", json={**g, "query": "x", "node": "n-acme"}).status_code == 422
    assert c.post("/graph", json={**g, "query": "x", "limit": 0}).status_code == 422
    assert c.post("/graph", json={**g, "query": "x", "limit": 61}).status_code == 422
    assert c.post("/graph", json={"graph": "brain-client", "query": "x"}).status_code == 403


def test_timestamp_tolerates_junk():
    assert query._timestamp("not a date") is None
    assert query._timestamp(None) is None
    now = datetime(2026, 1, 1, tzinfo=UTC)
    assert query._timestamp(now) is now
