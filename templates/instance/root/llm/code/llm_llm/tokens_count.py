from __future__ import annotations

import os

import httpx

from retry import post_with_retry

API_URL = "https://api.anthropic.com/v1/messages/count_tokens"
ANTHROPIC_VERSION = "2023-06-01"


def _body_of(provider_request: dict) -> dict:
    """The counting endpoint takes what is read as input; the generation limits are not part of it."""
    return {k: v for k, v in provider_request.items() if k in ("model", "messages", "system", "output_config")}


# <brique:capacity name="tokens.count">
async def tokens_count(runtime, params: dict) -> dict:
    from infer_anthropic import _adapt_request    # the same adaptation as the inference: what is counted is what would be sent
    request = dict(await params.value("request") or {})
    provider = request.get("provider") or "anthropic"
    if provider != "anthropic":
        raise ValueError(f"tokens.count does not count for provider '{provider}'")
    api_key = os.environ.get("ANTHROPIC_API_KEY")
    if not api_key:
        raise ValueError("ANTHROPIC_API_KEY is unavailable")
    request.setdefault("parameters", {}).setdefault("max_output_tokens", 1)     # required by the adaptation, unused by the count
    body = _body_of(_adapt_request(request))
    headers = {"x-api-key": api_key, "anthropic-version": ANTHROPIC_VERSION, "Content-Type": "application/json"}
    async with httpx.AsyncClient(timeout=60) as client:
        resp = await post_with_retry(client, API_URL, headers=headers, json=body)
    if resp.status_code >= 400:
        raise ValueError(f"Anthropic token count failed ({resp.status_code}): {resp.text}")
    return {"input_tokens": int(resp.json().get("input_tokens", 0))}
# </brique:capacity>
