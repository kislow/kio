"""Runtime settings from environment variables, validated with pydantic.

The worker and kb-query run the same code with different settings.
KI_ALLOWED_GRAPHS is the hard boundary: a pod refuses any graph not listed,
so a brain missing from the deployment fails instead of being created ad hoc.
An entry may be a family, "brain-client-*", covering "brain-client-<sub>".

Model providers are never defaulted, and neither are the FalkorDB host or
(for workers) the task queue: every deployment names them explicitly, so
nothing silently falls back to a provider nobody chose.
"""

from __future__ import annotations

import os
from collections.abc import Mapping
from pathlib import Path
from typing import Annotated, Literal
from urllib.parse import urlparse

from pydantic import (
    BaseModel,
    ConfigDict,
    Field,
    StringConstraints,
    ValidationError,
    ValidationInfo,
    field_validator,
)

from .brains import matches_any, valid_entry
from .errors import ConfigError, GraphNotAllowedError
from .peak import PeakSchedule, parse_peak_windows

# How structured answers are requested from the extraction model:
#   json_schema  native response_format with the schema (OpenAI-style servers)
#   json_object  JSON mode; graphiti puts the schema in the prompt (DeepSeek)
#   prompt       no response_format at all; schema in the prompt, answer
#                validated by SchemaEchoClient (Anthropic's OpenAI-compatible
#                endpoint, which only accepts strict schemas)
StructuredMode = Literal["json_schema", "json_object", "prompt"]
Required = Annotated[str, StringConstraints(strip_whitespace=True, min_length=1)]

# Environment variable for each field. Fields without a default are required.
_ENV = {
    "falkordb_host": "FALKORDB_HOST",
    "falkordb_port": "FALKORDB_PORT",
    "falkordb_password": "FALKORDB_PASSWORD",
    "falkordb_max_connections": "KI_FALKORDB_MAX_CONNECTIONS",
    "allowed_graphs": "KI_ALLOWED_GRAPHS",
    "llm_base_url": "KI_LLM_BASE_URL",
    "llm_api_key": "KI_LLM_API_KEY",
    "llm_model": "KI_LLM_MODEL",
    "llm_small_model": "KI_LLM_SMALL_MODEL",
    "llm_structured_mode": "KI_LLM_STRUCTURED_MODE",
    "llm_max_tokens": "KI_LLM_MAX_TOKENS",
    "llm_thinking": "KI_LLM_THINKING",
    "llm_peak_utc": "KI_LLM_PEAK_UTC",
    "ingest_bulk_threshold": "KI_INGEST_BULK_THRESHOLD",
    "llm_balance_url": "KI_LLM_BALANCE_URL",
    "llm_min_balance": "KI_LLM_MIN_BALANCE",
    "llm_balance_check_interval_minutes": "KI_LLM_BALANCE_CHECK_INTERVAL_MINUTES",
    "ingest_quiet_minutes": "KI_INGEST_QUIET_MINUTES",
    "ingest_batch": "KI_INGEST_BATCH",
    "edge_dedupe_candidates": "KI_EDGE_DEDUPE_CANDIDATES",
    "edge_invalidation_candidates": "KI_EDGE_INVALIDATION_CANDIDATES",
    "ingest_rate_limit_attempts": "KI_INGEST_RATE_LIMIT_ATTEMPTS",
    "ingest_rate_limit_max_wait_seconds": "KI_INGEST_RATE_LIMIT_MAX_WAIT_SECONDS",
    "sync_every_minutes": "KI_SYNC_EVERY_MINUTES",
    "sync_timeout_minutes": "KI_SYNC_TIMEOUT_MINUTES",
    "sync_bulk_timeout_minutes": "KI_SYNC_BULK_TIMEOUT_MINUTES",
    "sync_timeout_ceiling_minutes": "KI_SYNC_TIMEOUT_CEILING_MINUTES",
    "llm_timeout_seconds": "KI_LLM_TIMEOUT_SECONDS",
    "embedding_base_url": "KI_EMBEDDING_BASE_URL",
    "embedding_api_key": "KI_EMBEDDING_API_KEY",
    "embedding_model": "KI_EMBEDDING_MODEL",
    "embedding_dim": "KI_EMBEDDING_DIM",
    "aliases_file": "KI_ALIASES_FILE",
    "temporal_address": "TEMPORAL_ADDRESS",
    "temporal_namespace": "TEMPORAL_NAMESPACE",
    "task_queue": "KI_TASK_QUEUE",
    "metrics_port": "KI_METRICS_PORT",
    "projects_file": "KI_PROJECTS_FILE",
    "git_tokens_dir": "KI_GIT_TOKENS_DIR",
}


