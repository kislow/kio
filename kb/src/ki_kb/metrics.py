"""Prometheus metrics: what extraction costs and how fast the knowledge base answers.

Everything is registered on the default registry at import time, so the
worker serves it from a small HTTP server (``serve``) and kb-query mounts it
under ``/metrics`` (``asgi_app``). Labels carry providers, models, projects,
tools and stages, never a query, a document or a brain name.
"""

from __future__ import annotations

from urllib.parse import urlparse

from prometheus_client import Counter, Gauge, Histogram, make_asgi_app, start_http_server
from starlette.types import ASGIApp

LLM_TOKENS = Counter(
    "ki_llm_tokens",
    "Tokens billed by the extraction provider, by kind.",
    ["provider", "model", "kind"],
)
LLM_CALLS = Counter(
    "ki_llm_calls",
    "Chat completions sent to the extraction provider, by outcome.",
    ["provider", "model", "outcome"],
)
LLM_PROMPT_CALLS = Counter(
    "ki_llm_prompt_calls",
    "Chat completions sent to the extraction provider, by Graphiti prompt name.",
    ["prompt"],
)
LLM_PROMPT_INPUT_TOKENS = Counter(
    "ki_llm_prompt_input_tokens",
    "Input tokens sent to the extraction provider, by Graphiti prompt name.",
    ["prompt"],
)
LLM_SECONDS = Histogram(
    "ki_llm_call_seconds",
    "Wall time of one chat completion.",
    ["provider", "model"],
    buckets=(0.25, 0.5, 1, 2, 5, 10, 20, 40, 60, 120),
)
# Labelled by provider so no series exists until the guard has read a balance:
# an unlabelled gauge would report 0 from start-up, or forever with the guard off.
LLM_BALANCE = Gauge(
    "ki_llm_balance_usd",
    "Extraction provider account balance in USD, read once per sync run.",
    ["provider"],
)
INGEST_DOCUMENTS = Counter(
    "ki_ingest_documents",
    "Documents handled by sync runs, by outcome.",
    ["project", "outcome"],
)
INGEST_REJECTIONS = Counter(
    "ki_ingest_rejections",
    "Sections the scanner rejected, by project and rule kind.",
    ["project", "rule"],
)
# A skipped project does not fail the run, so this is the only signal that a
# repo has stopped syncing (a revoked token skips it on every tick).
SYNC_SKIPPED = Counter(
    "ki_sync_skipped",
    "Projects left out of a sync run, by reason.",
    ["project", "reason"],
)
INGEST_SECONDS = Histogram(
    "ki_ingest_document_seconds",
    "Wall time to extract one document into its graph.",
    ["project"],
    buckets=(5, 10, 20, 30, 45, 60, 90, 120, 180, 300),
)
QUERY_SECONDS = Histogram(
    "ki_query_seconds",
    "Wall time of one kb-query request, whole and per stage.",
    ["tool", "stage"],
    buckets=(0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10),
)
QUERY_REQUESTS = Counter(
    "ki_query_requests",
    "kb-query requests, by HTTP status.",
    ["tool", "status"],
)

LLM_OUTCOMES = ("ok", "error", "cut_off")
INGEST_OUTCOMES = ("added", "replaced", "removed", "rejected", "deferred", "failed")


def provider_of(base_url: str) -> str:
    """The provider label for a base URL: its host, or the URL itself if it has none."""
    host = urlparse(base_url).hostname
    return host or base_url


def serve(port: int) -> None:
    """Serve the default registry on every interface; 0 means do not serve."""
    if port > 0:
        start_http_server(port)


def asgi_app() -> ASGIApp:
    """The registry as an ASGI app, for mounting under /metrics."""
    return make_asgi_app()
