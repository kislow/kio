"""Extraction client that survives schema echoes from json_object providers.

In ``json_object`` mode graphiti embeds the JSON schema in the prompt and
trusts whatever comes back. DeepSeek sometimes answers with the schema's
shape instead of its values, for example ``{"properties": {"duplicate_facts":
[]}}``, and graphiti then builds its response model straight from that dict
and fails with a ValidationError that aborts the whole episode
(getzep/graphiti#1885 calls these "schema echoes"). This client unwraps that
shape, validates the answer against the requested model, and asks again,
with the validation errors, before giving up.

``metered`` logs the finish reason and token usage of every completion and
sums them per episode inside ``counting`` (graph.MeteredGraphiti opens one
around each add_episode call), so ingest cost and cut-off answers are visible.
"""

from __future__ import annotations

import json
import logging
import re
import time
import types
from collections.abc import Iterator, Mapping, Sequence
from contextlib import contextmanager
from contextvars import ContextVar
from dataclasses import dataclass, field
from typing import Union, cast, get_args, get_origin

from graphiti_core.llm_client import LLMConfig
from graphiti_core.llm_client.config import ModelSize
from graphiti_core.llm_client.openai_generic_client import OpenAIGenericClient
from graphiti_core.prompts.models import Message
from openai import AsyncOpenAI
from pydantic import BaseModel, ValidationError

from . import metrics

log = logging.getLogger(__name__)

ATTEMPTS = 3

# DeepSeek's switch for its reasoning models; other OpenAI-compatible servers
# ignore unknown fields or are never given it (Settings.llm_thinking).
NO_THINKING: dict[str, object] = {"thinking": {"type": "disabled"}}


JSON_ONLY = (
    "\n\nReply with only the JSON object: nothing before it, nothing after it, no code fences."
)


def first_json(text: str) -> str | None:
    """The first JSON value in text, re-serialised, or None when there is none.

    Models asked for JSON without a server-side format sometimes wrap it in a
    code fence, explain it afterwards, or send two objects. Graphiti strips a
    leading fence but still calls ``json.loads`` on the whole body.
    """
    stripped = text.strip()
    if stripped.startswith("```"):
        stripped = re.sub(r"^```[a-zA-Z0-9_-]*[ \t]*\r?\n?", "", stripped)
    starts = [i for i in (stripped.find("{"), stripped.find("[")) if i >= 0]
    if not starts:
        return None
    try:
        value, _ = json.JSONDecoder().raw_decode(stripped[min(starts) :])
    except json.JSONDecodeError:
        return None
    return json.dumps(value)


def prompt_only(client: AsyncOpenAI) -> AsyncOpenAI:
    """Make this client work with endpoints that offer no JSON mode.

    For every chat completion: drop ``response_format`` (Anthropic's
    OpenAI-compatible API accepts only strict schemas, which Graphiti's models
    are not) and ``temperature`` (Claude 5.5 models refuse it as deprecated;
    Graphiti always sends 0), tell the model in the last message to answer with JSON alone,
    and reduce the answer to its first JSON value so Graphiti's ``json.loads``
    sees nothing else. Graphiti still puts the schema in the prompt (its
    ``json_object`` mode) and SchemaEchoClient validates the result.
    """
    create = client.chat.completions.create

    async def create_prompt_only(
        *args: object,
        messages: Sequence[Mapping[str, object]] = (),
        response_format: object = None,
        temperature: object = None,
        **kwargs: object,
    ) -> object:
        del response_format, temperature
        msgs = [dict(m) for m in messages]
        for m in reversed(msgs):
            if m.get("role") == "user" and isinstance(m.get("content"), str):
                m["content"] = str(m["content"]) + JSON_ONLY
                break
        resp = await create(*args, messages=msgs, **kwargs)  # type: ignore[call-overload]
        for choice in getattr(resp, "choices", None) or []:
            msg = getattr(choice, "message", None)
            if msg is None:
                continue
            content = getattr(msg, "content", None)
            if isinstance(content, str) and (only := first_json(content)) is not None:
                msg.content = only
        return resp

    client.chat.completions.create = create_prompt_only  # type: ignore[method-assign,assignment]
    return client


