"""One transient failure of a provider is not a failure of the inference.

An inference is a read: asking again costs only the tokens of the failed attempt. A rate limit (429), an overloaded provider (529), a server
error (5xx), a request timeout (408) or a broken connection is asked again, waiting longer each time (the Retry-After of the provider when
it gives one). Any other answer is returned at once: a refusal (400, 401, 403...) will not change by waiting.
"""
from __future__ import annotations

import asyncio
from typing import Any, Awaitable, Callable, Dict, Optional

import httpx

RETRIABLE_STATUS = frozenset({408, 429, 500, 502, 503, 504, 529})
ATTEMPTS = 3
BASE_DELAY_S = 1.0
MAX_DELAY_S = 20.0


def delay_s(attempt: int, retry_after: Optional[str]) -> float:
    """How long to wait before the next attempt (attempt counts from 0)."""
    try:
        if retry_after is not None:
            return min(float(retry_after), MAX_DELAY_S)
    except ValueError:
        pass
    return min(BASE_DELAY_S * (2 ** attempt), MAX_DELAY_S)


async def post_with_retry(client: httpx.AsyncClient, url: str, *, headers: Dict[str, str], json: Any, attempts: int = ATTEMPTS,
                          sleep: Callable[[float], Awaitable[None]] = asyncio.sleep) -> httpx.Response:
    """The response of the last attempt: the caller reads its status. A broken connection that persists is raised."""
    for attempt in range(attempts):
        last = attempt == attempts - 1
        try:
            response = await client.post(url, headers=headers, json=json)
        except httpx.TransportError:
            if last:
                raise
            await sleep(delay_s(attempt, None))
            continue
        if response.status_code in RETRIABLE_STATUS and not last:
            await sleep(delay_s(attempt, response.headers.get("retry-after")))
            continue
        return response
    raise RuntimeError("unreachable")
