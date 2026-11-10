"""Balance guard: the extraction provider's account balance, read during bulk work.

Optional: only when KI_LLM_BALANCE_URL is set. SyncProjects reads it at the
start of a run and bulk ingest rechecks it between batches with the existing
LLM API key and a short timeout. Every reading is exported as ``ki_llm_balance_usd``.
A balance below
KI_LLM_MIN_BALANCE, or any failure to read it, holds bulk projects back; small
changes keep flowing either way. The key is sent as a bearer token and never
logged.
"""

from __future__ import annotations

import logging
import math

import httpx

from . import metrics
from .errors import AppError
from .settings import Settings

logger = logging.getLogger(__name__)

# The guard runs inside load_projects, whose activity has 30 seconds in all.
TIMEOUT_SECONDS = 5.0


class BalanceError(AppError, ValueError):
    """The balance endpoint answered with something that holds no USD balance."""


def _usd(value: object) -> float:
    if isinstance(value, bool) or not isinstance(value, str | int | float):
        raise BalanceError("balance is not a number")
    try:
        usd = float(value)
    except ValueError:
        raise BalanceError("balance is not a number") from None
    if not math.isfinite(usd):
        raise BalanceError("balance is not a finite number")
    return usd


def parse_balance(data: object) -> float:
    """The USD balance in a provider's JSON answer.

    DeepSeek: ``{"balance_infos": [{"currency": "USD", "total_balance": "10.00"}, ...]}``;
    an account funded only in another currency has no USD entry and is an error.
    Other providers: a top-level ``total_balance`` or ``balance``.
    """
    if not isinstance(data, dict):
        raise BalanceError("balance answer is not a JSON object")
    infos = data.get("balance_infos")
    if isinstance(infos, list):
        for info in infos:
            if isinstance(info, dict) and info.get("currency") == "USD":
                return _usd(info.get("total_balance"))
        raise BalanceError("balance answer has no USD entry")
    for key in ("total_balance", "balance"):
        if key in data:
            return _usd(data[key])
    raise BalanceError("balance answer has no balance field")


async def read_balance(
    settings: Settings, transport: httpx.AsyncBaseTransport | None = None
) -> float:
    """Fetch the balance in USD and export it as ki_llm_balance_usd."""
    if not settings.llm_balance_url:
        raise BalanceError("KI_LLM_BALANCE_URL is not set")
    headers = {"Accept": "application/json"}
    if settings.llm_api_key:
        headers["Authorization"] = f"Bearer {settings.llm_api_key}"
    async with httpx.AsyncClient(timeout=TIMEOUT_SECONDS, transport=transport) as client:
        resp = await client.get(settings.llm_balance_url, headers=headers)
        resp.raise_for_status()
        usd = parse_balance(resp.json())
    metrics.LLM_BALANCE.labels(metrics.provider_of(settings.llm_balance_url)).set(usd)
    return usd


async def balance_low(
    settings: Settings, transport: httpx.AsyncBaseTransport | None = None
) -> bool:
    """True when bulk work must wait for money: below the floor, or unreadable.

    False when no balance URL is configured. Logs one line per call, by error
    type only: the request carries the API key and the answer is the provider's.
    """
    if not settings.llm_balance_url:
        return False
    try:
        usd = await read_balance(settings, transport)
    except (httpx.HTTPError, ValueError) as err:  # ValueError covers bad JSON and BalanceError
        logger.warning(
            "LLM balance unreadable, bulk projects wait", extra={"error": type(err).__name__}
        )
        return True
    if usd < settings.llm_min_balance:
        logger.warning(
            "LLM balance below the floor, bulk projects wait",
            extra={"balance_usd": usd, "floor_usd": settings.llm_min_balance},
        )
        return True
    return False
