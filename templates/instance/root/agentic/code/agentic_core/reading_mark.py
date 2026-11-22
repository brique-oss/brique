from __future__ import annotations

from typing import Any, Dict

from substrate.cas import update_matter
from substrate.errors import EngineError, InvalidRequest, NotFound
from substrate.ids import matter_ids
from substrate.port import BriquePort, resolve_params
from substrate.states import matter_descriptor

CAP = "reading.mark"
MATTER_ID = "reading_cursors"
DEFAULT_READER = "user:1"


# <brique:capacity name="reading.mark">
async def reading_mark(runtime, params: dict) -> dict:
    return await run(BriquePort(runtime, CAP), await resolve_params(params))      # a large parameter arrives as a transit reference
# </brique:capacity>


async def run(port, params: Dict[str, Any]) -> Dict[str, Any]:
    """Move the cursor of a reader on a lineage forward, never back."""
    # --- #validate: nothing is written before the call is well formed -----------------------------------------------
    lineage = params.get("lineage_id")
    reader = params.get("reader", DEFAULT_READER)
    seq = params.get("seq")
    if not isinstance(lineage, str) or not lineage.strip():
        raise InvalidRequest("lineage_id is required")
    if not isinstance(reader, str) or not reader.strip():
        raise InvalidRequest("reader must be a non-empty string")
    if isinstance(seq, bool) or not isinstance(seq, int) or seq < 0:
        raise InvalidRequest("seq must be a non-negative integer")

    # --- #exists: a cursor never outlives the conversation it names (a late mark after its deletion would bring one back)
    if not await port.matter_exists(matter_ids(lineage).continuity):
        raise NotFound("no such continuity to mark", details={"continuity_id": lineage})

    # --- #ensure + #advance: the Matter exists, then the cursor is moved under a guarded write ----------------------------
    await _ensure(port)

    def advance(state: Dict[str, Any]):
        cursors = state["cursors"]
        entry = next((c for c in cursors if c["reader"] == reader and c["lineage_id"] == lineage), None)
        if entry is None:
            cursors.append({"reader": reader, "lineage_id": lineage, "seq": seq})
            return state, {"seq": seq, "changed": True}
        if entry["seq"] >= seq:
            return None, {"seq": entry["seq"], "changed": False}           # the cursor does not move: nothing is written
        entry["seq"] = seq
        return state, {"seq": seq, "changed": True}

    # the marks of one Matter are serialized by the short lock of its identifier; expected_revision stays the guarantee
    result = await update_matter(port, MATTER_ID, advance, continuity_id=MATTER_ID, schema="reading")
    return {"lineage_id": lineage, "reader": reader, **result}


async def _ensure(port) -> None:
    if await port.matter_exists(MATTER_ID):
        return
    try:
        await port.matter_create(MATTER_ID, matter_descriptor("reading", MATTER_ID, "Where each reader has read up to in each conversation."), {"cursors": []})
    except EngineError:
        if not await port.matter_exists(MATTER_ID):          # created meanwhile by a concurrent mark: nothing to do
            raise
