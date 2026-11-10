"""Admin commands, run inside a worker pod.

    python -m ki_kb.admin forget-graph <graph> [--dry-run]
    python -m ki_kb.admin forget-stranded [--dry-run]
    python -m ki_kb.admin forget-failures [--dry-run]
    python -m ki_kb.admin remove-duplicates [--dry-run]
    python -m ki_kb.admin rebuild-indexes [--dry-run]

forget-graph deletes one graph and the ingest state of every document that
fed it, so the next sync re-extracts exactly those documents from git. Use it
after changing the embedding model or to rebuild a damaged brain.

forget-stranded deletes, on this instance only, every graph that the ingest
state still records but this instance may no longer touch, with that state.
It is the clean-up after a brain changed sensitivity (see operations.md). It
only ever deletes.

Both also drop every project's sync marker, because a project whose remote
head has not moved is otherwise skipped without a clone and the forgotten
documents would never be re-extracted, and every recorded extraction failure,
so those documents are tried again too.

forget-failures drops only the recorded extraction failures, so the next sync
retries those documents. Use it after a fix that is not a release, such as a
higher KI_LLM_MAX_TOKENS or another model.

remove-duplicates repairs ingested documents that have more than one
episode (copies left by attempts that died with the database before they could
clean up) or whose recorded episode is gone. A copy that owns no knowledge (no
MENTIONS, no edges; the attempt died before extraction finished) is deleted
directly. A document with a copy that does own knowledge, or with no recorded
episode, has its ingest state forgotten, so the next sync removes every copy
with Graphiti's own removal and extracts it once more: Graphiti credits a fact
to the episode that first produced it, so deleting only the copy could take
facts the recorded version relies on. State is forgotten before anything else,
so an interrupted run never leaves a document recorded without its episode.
Graphs this instance may not touch are left alone.

rebuild-indexes drops every index of each graph this instance may touch and
lets Graphiti create them again, then checks that an index lookup finds what a
full scan finds. Run it once after moving FalkorDB from 4.x to 6.x: 6.x loads
4.x range indexes on properties that also have a full-text index as missing,
and creating them again on top of the loaded data gives indexes that answer
nothing (docs/operations.md, "Upgrade FalkorDB"). Lookups scan while it runs.
It ends with an append-only rewrite: 6.0.3 cannot always replay the index
drops from its log, and without the rewrite the next start can fail.
"""

from __future__ import annotations

import argparse
import asyncio
import contextlib
import dataclasses
import json
import sys
from collections.abc import AsyncIterator, Sequence

import redis.asyncio as redis
from redis.exceptions import ResponseError

from .brains import matches_any, valid_name
from .graph import build_graphiti
from .settings import Settings

STATE_PREFIX = "ki:ingest:"
STATE_PATTERN = STATE_PREFIX + "*"
SYNCED_PATTERN = "ki:synced:*"
FAILED_PATTERN = "ki:failed:*"


def entries_to_forget(state: dict[str, str], graph: str) -> list[str]:
    """Paths in one project's ingest state that were written to this graph."""
    return sorted(path for path, raw in state.items() if json.loads(raw).get("graph") == graph)


def stranded_in(state: dict[str, str], allowed: frozenset[str]) -> dict[str, list[str]]:
    """Graph -> paths for state entries whose graph this instance may not touch."""
    out: dict[str, list[str]] = {}
    for path, raw in sorted(state.items()):
        graph = str(json.loads(raw).get("graph", ""))
        if valid_name(graph) and not matches_any(allowed, graph):
            out.setdefault(graph, []).append(path)
    return out


@contextlib.asynccontextmanager
async def _redis(settings: Settings) -> AsyncIterator[redis.Redis]:
    r = redis.Redis(
        host=settings.falkordb_host,
        port=settings.falkordb_port,
        password=settings.falkordb_password,
        decode_responses=True,
        socket_timeout=30,
    )
    try:
        yield r
    finally:
        await r.aclose()


async def _states(r: redis.Redis) -> AsyncIterator[tuple[str, dict[str, str]]]:
    async for key in r.scan_iter(match=STATE_PATTERN):
        # decode_responses=True means keys and values are str at runtime.
        yield str(key), {str(k): str(v) for k, v in (await r.hgetall(key)).items()}


async def _drop(r: redis.Redis, *patterns: str) -> None:
    keys = [key for pattern in patterns async for key in r.scan_iter(match=pattern)]
    if keys:
        await r.delete(*keys)


