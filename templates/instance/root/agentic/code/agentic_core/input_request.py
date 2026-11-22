from __future__ import annotations

import asyncio
from datetime import datetime, timedelta, timezone
from typing import Any, Dict, Optional

from substrate import continuity_store, input_state
from substrate.errors import IdentityConflict, InvalidRequest, NotFound, RequestCancelled, RequestClosed, WaitTimeout
from substrate.ids import check_continuity_id, matter_ids, new_id, now
from substrate.journal import append_trace
from substrate.port import BriquePort, resolve_params
from substrate.settings import setting

CAP = "input.request"
INPUT_TIMEOUT_S = setting("INPUT_TIMEOUT_S", 86400.0, 1.0, "how long a request lives without an answer; the timeout_s of the call takes precedence")
INPUT_POLL_S = setting("INPUT_POLL_S", 1.0, 0.05, "safety net while waiting: an answer written in this process wakes the wait first")


# <brique:capacity name="input.request">
async def input_request(runtime, params: dict) -> dict:
    return await run(BriquePort(runtime, CAP), await resolve_params(params))      # a large parameter arrives as a transit reference
# </brique:capacity>


async def run(port, params: Dict[str, Any]) -> Dict[str, Any]:
    """Ask a person a precise question on behalf of a continuity, and answer with what they said. Long-running: it waits for the answer."""
    # --- #validate: nothing is written before everything asked is well formed --------------------------------------------
    cid = check_continuity_id(params.get("continuity_id"))
    question = params.get("question")
    if not isinstance(question, str) or not question.strip():
        raise InvalidRequest("question is required")
    kind = params.get("answer_kind", "text")
    if kind not in input_state.ANSWER_KINDS:
        raise InvalidRequest("answer_kind must be text, choice or confirm", details={"answer_kind": kind})
    choices = params.get("choices")
    if kind == "choice":
        if not (isinstance(choices, list) and len(choices) >= 2 and all(isinstance(c, str) and c.strip() for c in choices) and len(set(choices)) == len(choices)):
            raise InvalidRequest("a choice request needs at least two distinct texts in choices")
    elif choices is not None:
        raise InvalidRequest("choices belong to a choice request only")
    timeout_s = params.get("timeout_s", INPUT_TIMEOUT_S)
    if isinstance(timeout_s, bool) or not isinstance(timeout_s, (int, float)) or timeout_s <= 0:
        raise InvalidRequest("timeout_s must be a positive number")
    given = params.get("request_id")
    if given is not None and not (isinstance(given, str) and given.strip()):
        raise InvalidRequest("request_id must be a non-empty string")
    request_id = given or (f"req_{port.intention_id()}" if hasattr(port, "intention_id") and port.intention_id() else new_id("req"))

    # --- #write: the request is a line of the journal, idempotent by its identifier -----------------------------------
    await _check_continuity(port, cid)
    asked = datetime.now(timezone.utc)
    content: Dict[str, Any] = {"event": input_state.REQUEST, "request_id": request_id, "question": question, "answer_kind": kind,
                               "asked_at": now(), "expires_at": (asked + timedelta(seconds=float(timeout_s))).isoformat(timespec="milliseconds").replace("+00:00", "Z")}
    if kind == "choice":
        content["choices"] = list(choices)
    # an event line already in the journal is not given back by the append (it is not a retained kind): the journal is the source
    known = input_state.state_of(await continuity_store.all_messages(port, cid), request_id)
    if known["request"] is None:
        stored = await append_trace(port, cid, "event", content, message_id=f"input_request_{request_id}")
        known = input_state.state_of(await continuity_store.lines_after(port, cid, int(stored["seq"]) - 1), request_id)
    existing = known["request"] or content
    if (existing.get("question"), existing.get("answer_kind"), existing.get("choices")) != (question, kind, content.get("choices")):
        raise IdentityConflict("this request_id was used with another question", details={"request_id": request_id})
    stored = {"seq": known["seq"] if known["seq"] is not None else stored["seq"], "kind": "event", "content": existing}
    seq = int(stored["seq"])

    # --- #await: the answer of an intention is what the person said ----------------------------------------------------
    async with port.keepalive():           # a long-running capacity: a running response now and every few seconds, the answer when it comes
        with input_state.watching(request_id) as wake:
            cursor = seq
            state_lines = [stored]
            while True:
                fresh = await continuity_store.lines_after(port, cid, cursor)
                for line in fresh:
                    cursor = max(cursor, int(line.get("seq", 0)))
                    content = line.get("content")
                    if line.get("kind") == "event" and isinstance(content, dict) and content.get("request_id") == request_id:
                        state_lines.append(line)
                state = input_state.state_of(state_lines, request_id)
                if state["status"] == "answered":
                    response = state["response"]
                    return {"request_id": request_id, "answer": response.get("answer"), "answered_at": response.get("answered_at")}
                if state["status"] == "closed":
                    _raise_closed(state["closed"], request_id)
                remaining = (_expiry(existing) - datetime.now(timezone.utc)).total_seconds()
                if remaining <= 0:
                    await _close(port, cid, request_id, "timeout")
                    raise WaitTimeout("nobody answered in time", details={"request_id": request_id, "continuity_id": cid})
                try:
                    await _check_continuity(port, cid)
                except (NotFound, RequestCancelled):
                    await _close(port, cid, request_id, "cancelled", quiet=True)
                    raise RequestCancelled("the continuity that asked is gone", details={"request_id": request_id, "continuity_id": cid})
                async with port.yielding():                 # waiting for something that is not an answer of the engine must not hold a slot
                    await input_state.wait(wake, min(INPUT_POLL_S, remaining))


async def _check_continuity(port, cid: str) -> None:
    """The continuity must exist and still be the one that answers: an archived one has been replaced by its successor."""
    header = (await port.matter_read(matter_ids(cid).continuity)).content
    if header.get("status") == "archived":
        raise RequestCancelled("the continuity was replaced by its successor", details={"continuity_id": cid, "successor_id": header.get("successor_id")})


async def _close(port, cid: str, request_id: str, reason: str, quiet: bool = False) -> None:
    """The closure is written once (idempotent by its identifier); a gone continuity has no journal left to write in."""
    try:
        await append_trace(port, cid, "event", {"event": input_state.CLOSED, "request_id": request_id, "reason": reason, "closed_at": now()},
                           message_id=f"input_closed_{request_id}")
    except NotFound:
        if not quiet:
            raise


def _expiry(request: Dict[str, Any]) -> datetime:
    return datetime.fromisoformat(str(request["expires_at"]).replace("Z", "+00:00"))


def _raise_closed(closed: Dict[str, Any], request_id: str) -> None:
    details = {"request_id": request_id}
    if closed.get("reason") == "timeout":
        raise WaitTimeout("the request timed out", details=details)
    if closed.get("reason") == "cancelled":
        raise RequestCancelled("the request was cancelled", details=details)
    raise RequestClosed("the request is closed", details=details)
