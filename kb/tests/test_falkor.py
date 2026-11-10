"""Nested attribute values must reach FalkorDB as JSON strings."""

from __future__ import annotations

import json
from datetime import UTC, datetime

import pytest

from ki_kb.falkor import (
    FlatFalkorDriver,
    FlatFalkorDriverSession,
    avoid_name_index,
    flatten_params,
    flatten_value,
    scope_relation_search,
    searchable_text,
)


def test_primitives_and_flat_lists_pass_through() -> None:
    assert flatten_value("x") == "x"
    assert flatten_value(3) == 3
    assert flatten_value(None) is None
    assert flatten_value([0.1, 0.2]) == [0.1, 0.2]
    assert flatten_value(("a", "b")) == ["a", "b"]


def test_nested_values_become_json_strings() -> None:
    nested = {"vendor": "x", "tags": ["a"]}
    assert json.loads(flatten_value(nested)) == nested
    assert json.loads(flatten_value([{"k": 1}, "s"])) == [{"k": 1}, "s"]


def test_datetimes_are_left_for_the_driver() -> None:
    now = datetime.now(UTC)
    assert flatten_value(now) is now
    # ...but inside a nested value they are serialised with the rest.
    assert json.loads(flatten_value({"at": now}))["at"] == str(now)


def test_flatten_params_handles_bulk_and_single_saves() -> None:
    bulk = {
        "nodes": [
            {"uuid": "1", "name_embedding": [0.1], "stack": {"lang": "go"}},
            {"uuid": "2", "stack": "go"},
        ],
        "uuids": ["1", "2"],
    }
    out = flatten_params(bulk)
    assert out["nodes"][0]["name_embedding"] == [0.1]
    assert json.loads(out["nodes"][0]["stack"]) == {"lang": "go"}
    assert out["nodes"][1] == {"uuid": "2", "stack": "go"}
    assert out["uuids"] == ["1", "2"]

    single = {"props": {"a": [{"b": 1}]}, "name": "n"}
    assert json.loads(flatten_params(single)["props"]["a"]) == [{"b": 1}]
    assert flatten_params(single)["name"] == "n"


class _FakeGraph:
    def __init__(self) -> None:
        self.calls: list[tuple[str, dict[str, object]]] = []

    async def query(self, cypher: str, params: dict[str, object]) -> None:
        self.calls.append((cypher, params))


@pytest.mark.asyncio
async def test_session_flattens_kwargs_and_query_lists() -> None:
    graph = _FakeGraph()
    session = FlatFalkorDriverSession(graph)  # type: ignore[arg-type]

    await session.run("UNWIND $nodes AS n", nodes=[{"uuid": "1", "meta": {"k": "v"}}])
    await session.run([("MERGE (n)", {"props": {"deep": [{"x": 1}]}})])

    assert json.loads(graph.calls[0][1]["nodes"][0]["meta"]) == {"k": "v"}
    assert json.loads(graph.calls[1][1]["props"]["deep"]) == [{"x": 1}]


def test_searchable_text_drops_tokens_without_letters_or_digits() -> None:
    assert searchable_text("Graph names use dashes; Graphiti accepts letters, digits and `_`") == (
        "Graph names use dashes Graphiti accepts letters digits and"
    )
    assert searchable_text("_ -- ___") == ""
    assert searchable_text("v1.2.3 and 10Gi_x") == "v1 2 3 and 10Gi_x"


def test_driver_fulltext_query_has_no_bare_punctuation_tokens() -> None:
    driver = FlatFalkorDriver.__new__(FlatFalkorDriver)  # no connection needed for query building
    q = driver.build_fulltext_query("letters, digits and `_`", ["cortex"])
    assert "| _" not in q and "(_" not in q
    assert q.endswith("(letters | digits)")
    assert driver.build_fulltext_query("_", ["cortex"]) == ""


def test_driver_fulltext_query_carries_no_group_filter() -> None:
    driver = FlatFalkorDriver.__new__(FlatFalkorDriver)
    q = driver.build_fulltext_query("kubernetes upgrade", ["brain-work"])
    assert q.strip() == "(kubernetes | upgrade)"
    assert "@group_id" not in q


def test_name_list_lookup_is_kept_off_the_index() -> None:
    graphiti_lookup = """
            MATCH (n:Entity)
            WHERE n.group_id IN $group_ids
            AND n.name IN $names
            RETURN n.uuid"""
    out = avoid_name_index(graphiti_lookup)
    assert "AND toString(n.name) IN $names" in out
    assert "n.group_id IN $group_ids" in out  # group ids keep their index
    assert avoid_name_index("MATCH (e) WHERE e.name = $name") == "MATCH (e) WHERE e.name = $name"
    assert avoid_name_index("WHERE n.uuid IN $uuids") == "WHERE n.uuid IN $uuids"
    assert avoid_name_index("WHERE n.full_name IN $x") == "WHERE n.full_name IN $x"


@pytest.mark.asyncio
async def test_driver_execute_query_rewrites_name_lookups(monkeypatch: pytest.MonkeyPatch) -> None:
    seen: list[str] = []

    async def fake_execute(self, cypher_query_, **kwargs):  # type: ignore[no-untyped-def]
        seen.append(cypher_query_)
        return [], None, None

    from graphiti_core.driver.falkordb_driver import FalkorDriver

    monkeypatch.setattr(FalkorDriver, "execute_query", fake_execute)
    driver = FlatFalkorDriver.__new__(FlatFalkorDriver)
    await driver.execute_query("MATCH (n:Entity) WHERE n.name IN $names RETURN n", names=["IT"])
    assert seen == ["MATCH (n:Entity) WHERE toString(n.name) IN $names RETURN n"]


def test_relation_search_checks_the_group_on_the_source_entity() -> None:
    vector_search = """
        MATCH (n:Entity)-[e:RELATES_TO]->(m:Entity)
         WHERE e.group_id IN $group_ids
        WITH DISTINCT e, n, m, vec.cosineDistance(e.fact_embedding, vecf32($v)) AS score"""
    out = scope_relation_search(vector_search)
    assert "WHERE n.group_id IN $group_ids" in out
    assert "e.group_id" not in out
    # Only that pattern: a full-text relation search keeps its check on e.
    fulltext = (
        "CALL db.idx.fulltext.queryRelationships('RELATES_TO', $q) "
        "YIELD relationship AS e, score WHERE e.group_id IN $group_ids"
    )
    assert scope_relation_search(fulltext) == fulltext
    assert scope_relation_search("MATCH (n:Entity) WHERE n.group_id IN $group_ids") == (
        "MATCH (n:Entity) WHERE n.group_id IN $group_ids"
    )
