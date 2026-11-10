"""Graphiti's edge searches run with the configured candidate limits."""

from __future__ import annotations

from collections.abc import Iterator

import pytest
from graphiti_core.search.search_config import SearchResults
from graphiti_core.search.search_config_recipes import EDGE_HYBRID_SEARCH_RRF
from graphiti_core.search.search_filters import SearchFilters
from graphiti_core.utils.maintenance import edge_operations

from ki_kb import dedupe_bounds

PAIR = SearchFilters(edge_source_node_uuids=["a", "b"], edge_target_node_uuids=["a", "b"])
ANY = SearchFilters()


@pytest.fixture(autouse=True)
def restore_search() -> Iterator[None]:
    yield
    setattr(edge_operations, "search", dedupe_bounds._graphiti_search)  # noqa: B010


def _record(monkeypatch: pytest.MonkeyPatch) -> list[int]:
    limits: list[int] = []

    async def fake(
        clients: object, query: str, group_ids: object, config: object, flt: object, **k: object
    ) -> SearchResults:
        limits.append(config.limit)  # type: ignore[attr-defined]
        return SearchResults()

    monkeypatch.setattr(dedupe_bounds, "_graphiti_search", fake)
    return limits


async def test_each_search_gets_its_own_limit(monkeypatch: pytest.MonkeyPatch) -> None:
    limits = _record(monkeypatch)
    dedupe_bounds.install(4, 2)
    await edge_operations.search(  # type: ignore[attr-defined]
        None, "q", ["g"], EDGE_HYBRID_SEARCH_RRF, PAIR
    )
    await edge_operations.search(  # type: ignore[attr-defined]
        None, "q", ["g"], EDGE_HYBRID_SEARCH_RRF, ANY
    )
    assert limits == [4, 2]
    assert EDGE_HYBRID_SEARCH_RRF.limit == 10, "the shared recipe is not modified"


async def test_zero_skips_the_search(monkeypatch: pytest.MonkeyPatch) -> None:
    limits = _record(monkeypatch)
    dedupe_bounds.install(4, 0)
    res = await edge_operations.search(  # type: ignore[attr-defined]
        None, "q", ["g"], EDGE_HYBRID_SEARCH_RRF, ANY
    )
    assert res.edges == [] and limits == []


async def test_install_again_replaces_the_limits(monkeypatch: pytest.MonkeyPatch) -> None:
    limits = _record(monkeypatch)
    dedupe_bounds.install(4, 2)
    dedupe_bounds.install(7, 2)
    await edge_operations.search(  # type: ignore[attr-defined]
        None, "q", ["g"], EDGE_HYBRID_SEARCH_RRF, PAIR
    )
    assert limits == [7]
