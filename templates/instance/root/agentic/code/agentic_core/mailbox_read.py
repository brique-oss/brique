from __future__ import annotations

from typing import Any, Dict

from substrate import continuity_store
from substrate.cas import update_matter
from substrate.errors import ConsistencyError, InvalidRequest, NotFound, Refused
from substrate.ids import check_continuity_id, matter_ids
from substrate.port import BriquePort, resolve_params
from substrate.states import check_mailbox_consistency

CAP = "mailbox.read"


# <brique:capacity name="mailbox.read">
async def mailbox_read(runtime, params: dict) -> dict:
    return await run(BriquePort(runtime, CAP), await resolve_params(params))      # a large parameter arrives as a transit reference
# </brique:capacity>


async def run(port, params: Dict[str, Any]) -> Dict[str, Any]:
    cid = check_continuity_id(params.get("continuity_id"))
    run_id = params.get("run_id")
    priority = params.get("priority")
    if priority not in ("normal", "control"):
        raise InvalidRequest("priority must be normal or control; expected entries are never read")
    if not run_id:
        raise InvalidRequest("run_id is required")
    kind = params.get("kind")
    if kind is not None and kind != "user":
        raise InvalidRequest("kind accepts only user: the absorption read")
    if kind is not None and priority != "normal":
        raise InvalidRequest("kind user is a read of normal entries")
    ids = matter_ids(cid)

    # --- #authorize: only the current owner of the continuity may claim ---------------
    try:
        runtime_view, mailbox_view = await port.matter_read_many([ids.runtime, ids.mailbox])      # one request, not two
        runtime_state, mailbox_state = runtime_view.content, mailbox_view.content
        journal = await continuity_store.get(port, cid)                                           # the journal: it exists or the continuity does not
    except NotFound as exc:
        raise ConsistencyError("continuity, journal, mailbox or runtime is missing", details=exc.details) from exc
    problem = check_mailbox_consistency(mailbox_state, cid)
    if problem:
        raise ConsistencyError(problem, details={"continuity_id": cid})
    if runtime_state.get("run_id") != run_id or runtime_state.get("status") not in ("running", "waiting"):
        raise Refused("run_id is not the current owner of the continuity", details={"continuity_id": cid, "run_id": run_id})

    # The journal is append-only and immutable: a message seen once stays valid. The order comes from the in-memory continuity.
    seq_of = journal.seq_of

    # nothing eligible in what was just read: the answer is empty, with no claim and no write (the common case of the control sweep)
    def wanted(e) -> bool:
        return e["status"] == "pending" and e["priority"] == priority and journal.has(e["message_id"]) \
            and (kind is None or journal.kind_of(e["message_id"]) == kind)

    if not any(wanted(e) for e in mailbox_state["entries"]):
        return {"continuity_id": cid, "message_id": None, "priority": priority, "seq": None, "status": "empty", "claimed_by": None}

    # --- #select + #claim: one mailbox write guarded by expected_revision ------------------
    def compute(f):
        eligible = [e for e in f["entries"] if wanted(e)]
        if not eligible:
            return None, None
        entry = min(eligible, key=lambda e: seq_of(e["message_id"]))
        entry["status"] = "claimed"
        entry["claimed_by"] = run_id
        return f, dict(entry)

    entry = await update_matter(port, ids.mailbox, compute, continuity_id=cid, schema="mailbox")
    if entry is None:
        return {"continuity_id": cid, "message_id": None, "priority": priority, "seq": None, "status": "empty", "claimed_by": None}
    return {"continuity_id": cid, "message_id": entry["message_id"], "priority": priority, "seq": seq_of(entry["message_id"]),
            "status": "claimed", "claimed_by": run_id}