async def _drop_markers_and_failures(r: redis.Redis) -> None:
    await _drop(r, SYNCED_PATTERN, FAILED_PATTERN)


async def forget_graph(settings: Settings, graph: str, dry_run: bool = False) -> int:
    settings.check_graph(graph)
    forgotten = 0
    async with _redis(settings) as r:
        async for key, state in _states(r):
            paths = entries_to_forget(state, graph)
            if paths and not dry_run:
                await r.hdel(key, *paths)
            forgotten += len(paths)
        if not dry_run:
            await _drop_markers_and_failures(r)
            with contextlib.suppress(ResponseError):  # the graph may not exist yet
                await r.execute_command("GRAPH.DELETE", graph)  # type: ignore[no-untyped-call]
    return forgotten


async def forget_stranded(settings: Settings, dry_run: bool = False) -> dict[str, int]:
    found: dict[str, int] = {}
    async with _redis(settings) as r:
        async for key, state in _states(r):
            for graph, paths in stranded_in(state, settings.allowed_graphs).items():
                found[graph] = found.get(graph, 0) + len(paths)
                if not dry_run:
                    await r.hdel(key, *paths)
        if found and not dry_run:
            await _drop_markers_and_failures(r)
            for graph in found:
                with contextlib.suppress(ResponseError):
                    await r.execute_command("GRAPH.DELETE", graph)  # type: ignore[no-untyped-call]
    return found


async def forget_failures(settings: Settings, dry_run: bool = False) -> int:
    """Documents with a recorded extraction failure, across every project."""
    async with _redis(settings) as r:
        keys = [key async for key in r.scan_iter(match=FAILED_PATTERN)]
        count = sum([await r.hlen(key) for key in keys])
        if not dry_run:
            await _drop(r, FAILED_PATTERN)
    return count


def recorded_by_graph(
    states: dict[str, dict[str, str]], allowed: frozenset[str]
) -> dict[str, dict[str, str]]:
    """Graph -> episode name -> recorded episode uuid, for graphs this instance may touch.

    ``states`` maps a project to its ingest state; episodes are named
    ``<project>/<path>``.
    """
    out: dict[str, dict[str, str]] = {}
    for project, state in sorted(states.items()):
        for path, raw in sorted(state.items()):
            rec = json.loads(raw)
            graph = str(rec.get("graph", ""))
            if valid_name(graph) and matches_any(allowed, graph):
                out.setdefault(graph, {})[f"{project}/{path}"] = str(rec["episode"])
    return out


@dataclasses.dataclass
class Repair:
    """What remove-duplicates does in one graph."""

    empty_copies: list[str] = dataclasses.field(default_factory=list)
    reingest: list[str] = dataclasses.field(default_factory=list)


def plan_repair(recorded: dict[str, str], found: list[tuple[str, str, bool]]) -> Repair:
    """From (uuid, name, owns knowledge) rows of the recorded names' episodes."""
    by_name: dict[str, list[tuple[str, bool]]] = {}
    for uuid, name, owns in found:
        if name in recorded:
            by_name.setdefault(name, []).append((uuid, owns))
    repair = Repair()
    for name, rec_uuid in sorted(recorded.items()):
        episodes = by_name.get(name, [])
        copies = [(uuid, owns) for uuid, owns in episodes if uuid != rec_uuid]
        if not any(uuid == rec_uuid for uuid, _ in episodes) or any(owns for _, owns in copies):
            repair.reingest.append(name)
        else:
            repair.empty_copies.extend(uuid for uuid, _ in copies)
    repair.empty_copies.sort()
    return repair


