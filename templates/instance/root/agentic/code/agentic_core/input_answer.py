from __future__ import annotations

from typing import Any, Dict

from substrate import continuity_store, input_state
from substrate.errors import AlreadyAnswered, InvalidRequest, NotFound, RequestClosed
from substrate.ids import check_continuity_id, now
from substrate.journal import append_trace
from substrate.port import BriquePort, resolve_params

CAP = "input.answer"


# <brique:capacity name="input.answer">
async def input_answer(runtime, params: dict) -> dict:
    return await run(BriquePort(runtime, CAP), await resolve_params(params))      # a large parameter arrives as a transit reference
# </brique:capacity>


async def run(port, params: Dict[str, Any]) -> Dict[str, Any]:
    """Answer an open request. The first valid answer closes it; an invalid one leaves it open."""
    cid = check_continuity_id(params.get("continuity_id"))
    request_id = params.get("request_id")
    if not isinstance(request_id, str) or not request_id.strip():
        raise InvalidRequest("request_id is required")
    if "answer" not in params:
        raise InvalidRequest("answer is required")
    answer = params["answer"]

    state = input_state.state_of(await continuity_store.all_messages(port, cid), request_id)
    if state["status"] == "absent":
        raise NotFound("no such request in this continuity", details={"continuity_id": cid, "request_id": request_id})
    request = state["request"]
    if state["status"] == "closed":
        raise RequestClosed("the request is closed and cannot be answered", details={"request_id": request_id, "reason_closed": state["closed"].get("reason")})
    if state["status"] == "answered":
        return _same_or_refuse(state["response"], answer, request_id)
    try:
        valid = input_state.check_answer(request["answer_kind"], request.get("choices"), answer)
    except ValueError as exc:
        raise InvalidRequest(str(exc), details={"request_id": request_id, "answer_kind": request["answer_kind"]}) from exc

    await append_trace(port, cid, "event", {"event": input_state.RESPONSE, "request_id": request_id, "answer": valid, "answered_at": now()},
                       message_id=f"input_response_{request_id}")
    input_state.wake(request_id)                                      # the capacity that waits for it, in this process, goes on at once
    # a concurrent answer that came first is the one that counts: read what the journal holds
    final = input_state.state_of(await continuity_store.all_messages(port, cid), request_id)
    return _same_or_refuse(final["response"] or {"answer": valid}, valid, request_id)


def _same_or_refuse(response: Dict[str, Any], answer: Any, request_id: str) -> Dict[str, Any]:
    if response.get("answer") != answer:
        raise AlreadyAnswered("the request already has another answer", details={"request_id": request_id})
    return {"request_id": request_id, "answered": True}
