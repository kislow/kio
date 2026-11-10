from __future__ import annotations

import asyncio

import falkordb.asyncio.falkordb as falkordb_async
import pytest
import redis.asyncio as redis

from ki_kb.graph import POOL_WAIT_SECONDS, build_graphiti
from ki_kb.settings import Settings

ENV = {
    "KI_ALLOWED_GRAPHS": "cortex",
    "KI_LLM_BASE_URL": "http://llm.invalid/v1",
    "KI_LLM_MODEL": "m",
    "KI_LLM_API_KEY": "k",
    "KI_EMBEDDING_BASE_URL": "http://embed.invalid/v1",
    "KI_EMBEDDING_MODEL": "e",
    "KI_EMBEDDING_API_KEY": "k",
    "KI_EMBEDDING_DIM": "8",
    "FALKORDB_HOST": "falkordb.invalid",
}


@pytest.mark.parametrize(("setting", "want"), [(None, 300), ("16", 16)])
def test_graph_client_waits_for_a_connection_instead_of_failing(
    monkeypatch: pytest.MonkeyPatch, setting: str | None, want: int
) -> None:
    for k, v in ENV.items():
        monkeypatch.setenv(k, v)
    if setting is not None:
        monkeypatch.setenv("KI_FALKORDB_MAX_CONNECTIONS", setting)
    # FalkorDB's client asks the server whether it is a cluster when built.
    monkeypatch.setattr(falkordb_async, "Is_Cluster", lambda conn: False)

    graph = build_graphiti(Settings.from_env(), "cortex")
    pool = graph.driver.client.connection.connection_pool

    assert isinstance(pool, redis.BlockingConnectionPool)
    # A full pool waits (bounded) instead of raising MaxConnectionsError at once.
    assert (pool.max_connections, pool.timeout) == (want, POOL_WAIT_SECONDS)
    assert pool.connection_kwargs["decode_responses"] is True
    assert pool.connection_kwargs["host"] == "falkordb.invalid"
    assert pool.connection_kwargs["socket_timeout"] == 300


def test_three_lanes_share_a_small_pool_and_wait(monkeypatch: pytest.MonkeyPatch) -> None:
    """Three graph lanes against a pool smaller than their fan-out must not fail."""
    for k, v in ENV.items():
        monkeypatch.setenv(k, v)
    monkeypatch.setenv("KI_ALLOWED_GRAPHS", "brain-work,brain-voice,brain-security")
    monkeypatch.setenv("KI_FALKORDB_MAX_CONNECTIONS", "2")
    monkeypatch.setattr(falkordb_async, "Is_Cluster", lambda conn: False)

    settings = Settings.from_env()
    lanes = ("brain-work", "brain-voice", "brain-security")
    clients = [build_graphiti(settings, graph) for graph in lanes]
    pools = [client.driver.client.connection.connection_pool for client in clients]
    pool = pools[0]
    assert all(other is pool for other in pools)
    assert isinstance(pool, redis.BlockingConnectionPool)
    assert pool.max_connections == 2

    async def hold(self: redis.BlockingConnectionPool, connection: object) -> None:
        assert self.max_connections == 2
        assert connection is not None
        await asyncio.sleep(0.05)

    monkeypatch.setattr(redis.BlockingConnectionPool, "ensure_connection", hold)

    async def lane(graph: str) -> None:
        # More checkouts than the pool has, overlapping the other lanes.
        for _ in range(3):
            conn = await pool.get_connection()
            try:
                await asyncio.sleep(0.02)
            finally:
                await pool.release(conn)
        assert graph in lanes

    async def run_lanes() -> None:
        await asyncio.gather(*(lane(graph) for graph in lanes))

    asyncio.run(run_lanes())
