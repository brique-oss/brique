"""The state of a request for input, read from the journal of the continuity that asks, and the wake-up of whoever waits for its answer.

The journal carries three kinds of line (genre event, never a stimulus): input_request, input_response, input_closed. A request is open until
a response or a closure carries its request_id; if both exist, the first in the journal wins (the answer written just before the deadline stands).
"""
from __future__ import annotations

import asyncio
from contextlib import contextmanager
from typing import Any, Dict, Iterable, Iterator, Optional

REQUEST, RESPONSE, CLOSED = "input_request", "input_response", "input_closed"
ANSWER_KINDS = ("text", "choice", "confirm")
CONFIRM = ("yes", "no")


def state_of(lines: Iterable[Dict[str, Any]], request_id: str) -> Dict[str, Any]:
    """{status: absent | open | answered | closed, request, response, closed, seq} for one request."""
    out: Dict[str, Any] = {"status": "absent", "request": None, "response": None, "closed": None, "seq": None}
    for line in sorted(lines, key=lambda m: int(m.get("seq", 0))):
        content = line.get("content")
        if line.get("kind") != "event" or not isinstance(content, dict) or content.get("request_id") != request_id:
            continue
        event = content.get("event")
        if event == REQUEST and out["request"] is None:
            out.update(request=content, seq=line.get("seq"), status="open")
        elif event == RESPONSE and out["status"] == "open":
            out.update(response=content, status="answered")
        elif event == CLOSED and out["status"] == "open":
            out.update(closed=content, status="closed")
    return out


def check_answer(kind: str, choices: Optional[list], answer: Any) -> str:
    """The answer in the form the kind calls for, or a message saying what is wrong with it."""
    if kind == "text":
        if isinstance(answer, str) and answer.strip():
            return answer
        raise ValueError("the answer must be a non-empty text")
    if kind == "choice":
        if isinstance(answer, str) and answer in (choices or []):
            return answer
        raise ValueError("the answer must be one of the choices: " + ", ".join(map(str, choices or [])))
    if kind == "confirm":
        if answer in CONFIRM:
            return answer
        raise ValueError("the answer must be yes or no")
    raise ValueError(f"unknown answer kind {kind!r}")


# -- the wake-up: a response written in this process wakes the capacity that waits for it at once (the poll is the net) ----------------------------
_events: Dict[str, asyncio.Event] = {}


@contextmanager
def watching(request_id: str) -> Iterator[asyncio.Event]:
    """Register before reading the state: an answer that lands between the read and the wait is not missed."""
    event = _events.setdefault(request_id, asyncio.Event())
    try:
        yield event
    finally:
        _events.pop(request_id, None)


def wake(request_id: str) -> None:
    event = _events.get(request_id)
    if event is not None:
        event.set()


async def wait(event: asyncio.Event, timeout_s: float) -> None:
    try:
        await asyncio.wait_for(event.wait(), timeout=timeout_s)
    except asyncio.TimeoutError:
        pass
    event.clear()
