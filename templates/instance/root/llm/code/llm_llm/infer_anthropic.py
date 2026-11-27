from __future__ import annotations

import json
import os
import time

import httpx
from wrapper.calls import CapacityCallError

from native_tools import check_tool_pairing, uses_native_tools, validate_tools
from retry import post_with_retry

API_URL = "https://api.anthropic.com/v1/messages"
ANTHROPIC_VERSION = "2023-06-01"


def _adapt_request(request: dict) -> dict:
    model = request.get("model")
    messages = request.get("messages", [])
    parameters = request.get("parameters") or {}
    requirements = request.get("requirements") or {}
    output = request.get("output") or {}

    system_blocks = []
    body_messages = []
    for m in messages:
        role = m.get("role")
        content = m.get("content")
        if role == "system":
            block = {"type": "text", "text": content}
            if m.get("cache") is True:
                # stable prefix worth caching: the provider ignores it silently below its minimum cacheable length
                block["cache_control"] = {"type": "ephemeral"}
            system_blocks.append(block)
        elif role in ("user", "assistant") and isinstance(content, list):
            blocks = [_provider_block(b) for b in content]
            if m.get("cache") is True:
                for block in reversed(blocks):      # thinking blocks take no cache_control: mark the last block that does
                    if block.get("type") not in ("thinking", "redacted_thinking"):
                        block["cache_control"] = {"type": "ephemeral"}
                        break
            body_messages.append({"role": role, "content": blocks})
        elif role in ("user", "assistant"):
            if m.get("cache") is True:
                # the last message of a stable history: the provider reads the cache up to here on the next request
                content = [{"type": "text", "text": content, "cache_control": {"type": "ephemeral"}}]
            body_messages.append({"role": role, "content": content})
        else:
            raise ValueError(f"message role '{role}' has no Anthropic Messages API equivalent")

    max_output_tokens = parameters.get("max_output_tokens")
    if max_output_tokens is None:
        raise ValueError(
            "Anthropic requires max_tokens; request.parameters.max_output_tokens is absent"
        )

    body: dict = {
        "model": model,
        "messages": body_messages,
        "max_tokens": max_output_tokens,
    }
    if system_blocks:
        body["system"] = system_blocks

    if uses_native_tools(request):
        check_tool_pairing(messages)
        tools = request.get("tools") or []
        validate_tools(tools)
        if tools:
            # without strict: remote schemas are arbitrary, and the gateway validates the arguments anyway
            body["tools"] = [{"name": t["name"], "description": t.get("description") or "", "input_schema": t["input_schema"]} for t in tools]
        choice = request.get("tool_choice") or "auto"
        if choice == "none":
            body["tool_choice"] = {"type": "none"}
        elif choice != "auto":
            raise ValueError(f"unsupported tool_choice for Anthropic adapter: {choice}")

    if "temperature" in parameters and parameters["temperature"] is not None:
        body["temperature"] = parameters["temperature"]
    if "top_p" in parameters and parameters["top_p"] is not None:
        body["top_p"] = parameters["top_p"]
    if parameters.get("stop") is not None:
        body["stop_sequences"] = parameters["stop"]

    reasoning = requirements.get("reasoning")
    if reasoning:
        body["output_config"] = {"effort": reasoning}

    output_format = output.get("format") or "text"
    if output_format == "json_schema":
        output_config = body.setdefault("output_config", {})
        output_config["format"] = {"type": "json_schema", "schema": output.get("schema")}
    elif output_format == "json":
        raise ValueError(
            "a schemaless canonical 'json' output has no equivalent contract used by this adapter"
        )
    elif output_format != "text":
        raise ValueError(f"unsupported output.format for Anthropic adapter: {output_format}")

    return body


def _provider_block(block: dict) -> dict:
    """A canonical content block in the form of the Messages API."""
    kind = block.get("type")
    if kind == "text":
        return {"type": "text", "text": block.get("text", "")}
    if kind == "tool_call":
        return {"type": "tool_use", "id": block["id"], "name": block["name"], "input": block.get("arguments") or {}}
    if kind == "tool_result":
        out = {"type": "tool_result", "tool_use_id": block["call_id"], "content": block.get("content", "")}
        if block.get("is_error"):
            out["is_error"] = True
        return out
    if kind == "provider_block":
        if block.get("provider") != "anthropic":
            raise ValueError(f"a provider block of {block.get('provider')!r} cannot be sent to Anthropic")
        return dict(block.get("block") or {})
    raise ValueError(f"unsupported content block type for Anthropic adapter: {kind}")


