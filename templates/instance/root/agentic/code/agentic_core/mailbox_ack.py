from __future__ import annotations

from typing import Any, Dict

from substrate.cas import update_matter
from substrate.errors import ConsistencyError, InvalidRequest, NotFound, Refused
from substrate.ids import check_continuity_id, matter_ids
from substrate.port import BriquePort, resolve_params
from substrate.states import check_mailbox_consistency, find_entry

CAP = "mailbox.ack"


# <brique:capacity name="mailbox.ack">
async def mailbox_ack(runtime, params: dict) -> dict:
    return await run(BriquePort(runtime, CAP), await resolve_params(params))      # a large parameter arrives as a transit reference
# </brique:capacity>


async def run(port, params: Dict[str, Any]) -> Dict[str, Any]:
    cid = check_continuity_id(params.get("continuity_id"))
    message_id, run_id = params.get("message_id"), params.get("run_id")
    if not message_id or not run_id:
        raise InvalidRequest("message_id and run_id are required")
    ids = matter_ids(cid)

    def compute(f):
        if f.get("continuity_id") != cid:
            raise ConsistencyError("the mailbox does not belong to this continuity", details={"continuity_id": cid})
        entry = find_entry(f["entries"], message_id)
        if entry is None:
            raise NotFound("no mailbox entry for this message", details={"message_id": message_id})
        if entry["status"] != "claimed":
            raise Refused("a pending entry cannot be acknowledged; it must be claimed first", details={"message_id": message_id})
        if entry.get("claimed_by") != run_id:
            raise Refused("only the claiming run may acknowledge", details={"message_id": message_id, "run_id": run_id})
        f["entries"] = [e for e in f["entries"] if e["message_id"] != message_id]
        return f, True

    # one mailbox write guarded by expected_revision; continuity.messages is never touched
    await update_matter(port, ids.mailbox, compute, continuity_id=cid, schema="mailbox")
    return {"continuity_id": cid, "message_id": message_id, "acknowledged": True}
