from __future__ import annotations

import copy
import hashlib
import json
from typing import Any, Dict, Optional

from substrate import continuity_store, inference_window, liveness
from substrate import pilot_ref as pilot_refs
from substrate.cas import update_matter
from substrate.controls import APPLIED, DELETED, apply_when_not_running, parse_control
from substrate.errors import ConsistencyError, IdentityConflict, InvalidRequest, NotFound, Refused
from substrate.ids import CONTEXT, check_continuity_id, matter_ids, now
from substrate.journal import append_trace
from substrate.kinds import HISTORY_ONLY_KINDS
from substrate.port import WAKE_ATTEMPTS, BriquePort, resolve_params
from substrate.schemas import load_schema
from substrate.states import find_entry

CAP = "mailbox.send"
KINDS = load_schema("message")["functional"]["kind"]
CORRELATION_KEYS = [k for k in load_schema("correlation")["functional"] if not k.startswith("_")]


# <brique:capacity name="mailbox.send">
async def mailbox_send(runtime, params: dict) -> dict:
    return await run(BriquePort(runtime, CAP), await resolve_params(params))      # a large parameter arrives as a transit reference
# </brique:capacity>


async def run(port, params: Dict[str, Any]) -> Dict[str, Any]:
    cid = check_continuity_id(params.get("continuity_id"))
    ids = matter_ids(cid)
    message = _normalize(params.get("message"), cid)

    # --- #resolve_state: the four Matters must exist (the journal is read through the in-memory continuity) ---------
    cont, mbx, rt, journal = await _resolve_state(port, cid, ids)
    in_journal = _accepted(journal, message["id"])
    entry = find_entry(mbx.content["entries"], message["id"])

    # --- #redirect: a continuity that has a successor no longer receives anything ------------------
    successor_id = cont.content.get("successor_id")
    if successor_id:
        return await _forward(port, message, successor_id, consumed_here=in_journal is not None and entry is None)

    # --- #identity: the same id is the same message only when its content is the same -----------------
    fingerprint = fingerprint_of(message)
    known = fingerprint_of(in_journal) if in_journal is not None else (entry or {}).get("message_fingerprint")
    if known is not None and known != fingerprint:
        raise IdentityConflict("this message id was already used for a message with another content", details={"message_id": message["id"]})

    # --- #idempotency ---------------------------------------------------------------
    if in_journal is not None and entry is None:
        raise Refused("message id was accepted and consumed; reinsertion is forbidden",
                      details={"message_id": message["id"]})

    if in_journal is not None:
        # accepted message still active: the post-commit routing is completed (idempotently)
        return await _route(port, cid, ids, in_journal, entry["priority"], retry=True)

    # --- #classify + reservation (pre-commit) ---------------------------------------
    priority = entry["priority"] if entry is not None else _classify(message, rt.content)
    await _reserve(port, cid, ids, message["id"], priority, fingerprint)

    # --- #append: the semantic commit point -----------------------------------------
    # waits for the inference window when an inference holds it; a user request for a working pilot_definition run opens it
    opens = priority == "normal" and message["kind"] == "user" and _opens_window(cont.content, rt.content)
    stored = await (_append_opening(port, cid, message) if opens else _append(port, cid, message))
    # a succession may have switched the routing between the checks above and the commit: forward, never strand
    successor_id = (await port.matter_read(ids.continuity)).content.get("successor_id")
    if successor_id:
        result = await _forward(port, stored, successor_id, consumed_here=False)
        await _consume(port, cid, ids, message["id"])
        return result

    # --- #route (post-commit, idempotent) -------------------------------------------
    return await _route(port, cid, ids, stored, priority, retry=False)


# ---------------------------------------------------------------------------------
async def _forward(port, message: Dict[str, Any], successor_id: str, *, consumed_here: bool) -> Dict[str, Any]:
    """Hand the message to the successor, following the chain to the active continuity of the lineage."""
    if consumed_here:
        successor = await continuity_store.get(port, successor_id)
        if not successor.has(message["id"]):
            raise Refused("message id was accepted and consumed; reinsertion is forbidden", details={"message_id": message["id"]})
    forwarded = {k: v for k, v in message.items() if k not in ("seq", "to")}
    result = await port.call_capacity(f"{CONTEXT}/mailbox.send", {"continuity_id": successor_id, "message": forwarded})
    result["redirected_to"] = result.get("redirected_to") or result.get("continuity_id", successor_id)   # deepest hop wins
    return result


async def _resolve_state(port, cid, ids):
    try:
        cont, mbx, rt = await port.matter_read_many([ids.continuity, ids.mailbox, ids.runtime])     # one request for the three state Matters
        return cont, mbx, rt, await continuity_store.get(port, cid)
    except NotFound as exc:
        raise ConsistencyError("continuity, journal, mailbox or runtime is missing", details=exc.details) from exc