def without_thinking(client: AsyncOpenAI) -> AsyncOpenAI:
    """Make every chat completion from this client skip the model's reasoning.

    Graphiti's client calls ``chat.completions.create`` itself and passes no
    ``extra_body``, so the switch is attached to the HTTP client instead of to
    each call. Anything a caller does put in ``extra_body`` is kept.
    """
    create = client.chat.completions.create

    async def create_without_thinking(
        *args: object, extra_body: Mapping[str, object] | None = None, **kwargs: object
    ) -> object:
        body = {**(extra_body or {}), **NO_THINKING}
        return await create(*args, extra_body=body, **kwargs)  # type: ignore[call-overload]

    client.chat.completions.create = create_without_thinking  # type: ignore[method-assign,assignment]
    return client


def unwrap_schema_echo(response: object, model: type[BaseModel]) -> object:
    """Return the payload the model asked for, unwrapped from a schema-shaped answer.

    An array answer is unwrapped only where that is unambiguous: one object in
    an array, or the bare list for a model whose only required field is a list
    (``[...]`` for ``{"edges": [...]}``). Anything else comes back unchanged and
    fails validation, so it is asked again.
    """
    fields = set(model.model_fields)
    if isinstance(response, list):
        if len(response) == 1 and isinstance(response[0], dict) and fields & set(response[0]):
            return unwrap_schema_echo(response[0], model)
        required = [n for n, f in model.model_fields.items() if f.is_required()]
        if len(required) == 1 and get_origin(model.model_fields[required[0]].annotation) is list:
            return {required[0]: response}
        return response
    if not isinstance(response, dict) or fields & set(response):
        return response
    inner = response.get("properties")
    if isinstance(inner, dict):
        return {str(k): v for k, v in inner.items()}
    return response


def _accepts(annotation: object) -> tuple[bool, bool, bool]:
    """(list, str, None) a field annotation accepts, looking through Optional."""
    origin = get_origin(annotation)
    members = get_args(annotation) if origin in (Union, types.UnionType) else (annotation,)
    wants_list = any(get_origin(m) is list or m is list for m in members)
    wants_str = str in members
    wants_none = type(None) in members
    return wants_list, wants_str, wants_none


def _wants_int_list(annotation: object) -> bool:
    origin = get_origin(annotation)
    members = get_args(annotation) if origin in (Union, types.UnionType) else (annotation,)
    return any(get_origin(m) is list and get_args(m) == (int,) for m in members)


def _as_int(item: object) -> object:
    """The index a model meant by item, or item itself when it is not clear.

    Models answer an index list with ``"3"``, ``3.0`` or ``{"idx": 3, ...}``
    (pydantic's ``int_type`` on the second attempt at EdgeDuplicate).
    """
    if isinstance(item, bool):
        return item
    if isinstance(item, float) and item.is_integer():
        return int(item)
    if isinstance(item, str) and re.fullmatch(r"\s*-?\d+\s*", item):
        return int(item)
    if isinstance(item, dict):
        for key in ("idx", "index", "id"):
            if key in item:
                return _as_int(item[key])
    return item


def repair_shapes(answer: object, model: type[BaseModel]) -> object:
    """Fix the shape slips a model makes on top-level fields, before validation.

    Smaller models answer a one-item list field with the bare item, a missing
    list with null, and a text field with a number, a list of words or an
    object. Graphiti then fails the whole document after three re-asks (two of
    six postmortems on 2026-10-24). Only unambiguous repairs: a scalar into a
    one-item list, null into an empty list, a number into its text, a list of
    scalars into comma-separated text, an object into null where the field is
    optional, and the items of an index list into integers (``"2"``, ``2.0``,
    ``{"idx": 2}``; nulls are dropped). Anything else is left for validation and
    the re-ask.
    """
    if not isinstance(answer, dict):
        return answer
    fixed = dict(answer)
    for name, spec in model.model_fields.items():
        if name not in fixed:
            continue
        value = fixed[name]
        wants_list, wants_str, wants_none = _accepts(spec.annotation)
        if wants_list and not isinstance(value, list):
            if value is None:
                fixed[name] = []
            elif not isinstance(value, dict) or not wants_none:
                fixed[name] = [value]
        elif wants_str and not isinstance(value, str) and value is not None:
            if isinstance(value, (int, float)) and not isinstance(value, bool):
                fixed[name] = str(value)
            elif isinstance(value, list) and all(isinstance(v, (str, int, float)) for v in value):
                fixed[name] = ", ".join(str(v) for v in value)
            elif isinstance(value, dict) and wants_none:
                fixed[name] = None
        if _wants_int_list(spec.annotation) and isinstance(fixed[name], list):
            fixed[name] = [_as_int(v) for v in fixed[name] if v is not None]
    return fixed


@dataclass
class PromptUsage:
    calls: int = 0
    input_tokens: int = 0


