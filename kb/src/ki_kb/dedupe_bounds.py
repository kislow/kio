"""Bound the facts Graphiti shows the model when it resolves a new edge.

``resolve_extracted_edges`` runs two searches per new fact, each with
``SearchConfig.limit`` 10 (graphiti-core search_config.py:29,117): one limited
to the fact's two entities for duplicates, one over the whole graph for
contradictions. Both lists go into one ``dedupe_edges.resolve_edge`` prompt, and
the call is skipped only when both are empty. Graphiti has no setting for the
limit, and the recipe it passes is a module constant, so the only seam is the
``search`` name that edge_operations imported: replace it with a wrapper that
sets the limit. kb-query imports ``graphiti_core.search.search`` itself and is
not affected.
"""

from __future__ import annotations

from typing import Any

from graphiti_core.graphiti_types import GraphitiClients
from graphiti_core.search.search import search as _graphiti_search
from graphiti_core.search.search_config import SearchConfig, SearchResults
from graphiti_core.search.search_filters import SearchFilters
from graphiti_core.utils.maintenance import edge_operations


def install(duplicate_limit: int, invalidation_limit: int) -> None:
    """Cap the candidates per new fact; calling it again replaces the caps.

    The duplicate search is the one that carries an endpoint filter. A limit of
    0 skips the search and returns no candidates.
    """

    async def bounded_search(
        clients: GraphitiClients,
        query: str,
        group_ids: list[str] | None,
        config: SearchConfig,
        search_filter: SearchFilters,
        **kwargs: Any,  # noqa: ANN401 -- forwarded to graphiti unchanged
    ) -> SearchResults:
        limit = duplicate_limit if search_filter.edge_source_node_uuids else invalidation_limit
        if limit == 0:
            return SearchResults()
        bounded = config.model_copy(update={"limit": limit})
        return await _graphiti_search(clients, query, group_ids, bounded, search_filter, **kwargs)

    setattr(edge_operations, "search", bounded_search)  # noqa: B010 -- the seam, see module doc
