"""kb-query: Graphiti search over one FalkorDB instance, for the MCP server.

Cluster-internal only, reachable from ki-mcp alone (network policy), and
limited by KI_ALLOWED_GRAPHS. Which brains a caller may read is decided by
ki-mcp from the caller's token before a request gets here.
"""

from __future__ import annotations

import os
import time
from collections.abc import AsyncIterator, Awaitable, Callable
from contextlib import asynccontextmanager
from dataclasses import dataclass, field
from datetime import UTC, datetime

import uvicorn
from fastapi import FastAPI, HTTPException, Request, Response
from graphiti_core import Graphiti
from pydantic import BaseModel, ConfigDict, Field, model_validator

from . import kinds, metrics
from .errors import GraphNotAllowedError
from .graph import build_graphiti
from .logging_setup import configure_logging
from .settings import Settings


class SearchRequest(BaseModel):
    graph: str
    query: str = Field(min_length=1, max_length=2000)
    limit: int = Field(15, ge=1, le=50)
    kinds: list[str] | None = None


class Fact(BaseModel):
    kind: str
    fact: str
    source: str | None = None
    valid_at: datetime | None = None
    invalid_at: datetime | None = None
    score: float


class SearchResponse(BaseModel):
    facts: list[Fact]


class GraphRequest(BaseModel):
    graph: str


class StatsResponse(BaseModel):
    documents: int
    last_ingest: datetime | None = None


class RecentRequest(BaseModel):
    graph: str
    since: datetime | None = None
    limit: int = Field(20, ge=1, le=100)


class RecentDocument(BaseModel):
    source: str
    kind: str
    committed_at: datetime | None = None


class RecentResponse(BaseModel):
    documents: list[RecentDocument]


class ActivityRequest(BaseModel):
    graph: str
    since: datetime


class ActivityDay(BaseModel):
    day: str
    documents: int


class ActivityResponse(BaseModel):
    days: list[ActivityDay]


class GraphQueryRequest(BaseModel):
    graph: str
    query: str | None = Field(None, min_length=1, max_length=2000)
    node: str | None = Field(None, min_length=1, max_length=200)
    limit: int = Field(30, ge=1, le=60)

    @model_validator(mode="after")
    def _exactly_one_anchor(self) -> GraphQueryRequest:
        if (self.query is None) == (self.node is None):
            raise ValueError("exactly one of query or node is required")
        return self


class GraphNode(BaseModel):
    id: str
    name: str
    summary: str
    labels: list[str]


class GraphEdge(BaseModel):
    model_config = ConfigDict(populate_by_name=True)

    id: str
    from_: str = Field(alias="from")
    to: str
    name: str
    fact: str
    kind: str
    source: str | None = None
    valid_at: datetime | None = None
    invalid_at: datetime | None = None


class GraphResponse(BaseModel):
    nodes: list[GraphNode]
    edges: list[GraphEdge]


class _State:
    """Per-app state: the settings and one Graphiti client per graph."""

    def __init__(self, settings: Settings) -> None:
        self.settings = settings
        self.graphitis: dict[str, Graphiti] = {}

    def graphiti(self, graph: str) -> Graphiti:
        try:
            self.settings.check_graph(graph)
        except GraphNotAllowedError as e:
            raise HTTPException(status_code=403, detail=str(e)) from e
        if graph not in self.graphitis:
            self.graphitis[graph] = build_graphiti(self.settings, graph)
        return self.graphitis[graph]


@asynccontextmanager
async def _lifespan(app: FastAPI) -> AsyncIterator[None]:
    state = _State(Settings.from_env())
    app.state.kb = state
    try:
        yield
    finally:
        for g in state.graphitis.values():
            await g.close()  # type: ignore[no-untyped-call]


def _state(request: Request) -> _State:
    state = getattr(request.app.state, "kb", None)
    if not isinstance(state, _State):
        raise HTTPException(status_code=503, detail="not ready")
    return state


app = FastAPI(title="kb-query", lifespan=_lifespan)
# The registry under /metrics: scraped from inside the cluster only (the
# network policy opens this port to Prometheus and ki-mcp, nothing else).
app.mount("/metrics", metrics.asgi_app())