@dataclass
class Usage:
    """Token counts summed over model calls; zero where the endpoint reports nothing.

    ``prompts`` splits calls and input tokens by Graphiti prompt name, retries
    included, so the prompt that drives the cost shows in the episode log line.
    """

    calls: int = 0
    input_tokens: int = 0
    output_tokens: int = 0
    reasoning_tokens: int = 0
    cached_tokens: int = 0
    truncated: int = 0
    prompts: dict[str, PromptUsage] = field(default_factory=dict)

    def add(self, other: Usage) -> None:
        self.calls += other.calls
        self.input_tokens += other.input_tokens
        self.output_tokens += other.output_tokens
        self.reasoning_tokens += other.reasoning_tokens
        self.cached_tokens += other.cached_tokens
        self.truncated += other.truncated
        for name, used in other.prompts.items():
            mine = self.prompts.setdefault(name, PromptUsage())
            mine.calls += used.calls
            mine.input_tokens += used.input_tokens


_USAGE: ContextVar[Usage | None] = ContextVar("ki_llm_usage", default=None)
# Graphiti's prompt name for the call in flight, set by SchemaEchoClient: the
# OpenAI client underneath only sees messages, not which prompt built them.
_PROMPT: ContextVar[str] = ContextVar("ki_llm_prompt", default="unknown")


@contextmanager
def counting() -> Iterator[Usage]:
    """Sum the usage of every model call made inside the block.

    Tasks started inside the block copy the context and so share the Usage,
    which is how Graphiti's parallel extraction calls are counted too.
    """
    usage = Usage()
    token = _USAGE.set(usage)
    try:
        yield usage
    finally:
        _USAGE.reset(token)


def _count(value: object) -> int:
    return value if isinstance(value, int) else 0


def record(
    resp: object,
    model: object,
    max_tokens: object,
    provider: str = "unknown",
    seconds: float | None = None,
) -> None:
    """Log one completion's finish reason and usage, add it to the current Usage,
    and count it in the Prometheus metrics (tokens by kind, the call's outcome
    and, when given, its duration).

    Anthropic's endpoint counts hidden reasoning in ``completion_tokens`` and
    leaves the details empty; DeepSeek and OpenAI report reasoning and cache
    hits separately. ``answer_chars`` against ``output_tokens`` shows how much
    of the budget went to reasoning. No message text is logged.
    """
    usage = getattr(resp, "usage", None)
    choice = next(iter(getattr(resp, "choices", None) or []), None)
    content = getattr(getattr(choice, "message", None), "content", None)
    finish = getattr(choice, "finish_reason", None)
    call = Usage(
        calls=1,
        input_tokens=_count(getattr(usage, "prompt_tokens", None)),
        output_tokens=_count(getattr(usage, "completion_tokens", None)),
        reasoning_tokens=_count(
            getattr(getattr(usage, "completion_tokens_details", None), "reasoning_tokens", None)
        ),
        cached_tokens=_count(
            getattr(getattr(usage, "prompt_tokens_details", None), "cached_tokens", None)
        )
        or _count(getattr(usage, "prompt_cache_hit_tokens", None)),
        truncated=int(finish == "length"),
    )
    prompt = _PROMPT.get()
    call.prompts[prompt] = PromptUsage(calls=1, input_tokens=call.input_tokens)
    fields = {
        "model": model,
        "finish_reason": finish,
        "max_tokens": max_tokens,
        "input_tokens": call.input_tokens,
        "output_tokens": call.output_tokens,
        "reasoning_tokens": call.reasoning_tokens,
        "cached_tokens": call.cached_tokens,
        "answer_chars": len(content) if isinstance(content, str) else 0,
        "prompt": prompt,
    }
    log.debug("model call", extra=fields)
    if call.truncated:
        log.warning("model answer cut off by max_tokens", extra=fields)
    if (total := _USAGE.get()) is not None:
        total.add(call)
    name = str(model or "unknown")
    for kind, count in (
        ("input", call.input_tokens),
        ("output", call.output_tokens),
        ("reasoning", call.reasoning_tokens),
        ("cached", call.cached_tokens),
    ):
        if count:
            metrics.LLM_TOKENS.labels(provider, name, kind).inc(count)
    metrics.LLM_CALLS.labels(provider, name, "cut_off" if call.truncated else "ok").inc()
    metrics.LLM_PROMPT_CALLS.labels(prompt).inc()
    metrics.LLM_PROMPT_INPUT_TOKENS.labels(prompt).inc(call.input_tokens)
    if seconds is not None:
        metrics.LLM_SECONDS.labels(provider, name).observe(seconds)


