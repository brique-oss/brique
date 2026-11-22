from __future__ import annotations

from typing import Any, Dict, List

from substrate import continuity_store, inference_window
from substrate.cas import patch_matter, patch_structure, update_matter
from substrate.errors import AgenticError, ConsistencyError, NotFound, Refused
from substrate.ids import CONTEXT, STRUCTURE_ID, check_continuity_id, matter_ids
from substrate.locks import critical
from substrate.port import BriquePort, resolve_params
from substrate.states import structure_reference

CAP = "continuity.delete"
CURSORS = "reading_cursors"      # the Matter of reading.mark: where each reader has read up to in each conversation


# <brique:capacity name="continuity.delete">
async def continuity_delete(runtime, params: dict) -> dict:
    return await run(BriquePort(runtime, CAP), await resolve_params(params))      # a large parameter arrives as a transit reference
# </brique:capacity>


async def run(port, params: Dict[str, Any]) -> Dict[str, Any]:
    """A cascade deletion walks a whole tree: acknowledged as long-running."""
    async with port.keepalive():
        return await _run(port, params)


async def _run(port, params: Dict[str, Any]) -> Dict[str, Any]:
    """Recoverable multi-Matter operation.

    Order: children, parent relation, Structure reference, runtime, mailbox, journal, continuity (the header) LAST.
    Every step treats "already absent / already clean" as an idempotent success, and the
    absence of the continuity Matter marks completion. No lock is held across the recursive
    call on a child, and the parent state is read again when the call returns.
    """
    cid = check_continuity_id(params.get("continuity_id"))
    cascade = bool(params.get("cascade", False))
    ids = matter_ids(cid)

    # --- #lineage: a continuity that belongs to a chain of successions goes with its whole lineage, or not at all
    if await port.matter_exists(ids.continuity):
        head = (await port.matter_read(ids.continuity)).content
        if head.get("successor_id") or head.get("predecessor_id"):
            if not cascade:
                raise Refused("the continuity belongs to a lineage of successions: delete it with cascade, which removes the whole lineage",
                              details={"continuity_id": cid, "predecessor_id": head.get("predecessor_id"), "successor_id": head.get("successor_id")})
            members = await _lineage_members(port, cid, head)
            deleted_members: List[str] = []
            for member in members:                        # newest first; cid last
                deleted_members.append(member)
                await _delete_one(port, member, True)
            result = await _delete_one(port, cid, True)
            result["deleted_children"] = deleted_members + result.get("deleted_children", [])
            return result
    return await _delete_one(port, cid, cascade)


async def _lineage_members(port, cid: str, head: Dict[str, Any]) -> List[str]:
    """Every other member of the lineage, newest generation first."""
    found: Dict[str, int] = {}
    node, steps = head, 0
    while node.get("predecessor_id") and steps < 10_000:       # backwards
        previous = node["predecessor_id"]
        if previous in found or not await port.matter_exists(matter_ids(previous).continuity):
            break
        node = (await port.matter_read(matter_ids(previous).continuity)).content
        found[previous] = int(node.get("generation", 0))
        steps += 1
    node, steps = head, 0
    while node.get("successor_id") and steps < 10_000:         # forwards
        following = node["successor_id"]
        if following in found or not await port.matter_exists(matter_ids(following).continuity):
            break
        node = (await port.matter_read(matter_ids(following).continuity)).content
        found[following] = int(node.get("generation", 0))
        steps += 1
    found.pop(cid, None)
    return sorted(found, key=lambda m: -found[m])


async def _delete_one(port, cid: str, cascade: bool) -> Dict[str, Any]:
    ids = matter_ids(cid)

    if not await port.matter_exists(ids.continuity):
        # fully deleted, or a leftover of an interrupted deletion: finish it, deleted stays False
        await _clean_cursors(port, cid)
        await _delete_owned(port, cid, ids)
        await _clean_structure(port, ids)
        return {"continuity_id": cid, "deleted": False, "cascade": cascade, "detached_children": [], "deleted_children": []}

    state = (await port.matter_read(ids.continuity)).content
    # the owned bundle must be coherent before anything is removed
    for owned in (ids.mailbox, ids.runtime):
        if await port.matter_exists(owned):
            if (await port.matter_read(owned)).content.get("continuity_id") != cid:
                raise ConsistencyError(f"{owned} does not belong to continuity {cid}", details={"matter_id": owned})
    if state.get("id") != cid:
        raise ConsistencyError("the continuity Matter carries another identity", details={"continuity_id": cid})
    children: List[str] = list(state.get("children", []))
    parent_id = state.get("parent_id")
    detached: List[str] = []
    deleted_children: List[str] = []

    # --- #children -------------------------------------------------------------------
    for child in children:
        if cascade:
            result = await port.call_capacity(f"{CONTEXT}/continuity.delete", {"continuity_id": child, "cascade": True})
            deleted_children.append(child)
            deleted_children.extend(result.get("deleted_children", []))
        else:
            child_matter = matter_ids(child).continuity
            if not await port.matter_exists(child_matter):
                continue
            await patch_matter(port, child_matter,
                               lambda f: [{"op": "delete", "path": ["parent_id"]}] if f.get("parent_id") == cid else [],
                               continuity_id=child)
            detached.append(child)

    # --- #clean_relations --------------------------------------------------------------
    if parent_id:
        parent_matter = matter_ids(parent_id).continuity
        if await port.matter_exists(parent_matter):
            await patch_matter(port, parent_matter,
                               lambda f: [{"op": "remove", "path": ["children"], "value": cid}] if cid in f.get("children", []) else [],
                               continuity_id=parent_id)
    await _clean_structure(port, ids)
    await _clean_cursors(port, cid)

    # --- #delete_owned_state: runtime, mailbox, journal, continuity (the header) last ------------
    await _delete_owned(port, cid, ids)
    return {"continuity_id": cid, "deleted": True, "cascade": cascade, "detached_children": detached, "deleted_children": deleted_children}


async def _clean_structure(port, ids) -> None:
    reference = structure_reference(ids.continuity)
    try:
        await patch_structure(port, STRUCTURE_ID,
                              lambda f: [{"op": "remove", "path": ["functional", "items"], "value": reference}] if reference in f.get("items", []) else [])
    except NotFound:
        return   # the canonical Structure does not exist: nothing to clean


async def _clean_cursors(port, cid: str) -> None:
    """No reading cursor outlives the conversation it names (before the header goes, so that a rerun finishes the cleaning). Nothing is written when
    the Matter does not exist or holds no cursor of this continuity."""
    if not await port.matter_exists(CURSORS):
        return

    def drop(state: Dict[str, Any]):
        kept = [c for c in state.get("cursors", []) if c.get("lineage_id") != cid]
        if len(kept) == len(state.get("cursors", [])):
            return None, 0
        state["cursors"] = kept
        return state, 0
    try:
        await update_matter(port, CURSORS, drop, continuity_id=CURSORS, schema="reading")
    except NotFound:
        return                                           # deleted meanwhile: nothing left to clean


async def _delete_owned(port, cid: str, ids) -> None:
    # runtime first: pilot.activate and mailbox.send fail on a missing runtime, so no new run can start
    async with critical(cid, yielding=port.yielding):
        for matter_id in (ids.runtime, ids.mailbox, ids.journal, ids.continuity):
            if await port.matter_exists(matter_id):
                try:
                    await port.matter_delete(matter_id)
                except NotFound:
                    pass
        continuity_store.forget(port, cid)               # the in-memory continuity goes with its Matters
        inference_window.clear(cid)                      # a reservation never outlives the continuity
