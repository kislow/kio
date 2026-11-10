"""FalkorDB driver with two fixes graphiti-core 0.31 lacks.

Writes: graphiti-core (0.29.3 up to at least 0.31) spreads every extracted
entity attribute into its own node property. FalkorDB, like Neo4j, accepts
only primitives and arrays of primitives, so a single nested value from the
extraction model aborts the whole episode (getzep/graphiti#1885). Until the
upstream fix lands, nested values are serialised to JSON strings here, the
way graphiti's Kuzu driver already does.

Full-text search: graphiti turns the search text into ``word | word | ...``
after replacing punctuation with spaces, but keeps tokens such as ``_`` that
contain no letter or digit. RediSearch rejects those with a syntax error, and
one such token in a fact ("letters, digits and `_`") fails the whole episode
during edge deduplication. Tokens without a letter or digit are dropped here.

Full-text group filter: dropped from the search string, see
``FlatFalkorDriver.build_fulltext_query``.

Relation searches: on FalkorDB 6.0.3, ``MATCH (n:Entity)-[e:RELATES_TO]->(m:Entity)
WHERE e.group_id IN $group_ids`` is planned as a scan of every entity with a
relation index lookup for each one: 9.6 s for one vector search on a graph of
13,000 entities and 40,000 relations, 71 s under ingest load, which exhausted
the worker's connections. With the group check on the source entity the plan
is an entity index scan and a traverse: 0.13 s, same rows. graphiti gives a
relation the group of its entities, so the check is the same; see
``scope_relation_search``.

Name lookups: FalkorDB 4.x crashes the whole server (SIGSEGV in
Index_BuildQueryTree) when an ``IN`` list over an indexed string property
holds a stopword such as ``it``, ``to`` or ``the`` (FalkorDB/FalkorDB#3348,
fixed in 6.0). A label's range and full-text indexes share one search index,
so the stopword list graphiti gives the Entity full-text index applies to
``name`` too. graphiti's ``EntityNode.get_by_names`` sends ``n.name IN $names``
for every extracted entity, so one entity called "IT" took the database down
on each retry. The lookup is rewritten to ``toString(n.name) IN $names``,
which the planner does not serve from the index; the group_id index still
narrows the scan. Measured on a 7,000-entity graph: 2 to 4 ms. Other ``IN``
lists hold uuids and group ids, never stopwords.
"""

from __future__ import annotations

import json
import re
from collections.abc import Mapping
from typing import Any

from graphiti_core.driver.driver import GraphDriverSession
from graphiti_core.driver.falkordb.fulltext import sanitize_falkor_fulltext_query
from graphiti_core.driver.falkordb_driver import FalkorDriver, FalkorDriverSession

_HAS_ALNUM = re.compile(r"[A-Za-z0-9]")
_RELATION_PATTERN = "MATCH (n:Entity)-[e:RELATES_TO]->(m:Entity)"
_EDGE_GROUP_FILTER = re.compile(r"\be\.group_id\s+IN\s+\$group_ids\b")
_NAME_IN_LIST = re.compile(r"\b([A-Za-z_][A-Za-z0-9_]*)\.name\s+IN\s+\$")


def _is_primitive(value: object) -> bool:
    return value is None or isinstance(value, str | int | float | bool)


def flatten_value(value: object) -> object:
    """One property value.

    Primitives and flat lists pass through. Maps, and lists with anything but
    primitives in them, become JSON strings. Other objects (datetimes) are left
    for the driver, which converts them itself.
    """
    if _is_primitive(value):
        return value
    if isinstance(value, list | tuple):
        items = list(value)
        if all(_is_primitive(item) for item in items):
            return items
        return json.dumps(items, default=str)
    if isinstance(value, dict):
        return json.dumps(value, default=str)
    return value


def searchable_text(query: str) -> str:
    """The search text with every token that has no letter or digit removed."""
    words = sanitize_falkor_fulltext_query(query).split()
    return " ".join(word for word in words if _HAS_ALNUM.search(word))


def avoid_name_index(cypher: str) -> str:
    """``x.name IN $param`` becomes ``toString(x.name) IN $param``.

    Keeps the lookup off FalkorDB's range index, which crashes on stopword
    names (see the module docstring).
    """
    return _NAME_IN_LIST.sub(r"toString(\1.name) IN $", cypher)


def scope_relation_search(cypher: str) -> str:
    """In a relation search, the group check moves from the relation to its
    source entity (see the module docstring for the plan it avoids)."""
    if _RELATION_PATTERN not in cypher:
        return cypher
    return _EDGE_GROUP_FILTER.sub("n.group_id IN $group_ids", cypher)


def flatten_properties(props: Mapping[str, object]) -> dict[str, object]:
    return {key: flatten_value(value) for key, value in props.items()}


def flatten_params(params: Mapping[str, object]) -> dict[str, object]:
    """Query parameters as graphiti passes them.

    A bulk write passes a list of property maps (``nodes``, ``entity_edges``),
    a single save passes the properties themselves; both are flattened one
    level below the parameter name.
    """
    out: dict[str, object] = {}
    for key, value in params.items():
        if isinstance(value, dict):
            out[key] = flatten_properties(value)
        elif isinstance(value, list) and value and all(isinstance(item, dict) for item in value):
            out[key] = [flatten_properties(item) for item in value]
        else:
            out[key] = flatten_value(value)
    return out


class FlatFalkorDriverSession(FalkorDriverSession):
    """A graphiti FalkorDB session whose writes never carry nested values."""

    async def run(self, query: str | list, **kwargs: object) -> None:  # type: ignore[type-arg]
        if isinstance(query, list):
            flat = [(cypher, flatten_params(params or {})) for cypher, params in query]
            await super().run(flat)
        else:
            await super().run(query, **flatten_params(kwargs))


class FlatFalkorDriver(FalkorDriver):
    """FalkorDriver with flat writes, clean search text and name lookups off the index."""

    def session(self, database: str | None = None) -> GraphDriverSession:
        return FlatFalkorDriverSession(self._get_graph(database))

    # Any: graphiti's own parameters and result tuple pass through unchanged.
    async def execute_query(self, cypher_query_: str, **kwargs: Any) -> Any:  # noqa: ANN401
        cypher = scope_relation_search(avoid_name_index(cypher_query_))
        return await super().execute_query(cypher, **kwargs)

    def build_fulltext_query(
        self, query: str, group_ids: list[str] | None = None, max_query_length: int = 128
    ) -> str:
        # No group filter inside the search string: FalkorDB 6 matches
        # graphiti's ``(@group_id:"brain\-work")`` against nothing, so every
        # brain with a dash in its name found nothing by text. A graph is one
        # brain here, and graphiti's Cypher still filters ``group_id IN
        # $group_ids`` after the search, so the filter in the string is redundant.
        return super().build_fulltext_query(searchable_text(query), None, max_query_length)
