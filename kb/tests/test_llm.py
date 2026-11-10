"""Schema echoes from the extraction model are unwrapped, validated and retried."""

from __future__ import annotations

from collections.abc import Iterator
from types import SimpleNamespace

import pytest
from graphiti_core.llm_client.openai_generic_client import OpenAIGenericClient
from graphiti_core.prompts.models import Message
from openai import AsyncOpenAI
from pydantic import BaseModel, ValidationError

from ki_kb.llm import (
    JSON_ONLY,
    SchemaEchoClient,
    first_json,
    prompt_only,
    repair_shapes,
    unwrap_schema_echo,
    without_thinking,
)


class EdgeDuplicate(BaseModel):
    duplicate_facts: list[int]
    contradicted_facts: list[int]


def test_unwrap_takes_properties_when_no_field_is_present() -> None:
    echo: dict[str, object] = {"properties": {"duplicate_facts": [1], "contradicted_facts": []}}
    assert unwrap_schema_echo(echo, EdgeDuplicate) == {
        "duplicate_facts": [1],
        "contradicted_facts": [],
    }


def test_unwrap_leaves_a_real_answer_alone() -> None:
    real: dict[str, object] = {"duplicate_facts": [], "contradicted_facts": [2], "properties": "x"}
    assert unwrap_schema_echo(real, EdgeDuplicate) is real


class Entities(BaseModel):
    extracted_entities: list[str]
    note: str | None = None


@pytest.mark.parametrize(
    ("answer", "model", "want"),
    [
        ([{"duplicate_facts": [1], "contradicted_facts": []}], EdgeDuplicate, None),
        (["a", "b"], Entities, {"extracted_entities": ["a", "b"]}),
        ([{"name": "a"}], Entities, {"extracted_entities": [{"name": "a"}]}),
        ([{"duplicate_facts": []}, {"contradicted_facts": []}], EdgeDuplicate, "same"),
        ([], EdgeDuplicate, "same"),
        ("text", EdgeDuplicate, "same"),
        (None, EdgeDuplicate, "same"),
    ],
)
def test_unwrap_handles_answers_that_are_not_objects(
    answer: object, model: type[BaseModel], want: object
) -> None:
    got = unwrap_schema_echo(answer, model)
    if want == "same":
        assert got is answer
    elif want is None:
        assert got == answer[0]  # type: ignore[index]
    else:
        assert got == want


def _client(
    answers: list[object],
    calls: list[list[Message]],
    monkeypatch: pytest.MonkeyPatch,
    budgets: list[object] | None = None,
) -> SchemaEchoClient:
    it: Iterator[object] = iter(answers)

    async def fake(
        self: OpenAIGenericClient,
        messages: list[Message],
        response_model: object = None,
        max_tokens: object = None,
        *a: object,
        **k: object,
    ) -> object:
        calls.append(messages)
        if budgets is not None:
            budgets.append(max_tokens)
        return next(it)

    monkeypatch.setattr(OpenAIGenericClient, "generate_response", fake)
    client = SchemaEchoClient.__new__(SchemaEchoClient)
    client.max_tokens = 32768
    return client


@pytest.mark.asyncio
async def test_array_answer_is_asked_again_not_a_crash(monkeypatch: pytest.MonkeyPatch) -> None:
    calls: list[list[Message]] = []
    client = _client(
        [[{"duplicate_facts": [1]}, {"x": 2}], {"duplicate_facts": [], "contradicted_facts": []}],
        calls,
        monkeypatch,
    )
    out = await client.generate_response([Message(role="user", content="q")], EdgeDuplicate)
    assert out == {"duplicate_facts": [], "contradicted_facts": []}
    assert len(calls) == 2
    assert "root" in calls[1][-1].content


@pytest.mark.asyncio
async def test_bare_list_for_a_list_model_is_accepted(monkeypatch: pytest.MonkeyPatch) -> None:
    calls: list[list[Message]] = []
    client = _client([["a", "b"]], calls, monkeypatch)
    out = await client.generate_response([Message(role="user", content="q")], Entities)
    assert out == {"extracted_entities": ["a", "b"]}
    assert len(calls) == 1


