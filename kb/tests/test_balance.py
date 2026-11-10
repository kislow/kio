"""The balance guard: parsing provider answers, the metric, and what a failure means."""

import json
import logging

import httpx
import pytest
from prometheus_client import REGISTRY

from ki_kb import balance
from ki_kb.settings import Settings

KEY = "sk-balance-test-key"
URL = "https://balance.invalid/user/balance"
BASE_ENV = {
    "KI_ALLOWED_GRAPHS": "cortex",
    "KI_LLM_BASE_URL": "http://llm.invalid/v1",
    "KI_LLM_API_KEY": KEY,
    "KI_LLM_MODEL": "m",
    "KI_EMBEDDING_BASE_URL": "http://embed.invalid/v1",
    "KI_EMBEDDING_MODEL": "e",
    "KI_EMBEDDING_DIM": "8",
    "FALKORDB_HOST": "falkordb.invalid",
    "KI_LLM_MIN_BALANCE": "5",
}
DEEPSEEK = {
    "is_available": True,
    "balance_infos": [
        {"currency": "CNY", "total_balance": "100.00", "topped_up_balance": "100.00"},
        {"currency": "USD", "total_balance": "25.50", "topped_up_balance": "20.00"},
    ],
}


def _settings(**extra: str) -> Settings:
    return Settings.from_env({**BASE_ENV, "KI_LLM_BALANCE_URL": URL, **extra})


def _answer(status: int = 200, body: object = None, seen: list | None = None):
    def handler(request: httpx.Request) -> httpx.Response:
        if seen is not None:
            seen.append(request)
        if isinstance(body, Exception):
            raise body
        text = body if isinstance(body, str) else json.dumps(body)
        return httpx.Response(status, text=text)

    return httpx.MockTransport(handler)


@pytest.mark.parametrize(
    ("data", "want"),
    [
        (DEEPSEEK, 25.5),
        ({"total_balance": "42.0"}, 42.0),
        ({"balance": 9.5}, 9.5),
        ({"balance": 0}, 0.0),
    ],
)
def test_parse_balance(data, want):
    assert balance.parse_balance(data) == want


@pytest.mark.parametrize(
    "data",
    [
        {"balance_infos": [{"currency": "CNY", "total_balance": "100.00"}]},
        {"balance_infos": []},
        {"is_available": True},
        {"balance": "lots"},
        {"balance": "nan"},
        {"balance": True},
        {"balance": None},
        ["not", "an", "object"],
    ],
    ids=["cny-only", "empty", "no-field", "text", "nan", "bool", "null", "list"],
)
def test_parse_balance_rejects_answers_without_a_usd_number(data):
    with pytest.raises(balance.BalanceError):
        balance.parse_balance(data)


async def test_read_balance_sends_the_key_and_exports_the_metric():
    seen: list[httpx.Request] = []
    usd = await balance.read_balance(_settings(), _answer(body=DEEPSEEK, seen=seen))
    assert usd == 25.5
    assert seen[0].headers["authorization"] == f"Bearer {KEY}"
    assert str(seen[0].url) == URL
    assert REGISTRY.get_sample_value("ki_llm_balance_usd", {"provider": "balance.invalid"}) == 25.5


async def test_read_balance_needs_a_url():
    with pytest.raises(balance.BalanceError):
        await balance.read_balance(Settings.from_env(BASE_ENV))


async def test_no_balance_url_means_no_guard():
    assert await balance.balance_low(Settings.from_env(BASE_ENV)) is False


@pytest.mark.parametrize(
    ("usd", "low"), [(10.0, False), (5.0, False), (4.99, True)], ids=["above", "at", "below"]
)
async def test_balance_against_the_floor(usd, low, caplog):
    caplog.set_level(logging.WARNING, logger="ki_kb.balance")
    assert await balance.balance_low(_settings(), _answer(body={"balance": usd})) is low
    assert ("below the floor" in caplog.text) is low


@pytest.mark.parametrize(
    "transport",
    [
        _answer(500, "Internal Server Error"),
        _answer(401, {"error": f"invalid key {KEY}"}),
        _answer(200, "<html>not json</html>"),
        _answer(200, {"balance_infos": [{"currency": "CNY", "total_balance": "1"}]}),
        _answer(body=httpx.ConnectTimeout("timed out")),
        _answer(body=httpx.ConnectError("refused")),
    ],
    ids=["5xx", "401", "not-json", "no-usd", "timeout", "refused"],
)
async def test_an_unreadable_balance_holds_bulk_and_logs_once_without_the_key(transport, caplog):
    caplog.set_level(logging.WARNING, logger="ki_kb.balance")
    assert await balance.balance_low(_settings(), transport) is True
    lines = [r for r in caplog.records if r.name == "ki_kb.balance"]
    assert len(lines) == 1 and lines[0].getMessage() == "LLM balance unreadable, bulk projects wait"
    assert KEY not in caplog.text
    assert all(KEY not in str(v) for r in caplog.records for v in vars(r).values())