async def _episode_sources(g: Graphiti, episode_ids: list[str]) -> dict[str, tuple[str, str]]:
    """Return only the episode fields needed to label matching facts."""
    if not episode_ids:
        return {}
    records, _, _ = await g.driver.execute_query(
        """
        MATCH (e:Episodic)
        WHERE e.uuid IN $episode_ids
        RETURN e.uuid AS uuid, e.source_description AS source_description
        """,
        episode_ids=episode_ids,
        routing_="r",
    )
    sources: dict[str, tuple[str, str]] = {}
    for record in records:
        uuid = record.get("uuid")
        description = record.get("source_description")
        if isinstance(uuid, str) and isinstance(description, str):
            sources[uuid] = kinds.parse_source_description(description)
    return sources


def _first_source(
    episodes: list[str] | None, by_id: dict[str, tuple[str, str]]
) -> tuple[str, str | None]:
    """The kind and source of an edge's first episode that is known, else a pointer."""
    for ep in episodes or []:
        if ep in by_id:
            return by_id[ep]
    return kinds.POINTER, None


@app.middleware("http")
async def observe(
    request: Request, call_next: Callable[[Request], Awaitable[Response]]
) -> Response:
    """Count every request and time it whole; the handlers time their stages."""
    tool = request.url.path.strip("/").split("/")[0] or "root"
    if tool == "metrics":
        return await call_next(request)
    started = time.monotonic()
    status = "500"
    try:
        response = await call_next(request)
        status = str(response.status_code)
        return response
    finally:
        metrics.QUERY_REQUESTS.labels(tool, status).inc()
        metrics.QUERY_SECONDS.labels(tool, "total").observe(time.monotonic() - started)


@app.get("/healthz")
async def healthz() -> dict[str, str]:
    return {"status": "ok"}


@app.post("/search", response_model=SearchResponse)
async def search(req: SearchRequest, request: Request) -> SearchResponse:
    g = _state(request).graphiti(req.graph)
    want = set(req.kinds or [])
    # Over-fetch when filtering by kind so the filter does not starve results.
    with metrics.QUERY_SECONDS.labels("search", "search").time():
        edges = await g.search(
            req.query, group_ids=[req.graph], num_results=req.limit * (3 if want else 1)
        )

    episode_ids = sorted({ep for e in edges for ep in (e.episodes or [])})
    with metrics.QUERY_SECONDS.labels("search", "episodes").time():
        by_id = await _episode_sources(g, episode_ids)

    facts: list[Fact] = []
    for rank, edge in enumerate(edges):
        kind, source = _first_source(edge.episodes, by_id)
        if want and kind not in want:
            continue
        facts.append(
            Fact(
                kind=kind,
                fact=edge.fact,
                source=source,
                valid_at=edge.valid_at,
                invalid_at=edge.invalid_at or edge.expired_at,
                score=1.0 - rank / max(len(edges), 1),
            )
        )
        if len(facts) >= req.limit:
            break
    return SearchResponse(facts=facts)


def _timestamp(value: object) -> datetime | None:
    """Graphiti stores times as ISO strings in FalkorDB; a driver may hand back datetimes."""
    if isinstance(value, datetime):
        return value
    if isinstance(value, str) and value:
        try:
            return datetime.fromisoformat(value)
        except ValueError:
            return None
    return None


@app.post("/stats", response_model=StatsResponse)
async def stats(req: GraphRequest, request: Request) -> StatsResponse:
    """What a brain holds: documents (sections of one file count once) and the last ingest."""
    g = _state(request).graphiti(req.graph)
    with metrics.QUERY_SECONDS.labels("stats", "stats").time():
        records, _, _ = await g.driver.execute_query(
            """
            MATCH (e:Episodic)
            WHERE e.group_id = $group_id
            RETURN count(DISTINCT split(e.name, '#')[0]) AS documents,
                   max(e.created_at) AS last_ingest
            """,
            group_id=req.graph,
            routing_="r",
        )
    row = records[0] if records else {}
    return StatsResponse(
        documents=int(row.get("documents") or 0), last_ingest=_timestamp(row.get("last_ingest"))
    )


