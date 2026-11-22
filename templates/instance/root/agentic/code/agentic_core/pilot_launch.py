from __future__ import annotations

import asyncio
from typing import Any, Dict, List, Optional, Set

from substrate import continuity_store, liveness
from substrate.errors import InvalidRequest, MessageParked, NotFound, WaitTimeout
from substrate.ids import CONTEXT, check_continuity_id, matter_ids
from substrate.port import BriquePort, resolve_params
from substrate.settings import setting

CAP = "pilot.launch"
ROOT_SENDER = "user:1"            # the sender of the first message of a root continuity: the person who opened it
LAUNCH_WAIT_TIMEOUT_S = setting("LAUNCH_WAIT_TIMEOUT_S", 3600.0, 1.0, "how long a launch waits for its pilot to finish the task; reaching it is a refusal")
LAUNCH_POLL_S = setting("LAUNCH_POLL_S", 1.0, 0.05, "safety net while waiting: a run of this process that ends wakes the wait first")
LAUNCH_SETTLE_S = setting("LAUNCH_SETTLE_S", 0.3, 0.0, "how long the continuity must stay at rest before the task counts as finished")
MAX_SUCCESSIONS = 32              # a lineage followed through this many successions is not a lineage: stop following


# <brique:capacity name="pilot.launch">
async def pilot_launch(runtime, params: dict) -> dict:
    return await run(BriquePort(runtime, CAP), await resolve_params(params))      # a large parameter arrives as a transit reference
# </brique:capacity>


async def run(port, params: Dict[str, Any]) -> Dict[str, Any]:
    """Create a continuity for a pilot and hand it its first message. A parent that delegates a task (parent_id and message) waits for the pilot
    to finish it and gets its result as the answer; anything else answers at once."""
    # --- #validate: nothing is done before everything asked is well formed ------------------------------------------
    cid = check_continuity_id(params.get("continuity_id"))
    pilot_ref = params.get("pilot_ref")
    if not isinstance(pilot_ref, dict):
        raise InvalidRequest("pilot_ref is required")
    intention = params.get("initial_intention")
    if intention is None or (isinstance(intention, str) and not intention.strip()):
        raise InvalidRequest("initial_intention is required")
    parent_id = check_continuity_id(params["parent_id"]) if params.get("parent_id") else None
    message = params.get("message")
    first = _first_message(cid, parent_id, message)

    # --- #create: idempotent, and it checks that the pilot and the parent exist before anything is written -------------
    create: Dict[str, Any] = {"continuity_id": cid, "pilot_ref": pilot_ref, "initial_intention": intention}
    if parent_id is not None:
        create["parent_id"] = parent_id
    created = await port.call_capacity(f"{CONTEXT}/continuity.create", create)

    # --- #hand_over: the message wakes the pilot (mailbox.send fires a non-blocking activation); nothing is awaited here ---------
    sent: Optional[Dict[str, Any]] = None
    if first is not None:
        sent = await port.call_capacity(f"{CONTEXT}/mailbox.send", {"continuity_id": cid, "message": first})
    out = {"continuity_id": cid, "created": bool(created.get("created")),
           "message_id": sent.get("message_id") if sent else None, "seq": sent.get("seq") if sent else None}

    # --- #await_result: the answer of an intention is the result of what it asked ---------------------------------------
    if _waits(params):
        async with port.keepalive():           # a long-running capacity: a running response now and every few seconds, the answer when the pilot has finished
            out.update(await _await_result(port, cid, first["id"], int(out["seq"]) if isinstance(out["seq"], int) else 0))
    return out


def _waits(params: Dict[str, Any]) -> bool:
    """wait, as given, else true only for a parent delegating a task: an interface opening a chat must not wait."""
    wait = params.get("wait")
    if wait is not None:
        if not isinstance(wait, bool):
            raise InvalidRequest("wait must be a boolean")
        return wait and params.get("message") is not None
    return bool(params.get("parent_id")) and params.get("message") is not None