async def remove_duplicates(settings: Settings, dry_run: bool = False) -> dict[str, Repair]:
    """Graph -> what was (or would be) repaired, for graphs that need it."""
    async with _redis(settings) as r:
        states = {key.removeprefix(STATE_PREFIX): state async for key, state in _states(r)}
        out: dict[str, Repair] = {}
        for graph, recorded in recorded_by_graph(states, settings.allowed_graphs).items():
            gi = build_graphiti(settings, graph)
            try:
                rows, _, _ = await gi.driver.execute_query(
                    "MATCH (e:Episodic) WHERE e.group_id = $group AND e.name IN $names "
                    "OPTIONAL MATCH (e)-[m:MENTIONS]->() "
                    "WITH e, count(m) AS mentions "
                    "RETURN e.uuid AS uuid, e.name AS name, "
                    "mentions > 0 OR size(coalesce(e.entity_edges, [])) > 0 AS owns",
                    group=graph,
                    names=sorted(recorded),
                )
                repair = plan_repair(
                    recorded, [(str(x["uuid"]), str(x["name"]), bool(x["owns"])) for x in rows]
                )
                if not dry_run:
                    for name in repair.reingest:
                        project, path = name.split("/", 1)
                        await r.hdel(STATE_PREFIX + project, path)
                        await r.delete(f"ki:synced:{project}")
                    if repair.empty_copies:
                        await gi.driver.execute_query(
                            "MATCH (e:Episodic) WHERE e.uuid IN $uuids DETACH DELETE e",
                            uuids=repair.empty_copies,
                        )
            finally:
                await gi.close()  # type: ignore[no-untyped-call]
            if repair.empty_copies or repair.reingest:
                out[graph] = repair
    return out


def index_drops(rows: list[tuple[str, list[str], dict[str, list[str]], str]]) -> list[str]:
    """Statements that drop every index in ``CALL db.indexes()`` rows
    (label, properties, types per property, entity type)."""
    out: list[str] = []
    for label, _props, types, etype in rows:
        pattern = f"(n:{label})" if etype == "NODE" else f"()-[n:{label}]-()"
        fulltext = [p for p, t in types.items() if "FULLTEXT" in t]
        if fulltext and etype == "NODE":
            out.append(f"CALL db.idx.fulltext.drop('{label}')")
        elif fulltext:
            out.append(
                f"DROP FULLTEXT INDEX FOR {pattern} ON ({', '.join('n.' + p for p in fulltext)})"
            )
        out.extend(f"DROP INDEX FOR {pattern} ON (n.{p})" for p, t in types.items() if "RANGE" in t)
    return out


INDEX_CHECKS = [
    (
        "MATCH (n:Entity) WHERE n.group_id = $g RETURN count(n) AS c",
        "MATCH (n:Entity) WHERE toString(n.group_id) = $g RETURN count(n) AS c",
    ),
    (
        "MATCH (n:Episodic) WHERE n.group_id = $g RETURN count(n) AS c",
        "MATCH (n:Episodic) WHERE toString(n.group_id) = $g RETURN count(n) AS c",
    ),
    (
        "MATCH ()-[e:RELATES_TO]->() WHERE e.group_id = $g RETURN count(e) AS c",
        "MATCH ()-[e:RELATES_TO]->() WHERE toString(e.group_id) = $g RETURN count(e) AS c",
    ),
]


class IndexCheckError(RuntimeError):
    """An index lookup found something other than a full scan."""


async def rebuild_indexes(settings: Settings, dry_run: bool = False) -> dict[str, int]:
    """Graph -> indexes dropped (and recreated by Graphiti)."""
    async with _redis(settings) as r:
        graphs = [
            str(g)
            for g in await r.execute_command("GRAPH.LIST")  # type: ignore[no-untyped-call]
            if valid_name(str(g)) and matches_any(settings.allowed_graphs, str(g))
        ]
    out: dict[str, int] = {}
    for graph in sorted(graphs):
        gi = build_graphiti(settings, graph)
        try:
            dropped = 0
            # FalkorDB 6.0 can bring an index back while another one on the same
            # graph is dropped (a full-text drop on RELATES_TO restored the Entity
            # one on data loaded from 4.x), and drops finish in the background.
            # So drop what is listed until nothing is, and only then let Graphiti
            # create them: a range index created next to a surviving full-text
            # index answers nothing.
            for _ in range(30):
                rows, _, _ = await gi.driver.execute_query(
                    "CALL db.indexes() YIELD label, properties, types, entitytype "
                    "RETURN label, properties, types, entitytype"
                )
                drops = index_drops(
                    [
                        (
                            str(x["label"]),
                            list(x["properties"]),
                            dict(x["types"]),
                            str(x["entitytype"]),
                        )
                        for x in rows
                    ]
                )
                if not drops or dry_run:
                    break
                for stmt in drops:
                    with contextlib.suppress(Exception):  # already gone
                        await gi.driver.execute_query(stmt)
                dropped += len(drops)
                await asyncio.sleep(1)
            else:
                raise IndexCheckError(f"{graph}: indexes still present after dropping them")
            out[graph] = len(drops) if dry_run else dropped
            if dry_run:
                continue
            await gi.driver.build_indices_and_constraints()
            for _ in range(120):
                states, _, _ = await gi.driver.execute_query(
                    "CALL db.indexes() YIELD status RETURN status"
                )
                if all(x["status"] == "OPERATIONAL" for x in states):
                    break
                await asyncio.sleep(1)
            for indexed, scanned in INDEX_CHECKS:
                a, _, _ = await gi.driver.execute_query(indexed, g=graph)
                b, _, _ = await gi.driver.execute_query(scanned, g=graph)
                if a[0]["c"] != b[0]["c"]:
                    raise IndexCheckError(f"{graph}: index finds {a[0]['c']}, scan {b[0]['c']}")
        finally:
            await gi.close()  # type: ignore[no-untyped-call]
    if out and not dry_run:
        await _rewrite_aof(settings)
    return out


