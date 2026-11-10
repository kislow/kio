"""Model usage is logged per call, summed per episode, and truncation is visible."""

from __future__ import annotations

import asyncio
import logging
from types import SimpleNamespace
from typing import Any

import pytest
from graphiti_core import Graphiti
from graphiti_core.errors import NodeNotFoundError
from graphiti_core.nodes import EpisodicNode
from openai import AsyncOpenAI

from ki_kb.graph import MeteredGraphiti
from ki_kb.llm import PromptUsage, Usage, counting, metered, record


def _resp(
    finish: str = "stop",
    content: str | None = '{"a": 1}',
    usage: object = None,
) -> SimpleNamespace:
    return SimpleNamespace(
        choices=[SimpleNamespace(finish_reason=finish, message=SimpleNamespace(content=content))],
        usage=usage,
    )


ANTHROPIC = SimpleNamespace(
    prompt_tokens=1200,
    completion_tokens=300,
    completion_tokens_details=None,
    prompt_tokens_details=None,
)
OPENAI = SimpleNamespace(
    prompt_tokens=100,
    completion_tokens=50,
    completion_tokens_details=SimpleNamespace(reasoning_tokens=20),
    prompt_tokens_details=SimpleNamespace(cached_tokens=40),
)
DEEPSEEK = SimpleNamespace(prompt_tokens=10, completion_tokens=5, prompt_cache_hit_tokens=7)


def test_counting_sums_calls_including_tasks_started_inside() -> None:
    async def call(resp: SimpleNamespace) -> None:
        record(resp, "m", 100)

    async def run() -> Usage:
        with counting() as usage:
            record(_resp(usage=ANTHROPIC), "m", 100)
            # gather runs each call in its own task, as Graphiti's extraction does.
            await asyncio.gather(call(_resp(usage=OPENAI)), call(_resp(usage=DEEPSEEK)))
        record(_resp(usage=ANTHROPIC), "m", 100)
        return usage

    assert asyncio.run(run()) == Usage(
        calls=3,
        input_tokens=1310,
        output_tokens=355,
        reasoning_tokens=20,
        cached_tokens=47,
        truncated=0,
        prompts={"unknown": PromptUsage(calls=3, input_tokens=1310)},
    )


def test_record_logs_usage_at_debug_without_text(caplog: pytest.LogCaptureFixture) -> None:
    caplog.set_level(logging.DEBUG, logger="ki_kb.llm")
    record(_resp(content='{"secret": "document text"}', usage=ANTHROPIC), "claude-x", 32768)
    (rec,) = caplog.records
    assert rec.levelno == logging.DEBUG and rec.getMessage() == "model call"
    assert vars(rec)["model"] == "claude-x"
    assert vars(rec)["finish_reason"] == "stop"
    assert vars(rec)["max_tokens"] == 32768
    assert vars(rec)["input_tokens"] == 1200 and vars(rec)["output_tokens"] == 300
    assert vars(rec)["answer_chars"] == len('{"secret": "document text"}')
    assert "document text" not in str(vars(rec))


def test_truncated_answer_warns_and_is_counted(caplog: pytest.LogCaptureFixture) -> None:
    caplog.set_level(logging.WARNING, logger="ki_kb.llm")
    with counting() as usage:
        record(_resp(finish="length", content='{"a": "unterminated', usage=ANTHROPIC), "m", 16384)
    (rec,) = caplog.records
    assert rec.getMessage() == "model answer cut off by max_tokens"
    assert vars(rec)["finish_reason"] == "length" and vars(rec)["max_tokens"] == 16384
    assert usage.truncated == 1


def test_record_tolerates_a_response_without_usage_or_choices() -> None:
    with counting() as usage:
        record(SimpleNamespace(), None, None)
        record(_resp(content=None), None, None)
    assert usage == Usage(calls=2, prompts={"unknown": PromptUsage(calls=2)})


async def test_metered_records_the_request_as_sent() -> None:
    client = AsyncOpenAI(api_key="x", base_url="http://llm.invalid/v1")

    async def fake_create(**kwargs: object) -> SimpleNamespace:
        return _resp(finish="length", usage=ANTHROPIC)

    client.chat.completions.create = fake_create  # type: ignore[method-assign]
    with counting() as usage:
        resp = await metered(client).chat.completions.create(model="m", messages=[], max_tokens=9)
    assert resp.choices[0].finish_reason == "length"
    assert usage.calls == 1 and usage.truncated == 1 and usage.output_tokens == 300


