"""Graphiti clients and the ingest state store.

One Graphiti instance per graph: in FalkorDB each brain is its own graph, and
Graphiti maps group_id to the graph name, so a per-graph driver keeps adds,
removals and searches pinned to exactly one brain.
"""

from __future__ import annotations

import json
import logging
from dataclasses import asdict
from typing import Any

import redis.asyncio as redis
from falkordb.asyncio import FalkorDB
from graphiti_core import Graphiti
from graphiti_core.cross_encoder.openai_reranker_client import OpenAIRerankerClient
from graphiti_core.embedder.openai import OpenAIEmbedder, OpenAIEmbedderConfig
from graphiti_core.errors import NodeNotFoundError
from graphiti_core.graphiti import AddBulkEpisodeResults, AddEpisodeResults
from graphiti_core.llm_client import LLMConfig
from graphiti_core.nodes import EpisodicNode
from graphiti_core.utils.bulk_utils import RawEpisode
from openai import AsyncOpenAI

from . import dedupe_bounds, metrics
from .falkor import FlatFalkorDriver
from .llm import SchemaEchoClient, Usage, counting, metered, prompt_only, without_thinking
from .plan import Backoff, Failed, Recorded
from .settings import Settings

log = logging.getLogger(__name__)

# One pool per FalkorDB endpoint and size. Every graph client in the process
# shares it, so three lanes do not each open a full set of connections.
_PoolKey = tuple[str, int, str | None, int]
# Long enough for a burst to drain, short enough that a deadlock ends the run.
POOL_WAIT_SECONDS = 300
_pools: dict[_PoolKey, redis.BlockingConnectionPool] = {}


def falkordb_pool(settings: Settings) -> redis.BlockingConnectionPool:
    """The shared blocking pool for this FalkorDB.

    A checkout waits until a connection is free. redis-py's non-blocking pool
    raises MaxConnectionsError ("Too many connections") the moment it is full,
    and that error discarded a paid batch. The wait is bounded: the activity's
    heartbeat runs in its own task and keeps beating while a query waits, so
    only the 2h StartToClose would end an unbounded wait, and nested Graphiti
    gathers sharing one full pool can each hold connections the other needs.
    After POOL_WAIT_SECONDS the checkout raises ConnectionError, an outage, and
    the run ends without Temporal retrying it.
    """
    key: _PoolKey = (
        settings.falkordb_host,
        settings.falkordb_port,
        settings.falkordb_password,
        settings.falkordb_max_connections,
    )
    pool = _pools.get(key)
    if pool is None:
        pool = redis.BlockingConnectionPool(
            host=settings.falkordb_host,
            port=settings.falkordb_port,
            password=settings.falkordb_password,
            max_connections=settings.falkordb_max_connections,
            timeout=POOL_WAIT_SECONDS,
            # redis-py's default read timeout is 5 s; FalkorDB's own client passes
            # none. Graphiti's bulk merge queries run longer on a large graph, and
            # a timed-out query sent the whole batch back to one document at a time.
            socket_timeout=300,
            # FalkorDB's client sets this itself, but a given pool's own setting wins.
            decode_responses=True,
        )
        _pools[key] = pool
    return pool