def _accepted(journal, message_id: str) -> Optional[Dict[str, Any]]:
    """The message already accepted in the journal under this id, or None. A follow-up line that carries the id is a contradiction."""
    if not journal.has(message_id):
        return None
    found = journal.message(message_id)
    if found is None:
        raise ConsistencyError("the message id is already used by a follow-up line of the journal", details={"message_id": message_id})
    return found


def _normalize(raw: Any, cid: str) -> Dict[str, Any]:
    if not isinstance(raw, dict):
        raise InvalidRequest("message must be an object")
    mid = raw.get("id")
    if not isinstance(mid, str) or not mid.strip():
        raise InvalidRequest("message.id is required")
    if raw.get("kind") not in KINDS:
        raise InvalidRequest(f"message.kind must be one of {KINDS}")
    if raw["kind"] in HISTORY_ONLY_KINDS:
        raise InvalidRequest(f"message.kind {raw['kind']!r} is history only: it is recorded by agentic_core, never sent",
                             details={"history_only": list(HISTORY_ONLY_KINDS)})
    if raw.get("content") is None:
        raise InvalidRequest("message.content is required")
    if not raw.get("from"):
        raise InvalidRequest("message.from must be explicit before persistence")
    to = raw.get("to") or cid
    if to != cid:
        raise InvalidRequest("message.to must designate the target continuity", details={"to": to, "continuity_id": cid})
    correlation = raw.get("correlation") or {}
    if not isinstance(correlation, dict) or any(k not in CORRELATION_KEYS for k in correlation):
        raise InvalidRequest("message.correlation has unknown keys", details={"allowed": CORRELATION_KEYS})
    if raw.get("kind") == "control":
        parse_control(raw.get("content"))
    # a caller-provided seq is never authoritative; the content is written as is, whatever its size
    return {"id": mid.strip(), "from": raw["from"], "to": cid, "kind": raw["kind"], "content": copy.deepcopy(raw["content"]),
            "created_at": raw.get("created_at") or now(), "correlation": copy.deepcopy(correlation)}


def _matching_group(runtime_state: Dict[str, Any], message: Dict[str, Any]) -> Optional[Dict[str, Any]]:
    """The unique active wait group with an unresolved expected item matching the message correlation."""
    correlation = message.get("correlation") or {}
    matches = []
    for group in runtime_state.get("active_wait_groups", []):
        if group.get("status") != "waiting":
            continue
        received_refs = {r.get("expected_ref") for r in group.get("received", [])}
        hits = 0
        for item in group.get("expected", []):
            wanted = item.get("correlation") or {}
            if item.get("ref") in received_refs or not wanted:
                continue
            if all(correlation.get(k) == v for k, v in wanted.items()):
                hits += 1
        if hits > 1:
            raise InvalidRequest("correlation matches several expected items of one wait group", details={"wait_group_id": group.get("id")})
        if hits == 1:
            matches.append(group)
    if len(matches) > 1:
        raise InvalidRequest("correlation matches more than one active wait group")
    return matches[0] if matches else None


def _classify(message: Dict[str, Any], runtime_state: Dict[str, Any]) -> str:
    if message["kind"] == "control":
        return "control"
    return "expected" if _matching_group(runtime_state, message) is not None else "normal"