def _graphiti(monkeypatch: pytest.MonkeyPatch, fail: bool) -> MeteredGraphiti:
    async def add_episode(self: Graphiti, *a: object, **k: object) -> SimpleNamespace:
        record(_resp(usage=ANTHROPIC), "m", 1)
        record(_resp(usage=ANTHROPIC), "m", 1)
        if fail:
            raise ValueError("bad answer with document text")
        return SimpleNamespace(episode=SimpleNamespace(uuid="u1"))

    async def add_episode_bulk(self: Graphiti, eps: list[object], **k: object) -> SimpleNamespace:
        record(_resp(usage=OPENAI), "m", 1)
        if fail:
            raise ValueError("bad")
        return SimpleNamespace(episodes=[SimpleNamespace(uuid="u1"), SimpleNamespace(uuid="u2")])

    monkeypatch.setattr(Graphiti, "add_episode", add_episode)
    monkeypatch.setattr(Graphiti, "add_episode_bulk", add_episode_bulk)
    g = MeteredGraphiti.__new__(MeteredGraphiti)
    g._usage_fields = {"graph": "brain-x", "model": "claude-x"}
    g.driver = SimpleNamespace()
    return g


def _usage_lines(caplog: pytest.LogCaptureFixture) -> list[dict[str, object]]:
    return [vars(r) for r in caplog.records if r.getMessage() == "episode model usage"]


async def test_one_info_line_per_episode(
    monkeypatch: pytest.MonkeyPatch, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.INFO, logger="ki_kb.graph")
    g = _graphiti(monkeypatch, fail=False)
    res = await g.add_episode(name="secret/path.md", episode_body="document text")
    assert res.episode.uuid == "u1"
    (line,) = _usage_lines(caplog)
    assert line["levelname"] == "INFO"
    assert line["graph"] == "brain-x" and line["model"] == "claude-x"
    assert line["episodes"] == 1 and line["episode_uuids"] == ["u1"]
    assert line["calls"] == 2 and line["input_tokens"] == 2400 and line["output_tokens"] == 600
    assert "secret/path.md" not in str(line) and "document text" not in str(line)


async def test_bulk_and_failures_are_metered_too(
    monkeypatch: pytest.MonkeyPatch, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.INFO, logger="ki_kb.graph")
    ok = _graphiti(monkeypatch, fail=False)
    await ok.add_episode_bulk([object(), object()], group_id="brain-x")
    bad = _graphiti(monkeypatch, fail=True)
    with pytest.raises(ValueError, match="bad"):
        await bad.add_episode(name="n")
    with pytest.raises(ValueError, match="bad"):
        await bad.add_episode_bulk([object()])
    bulk, failed, failed_bulk = _usage_lines(caplog)
    assert bulk["episodes"] == 2 and bulk["episode_uuids"] == ["u1", "u2"]
    assert bulk["calls"] == 1 and bulk["cached_tokens"] == 40 and bulk["reasoning_tokens"] == 20
    assert failed["error"] == "ValueError" and failed["calls"] == 2
    assert "document text" not in str(failed)
    assert failed_bulk["episodes"] == 1 and failed_bulk["error"] == "ValueError"


async def test_calls_and_input_tokens_are_split_by_prompt(monkeypatch: pytest.MonkeyPatch) -> None:
    from graphiti_core.llm_client.openai_generic_client import OpenAIGenericClient

    from ki_kb import metrics
    from ki_kb.llm import SchemaEchoClient

    async def fake(self: OpenAIGenericClient, *a: object, **k: object) -> dict[str, object]:
        record(_resp(usage=ANTHROPIC), "m", 1)
        return {}

    monkeypatch.setattr(OpenAIGenericClient, "generate_response", fake)
    client = SchemaEchoClient.__new__(SchemaEchoClient)
    client.max_tokens = 100
    before = metrics.LLM_PROMPT_CALLS.labels("dedupe_edges.resolve_edge")._value.get()
    with counting() as usage:
        await client.generate_response([], None, prompt_name="dedupe_edges.resolve_edge")
        await client.generate_response([], None, prompt_name="dedupe_edges.resolve_edge")
        await client.generate_response([], None, prompt_name="extract_edges.edge")
        record(_resp(usage=OPENAI), "m", 1)
    assert usage.prompts == {
        "dedupe_edges.resolve_edge": PromptUsage(calls=2, input_tokens=2400),
        "extract_edges.edge": PromptUsage(calls=1, input_tokens=1200),
        "unknown": PromptUsage(calls=1, input_tokens=100),
    }
    assert metrics.LLM_PROMPT_CALLS.labels("dedupe_edges.resolve_edge")._value.get() == before + 2
    assert metrics.LLM_PROMPT_INPUT_TOKENS.labels("extract_edges.edge")._value.get() >= 1200