class MeteredGraphiti(Graphiti):
    """Graphiti that logs the model usage of each episode it adds or removes.

    One INFO line per add_episode or add_episode_bulk call, success or not,
    with episode ids but never names or text, so a sensitive project's paths
    stay out of the log too. ``prompts`` splits calls and input tokens by
    Graphiti prompt name; the per-episode figures divide by the batch size.

    remove_episode calls the model too: it rebuilds the summary of every entity
    the episode touched. That is the bulk of a replaced document's cost, so a
    removal that called the model logs its own line, with ``removed_episode``.
    A removal killed during that rebuild leaves the store record pointing at a
    deleted episode, so remove_episode treats a missing episode as removed.
    """

    # Any: these forward Graphiti's own parameters unchanged.
    def __init__(self, *args: Any, graph: str, model: str, **kwargs: Any) -> None:  # noqa: ANN401
        super().__init__(*args, **kwargs)
        self._usage_fields = {"graph": graph, "model": model}

    def _log_usage(self, usage: Usage, episodes: int, **fields: object) -> None:
        log.info(
            "episode model usage",
            extra={
                **self._usage_fields,
                "episodes": episodes,
                **asdict(usage),
                "calls_per_episode": usage.calls / episodes,
                "input_tokens_per_episode": usage.input_tokens // episodes,
                **fields,
            },
        )

    async def add_episode(self, *args: Any, **kwargs: Any) -> AddEpisodeResults:  # noqa: ANN401
        with counting() as usage:
            try:
                res = await super().add_episode(*args, **kwargs)
            except Exception as err:
                self._log_usage(usage, 1, error=type(err).__name__)
                raise
        self._log_usage(usage, 1, episode_uuids=[str(res.episode.uuid)])
        return res

    async def add_episode_bulk(
        self,
        bulk_episodes: list[RawEpisode],
        *args: Any,  # noqa: ANN401
        **kwargs: Any,  # noqa: ANN401
    ) -> AddBulkEpisodeResults:
        with counting() as usage:
            try:
                res = await super().add_episode_bulk(bulk_episodes, *args, **kwargs)
            except Exception as err:
                self._log_usage(usage, len(bulk_episodes), error=type(err).__name__)
                raise
        self._log_usage(
            usage, len(bulk_episodes), episode_uuids=[str(e.uuid) for e in res.episodes]
        )
        return res

    async def remove_episode(self, episode_uuid: str) -> None:
        try:
            await EpisodicNode.get_by_uuid(self.driver, episode_uuid)
        except NodeNotFoundError:
            log.info(
                "episode already removed",
                extra={**self._usage_fields, "removed_episode": episode_uuid},
            )
            return
        failed: dict[str, str] = {}
        with counting() as usage:
            try:
                await super().remove_episode(episode_uuid)
            except Exception as err:
                failed = {"error": type(err).__name__}
                raise
            finally:
                if usage.calls:
                    self._log_usage(usage, 1, removed_episode=episode_uuid, **failed)


def build_graphiti(settings: Settings, graph: str) -> Graphiti:
    """Graphiti bound to one graph.

    Extraction and embeddings can use different OpenAI-compatible providers
    (e.g. DeepSeek has no embeddings API). For providers without json_schema
    support, such as DeepSeek, set KI_LLM_STRUCTURED_MODE=json_object: Graphiti
    then embeds the schema in the prompt and retries empty responses.

    Every model client is built here with an explicit timeout; Graphiti's own
    defaults would wait for minutes.
    """
    settings.check_graph(graph)
    dedupe_bounds.install(settings.edge_dedupe_candidates, settings.edge_invalidation_candidates)
    # FlatFalkorDriver: nested attribute values become JSON strings (see falkor.py).
    # The pool is shared by every graph so lanes wait on one limit instead of
    # each opening KI_FALKORDB_MAX_CONNECTIONS sockets.
    driver = FlatFalkorDriver(
        falkor_db=FalkorDB(connection_pool=falkordb_pool(settings)), database=graph
    )
    llm_config = LLMConfig(
        api_key=settings.llm_api_key,
        model=settings.llm_model,
        small_model=settings.small_model,
        base_url=settings.llm_base_url,
        temperature=0,
        max_tokens=settings.llm_max_tokens,
    )
    llm_http = AsyncOpenAI(
        api_key=settings.llm_api_key,
        base_url=settings.llm_base_url,
        timeout=settings.llm_timeout_seconds,
    )
    llm_http = metered(llm_http, metrics.provider_of(settings.llm_base_url))
    if not settings.llm_thinking:
        llm_http = without_thinking(llm_http)
    # "prompt" is graphiti's json_object mode (schema in the prompt) minus the
    # response_format the server would reject, plus a JSON-only instruction and
    # an answer trimmed to its first JSON value.
    graphiti_mode = settings.llm_structured_mode
    if graphiti_mode == "prompt":
        graphiti_mode = "json_object"
        llm_http = prompt_only(llm_http)
    embed_http = AsyncOpenAI(
        api_key=settings.embedding_api_key,
        base_url=settings.embedding_base_url,
        timeout=settings.llm_timeout_seconds,
    )
    return MeteredGraphiti(
        graph=graph,
        model=settings.llm_model,
        graph_driver=driver,
        # SchemaEchoClient: validates json_object answers and re-asks on schema echoes (llm.py).
        llm_client=SchemaEchoClient(
            config=llm_config,
            client=llm_http,
            max_tokens=settings.llm_max_tokens,
            structured_output_mode=graphiti_mode,
        ),
        embedder=OpenAIEmbedder(
            OpenAIEmbedderConfig(
                api_key=settings.embedding_api_key,
                embedding_model=settings.embedding_model,
                base_url=settings.embedding_base_url,
                embedding_dim=settings.embedding_dim,
            ),
            client=embed_http,
        ),
        # Only used by cross-encoder search recipes; the default hybrid search
        # (reciprocal rank fusion) never calls it. Passed explicitly so Graphiti
        # does not construct a default client pointed at api.openai.com.
        cross_encoder=OpenAIRerankerClient(config=llm_config, client=llm_http),
    )


