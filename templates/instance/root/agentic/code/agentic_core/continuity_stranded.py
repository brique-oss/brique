from __future__ import annotations

from typing import Any, Dict, List, Optional

from substrate import continuity_store, liveness
from substrate.errors import AgenticError, ConsistencyError, InvalidRequest, NotFound
from substrate.ids import STRUCTURE_ID, check_continuity_id, matter_ids
from substrate.port import BriquePort, resolve_params

CAP = "continuity.stranded"
PREFIX = "continuity_"


# <brique:capacity name="continuity.stranded">
async def continuity_stranded(runtime, params: dict) -> dict:
    return await run(BriquePort(runtime, CAP), await resolve_params(params))      # a large parameter arrives as a transit reference
# </brique:capacity>


async def run(port, params: Dict[str, Any]) -> Dict[str, Any]:
    """The continuities that cannot go on by themselves. It only reads."""
    wanted = params.get("continuity_ids")
    if wanted is not None:
        if not isinstance(wanted, list):
            raise InvalidRequest("continuity_ids must be a list of continuity identifiers")
        wanted = {check_continuity_id(c) for c in wanted}

    # --- #list: the canonical Structure, in its order ------------------------------------------------
    try:
        structure = await port.structure_read(STRUCTURE_ID)
    except NotFound as exc:
        raise ConsistencyError("the Structure of the continuities does not exist", details={"structure_id": STRUCTURE_ID}) from exc
    cids: List[str] = []
    for ref in structure.functional.get("items") or []:
        matter_id = str((ref or {}).get("matter_id") or "")
        if matter_id.startswith(PREFIX) and (wanted is None or matter_id[len(PREFIX):] in wanted):
            cids.append(matter_id[len(PREFIX):])

    # --- #examine: one continuity at a time, one that cannot be read does not stop the others ----------
    stranded: List[str] = []
    reasons: Dict[str, str] = {}
    skipped: List[Dict[str, Any]] = []
    for cid in cids:
        try:
            reason = await _why(port, cid)
        except AgenticError as exc:
            skipped.append({"continuity_id": cid, "error": exc.message})
            continue
        if reason:
            stranded.append(cid)
            reasons[cid] = reason
    return {"continuity_ids": stranded, "reasons": reasons, "examined": len(cids), "skipped": skipped}


async def _why(port, cid: str) -> Optional[str]:
    """Why this continuity needs a run, or None when it does not."""
    ids = matter_ids(cid)
    try:
        header, mailbox, runtime = await port.matter_read_many([ids.continuity, ids.mailbox, ids.runtime])
        journal = await continuity_store.get(port, cid)
    except NotFound as exc:
        raise ConsistencyError("continuity, journal, mailbox or runtime is missing", details=exc.details) from exc
    if header.content.get("status") == "archived":
        return None
    rt = runtime.content
    status = rt.get("status")
    if status == "suspended":
        return None
    if status in ("running", "waiting"):
        return None if liveness.is_live(cid, rt.get("run_id")) else f"the run {rt.get('run_id')} of a {status} continuity is not alive"
    if any(e["status"] == "pending" and journal.has(e["message_id"]) for e in mailbox.content["entries"]):
        return "a message is pending and no run takes it"
    if rt.get("current_message_id"):
        return "a message was in progress and its run is gone"
    if rt.get("active_wait_groups"):
        return "a wait group is open and no run holds it"
    return None