async def _rewrite_aof(settings: Settings) -> None:
    """Folds the rebuild into a fresh snapshot of the append-only file.

    FalkorDB 6.0.3 records each index drop in the append-only log and cannot
    always replay them: on the next start it stopped with "Diverged applying
    GRAPH.EFFECT ... Unable to drop index on :Community(uuid): no such index"
    and refused to load. A rewrite leaves no drop records to replay.
    """
    async with _redis(settings) as r:
        await r.execute_command("BGREWRITEAOF")  # type: ignore[no-untyped-call]
        for _ in range(600):
            info = await r.info("persistence")
            if not info.get("aof_rewrite_in_progress") and not info.get("aof_rewrite_scheduled"):
                break
            await asyncio.sleep(1)
        info = await r.info("persistence")
        if info.get("aof_last_bgrewrite_status") != "ok":
            raise IndexCheckError(
                "append-only rewrite after the rebuild failed; run BGREWRITEAOF before "
                "FalkorDB restarts"
            )


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        prog="python -m ki_kb.admin", description=__doc__.split("\n\n")[0]
    )
    sub = parser.add_subparsers(dest="command", required=True)
    fg = sub.add_parser("forget-graph", help="delete one graph and its ingest state")
    fg.add_argument("graph")
    fg.add_argument("--dry-run", action="store_true")
    fs = sub.add_parser("forget-stranded", help="delete graphs this instance may no longer touch")
    fs.add_argument("--dry-run", action="store_true")
    ff = sub.add_parser("forget-failures", help="retry documents whose extraction failed")
    ff.add_argument("--dry-run", action="store_true")
    rd = sub.add_parser(
        "remove-duplicates", help="repair documents with copies or a missing episode"
    )
    rd.add_argument("--dry-run", action="store_true")
    ri = sub.add_parser("rebuild-indexes", help="drop and recreate every index (after 4.x to 6.x)")
    ri.add_argument("--dry-run", action="store_true")
    args = parser.parse_args(argv)

    settings = Settings.from_env()
    verb = "would delete" if args.dry_run else "deleted"
    if args.command == "forget-graph":
        count = asyncio.run(forget_graph(settings, args.graph, args.dry_run))
        sys.stdout.write(
            f"{verb} graph {args.graph}; {count} document(s) to re-ingest on the next sync\n"
        )
        return 0
    if args.command == "forget-failures":
        count = asyncio.run(forget_failures(settings, args.dry_run))
        sys.stdout.write(f"{verb} {count} failure(s); the next sync retries those documents\n")
        return 0
    if args.command == "rebuild-indexes":
        rebuilt = asyncio.run(rebuild_indexes(settings, args.dry_run))
        for graph, count in sorted(rebuilt.items()):
            done = "would rebuild" if args.dry_run else "rebuilt and checked"
            sys.stdout.write(f"{graph}: {done} ({count} index drop statement(s))\n")
        return 0
    if args.command == "remove-duplicates":
        repairs = asyncio.run(remove_duplicates(settings, args.dry_run))
        if not repairs:
            sys.stdout.write("no duplicated documents\n")
        for graph, rep in sorted(repairs.items()):
            sys.stdout.write(
                f"{graph}: {verb} {len(rep.empty_copies)} empty copies; "
                f"{len(rep.reingest)} document(s) "
                f"{'would be' if args.dry_run else 'are'} re-ingested on the next sync\n"
            )
        return 0
    found = asyncio.run(forget_stranded(settings, args.dry_run))
    if not found:
        sys.stdout.write("nothing stranded on this instance\n")
    for graph, count in sorted(found.items()):
        sys.stdout.write(f"{verb} stranded graph {graph} ({count} document(s))\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