async def _await_result(port, cid: str, message_id: str, from_seq: int) -> Dict[str, Any]:
    """Wait until the pilot is AT REST on the task, then read what it said.

    Acknowledging the first message is not the end: a pilot that emits an awaited intention without a wait group ends its turn, and what comes back
    arrives as new messages that start new turns. The task is finished when the continuity is idle, its mailbox is empty, no wait group is active and
    no awaited intention is still without its Response, and it stays so for a moment (the Response of the last tool is written, then its entry)."""
    deadline = asyncio.get_running_loop().time() + LAUNCH_WAIT_TIMEOUT_S
    lineage: List[str] = [cid]                           # the generations the message went through, oldest first
    journals = {cid: _JournalTail(max(0, from_seq - 1))}
    while True:
        head = lineage[-1]
        header = (await port.matter_read(matter_ids(head).continuity)).content
        successor = header.get("successor_id")
        if successor and successor not in lineage and len(lineage) < MAX_SUCCESSIONS:
            lineage.append(successor)                    # the continuity continued elsewhere: the successor goes on with the task
            journals[successor] = _JournalTail(0)
            continue
        entries = (await port.matter_read(matter_ids(head).mailbox)).content["entries"]
        failed = next((e for e in entries if e.get("status") == "failed"), None)
        if failed is not None:                           # a message the pilot set aside after its attempts: the task cannot be finished
            try:
                await journals[head].refresh(port, head)
                cause = journals[head].last_failure(failed["message_id"])
            except NotFound:
                cause = ""
            raise MessageParked("the pilot could not treat the message and set it aside" + (f": {cause}" if cause else ""),
                                details={"continuity_id": head, "message_id": failed["message_id"], "attempts": failed.get("attempts"),
                                         **({"last_error": cause} if cause else {})})
        if await _at_rest(port, head, journals[head]):
            await asyncio.sleep(LAUNCH_SETTLE_S)         # the Response of the last tool is written before its entry: look again before concluding
            if await _at_rest(port, head, journals[head]):
                return {"treated": True, "result": await _result_of(port, lineage, journals)}
        remaining = deadline - asyncio.get_running_loop().time()
        if remaining <= 0:
            raise WaitTimeout("the pilot did not finish the task in time", details={"continuity_id": head, "message_id": message_id})
        async with port.yielding():                      # waiting for something that is not an answer of the engine must not hold a slot
            await liveness.wait_change(head, min(LAUNCH_POLL_S, remaining))


class _JournalTail:
    """Incremental view of the journal facts needed by pilot.launch."""

    def __init__(self, cursor: int) -> None:
        self.cursor = cursor
        self.asked: Set[Any] = set()
        self.answered: Set[Any] = set()
        self.said: List[Dict[str, Any]] = []
        self.failures: Dict[str, str] = {}

    async def refresh(self, port, cid: str) -> None:
        lines = await continuity_store.lines_after(port, cid, self.cursor)
        for line in lines:
            self.cursor = max(self.cursor, int(line.get("seq", 0)))
            content = line.get("content")
            if line.get("kind") == "capacity_intention" and isinstance(content, dict) and content.get("await_response") is True:
                self.asked.add(content.get("id"))
            elif line.get("kind") == "capacity_response":
                self.answered.add((line.get("correlation") or {}).get("intention_id"))
            elif line.get("kind") == "pilot":
                value = content.get("content", content) if isinstance(content, dict) else content
                self.said.append({"to": line.get("to"), "kind": content.get("kind") if isinstance(content, dict) else None,
                                  "content": value})
            elif line.get("kind") == "error" and isinstance(content, dict) and content.get("event") == "treatment_failed":
                self.failures[str(content.get("message_id"))] = str(content.get("error") or "")[:400]

    def last_failure(self, message_id: str) -> str:
        return self.failures.get(str(message_id), "")


async def _at_rest(port, cid: str, journal: _JournalTail) -> bool:
    """Idle, nothing in the mailbox, no wait group, and no awaited intention of the task without its Response."""
    runtime = (await port.matter_read(matter_ids(cid).runtime)).content
    if runtime.get("status") != "idle" or runtime.get("active_wait_groups"):
        return False
    if (await port.matter_read(matter_ids(cid).mailbox)).content["entries"]:
        return False
    await journal.refresh(port, cid)
    return journal.asked <= journal.answered


async def _result_of(port, lineage: List[str], journals: Dict[str, _JournalTail]) -> Dict[str, Any]:
    """What the pilot said while treating the message: the lines of kind pilot written after it, in order, in the generations it went through."""
    said: List[Dict[str, Any]] = []
    for cid in lineage:
        try:
            await journals[cid].refresh(port, cid)
        except NotFound:
            continue
        said.extend(journals[cid].said)
    text = "\n\n".join(m["content"] if isinstance(m["content"], str) else str(m["content"]) for m in said)
    return {"text": text, "messages": said}


def _first_message(cid: str, parent_id: Optional[str], message: Any) -> Optional[Dict[str, Any]]:
    """The first message as mailbox.send takes it: a request (kind user) from the parent, or from the person for a root."""
    if message is None:
        return None
    if not isinstance(message, dict) or message.get("content") is None:
        raise InvalidRequest("message must carry content")
    given = message.get("id")
    if given is not None and not (isinstance(given, str) and given.strip()):
        raise InvalidRequest("message.id must be a non-empty string")
    return {"id": given or f"{cid}_launch", "from": f"continuity:{parent_id}" if parent_id else ROOT_SENDER,
            "kind": "user", "content": message["content"]}