class StateStore:
    """Remembers what was ingested from each repo, per graph instance.

    Stored as a Redis hash in the same FalkorDB instance as the graphs, so
    ingest state lives and is backed up with them. Key: ki:ingest:<repo>.
    The marker of the last complete sync is a plain string at ki:synced:<repo>;
    documents whose extraction failed are a hash at ki:failed:<repo>, and the
    run failures that defer a project are a string at ki:backoff:<repo>.
    """

    def __init__(self, settings: Settings) -> None:
        self._r = redis.Redis(
            host=settings.falkordb_host,
            port=settings.falkordb_port,
            password=settings.falkordb_password,
            decode_responses=True,
            socket_timeout=30,
            socket_connect_timeout=10,
        )

    @staticmethod
    def _key(repo: str) -> str:
        return f"ki:ingest:{repo}"

    async def all(self, repo: str) -> dict[str, Recorded]:
        raw = await self._r.hgetall(self._key(repo))
        # decode_responses=True means keys and values are str at runtime.
        return {str(path): Recorded(**json.loads(v)) for path, v in raw.items()}

    async def put(self, repo: str, path: str, rec: Recorded) -> None:
        await self._r.hset(self._key(repo), path, json.dumps(asdict(rec)))

    async def delete(self, repo: str, path: str) -> None:
        await self._r.hdel(self._key(repo), path)

    async def synced(self, repo: str) -> str | None:
        value = await self._r.get(f"ki:synced:{repo}")
        return str(value) if value is not None else None

    async def mark_synced(self, repo: str, marker: str) -> None:
        await self._r.set(f"ki:synced:{repo}", marker)

    async def failures(self, repo: str) -> dict[str, Failed]:
        raw = await self._r.hgetall(f"ki:failed:{repo}")
        return {str(path): Failed(**json.loads(v)) for path, v in raw.items()}

    async def put_failure(self, repo: str, path: str, failed: Failed) -> None:
        await self._r.hset(f"ki:failed:{repo}", path, json.dumps(asdict(failed)))

    async def drop_failures(self, repo: str, paths: list[str]) -> None:
        if paths:
            await self._r.hdel(f"ki:failed:{repo}", *paths)

    async def backoff(self, repo: str) -> Backoff | None:
        raw = await self._r.get(f"ki:backoff:{repo}")
        return Backoff(**json.loads(raw)) if raw is not None else None

    async def put_backoff(self, repo: str, backoff: Backoff) -> None:
        await self._r.set(f"ki:backoff:{repo}", json.dumps(asdict(backoff)))

    async def clear_backoff(self, repo: str) -> None:
        await self._r.delete(f"ki:backoff:{repo}")

    async def close(self) -> None:
        await self._r.aclose()