async def _execute(provider_request: dict) -> tuple[dict, int]:
    api_key = os.environ.get("ANTHROPIC_API_KEY")
    if not api_key:
        raise ValueError("ANTHROPIC_API_KEY is unavailable")

    headers = {
        "x-api-key": api_key,
        "anthropic-version": ANTHROPIC_VERSION,
        "Content-Type": "application/json",
    }

    start = time.monotonic()
    async with httpx.AsyncClient(timeout=120) as client:
        resp = await post_with_retry(client, API_URL, headers=headers, json=provider_request)
    duration_ms = int((time.monotonic() - start) * 1000)

    if resp.status_code >= 400:
        raise ValueError(f"Anthropic request failed ({resp.status_code}): {resp.text}")

    return resp.json(), duration_ms


def _normalize_tool_response(provider_response: dict, output_format: str) -> dict:
    """The output of a request that uses native tools: tool calls, or the final content. Every block is read;
    an unknown block, a truncated tool call or an answer with neither text nor tool call is an explicit error."""
    texts, tool_calls, assistant = [], [], []
    for block in provider_response.get("content", []):
        kind = block.get("type")
        if kind == "text":
            texts.append(block.get("text", ""))
            assistant.append({"type": "text", "text": block.get("text", "")})
        elif kind == "tool_use":
            call = {"id": block.get("id"), "name": block.get("name"), "arguments": block.get("input") or {}}
            tool_calls.append(call)
            assistant.append(dict(call, type="tool_call"))
        elif kind in ("thinking", "redacted_thinking"):
            assistant.append({"type": "provider_block", "provider": "anthropic", "block": block})
        else:
            raise CapacityCallError("invalid", f"Anthropic returned an unsupported content block: {kind}", {"reason": "unsupported_block"})
    stop_reason = provider_response.get("stop_reason")
    if tool_calls:
        if stop_reason == "max_tokens":
            raise CapacityCallError("invalid", "Anthropic stopped at max_tokens inside a tool call: raise parameters.max_output_tokens",
                                    {"reason": "output_truncated"})
        return {"format": output_format, "content": None, "tool_calls": tool_calls, "assistant_content": assistant}
    raw_text = "".join(texts)
    if not raw_text:
        raise CapacityCallError("invalid", "Anthropic returned neither text nor a tool call", {"reason": "empty_output"})
    if output_format == "json_schema":
        try:
            content = json.loads(raw_text)
        except ValueError as exc:
            if stop_reason == "max_tokens":
                raise CapacityCallError("invalid", "Anthropic stopped at max_tokens before the JSON answer was complete: raise parameters.max_output_tokens",
                                        {"reason": "output_truncated"}) from exc
            raise
    else:
        content = raw_text
    return {"format": output_format, "content": content, "assistant_content": assistant}


def _normalize_response(provider_response: dict, duration_ms: int, output_format: str, native_tools: bool = False) -> dict:
    if native_tools:
        output = _normalize_tool_response(provider_response, output_format)
        return _envelope(provider_response, duration_ms, output)
    texts = [
        block.get("text", "")
        for block in provider_response.get("content", [])
        if block.get("type") == "text"
    ]
    raw_text = "".join(texts)

    if output_format == "json_schema":
        try:
            content = json.loads(raw_text) if raw_text else None
        except ValueError as exc:
            if provider_response.get("stop_reason") == "max_tokens":
                raise CapacityCallError("invalid", "Anthropic stopped at max_tokens before the JSON answer was complete: raise parameters.max_output_tokens",
                                        {"reason": "output_truncated"}) from exc
            raise
    else:
        content = raw_text

    return _envelope(provider_response, duration_ms, {"format": output_format, "content": content})


def _envelope(provider_response: dict, duration_ms: int, output: dict) -> dict:
    usage = provider_response.get("usage") or {}
    input_tokens = (
        (usage.get("input_tokens") or 0)
        + (usage.get("cache_creation_input_tokens") or 0)
        + (usage.get("cache_read_input_tokens") or 0)
    )
    output_tokens = usage.get("output_tokens") or 0
    return {
        "output": output,
        "resolved": {
            "provider": "anthropic",
            "model": provider_response.get("model"),
        },
        "usage": {
            "input_tokens": input_tokens,
            "output_tokens": output_tokens,
            "total_tokens": input_tokens + output_tokens,
            "cached_input_tokens": usage.get("cache_read_input_tokens") or 0,
            "cache_creation_input_tokens": usage.get("cache_creation_input_tokens") or 0,
        },
        "finish_reason": provider_response.get("stop_reason"),
        "duration_ms": duration_ms,
    }


# <brique:capacity name="infer.anthropic">
async def infer_anthropic(runtime, params: dict) -> dict:
    # a model call lasts seconds to minutes: acknowledge at once (`running`), then heartbeat until the answer
    async with runtime.keepalive():
        request = await params.value("request") or {}     # value, @file or transient: the runtime resolves it
        output_format = (request.get("output") or {}).get("format") or "text"

        provider_request = _adapt_request(request)
        provider_response, duration_ms = await _execute(provider_request)
        response = _normalize_response(provider_response, duration_ms, output_format, uses_native_tools(request))

        return {"response": response}
# </brique:capacity>
