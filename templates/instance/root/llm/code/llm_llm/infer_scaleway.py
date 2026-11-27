from __future__ import annotations

import json
import os
import time

import httpx

from native_tools import refuse_native_tools
from retry import post_with_retry

API_URL = "https://api.scaleway.ai/v1/chat/completions"


def _adapt_request(request: dict) -> dict:
    refuse_native_tools(request, "scaleway")
    model = request.get("model")
    messages = request.get("messages", [])
    parameters = request.get("parameters") or {}
    requirements = request.get("requirements") or {}
    output = request.get("output") or {}

    body: dict = {
        "model": model,
        "messages": [
            {"role": m.get("role"), "content": m.get("content")}
            for m in messages
        ],
    }

    if "max_output_tokens" in parameters and parameters["max_output_tokens"] is not None:
        body["max_tokens"] = parameters["max_output_tokens"]
    if "temperature" in parameters and parameters["temperature"] is not None:
        body["temperature"] = parameters["temperature"]
    if "top_p" in parameters and parameters["top_p"] is not None:
        body["top_p"] = parameters["top_p"]
    if parameters.get("stop") is not None:
        body["stop"] = parameters["stop"]

    reasoning = requirements.get("reasoning")
    if reasoning:
        body["reasoning_effort"] = reasoning

    output_format = output.get("format") or "text"
    if output_format == "text":
        pass
    elif output_format == "json":
        body["response_format"] = {"type": "json_object"}
    elif output_format == "json_schema":
        body["response_format"] = {
            "type": "json_schema",
            "json_schema": {"name": "brique_response", "schema": output.get("schema")},
        }
    else:
        raise ValueError(f"unsupported output.format for Scaleway adapter: {output_format}")

    return body


async def _execute(provider_request: dict) -> tuple[dict, int]:
    api_key = os.environ.get("SCW_SECRET_KEY")
    if not api_key:
        raise ValueError("SCW_SECRET_KEY is unavailable")

    headers = {
        "Authorization": f"Bearer {api_key}",
        "Content-Type": "application/json",
    }

    start = time.monotonic()
    async with httpx.AsyncClient(timeout=120) as client:
        resp = await post_with_retry(client, API_URL, headers=headers, json=provider_request)
    duration_ms = int((time.monotonic() - start) * 1000)

    if resp.status_code >= 400:
        raise ValueError(f"Scaleway request failed ({resp.status_code}): {resp.text}")

    return resp.json(), duration_ms


def _normalize_response(provider_response: dict, duration_ms: int, output_format: str) -> dict:
    choice = (provider_response.get("choices") or [{}])[0]
    message = choice.get("message") or {}
    raw_text = message.get("content") or ""

    if output_format in ("json", "json_schema"):
        content = json.loads(raw_text) if raw_text else None
    else:
        content = raw_text

    usage = provider_response.get("usage") or {}

    return {
        "output": {
            "format": output_format,
            "content": content,
        },
        "resolved": {
            "provider": "scaleway",
            "model": provider_response.get("model"),
        },
        "usage": {
            "input_tokens": usage.get("prompt_tokens"),
            "output_tokens": usage.get("completion_tokens"),
            "total_tokens": usage.get("total_tokens"),
        },
        "finish_reason": choice.get("finish_reason"),
        "duration_ms": duration_ms,
    }


# <brique:capacity name="infer.scaleway">
async def infer_scaleway(runtime, params: dict) -> dict:
    # a model call lasts seconds to minutes: acknowledge at once (`running`), then heartbeat until the answer
    async with runtime.keepalive():
        request = await params.value("request") or {}     # value, @file or transient: the runtime resolves it
        output_format = (request.get("output") or {}).get("format") or "text"

        provider_request = _adapt_request(request)
        provider_response, duration_ms = await _execute(provider_request)
        response = _normalize_response(provider_response, duration_ms, output_format)

        return {"response": response}
# </brique:capacity>