class Settings(BaseModel):
    model_config = ConfigDict(frozen=True, extra="forbid")

    falkordb_host: Required
    falkordb_port: int = Field(6379, ge=1, le=65535)
    falkordb_password: str | None = None
    # One pool for every graph lane of a sync. Graphiti's SEMAPHORE_LIMIT (10
    # on the worker) caps each gather, and a bulk resolve nests a second gather
    # inside the first, so one lane can hold 100 queries. Three lanes at once
    # (brains work, voice, security) need 300. A separate pool of 64 per
    # graph raised MaxConnectionsError once those lanes ran side by side.
    # The pool waits for a free connection instead of failing the batch.
    falkordb_max_connections: int = Field(300, ge=1, le=1024)
    allowed_graphs: frozenset[str]
    # Extraction model (any OpenAI-compatible endpoint: DeepSeek, Qwen, vLLM, Ollama).
    llm_base_url: Required
    llm_api_key: str | None = None
    llm_model: Required
    llm_small_model: str | None = None
    llm_structured_mode: StructuredMode = "json_schema"
    llm_max_tokens: int = Field(8192, ge=256)
    # False asks a DeepSeek reasoning model not to think before answering
    # (``thinking: {"type": "disabled"}`` on every chat completion). Extraction
    # is forty short structured calls per document; the reasoning cost about
    # three times the answer and is billed as output. Leave True for endpoints
    # that do not know the field.
    llm_thinking: bool = True
    # Recurring UTC windows in which the provider bills more, such as
    # "Mon-Fri 01:00-04:00,06:00-10:00" (see peak.py); bulk projects wait them
    # out. Provider-neutral and empty by default: no windows.
    llm_peak_utc: str = ""
    # A project with more documents than this waiting for extraction is bulk and
    # waits for off-peak and a sufficient balance; fewer go through at any hour.
    # 25 is under two batches, about five minutes at the 5.3 documents a minute
    # measured on 2026-10-25, and under a dollar at $0.03 a document.
    ingest_bulk_threshold: int = Field(25, ge=1)
    # Optional balance endpoint (DeepSeek: https://api.deepseek.com/user/balance),
    # called with llm_api_key once per sync run. Unset turns the guard off.
    llm_balance_url: str | None = None
    # Bulk projects wait while the balance is below this many USD. One sync
    # attempt may run for four hours, so the worker rechecks between batches.
    llm_min_balance: float = Field(5.0, ge=0.0)
    # In a bulk sync, recheck between batches no more often than this interval.
    llm_balance_check_interval_minutes: int = Field(10, ge=1)
    # A document committed less than this many minutes ago is left for a later
    # run, so a note pushed three times in an hour is extracted once, after the
    # editing stops. 0 ingests everything at once.
    ingest_quiet_minutes: int = Field(30, ge=0)
    # Documents per Graphiti add_episode_bulk call within one graph. 1 adds them
    # one at a time with add_episode, the order Graphiti recommends; larger
    # batches extract in parallel and deduplicate across the batch in one pass.
    # Documents per Graphiti bulk call within one graph. 16 measured best on
    # 2026-10-25 (5.3 documents a minute against 3 at 8, and fewer input tokens
    # per document); a default of 1 meant a config that lost the line silently
    # fell back to one document at a time. See docs/operations.md, "Bulk ingest".
    ingest_batch: int = Field(16, ge=1, le=32)
    # Facts of the same two entities shown to the model with each new fact, and
    # facts of any entities shown to check for contradictions. Graphiti shows up
    # to 10 of each, so on a populated graph every new fact costs one call with
    # 20 facts in the prompt. With the second at 0 a fact whose entities share no
    # fact yet needs no call at all (see dedupe_bounds.py).
    edge_dedupe_candidates: int = Field(5, ge=1, le=50)
    edge_invalidation_candidates: int = Field(3, ge=0, le=50)
    # Rate-limited bulk requests retry within one activity attempt. Each sleep
    # is capped so the activity can keep heartbeating between retries.
    ingest_rate_limit_attempts: int = Field(5, ge=0)
    ingest_rate_limit_max_wait_seconds: float = Field(600, ge=0)
    # StartToClose of one sync_repo attempt. A stuck attempt is still caught by
    # the 5-minute heartbeat, so a long limit only lets progress continue.
    sync_timeout_minutes: int = Field(45, ge=1)
    # How often the schedule starts SyncProjects; 0 is manual (see schedule.py).
    sync_every_minutes: int = Field(5, ge=0)
    # The same for a project flagged `bulk: true`, whose load runs for hours.
    # Declared after the ceiling it is checked against (see _check_bulk_timeout).
    sync_timeout_ceiling_minutes: int = Field(360, ge=1)
    sync_bulk_timeout_minutes: int = Field(240, ge=1)
    # Applies to every model call, extraction and embeddings alike.
    llm_timeout_seconds: float = Field(120, gt=0, le=900)
    # Embedding model. Must match between ingest and query, or search breaks.
    embedding_base_url: Required
    embedding_api_key: str | None = None
    embedding_model: Required
    embedding_dim: int = Field(ge=1)
    aliases_file: Path | None = None
    temporal_address: str = "temporal-frontend.temporal.svc.cluster.local:7233"
    temporal_namespace: str = "default"
    # Required by the worker (see worker.run); kb-query has no queue.
    task_queue: str = ""
    # The worker serves Prometheus metrics here (ki_llm_*, ki_ingest_*); 0 disables.
    metrics_port: int = Field(9464, ge=0, le=65535)
    projects_file: Path | None = None
    git_tokens_dir: Path = Path("/etc/ki/git-tokens")

    @field_validator("allowed_graphs", mode="before")
    @classmethod
    def _split_graphs(cls, value: object) -> frozenset[str]:
        if not isinstance(value, str):
            raise ValueError("must be a comma-separated list")  # noqa: TRY004 -- pydantic expects ValueError
        graphs = frozenset(v.strip() for v in value.split(",") if v.strip())
        if not graphs:
            raise ValueError("must list the graphs this instance may touch")
        bad = sorted(g for g in graphs if not valid_entry(g))
        if bad:
            raise ValueError(f"has invalid entries: {', '.join(bad)}")
        return graphs

    @field_validator("llm_peak_utc")
    @classmethod
    def _check_peak_windows(cls, value: str) -> str:
        parse_peak_windows(value)  # its ValueError names the bad part; _describe adds the variable
        return value

    @field_validator("sync_bulk_timeout_minutes")
    @classmethod
    def _check_bulk_timeout(cls, value: int, info: ValidationInfo) -> int:
        ceiling = info.data.get("sync_timeout_ceiling_minutes")
        if ceiling is not None and value > ceiling:
            raise ValueError(f"must not exceed KI_SYNC_TIMEOUT_CEILING_MINUTES ({ceiling})")
        return value

    @field_validator("llm_balance_url")
    @classmethod
    def _check_balance_url(cls, value: str | None) -> str | None:
        if value is None:
            return None
        parsed = urlparse(value)
        if parsed.scheme not in ("http", "https") or not parsed.hostname:
            raise ValueError("must be an http(s) URL")
        return value

    @classmethod
    def from_env(cls, env: Mapping[str, str] | None = None) -> Settings:
        env = os.environ if env is None else env
        raw = {field: env[var] for field, var in _ENV.items() if env.get(var, "").strip()}
        try:
            return cls.model_validate(raw)
        except ValidationError as err:
            raise ConfigError(_describe(err)) from None

    def sync_timeout_seconds(self, *, bulk: bool) -> int:
        """StartToClose for one project's sync attempt, never above the ceiling."""
        minutes = self.sync_bulk_timeout_minutes if bulk else self.sync_timeout_minutes
        return min(minutes, self.sync_timeout_ceiling_minutes) * 60

    @property
    def small_model(self) -> str:
        return self.llm_small_model or self.llm_model

    @property
    def peak_schedule(self) -> PeakSchedule:
        """KI_LLM_PEAK_UTC parsed; validated at start-up, so this cannot fail."""
        return parse_peak_windows(self.llm_peak_utc)

    def check_graph(self, graph: str) -> None:
        if not matches_any(self.allowed_graphs, graph):
            raise GraphNotAllowedError(f"graph {graph!r} is not allowed on this instance")

    def git_token(self, credential: str) -> str:
        """Read-only token for one GitHub org or account, mounted from a secret."""
        path = self.git_tokens_dir / credential
        try:
            token = path.read_text(encoding="utf-8").strip()
        except FileNotFoundError:
            raise ConfigError(
                f"no git token for credential {credential!r} on this instance"
            ) from None
        if not token:
            raise ConfigError(f"git token for credential {credential!r} is empty")
        return token


def _describe(err: ValidationError) -> str:
    """Name the environment variables at fault; never echo their values."""
    parts = []
    for e in err.errors():
        field = str(e["loc"][0]) if e["loc"] else ""
        var = _ENV.get(field, field)
        reason = "must be set" if e["type"] == "missing" else e["msg"].removeprefix("Value error, ")
        parts.append(f"{var} {reason}")
    return "; ".join(parts)