@pytest.mark.asyncio
@pytest.mark.parametrize(("asked", "sent"), [(None, 32768), (16384, 32768), (65536, 65536)])
async def test_configured_max_tokens_is_the_floor(
    monkeypatch: pytest.MonkeyPatch, asked: int | None, sent: int
) -> None:
    budgets: list[object] = []
    answer = {"duplicate_facts": [], "contradicted_facts": []}
    client = _client([answer, {"free": "form"}], [], monkeypatch, budgets)
    msgs = [Message(role="user", content="q")]
    await client.generate_response(msgs, EdgeDuplicate, max_tokens=asked)
    await client.generate_response(msgs, None, max_tokens=asked)
    assert budgets == [sent, sent]


@pytest.mark.asyncio
async def test_echo_is_unwrapped_without_a_retry(monkeypatch: pytest.MonkeyPatch) -> None:
    calls: list[list[Message]] = []
    client = _client(
        [{"properties": {"duplicate_facts": [1], "contradicted_facts": []}}], calls, monkeypatch
    )
    out = await client.generate_response([Message(role="user", content="q")], EdgeDuplicate)
    assert out == {"duplicate_facts": [1], "contradicted_facts": []}
    assert len(calls) == 1


@pytest.mark.asyncio
async def test_invalid_answer_is_asked_again_with_the_errors(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls: list[list[Message]] = []
    client = _client(
        [{"duplicate_facts": {"type": "array"}}, {"duplicate_facts": [], "contradicted_facts": []}],
        calls,
        monkeypatch,
    )
    out = await client.generate_response([Message(role="user", content="q")], EdgeDuplicate)
    assert out == {"duplicate_facts": [], "contradicted_facts": []}
    assert len(calls) == 2
    assert calls[1][-1].role == "user" and "did not match" in calls[1][-1].content
    assert "contradicted_facts" in calls[1][-1].content


@pytest.mark.asyncio
async def test_gives_up_after_three_attempts(monkeypatch: pytest.MonkeyPatch) -> None:
    calls: list[list[Message]] = []
    client = _client([{"nope": 1}] * 3, calls, monkeypatch)
    with pytest.raises(ValidationError):
        await client.generate_response([Message(role="user", content="q")], EdgeDuplicate)
    assert len(calls) == 3


@pytest.mark.asyncio
async def test_no_model_means_no_validation(monkeypatch: pytest.MonkeyPatch) -> None:
    calls: list[list[Message]] = []
    client = _client([{"anything": True}], calls, monkeypatch)
    assert await client.generate_response([Message(role="user", content="q")]) == {"anything": True}


@pytest.mark.asyncio
async def test_without_thinking_adds_the_switch_and_keeps_extra_body() -> None:
    client = AsyncOpenAI(api_key="x", base_url="http://llm.invalid/v1")
    seen: dict[str, object] = {}

    async def fake_create(*args: object, **kwargs: object) -> str:
        seen.update(kwargs)
        return "ok"

    client.chat.completions.create = fake_create  # type: ignore[method-assign]
    assert without_thinking(client) is client
    result = await client.chat.completions.create(model="m", messages=[], extra_body={"keep": 1})
    assert result == "ok"
    assert seen["model"] == "m"
    assert seen["extra_body"] == {"keep": 1, "thinking": {"type": "disabled"}}


@pytest.mark.asyncio
async def test_without_thinking_works_with_no_extra_body() -> None:
    client = AsyncOpenAI(api_key="x", base_url="http://llm.invalid/v1")
    seen: dict[str, object] = {}

    async def fake_create(**kwargs: object) -> None:
        seen.update(kwargs)

    client.chat.completions.create = fake_create  # type: ignore[method-assign]
    await without_thinking(client).chat.completions.create(model="m", messages=[])
    assert seen["extra_body"] == {"thinking": {"type": "disabled"}}


@pytest.mark.parametrize(
    ("text", "want"),
    [
        ('{"a": 1}', '{"a": 1}'),
        ('```json\n{"a": 1}\n```', '{"a": 1}'),
        ('{"a": 1}\n\nHere is why I chose that.', '{"a": 1}'),
        ('{"a": 1}\n{"b": 2}', '{"a": 1}'),
        ("Sure! [1, 2]", "[1, 2]"),
        ("no json here", None),
        ('{"a": ', None),
    ],
)
def test_first_json(text: str, want: str | None) -> None:
    assert first_json(text) == want


class _Msg:
    def __init__(self, content: str | None) -> None:
        self.content = content


class _Resp:
    def __init__(self, *contents: str | None) -> None:
        self.choices = [SimpleNamespace(message=_Msg(c)) for c in contents]


async def test_prompt_only_drops_format_asks_for_json_and_trims_the_answer() -> None:
    client = AsyncOpenAI(api_key="x", base_url="http://llm.invalid/v1")
    seen: dict[str, object] = {}

    async def fake_create(**kwargs: object) -> _Resp:
        seen.update(kwargs)
        return _Resp('```json\n{"entities": []}\n```\nI found none.', None)

    client.chat.completions.create = fake_create  # type: ignore[method-assign]
    messages = [{"role": "system", "content": "s"}, {"role": "user", "content": "extract"}]
    resp = await prompt_only(client).chat.completions.create(
        model="m",
        messages=messages,
        response_format={"type": "json_object"},
        temperature=0,
        max_tokens=5,
    )
    assert "response_format" not in seen and "temperature" not in seen
    assert seen["max_tokens"] == 5
    sent = seen["messages"]
    assert isinstance(sent, list)
    assert sent[1]["content"] == "extract" + JSON_ONLY and sent[0]["content"] == "s"
    assert messages[1]["content"] == "extract", "the caller's messages are untouched"
    assert resp.choices[0].message.content == '{"entities": []}'
    assert resp.choices[1].message.content is None


class _Shapes(BaseModel):
    ids: list[int]
    note: str
    when: str | None = None
    tags: list[str] | None = None


@pytest.mark.parametrize(
    ("raw", "want"),
    [
        ({"ids": 3, "note": "x"}, {"ids": [3], "note": "x"}),
        ({"ids": None, "note": "x"}, {"ids": [], "note": "x"}),
        ({"ids": [1], "note": 42}, {"ids": [1], "note": "42"}),
        ({"ids": [1], "note": ["a", "b"]}, {"ids": [1], "note": "a, b"}),
        (
            {"ids": [1], "note": "x", "when": {"date": "2026-10-01"}},
            {"ids": [1], "note": "x", "when": None},
        ),
        ({"ids": [1], "note": "x", "tags": "infra"}, {"ids": [1], "note": "x", "tags": ["infra"]}),
        ({"ids": [1], "note": "x", "when": None}, {"ids": [1], "note": "x", "when": None}),
        ({"ids": [1], "note": True}, {"ids": [1], "note": True}),
        (["not", "a", "dict"], ["not", "a", "dict"]),
    ],
)
def test_repair_shapes(raw: object, want: object) -> None:
    assert repair_shapes(raw, _Shapes) == want


def test_repair_shapes_makes_graphiti_answers_validate() -> None:
    from graphiti_core.prompts.dedupe_edges import EdgeDuplicate
    from graphiti_core.prompts.extract_edges import EdgeTimestamps

    EdgeDuplicate.model_validate(
        repair_shapes({"duplicate_facts": 2, "contradicted_facts": None}, EdgeDuplicate)
    )
    EdgeTimestamps.model_validate(
        repair_shapes({"valid_at": {"year": 2026}, "invalid_at": None}, EdgeTimestamps)
    )


@pytest.mark.parametrize(
    "raw",
    [
        ["1", " 2 "],
        [1.0, 2.0],
        [{"idx": 1, "fact": "x"}, {"index": "2"}],
        [None, 1, 2],
    ],
)
def test_index_lists_are_coerced_so_they_do_not_cost_a_retry(raw: list[object]) -> None:
    from graphiti_core.prompts.dedupe_edges import EdgeDuplicate as Graphiti

    answer = repair_shapes({"duplicate_facts": raw, "contradicted_facts": []}, Graphiti)
    assert Graphiti.model_validate(answer).duplicate_facts in ([1, 2], [1], [2])


def test_unreadable_index_items_are_left_for_the_retry() -> None:
    answer = repair_shapes({"ids": [1, "two", 1.5, True, {"x": 1}], "note": "n"}, _Shapes)
    assert answer == {"ids": [1, "two", 1.5, True, {"x": 1}], "note": "n"}


@pytest.mark.asyncio
async def test_int_type_answer_costs_no_second_call(monkeypatch: pytest.MonkeyPatch) -> None:
    calls: list[list[Message]] = []
    client = _client(
        [{"duplicate_facts": [{"idx": 0}], "contradicted_facts": [None]}], calls, monkeypatch
    )
    out = await client.generate_response([Message(role="user", content="q")], EdgeDuplicate)
    assert out == {"duplicate_facts": [0], "contradicted_facts": []}
    assert len(calls) == 1