@app.post("/recent", response_model=RecentResponse)
async def recent(req: RecentRequest, request: Request) -> RecentResponse:
    """Documents by the time of their last commit, newest first."""
    g = _state(request).graphiti(req.graph)
    since = req.since.isoformat() if req.since else ""
    with metrics.QUERY_SECONDS.labels("recent", "recent").time():
        records, _, _ = await g.driver.execute_query(
            """
            MATCH (e:Episodic)
            WHERE e.group_id = $group_id AND e.valid_at >= $since
            WITH split(e.name, '#')[0] AS name,
                 max(e.valid_at) AS committed_at,
                 head(collect(e.source_description)) AS source_description
            RETURN name, committed_at, source_description
            ORDER BY committed_at DESC
            LIMIT $limit
            """,
            group_id=req.graph,
            since=since,
            limit=req.limit,
            routing_="r",
        )
    documents = []
    for record in records:
        description = record.get("source_description")
        kind, source = kinds.parse_source_description(
            description if isinstance(description, str) else ""
        )
        documents.append(
            RecentDocument(
                source=source or str(record.get("name") or ""),
                kind=kind,
                committed_at=_timestamp(record.get("committed_at")),
            )
        )
    return RecentResponse(documents=documents)


@app.post("/activity", response_model=ActivityResponse)
async def activity(req: ActivityRequest, request: Request) -> ActivityResponse:
    """Documents ingested per UTC day, by ingest time rather than commit time."""
    g = _state(request).graphiti(req.graph)
    since = req.since if req.since.tzinfo else req.since.replace(tzinfo=UTC)
    with metrics.QUERY_SECONDS.labels("activity", "activity").time():
        records, _, _ = await g.driver.execute_query(
            """
            MATCH (e:Episodic)
            WHERE e.group_id = $group_id AND e.created_at >= $since
            RETURN substring(e.created_at, 0, 10) AS day,
                   count(DISTINCT split(e.name, '#')[0]) AS documents
            ORDER BY day
            """,
            group_id=req.graph,
            since=since.astimezone(UTC).isoformat(),
            routing_="r",
        )
    return ActivityResponse(
        days=[
            ActivityDay(day=str(record["day"]), documents=int(record["documents"]))
            for record in records
            if record.get("day")
        ]
    )


_SUMMARY_CHARS = 400
# Graphiti adds Entity and a per-group Entity_<group> label to every entity node.
_GENERIC_LABELS = ("Entity", "Entity_")

_NODE_COLUMNS = "n.uuid AS uuid, n.name AS name, n.summary AS summary, labels(n) AS labels"


@dataclass
class _Edge:
    id: str
    source: str
    target: str
    name: str
    fact: str
    episodes: list[str]
    valid_at: datetime | None
    invalid_at: datetime | None


@dataclass
class _Subgraph:
    edges: list[_Edge] = field(default_factory=list)
    nodes: dict[str, GraphNode] = field(default_factory=dict)


def _graph_node(record: dict[str, object]) -> GraphNode:
    labels = record.get("labels")
    return GraphNode(
        id=str(record["uuid"]),
        name=str(record.get("name") or ""),
        summary=str(record.get("summary") or "")[:_SUMMARY_CHARS],
        labels=[
            label
            for label in (labels if isinstance(labels, list) else [])
            if isinstance(label, str) and not label.startswith(_GENERIC_LABELS)
        ],
    )


async def _nodes_by_id(g: Graphiti, graph: str, uuids: list[str]) -> dict[str, GraphNode]:
    if not uuids:
        return {}
    records, _, _ = await g.driver.execute_query(
        f"""
        MATCH (n:Entity)
        WHERE n.uuid IN $uuids AND n.group_id = $group_id
        RETURN {_NODE_COLUMNS}
        """,
        uuids=uuids,
        group_id=graph,
        routing_="r",
    )
    return {str(r["uuid"]): _graph_node(r) for r in records}