async def test_episode_line_carries_the_prompt_split(
    monkeypatch: pytest.MonkeyPatch, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.INFO, logger="ki_kb.graph")
    g = _graphiti(monkeypatch, fail=False)
    await g.add_episode_bulk([object(), object()])  # type: ignore[list-item]
    (line,) = _usage_lines(caplog)
    assert line["prompts"] == {"unknown": {"calls": 1, "input_tokens": 100}}
    assert line["calls_per_episode"] == 0.5 and line["input_tokens_per_episode"] == 50


SUMMARY_PROMPT = "extract_nodes.extract_entity_summaries_from_episodes"


def _removing_graphiti(
    monkeypatch: pytest.MonkeyPatch, calls: int, fail: bool = False, exists: bool = True
) -> Any:
    from ki_kb.llm import _PROMPT

    async def get_by_uuid(driver: object, uuid: str) -> SimpleNamespace:
        if not exists:
            raise NodeNotFoundError(uuid)
        return SimpleNamespace(uuid=uuid)

    monkeypatch.setattr(EpisodicNode, "get_by_uuid", get_by_uuid)

    async def remove_episode(self: Graphiti, episode_uuid: str) -> None:
        # Graphiti rebuilds the summaries of the removed episode's entities.
        for _ in range(calls):
            token = _PROMPT.set(SUMMARY_PROMPT)
            try:
                record(_resp(usage=ANTHROPIC), "m", 1)
            finally:
                _PROMPT.reset(token)
        if fail:
            raise ValueError("bad")

    monkeypatch.setattr(Graphiti, "remove_episode", remove_episode)
    g = MeteredGraphiti.__new__(MeteredGraphiti)
    g._usage_fields = {"graph": "brain-x", "model": "claude-x"}
    g.driver = SimpleNamespace()
    return g


async def test_removal_logs_the_summary_rebuild_it_pays_for(
    monkeypatch: pytest.MonkeyPatch, caplog: pytest.LogCaptureFixture
) -> None:
    from ki_kb import metrics

    caplog.set_level(logging.INFO, logger="ki_kb.graph")
    g = _graphiti(monkeypatch, fail=False)
    _removing_graphiti(monkeypatch, calls=3)
    counter = metrics.LLM_TOKENS.labels("unknown", "m", "input")
    before = counter._value.get()
    await g.add_episode_bulk([object(), object()])
    await g.remove_episode("old-uuid")
    bulk, removal = _usage_lines(caplog)
    assert removal["removed_episode"] == "old-uuid" and "episode_uuids" not in removal
    assert removal["prompts"] == {SUMMARY_PROMPT: {"calls": 3, "input_tokens": 3600}}
    assert removal["input_tokens"] == 3600 and "error" not in removal
    # Every model token of the sync shows in some line: the lines sum to the counter.
    assert bulk["input_tokens"] + removal["input_tokens"] == counter._value.get() - before


async def test_removal_without_model_calls_logs_nothing(
    monkeypatch: pytest.MonkeyPatch, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.INFO, logger="ki_kb.graph")
    await _removing_graphiti(monkeypatch, calls=0).remove_episode("old-uuid")
    assert _usage_lines(caplog) == []


async def test_failed_removal_still_logs_what_it_spent(
    monkeypatch: pytest.MonkeyPatch, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.INFO, logger="ki_kb.graph")
    g = _removing_graphiti(monkeypatch, calls=1, fail=True)
    with pytest.raises(ValueError, match="bad"):
        await g.remove_episode("old-uuid")
    (line,) = _usage_lines(caplog)
    assert line["error"] == "ValueError" and line["calls"] == 1


async def test_removing_an_episode_that_is_already_gone_is_a_no_op(
    monkeypatch: pytest.MonkeyPatch, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.INFO, logger="ki_kb.graph")
    g = _removing_graphiti(monkeypatch, calls=3, exists=False)
    with counting() as usage:
        await g.remove_episode("old-uuid")
    assert usage.calls == 0 and _usage_lines(caplog) == []
    (rec,) = [r for r in caplog.records if r.getMessage() == "episode already removed"]
    assert rec.levelno == logging.INFO
    assert vars(rec)["graph"] == "brain-x" and vars(rec)["removed_episode"] == "old-uuid"
