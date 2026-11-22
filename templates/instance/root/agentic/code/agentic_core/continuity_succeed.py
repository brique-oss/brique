from __future__ import annotations

from typing import Any, Dict, List

from substrate import continuity_store
from substrate.cas import patch_matter, update_matter
from substrate.errors import ConsistencyError, InvalidRequest, NotFound, Refused
from substrate.ids import CONTEXT, check_continuity_id, matter_ids
from substrate.journal import append_trace
from substrate.ledger import build_ledger, with_ledger
from substrate.port import BriquePort, resolve_params
from substrate.states import find_entry

CAP = "continuity.succeed"
MAX_TRANSFER_SWEEPS = 5      # sweeps of the source mailbox: a message may still arrive while the transfer runs


# <brique:capacity name="continuity.succeed">
async def continuity_succeed(runtime, params: dict) -> dict:
    return await run(BriquePort(runtime, CAP), await resolve_params(params))      # a large parameter arrives as a transit reference
# </brique:capacity>


def successor_of(lineage_id: str, generation: int) -> str:
    return f"{lineage_id}_g{generation + 1}"


async def run(port, params: Dict[str, Any]) -> Dict[str, Any]:
    """A succession has several steps and a transfer: acknowledged as long-running."""
    async with port.keepalive():
        return await _run(port, params)


async def _run(port, params: Dict[str, Any]) -> Dict[str, Any]:
    """Recoverable multi-Matter operation. Order: successor, summary, routing switch, transfer, parent, archive.

    successor_id is set BEFORE the transfer: from then on mailbox.send redirects, so no message can be
    stranded in the source. The archive status is the last write and the commit point of the succession.
    """
    # --- #validate -----------------------------------------------------------------
    cid = check_continuity_id(params.get("continuity_id"))
    summary = params.get("summary")
    if summary is None or (isinstance(summary, str) and not summary.strip()) or summary == {}:
        raise InvalidRequest("summary is required")
    run_id = params.get("run_id")
    ids = matter_ids(cid)
    try:
        source = (await port.matter_read(ids.continuity)).content
        rt = (await port.matter_read(ids.runtime)).content
    except NotFound as exc:
        raise NotFound("the continuity does not exist", details={"continuity_id": cid}) from exc
    if rt.get("active_wait_groups"):
        raise Refused("a continuity with an active wait group cannot succeed", details={"continuity_id": cid})
    if run_id is None and rt["status"] != "idle":
        raise Refused("only an idle continuity can succeed, or the run that owns it", details={"continuity_id": cid, "status": rt["status"]})
    if run_id is not None and rt.get("run_id") != run_id:
        raise Refused("only the run that owns the continuity may make it succeed", details={"continuity_id": cid, "run_id": run_id})

    lineage_id, generation = source["lineage_id"], int(source["generation"])
    successor_id = source.get("successor_id") or successor_of(lineage_id, generation)
    if source.get("status") == "archived" and source.get("successor_id"):
        return {"continuity_id": cid, "successor_id": successor_id, "archived": True, "transferred": []}   # idempotent retry

    # --- #create_successor -----------------------------------------------------------
    await port.call_capacity(f"{CONTEXT}/continuity.create", {
        "continuity_id": successor_id, "pilot_ref": source["pilot_ref"], "initial_intention": source["initial_intention"],
        "current_intention": source["current_intention"], "parent_id": source.get("parent_id"),
        "predecessor_id": cid, "lineage_id": lineage_id, "generation": generation + 1})
    await _check_successor(port, successor_id, cid, lineage_id, generation + 1)

    # --- #summary: the first message of the successor, a fact of its history, never a stimulus ----------
    # what the model wrote is the analysis; the exact list of the calls made is the code's, from the journal of the source
    summary = with_ledger(summary, build_ledger((await continuity_store.get(port, cid)).thread()))
    await append_trace(port, successor_id, "summary", summary, message_id=f"summary_{successor_id}", sender=f"continuity:{cid}")

    # --- #switch_routing: from here mailbox.send redirects to the successor ----------------------
    def link(f):
        if f.get("successor_id") not in (None, successor_id):
            raise ConsistencyError("the continuity already has another successor", details={"successor_id": f.get("successor_id")})
        if f.get("successor_id") == successor_id:
            return None, None
        f["successor_id"] = successor_id
        return f, None
    await update_matter(port, ids.continuity, link, continuity_id=cid, schema="continuity")

    # --- #transfer: pending messages, in seq order, identifiers preserved (idempotent) ---------------
    transferred: List[str] = []
    for _ in range(MAX_TRANSFER_SWEEPS):
        batch = await _pending_in_order(port, cid, ids)
        if not batch:
            break
        for message in batch:
            await _transfer(port, cid, successor_id, ids, message)
            transferred.append(message["id"])

    # --- #parent: the parent now sees the active continuity of the lineage ------------------------------
    parent_id = source.get("parent_id")
    if parent_id is not None and await port.matter_exists(matter_ids(parent_id).continuity):
        def ops(f):
            out = []
            children = f.get("children", [])
            if cid in children:
                out.append({"op": "remove", "path": ["children"], "value": cid})
            if successor_id not in children:
                out.append({"op": "add", "path": ["children"], "value": successor_id})
            return out
        await patch_matter(port, matter_ids(parent_id).continuity, ops, continuity_id=parent_id)

    # --- #archive: the commit point -----------------------------------------------------------------
    def archive(f):
        if f.get("status") == "archived":
            return None, None
        f["status"] = "archived"
        return f, None
    await update_matter(port, ids.continuity, archive, continuity_id=cid, schema="continuity")
    return {"continuity_id": cid, "successor_id": successor_id, "archived": True, "transferred": transferred}


async def _check_successor(port, successor_id, predecessor_id, lineage_id, generation) -> None:
    state = (await port.matter_read(matter_ids(successor_id).continuity)).content
    if state.get("predecessor_id") != predecessor_id or state.get("lineage_id") != lineage_id or state.get("generation") != generation:
        raise ConsistencyError("an existing continuity contradicts the planned successor", details={"successor_id": successor_id})


async def _pending_in_order(port, cid, ids) -> List[Dict[str, Any]]:
    mailbox = (await port.matter_read(ids.mailbox)).content
    journal = await continuity_store.get(port, cid)           # the in-memory continuity: a stimulus is always retained in full
    out = []
    for entry in mailbox["entries"]:
        if entry["status"] != "pending":
            continue
        message = journal.message(entry["message_id"])
        if message is not None:               # a reservation not yet appended is forwarded by mailbox.send itself
            out.append(message)
    return sorted(out, key=lambda m: m["seq"])


async def _transfer(port, cid, successor_id, ids, message: Dict[str, Any]) -> None:
    try:
        await port.call_capacity(f"{CONTEXT}/mailbox.send", {"continuity_id": successor_id, "message": {
            "id": message["id"], "from": message["from"], "to": successor_id, "kind": message["kind"],
            "content": message["content"], "correlation": message.get("correlation") or {}}})
    except Refused:
        pass                                   # already transferred and consumed there: a retry completes the source side

    def consume(f):
        if find_entry(f["entries"], message["id"]) is None:
            return None, None
        f["entries"] = [e for e in f["entries"] if e["message_id"] != message["id"]]
        return f, None
    await update_matter(port, ids.mailbox, consume, continuity_id=cid, schema="mailbox")