def metered(client: AsyncOpenAI, provider: str = "unknown") -> AsyncOpenAI:
    """Record the finish reason, usage and duration of every chat completion
    from this client; a failed call counts as an error for the provider.

    Wrap the client first, so it sees the request as sent and the answer as
    received, before prompt_only trims it.
    """
    create = client.chat.completions.create

    async def create_metered(*args: object, **kwargs: object) -> object:
        started = time.monotonic()
        try:
            resp = await create(*args, **kwargs)  # type: ignore[call-overload]
        except Exception:
            metrics.LLM_CALLS.labels(provider, str(kwargs.get("model") or "unknown"), "error").inc()
            raise
        record(
            resp,
            kwargs.get("model"),
            kwargs.get("max_tokens"),
            provider,
            time.monotonic() - started,
        )
        return resp

    client.chat.completions.create = create_metered  # type: ignore[method-assign,assignment]
    return client


class SchemaEchoClient(OpenAIGenericClient):
    """OpenAIGenericClient whose structured answers always validate."""

    def __init__(
        self,
        config: LLMConfig,
        client: AsyncOpenAI,
        max_tokens: int,
        structured_output_mode: str,
    ) -> None:
        super().__init__(
            config=config,
            client=client,
            max_tokens=max_tokens,
            structured_output_mode=structured_output_mode,  # type: ignore[arg-type]
        )

    async def generate_response(
        self,
        messages: list[Message],
        response_model: type[BaseModel] | None = None,
        max_tokens: int | None = None,
        model_size: ModelSize = ModelSize.medium,
        group_id: str | None = None,
        prompt_name: str | None = None,
        *,
        attribute_extraction: bool = False,
        model: str | None = None,
    ) -> dict[str, object]:
        token = _PROMPT.set(prompt_name or "unknown")
        try:
            return await self._generate(
                messages,
                response_model,
                max_tokens,
                model_size,
                group_id,
                prompt_name,
                attribute_extraction=attribute_extraction,
                model=model,
            )
        finally:
            _PROMPT.reset(token)

    async def _generate(
        self,
        messages: list[Message],
        response_model: type[BaseModel] | None,
        max_tokens: int | None,
        model_size: ModelSize,
        group_id: str | None,
        prompt_name: str | None,
        *,
        attribute_extraction: bool,
        model: str | None,
    ) -> dict[str, object]:
        # Graphiti caps edge extraction at 16384 tokens and node dedupe at
        # min(max_tokens, 16384) whatever the client allows, and Claude 5 models
        # spend part of max_tokens on hidden reasoning: long answers were cut off
        # mid-JSON. The configured budget is the floor for every call.
        max_tokens = max(max_tokens or 0, self.max_tokens)
        if response_model is None:
            return await super().generate_response(
                messages,
                None,
                max_tokens,
                model_size,
                group_id,
                prompt_name,
                attribute_extraction=attribute_extraction,
                model=model,
            )
        last: ValidationError | None = None
        for attempt in range(1, ATTEMPTS + 1):
            # The parent appends the schema and language hints to the messages
            # in place, so every attempt works on its own copy.
            copies = [m.model_copy() for m in messages]
            raw = await super().generate_response(
                copies,
                response_model,
                max_tokens,
                model_size,
                group_id,
                prompt_name,
                attribute_extraction=attribute_extraction,
                model=model,
            )
            answer = repair_shapes(unwrap_schema_echo(raw, response_model), response_model)
            try:
                response_model.model_validate(answer)
            except ValidationError as err:
                last = err
                log.warning(
                    "structured answer did not validate",
                    extra={
                        "prompt": prompt_name,
                        "model": response_model.__name__,
                        "attempt": attempt,
                        "errors": [e["type"] for e in err.errors()],
                    },
                )
                messages = [
                    *messages,
                    Message(
                        role="user",
                        content=(
                            "Your previous answer did not match the required format: "
                            + "; ".join(
                                f"{'.'.join(str(p) for p in e['loc']) or 'root'}: {e['msg']}"
                                for e in err.errors()
                            )
                            + ". Reply with only the JSON object holding the values, "
                            "not the schema."
                        ),
                    ),
                ]
                continue
            # model_validate accepted it, so it is the JSON object Graphiti expects.
            return cast(dict[str, object], answer)
        if last is None:  # pragma: no cover - ATTEMPTS is a positive constant
            raise RuntimeError("no attempt was made")
        raise last