async def _query_subgraph(g: Graphiti, graph: str, query: str, limit: int) -> _Subgraph:
    with metrics.QUERY_SECONDS.labels("graph", "search").time():
        found = await g.search(query, group_ids=[graph], num_results=limit)
    edges = [
        _Edge(
            e.uuid,
            e.source_node_uuid,
            e.target_node_uuid,
            e.name,
            e.fact,
            e.episodes or [],
            e.valid_at,
            e.invalid_at,
        )
        for e in found
        if e.expired_at is None
    ]
    with metrics.QUERY_SECONDS.labels("graph", "nodes").time():
        nodes = await _nodes_by_id(
            g, graph, sorted({u for e in edges for u in (e.source, e.target)})
        )
    return _Subgraph(edges, nodes)


async def _node_subgraph(g: Graphiti, graph: str, node: str, limit: int) -> _Subgraph:
    with metrics.QUERY_SECONDS.labels("graph", "nodes").time():
        center = await _nodes_by_id(g, graph, [node])
    if node not in center:
        raise HTTPException(status_code=404, detail="no such node in this graph")
    with metrics.QUERY_SECONDS.labels("graph", "edges").time():
        records, _, _ = await g.driver.execute_query(
            """
            MATCH (n:Entity {uuid: $node, group_id: $group_id})-[e:RELATES_TO]-(m:Entity)
            WHERE e.expired_at IS NULL AND m.group_id = $group_id
            RETURN e.uuid AS uuid, startNode(e).uuid AS source, endNode(e).uuid AS target,
                   e.name AS name, e.fact AS fact, e.episodes AS episodes,
                   e.valid_at AS valid_at, e.invalid_at AS invalid_at,
                   m.uuid AS m_uuid, m.name AS m_name, m.summary AS m_summary,
                   labels(m) AS m_labels
            ORDER BY coalesce(e.valid_at, '') DESC
            LIMIT $limit
            """,
            node=node,
            group_id=graph,
            limit=limit,
            routing_="r",
        )
    sub = _Subgraph(nodes=dict(center))
    for r in records:
        if any(e.id == r["uuid"] for e in sub.edges):
            continue  # a self-loop matches in both directions
        sub.edges.append(
            _Edge(
                str(r["uuid"]),
                str(r["source"]),
                str(r["target"]),
                str(r.get("name") or ""),
                str(r.get("fact") or ""),
                r.get("episodes") or [],
                _timestamp(r.get("valid_at")),
                _timestamp(r.get("invalid_at")),
            )
        )
        sub.nodes[str(r["m_uuid"])] = _graph_node(
            {
                "uuid": r["m_uuid"],
                "name": r.get("m_name"),
                "summary": r.get("m_summary"),
                "labels": r.get("m_labels"),
            }
        )
    return sub


@app.post("/graph", response_model=GraphResponse, response_model_by_alias=True)
async def graph(req: GraphQueryRequest, request: Request) -> GraphResponse:
    """Entities and facts of one brain, around a query or around one entity."""
    g = _state(request).graphiti(req.graph)
    if req.query is not None:
        sub = await _query_subgraph(g, req.graph, req.query, req.limit)
    else:
        sub = await _node_subgraph(g, req.graph, req.node or "", req.limit)

    with metrics.QUERY_SECONDS.labels("graph", "episodes").time():
        by_id = await _episode_sources(g, sorted({ep for e in sub.edges for ep in e.episodes}))
    edges = []
    for e in sub.edges:
        if e.source not in sub.nodes or e.target not in sub.nodes:
            continue  # an edge whose endpoint cannot be drawn is no use to the client
        kind, source = _first_source(e.episodes, by_id)
        edges.append(
            GraphEdge(
                id=e.id,
                from_=e.source,
                to=e.target,
                name=e.name,
                fact=e.fact,
                kind=kind,
                source=source,
                valid_at=e.valid_at,
                invalid_at=e.invalid_at,
            )
        )
    used = {u for e in edges for u in (e.from_, e.to)}
    nodes = [n for n in sub.nodes.values() if n.id in used or req.node == n.id]
    return GraphResponse(nodes=nodes, edges=edges)


def main() -> None:
    configure_logging()
    uvicorn.run(
        app,
        host=os.environ.get("KI_QUERY_HOST", "0.0.0.0"),
        port=int(os.environ.get("KI_QUERY_PORT", "8000")),
        log_config=None,  # keep the JSON handler configure_logging installed
        timeout_keep_alive=30,
    )


if __name__ == "__main__":
    main()
