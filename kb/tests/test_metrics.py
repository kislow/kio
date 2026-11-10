"""The metrics the worker, kb-query and the cluster's dashboards agree on."""

from __future__ import annotations

from types import SimpleNamespace

import pytest
from prometheus_client import REGISTRY
from prometheus_client.parser import text_string_to_metric_families

from ki_kb import llm, metrics
from ki_kb.activities import SyncResult, count_outcomes


def _value(name: str, **labels: str) -> float:
    got = REGISTRY.get_sample_value(name, labels)
    return got if got is not None else 0.0


def test_metric_names_and_labels() -> None:
    families = {
        f.name: f
        for f in text_string_to_metric_families(
            __import__("prometheus_client").generate_latest(REGISTRY).decode()
        )
    }
    for name in (
        "ki_llm_tokens",
        "ki_llm_calls",
        "ki_llm_call_seconds",
        "ki_llm_balance_usd",
        "ki_ingest_documents",
        "ki_ingest_document_seconds",
        "ki_ingest_rejections",
        "ki_query_seconds",
        "ki_query_requests",
    ):
        assert name in families, name
    assert metrics.INGEST_OUTCOMES == (
        "added",
        "replaced",
        "removed",
        "rejected",
        "deferred",
        "failed",
    )
    assert metrics.LLM_OUTCOMES == ("ok", "error", "cut_off")


@pytest.mark.parametrize(
    ("url", "want"),
    [
        ("https://api.example.com/v1", "api.example.com"),
        ("http://llm.internal:8080", "llm.internal"),
        ("not a url", "not a url"),
    ],
)
def test_provider_of(url: str, want: str) -> None:
    assert metrics.provider_of(url) == want


def _resp(prompt: int, completion: int, reasoning: int = 0, cached: int = 0, finish: str = "stop"):
    usage = SimpleNamespace(
        prompt_tokens=prompt,
        completion_tokens=completion,
        completion_tokens_details=SimpleNamespace(reasoning_tokens=reasoning),
        prompt_tokens_details=SimpleNamespace(cached_tokens=cached),
    )
    choice = SimpleNamespace(message=SimpleNamespace(content="{}"), finish_reason=finish)
    return SimpleNamespace(usage=usage, choices=[choice])


def test_record_counts_tokens_calls_and_duration() -> None:
    before = {
        kind: _value("ki_llm_tokens_total", provider="p.example", model="m", kind=kind)
        for kind in ("input", "output", "reasoning", "cached")
    }
    ok = _value("ki_llm_calls_total", provider="p.example", model="m", outcome="ok")
    cut = _value("ki_llm_calls_total", provider="p.example", model="m", outcome="cut_off")
    obs = _value("ki_llm_call_seconds_count", provider="p.example", model="m")

    llm.record(_resp(100, 40, reasoning=10, cached=60), "m", 4096, "p.example", 1.5)
    llm.record(_resp(7, 3, finish="length"), "m", 4096, "p.example", 0.2)

    assert (
        _value("ki_llm_tokens_total", provider="p.example", model="m", kind="input")
        == before["input"] + 107
    )
    assert (
        _value("ki_llm_tokens_total", provider="p.example", model="m", kind="output")
        == before["output"] + 43
    )
    assert (
        _value("ki_llm_tokens_total", provider="p.example", model="m", kind="reasoning")
        == before["reasoning"] + 10
    )
    assert (
        _value("ki_llm_tokens_total", provider="p.example", model="m", kind="cached")
        == before["cached"] + 60
    )
    assert _value("ki_llm_calls_total", provider="p.example", model="m", outcome="ok") == ok + 1
    assert (
        _value("ki_llm_calls_total", provider="p.example", model="m", outcome="cut_off") == cut + 1
    )
    assert _value("ki_llm_call_seconds_count", provider="p.example", model="m") == obs + 2


async def test_metered_counts_errors_and_passes_the_provider() -> None:
    from openai import AsyncOpenAI

    client = AsyncOpenAI(api_key="x", base_url="http://llm.invalid/v1")
    calls = {"n": 0}

    async def fake_create(**kwargs: object):
        calls["n"] += 1
        if calls["n"] == 1:
            raise RuntimeError("boom")
        return _resp(5, 2)

    client.chat.completions.create = fake_create  # type: ignore[method-assign]
    wrapped = llm.metered(client, "llm.invalid")
    errors = _value("ki_llm_calls_total", provider="llm.invalid", model="m", outcome="error")
    ok = _value("ki_llm_calls_total", provider="llm.invalid", model="m", outcome="ok")
    with pytest.raises(RuntimeError):
        await wrapped.chat.completions.create(model="m", messages=[])
    await wrapped.chat.completions.create(model="m", messages=[])
    assert (
        _value("ki_llm_calls_total", provider="llm.invalid", model="m", outcome="error")
        == errors + 1
    )
    assert _value("ki_llm_calls_total", provider="llm.invalid", model="m", outcome="ok") == ok + 1


def test_count_outcomes_adds_each_nonzero_count() -> None:
    before = {
        o: _value("ki_ingest_documents_total", project="acme", outcome=o)
        for o in metrics.INGEST_OUTCOMES
    }
    count_outcomes(SyncResult(project="acme", added=3, replaced=1, failed=2))
    assert (
        _value("ki_ingest_documents_total", project="acme", outcome="added") == before["added"] + 3
    )
    assert (
        _value("ki_ingest_documents_total", project="acme", outcome="replaced")
        == before["replaced"] + 1
    )
    assert (
        _value("ki_ingest_documents_total", project="acme", outcome="failed")
        == before["failed"] + 2
    )
    assert (
        _value("ki_ingest_documents_total", project="acme", outcome="removed") == before["removed"]
    )
