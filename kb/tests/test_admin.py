"""Admin clean-up commands against an in-memory Redis."""

import json

import fakeredis
import pytest

from ki_kb import admin
from ki_kb.errors import GraphNotAllowedError

ENV = {
    "FALKORDB_HOST": "falkordb.invalid",
    "KI_ALLOWED_GRAPHS": "cortex,brain-voice",
    "KI_LLM_BASE_URL": "http://llm.invalid/v1",
    "KI_LLM_MODEL": "m",
    "KI_EMBEDDING_BASE_URL": "http://embed.invalid/v1",
    "KI_EMBEDDING_MODEL": "e",
    "KI_EMBEDDING_DIM": "8",
}


def rec(graph: str) -> str:
    return json.dumps({"hash": "h", "episode": "e", "graph": graph})


@pytest.fixture
def server(monkeypatch):
    for k, v in ENV.items():
        monkeypatch.setenv(k, v)
    srv = fakeredis.FakeServer()
    monkeypatch.setattr(
        admin.redis,
        "Redis",
        lambda **kw: fakeredis.aioredis.FakeRedis(server=srv, decode_responses=True),
    )
    sync = fakeredis.FakeRedis(server=srv, decode_responses=True)
    sync.hset(
        "ki:ingest:notes",
        mapping={"docs/a.md": rec("brain-voice"), "docs/b.md": rec("brain-client")},
    )
    sync.hset("ki:ingest:other", mapping={"docs/c.md": rec("brain-voice")})
    sync.set("ki:synced:notes", "marker")
    sync.hset("ki:failed:notes", "docs/x.md", "{}")
    sync.hset("ki:failed:other", mapping={"docs/y.md": "{}", "docs/z.md": "{}"})
    return sync


def test_forget_graph_dry_run_then_real(server, capsys):
    assert admin.main(["forget-graph", "brain-voice", "--dry-run"]) == 0
    assert "would delete graph brain-voice; 2 document(s)" in capsys.readouterr().out
    assert server.hexists("ki:ingest:notes", "docs/a.md")
    assert server.exists("ki:synced:notes")

    assert admin.main(["forget-graph", "brain-voice"]) == 0
    assert not server.exists("ki:synced:notes")
    assert not server.exists("ki:failed:notes")
    assert "deleted graph brain-voice; 2 document(s)" in capsys.readouterr().out
    assert not server.hexists("ki:ingest:notes", "docs/a.md")
    assert server.hexists("ki:ingest:notes", "docs/b.md")


def test_forget_graph_refuses_a_graph_this_instance_may_not_touch(server):
    with pytest.raises(GraphNotAllowedError):
        admin.main(["forget-graph", "brain-client"])


def test_forget_stranded(server, capsys):
    assert admin.main(["forget-stranded", "--dry-run"]) == 0
    assert "would delete stranded graph brain-client (1 document(s))" in capsys.readouterr().out
    assert server.exists("ki:synced:notes")
    assert admin.main(["forget-stranded"]) == 0
    assert not server.exists("ki:synced:notes")
    assert not server.exists("ki:failed:other")
    assert not server.hexists("ki:ingest:notes", "docs/b.md")
    assert server.hexists("ki:ingest:notes", "docs/a.md")
    assert admin.main(["forget-stranded"]) == 0
    assert "nothing stranded" in capsys.readouterr().out


def test_forget_failures_keeps_ingest_state(server, capsys):
    assert admin.main(["forget-failures", "--dry-run"]) == 0
    assert "would delete 3 failure(s)" in capsys.readouterr().out
    assert server.exists("ki:failed:notes")
    assert admin.main(["forget-failures"]) == 0
    assert not server.exists("ki:failed:notes") and not server.exists("ki:failed:other")
    assert server.exists("ki:synced:notes") and server.hexists("ki:ingest:notes", "docs/a.md")


async def test_state_store_records_failures(server, monkeypatch):
    from ki_kb import graph
    from ki_kb.plan import Failed
    from ki_kb.settings import Settings

    monkeypatch.setattr(graph.redis, "Redis", admin.redis.Redis)
    store = graph.StateStore(Settings.from_env())
    failed = Failed(hash="h", graph="brain-voice", version="1.0.0", error="JSONDecodeError")
    try:
        await store.put_failure("fresh", "docs/a.md", failed)
        await store.put_failure("fresh", "docs/b.md", failed)
        assert await store.failures("fresh") == {"docs/a.md": failed, "docs/b.md": failed}
        await store.drop_failures("fresh", ["docs/a.md"])
        await store.drop_failures("fresh", [])
        assert list(await store.failures("fresh")) == ["docs/b.md"]
    finally:
        await store.close()


async def test_state_store_keeps_the_sync_marker(server, monkeypatch):
    from ki_kb import graph
    from ki_kb.settings import Settings

    monkeypatch.setattr(graph.redis, "Redis", admin.redis.Redis)
    store = graph.StateStore(Settings.from_env())
    try:
        assert await store.synced("notes") == "marker"
        await store.mark_synced("other", "m2")
        assert await store.synced("other") == "m2"
        assert await store.synced("missing") is None
    finally:
        await store.close()


