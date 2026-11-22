from __future__ import annotations

from typing import Any, Dict

from substrate.cas import update_matter
from substrate.errors import ConsistencyError, InvalidRequest, NotFound, Refused
from substrate.ids import check_continuity_id, matter_ids
from substrate.port import BriquePort, resolve_params
from substrate.states import check_mailbox_consistency, find_entry

CAP = "mailbox.release"
MAX_ATTEMPTS = 3         # a message whose treatment failed this many times is parked


# <brique:capacity name="mailbox.release">
async def mailbox_release(runtime, params: dict) -> dict:
    return await run(BriquePort(runtime, CAP), await resolve_params(params))      # a large parameter arrives as a transit reference
# </brique:capacity>


async def run(port, params: Dict[str, Any]) -> Dict[str, Any]:
    cid = check_continuity_id(params.get("continuity_id"))
    message_id, run_id = params.get("message_id"), params.get("run_id")
    if not message_id or not run_id:
        raise InvalidRequest("message_id and run_id are required")
    failed = params.get("failed")
    if failed is not None and not isinstance(failed, bool):
        raise InvalidRequest("failed must be a boolean")
    ids = matter_ids(cid)

    def compute(f):
        if f.get("continuity_id") != cid:
            raise ConsistencyError("the mailbox does not belong to this continuity", details={"continuity_id": cid})
        entry = find_entry(f["entries"], message_id)
        if entry is None:
            raise NotFound("no mailbox entry for this message", details={"message_id": message_id})
        if entry["status"] == "failed":
            raise Refused("a parked entry is not released again", details={"message_id": message_id})
        if entry["status"] != "claimed":
            raise Refused("a pending entry cannot be released", details={"message_id": message_id})
        if entry.get("claimed_by") != run_id:
            raise Refused("only the claiming run may release", details={"message_id": message_id, "run_id": run_id})
        if failed:
            entry["attempts"] = int(entry.get("attempts", 0)) + 1
        entry["status"] = "failed" if failed and entry["attempts"] >= MAX_ATTEMPTS else "pending"
        entry.pop("claimed_by", None)    # neither pending nor parked keeps an owner
        return f, (int(entry.get("attempts", 0)), entry["status"] == "failed")

    attempts, parked = await update_matter(port, ids.mailbox, compute, continuity_id=cid, schema="mailbox")
    return {"continuity_id": cid, "message_id": message_id, "released": True, "attempts": attempts, "parked": parked}