def fingerprint_of(message: Dict[str, Any]) -> str:
    """What a message is, apart from what the system gives it: from, kind, content and correlation, as canonical JSON. Not seq, not to (always
    the continuity), not created_at (given by the caller or by the system)."""
    canonical = json.dumps({"from": message.get("from"), "kind": message.get("kind"), "content": message.get("content"),
                            "correlation": message.get("correlation") or {}}, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    return "sha256:" + hashlib.sha256(canonical.encode("utf-8")).hexdigest()


async def _reserve(port, cid, ids, message_id: str, priority: str, fingerprint: str) -> None:
    def compute(f):
        if find_entry(f["entries"], message_id) is not None:
            return None, None
        f["entries"].append({"message_id": message_id, "priority": priority, "status": "pending", "message_fingerprint": fingerprint})
        return f, None
    await update_matter(port, ids.mailbox, compute, continuity_id=cid, schema="mailbox")


def _opens_window(header: Dict[str, Any], runtime_state: Dict[str, Any]) -> bool:
    """A user request opens the inference window when a run of a pilot_definition (not a Capacity pilot) is working or waiting."""
    return not pilot_refs.is_capacity(header["pilot_ref"]) and runtime_state.get("status") in ("running", "waiting")


async def _append(port, cid, message: Dict[str, Any]) -> Dict[str, Any]:
    """The semantic commit point: one line appended to the journal (seq is its line number), then the in-memory continuity updated.

    While an inference holds the window the append waits its turn, with a `running` acknowledgement so that the caller (the delivery of
    the answer of a tool, for one) does not expire."""
    if inference_window.closed_for(cid):
        async with port.keepalive():
            return (await continuity_store.append(port, cid, [message]))[0]
    return (await continuity_store.append(port, cid, [message]))[0]


async def _append_opening(port, cid, message: Dict[str, Any]) -> Dict[str, Any]:
    """The append of a user request for a working run: written inside the window it opens, then reserved for the run that absorbs it."""
    async def write() -> Dict[str, Any]:
        async with inference_window.hold(cid, yielding=port.yielding) as window:
            stored = (await continuity_store.append(port, cid, [message]))[0]
            window.reserve([message["id"]])
            return stored
    if inference_window.closed_for(cid):
        async with port.keepalive():
            return await write()
    return await write()


# ---------------------------------------------------------------------------------
async def _route(port, cid, ids, message: Dict[str, Any], priority: str, *, retry: bool) -> Dict[str, Any]:
    out = {"continuity_id": cid, "message_id": message["id"], "seq": message["seq"], "priority": priority,
           "status": "pending", "claimed_by": None, "wait_group_id": None, "wake": False, "redirected_to": None}
    if priority == "expected":
        await _route_expected(port, cid, ids, message, out)
    elif priority == "control":
        await _route_control(port, cid, ids, message, out)
    if out["priority"] == "normal" and message["kind"] == "capacity_response" and await _is_late_member(port, cid, message):
        await _consume(port, cid, ids, message["id"])
        out["status"] = "consumed"
        return out
    # A control that could not be applied now (a run is in progress) stays pending: if that run is dead (an orphan: the wrapper restarted) nobody
    # would ever reach its safe point, so the continuity is woken to be taken over and the control applied. A live run is only told.
    if out["priority"] == "normal" or (out["priority"] == "control" and out["status"] in ("consumed", "pending")):
        out["wake"] = await _wake_if_needed(port, cid, ids)
    return out


async def _is_late_member(port, cid, message) -> bool:
    """A Response of an intention that a wait group expected, arriving after that group is no longer active.

    It is a late result of an already resolved wait: it enters the history, never reopens the group
    and never triggers a new inference. A Response nobody expected stays a normal message.
    """
    intention_id = (message.get("correlation") or {}).get("intention_id")
    if not intention_id:
        return False
    return intention_id in (await continuity_store.get(port, cid)).expected_refs


async def _consume(port, cid, ids, message_id: str) -> None:
    def remove(f):
        if find_entry(f["entries"], message_id) is None:
            return None, None
        f["entries"] = [x for x in f["entries"] if x["message_id"] != message_id]
        return f, None
    await update_matter(port, ids.mailbox, remove, continuity_id=cid, schema="mailbox")


async def _route_expected(port, cid, ids, message, out) -> None:
    """#claim_entry + #capture + #evaluate_wait. Falls back to normal when the wait group is gone."""
    def capture(f):
        group = _matching_group(f, message)
        for g in f["active_wait_groups"]:
            for r in g.get("received", []):
                if r.get("message_id") == message["id"]:
                    return None, {"outcome": "captured", "group_id": g["id"], "run_id": f.get("run_id"), "satisfied": g["status"] == "satisfied"}
        if group is None:
            return None, {"outcome": "nomatch"}
        item = next(i for i in group["expected"]
                    if i.get("ref") not in {r.get("expected_ref") for r in group.get("received", [])}
                    and i.get("correlation") and all((message["correlation"] or {}).get(k) == v for k, v in i["correlation"].items()))
        group.setdefault("received", []).append({"expected_ref": item["ref"], "message_id": message["id"], "outcome": _outcome(message)})
        if _satisfied(group):
            group["status"] = "satisfied"
            if f["status"] == "waiting":
                f["status"] = "running"
        return f, {"outcome": "captured", "group_id": group["id"], "run_id": f.get("run_id"), "satisfied": group["status"] == "satisfied"}

    existing = find_entry((await port.matter_read(ids.mailbox)).content["entries"], message["id"])
    if existing is not None and existing["status"] == "claimed":
        # routed already (the classification is fixed at first acceptance): a retry reports it, changes nothing
        out.update(status="claimed", claimed_by=existing.get("claimed_by"))
        return

    async with _lock(port, cid):
        result = await update_matter(port, ids.runtime, capture, schema="runtime")
        mid = message["id"]
        if result["outcome"] == "nomatch":
            def to_normal(f):
                e = find_entry(f["entries"], mid)
                if e is None or e["status"] != "pending" or e["priority"] == "normal":
                    return None, None
                e["priority"] = "normal"
                return f, None
            await update_matter(port, ids.mailbox, to_normal, schema="mailbox")
            out["priority"] = "normal"
            return

        def claim(f):
            e = find_entry(f["entries"], mid)
            if e is None or (e["status"] == "claimed" and e.get("claimed_by") == result["run_id"]):
                return None, None
            e["status"] = "claimed"
            e["claimed_by"] = result["run_id"]
            return f, None
        await update_matter(port, ids.mailbox, claim, schema="mailbox")
    out.update(status="claimed", claimed_by=result["run_id"], wait_group_id=result["group_id"])
    if result["satisfied"]:
        liveness.notify(cid)
        out["wake"] = await _wake_orphan(port, cid, result["run_id"])


def _lock(port, cid):
    from substrate.locks import critical
    return critical(cid, yielding=port.yielding)


def _outcome(message: Dict[str, Any]) -> str:
    content = message.get("content")
    if message["kind"] == "capacity_response" and isinstance(content, dict):
        response = content.get("response")
        if isinstance(response, dict) and response.get("status") == "error":
            return "error"
    if isinstance(content, dict) and content.get("outcome") in ("success", "error"):
        return content["outcome"]
    return "success"


def _satisfied(group: Dict[str, Any]) -> bool:
    expected, received = group.get("expected", []), group.get("received", [])
    cond = group.get("condition")
    if cond == "all":
        return len(received) >= len(expected)
    if cond == "any":
        return len(received) >= 1
    if cond == "first_success":
        return any(r.get("outcome") == "success" for r in received) or len(received) >= len(expected)
    if cond == "n_of_m":
        return len(received) >= int(group.get("n", 0))
    return False


async def _route_control(port, cid, ids, message, out) -> None:
    action, content = parse_control(message["content"])
    rt = (await port.matter_read(ids.runtime)).content
    verdict = await apply_when_not_running(port, cid, ids, action, content, rt["status"])
    if verdict == DELETED:
        out.update(status="deleted")
        return
    if verdict == APPLIED:
        mid = message["id"]

        def remove(f):
            e = find_entry(f["entries"], mid)
            if e is None or e["status"] != "pending":
                return None, None
            f["entries"] = [x for x in f["entries"] if x["message_id"] != mid]
            return f, None
        await update_matter(port, ids.mailbox, remove, continuity_id=cid, schema="mailbox")
        out["status"] = "consumed"
        return
    # deferred: stays pending; a live waiting run is told that a control is waiting for its safe point
    liveness.notify(cid)


async def _wake_if_needed(port, cid, ids) -> bool:
    rt = (await port.matter_read(ids.runtime)).content
    if rt["status"] == "idle":
        mbx = (await port.matter_read(ids.mailbox)).content
        # pending work, or the interrupted chain of a suspended run that is now resumable
        if any(e["status"] == "pending" for e in mbx["entries"]) or rt.get("current_message_id") or rt.get("active_wait_groups"):
            await _wake(port, cid)
            return True
        return False
    if rt["status"] in ("running", "waiting"):
        liveness.notify(cid)
        return await _wake_orphan(port, cid, rt.get("run_id"))
    return False   # suspended: no automatic wake


async def _wake(port, cid) -> None:
    """Ask for the activation without waiting for the run, and make a terminal wake failure visible when work still needs that wake."""
    async def failed(error) -> None:
        # A timeout or a broken connection has an unknown outcome. Re-read the durable state before claiming that work was not woken.
        rt, mbx = await port.matter_read_many([matter_ids(cid).runtime, matter_ids(cid).mailbox])
        state = rt.content
        work_waits = (state.get("status") == "idle" and
                      (any(e.get("status") == "pending" for e in mbx.content.get("entries", [])) or
                       state.get("current_message_id") or state.get("active_wait_groups")))
        if not work_waits:
            return
        details = (error or {}).get("details") if isinstance(error, dict) else None
        reason = str((details or {}).get("reason", "wake_failed"))
        event = "wake_refused" if reason in ("busy", "loop busy") else "wake_failed"
        content = {"event": event, "reason": reason}
        if event == "wake_refused":
            content["attempts"] = WAKE_ATTEMPTS
        if (details or {}).get("outcome"):
            content["outcome"] = details["outcome"]
        await append_trace(port, cid, "error", content)
    await port.wake(f"{CONTEXT}/pilot.activate", {"continuity_id": cid, "wake": True}, on_refused=failed)


async def _wake_orphan(port, cid, run_id) -> bool:
    """A running/waiting runtime whose run is not alive in this process is an orphan: recover it."""
    if liveness.is_live(cid, run_id):
        return False
    await _wake(port, cid)
    return True