def test_recorded_by_graph_names_episodes_and_skips_other_instances() -> None:
    states = {
        "notes": {"docs/a.md": json.dumps({"hash": "h", "episode": "u-a", "graph": "brain-voice"})},
        "other": {
            "docs/b.md": json.dumps({"hash": "h", "episode": "u-b", "graph": "brain-client"})
        },
    }
    out = admin.recorded_by_graph(states, frozenset({"cortex", "brain-voice"}))
    assert out == {"brain-voice": {"notes/docs/a.md": "u-a"}}


def test_plan_repair_deletes_empty_copies_and_reingests_the_rest() -> None:
    recorded = {"p/a.md": "a", "p/b.md": "b", "p/c.md": "c", "p/d.md": "d"}
    found = [
        ("a", "p/a.md", True),
        ("a-crash", "p/a.md", False),  # died before extraction: owns nothing
        ("b", "p/b.md", True),
        ("b-twice", "p/b.md", True),  # extracted twice: may own shared facts
        ("c", "p/c.md", True),
        ("x", "p/untracked.md", False),
    ]  # d's recorded episode is gone
    rep = admin.plan_repair(recorded, found)
    assert rep.empty_copies == ["a-crash"]
    assert rep.reingest == ["p/b.md", "p/d.md"]


class _FakeDriver:
    def __init__(self, rows: list[dict[str, object]]) -> None:
        self.rows = rows
        self.deleted: list[str] = []

    async def execute_query(self, cypher, **params):  # type: ignore[no-untyped-def]
        if "DETACH DELETE" in cypher:
            self.deleted.extend(params["uuids"])
            return [], None, None
        names = set(params["names"])
        return [r for r in self.rows if r["name"] in names], None, None


class _FakeGraphiti:
    def __init__(self, rows: list[dict[str, object]]) -> None:
        self.driver = _FakeDriver(rows)
        self.closed = False

    async def close(self) -> None:
        self.closed = True


@pytest.mark.asyncio
@pytest.mark.parametrize("dry_run", [True, False])
async def test_remove_duplicates(server, monkeypatch, dry_run) -> None:
    # Every state entry in the fixture records episode "e".
    rows = [
        {"uuid": "e", "name": "notes/docs/a.md", "owns": True},
        {"uuid": "crash", "name": "notes/docs/a.md", "owns": False},
        {"uuid": "e", "name": "other/docs/c.md", "owns": True},
        {"uuid": "twice", "name": "other/docs/c.md", "owns": True},
    ]
    built: dict[str, _FakeGraphiti] = {}

    def fake_build(settings, graph):  # type: ignore[no-untyped-def]
        return built.setdefault(graph, _FakeGraphiti(rows))

    monkeypatch.setattr(admin, "build_graphiti", fake_build)
    out = await admin.remove_duplicates(admin.Settings.from_env(), dry_run=dry_run)
    assert set(out) == {"brain-voice"}  # brain-client is not this instance's
    assert out["brain-voice"].empty_copies == ["crash"]
    assert out["brain-voice"].reingest == ["other/docs/c.md"]
    voice = built["brain-voice"]
    assert voice.closed
    assert server.hexists("ki:ingest:notes", "docs/a.md")  # its copy was empty: kept
    if dry_run:
        assert voice.driver.deleted == []
        assert server.hexists("ki:ingest:other", "docs/c.md")
    else:
        assert voice.driver.deleted == ["crash"]
        assert not server.hexists("ki:ingest:other", "docs/c.md")
        assert not server.exists("ki:synced:other")


def test_index_drops_cover_fulltext_and_range_on_nodes_and_relations() -> None:
    rows = [
        ("Entity", ["uuid", "name"], {"uuid": ["RANGE"], "name": ["RANGE", "FULLTEXT"]}, "NODE"),
        ("RELATES_TO", ["fact", "uuid"], {"fact": ["FULLTEXT"], "uuid": ["RANGE"]}, "RELATIONSHIP"),
    ]
    assert admin.index_drops(rows) == [
        "CALL db.idx.fulltext.drop('Entity')",
        "DROP INDEX FOR (n:Entity) ON (n.uuid)",
        "DROP INDEX FOR (n:Entity) ON (n.name)",
        "DROP FULLTEXT INDEX FOR ()-[n:RELATES_TO]-() ON (n.fact)",
        "DROP INDEX FOR ()-[n:RELATES_TO]-() ON (n.uuid)",
    ]


@pytest.mark.asyncio
async def test_rewrite_aof_waits_and_checks_status(monkeypatch) -> None:
    calls: list[str] = []

    class FakeR:
        def __init__(self) -> None:
            self.polls = 0

        async def execute_command(self, *args):  # type: ignore[no-untyped-def]
            calls.append(args[0])

        async def info(self, section):  # type: ignore[no-untyped-def]
            self.polls += 1
            busy = self.polls < 2
            return {
                "aof_rewrite_in_progress": int(busy),
                "aof_rewrite_scheduled": 0,
                "aof_last_bgrewrite_status": "ok",
            }

        async def aclose(self) -> None:
            pass

    for k, v in ENV.items():
        monkeypatch.setenv(k, v)
    monkeypatch.setattr(admin.redis, "Redis", lambda **kw: FakeR())
    monkeypatch.setattr(admin.asyncio, "sleep", lambda s: asyncio_sleep0())
    await admin._rewrite_aof(admin.Settings.from_env())
    assert calls == ["BGREWRITEAOF"]


async def asyncio_sleep0() -> None:
    return None
