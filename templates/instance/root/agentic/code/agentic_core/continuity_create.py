from __future__ import annotations

from typing import Any, Dict

from substrate import continuity_store
from substrate import pilot_ref as pilot_refs
from substrate.cas import patch_matter, patch_structure
from substrate.errors import AgenticError, ConsistencyError, EngineError, InvalidRequest, NotFound
from substrate.journal import append_trace
from substrate.ids import CONTEXT, FRAMEWORK_CONTEXT, FRAMEWORK_MATTER, STRUCTURE_ID, check_continuity_id, matter_ids
from substrate.locks import critical
from substrate.port import BriquePort, resolve_params
from substrate.schemas import validate_state
from substrate.states import initial_continuity, initial_mailbox, initial_runtime, matter_descriptor, structure_reference

CAP = "continuity.create"


# <brique:capacity name="continuity.create">
async def continuity_create(runtime, params: dict) -> dict:
    return await run(BriquePort(runtime, CAP), await resolve_params(params))      # a large parameter arrives as a transit reference
# </brique:capacity>


async def run(port, params: Dict[str, Any]) -> Dict[str, Any]:
    """Recoverable multi-Matter operation. Order: journal, continuity (the header, which references the journal), mailbox,
    runtime, parent, Structure.

    Each step accepts "already realized in the expected state" as an idempotent success and
    fails with a consistency failure when the existing Matter or relation contradicts the request.
    """
    # --- #prepare ----------------------------------------------------------------
    cid = check_continuity_id(params.get("continuity_id"))
    ids = matter_ids(cid)
    pilot_ref = pilot_refs.normalize(params.get("pilot_ref"))
    if params.get("initial_intention") is None:
        raise InvalidRequest("initial_intention is required")
    parent_id = params.get("parent_id") or None
    if parent_id is not None:
        parent_id = check_continuity_id(parent_id)
        if not await port.matter_exists(matter_ids(parent_id).continuity):
            raise NotFound("parent continuity does not exist", details={"parent_id": parent_id})
    await pilot_refs.ensure_exists(port, pilot_ref)
    frame = await port.matter_read_text(FRAMEWORK_CONTEXT, FRAMEWORK_MATTER)       # before any effect: a missing frame refuses the creation
    if not frame.strip():
        raise InvalidRequest("the cognitive frame of the pilots is empty", details={"context": FRAMEWORK_CONTEXT, "matter_id": FRAMEWORK_MATTER})

    # lineage fields: only a succession passes them; a plain creation starts a new lineage
    predecessor_id = params.get("predecessor_id") or None
    lineage_id = params.get("lineage_id") or None
    generation = params.get("generation", 1)
    if predecessor_id is not None:
        predecessor_id = check_continuity_id(predecessor_id)
        lineage_id = check_continuity_id(lineage_id or predecessor_id)
    if not isinstance(generation, int) or isinstance(generation, bool) or generation < 1:
        raise InvalidRequest("generation must be a positive integer")
    wanted = initial_continuity(cid, pilot_ref, params["initial_intention"], parent_id, current_intention=params.get("current_intention"),
                                lineage_id=lineage_id, generation=generation, predecessor_id=predecessor_id)
    states = (("continuity", ids.continuity, wanted), ("mailbox", ids.mailbox, initial_mailbox(cid)), ("runtime", ids.runtime, initial_runtime(cid)))
    created = False

    # --- #create_core: four Matters, in order: the journal first, so journal_ref never designates an absent Matter ---
    async with critical(cid, yielding=port.yielding):
        if await _ensure_journal(port, ids.journal, cid):
            continuity_store.forget(port, cid)          # a new journal: no object of a previous incarnation may survive
            created = True
        for kind, matter_id, state in states:
            validate_state(kind, state)
            created = await _ensure_matter(port, kind, matter_id, state, cid) or created

    # --- #frame: the first line of the journal, the cognitive frame copied as it is now (the history keeps what was given, whatever the Matter
    # becomes); the same id is recorded once, so a retry is the same creation. Outside the lock: appending takes it.
    await append_trace(port, cid, "framework", {"text": frame, "source": f"{FRAMEWORK_CONTEXT}/{FRAMEWORK_MATTER}"},
                       message_id=f"framework_{cid}", sender=CONTEXT)

    # --- #relate: parent relation, then Structure relation --------------------------
    if parent_id is not None:
        child_marker = cid
        created = await patch_matter(port, matter_ids(parent_id).continuity,
                                     lambda f: [] if child_marker in f.get("children", []) else [{"op": "add", "path": ["children"], "value": child_marker}],
                                     continuity_id=parent_id) or created
    reference = structure_reference(ids.continuity)
    created = await patch_structure(port, STRUCTURE_ID,
                                    lambda f: [] if reference in f.get("items", []) else [{"op": "add", "path": ["functional", "items"], "value": reference}]) or created
    return {"continuity_id": cid, "created": created}


async def _ensure_journal(port, matter_id: str, cid: str) -> bool:
    """Create the empty journal (JSON Lines), or accept the existing one as the same creation (idempotent success)."""
    if not await port.matter_exists(matter_id):
        try:
            await port.matter_create_empty(matter_id, matter_descriptor("journal", matter_id, f"journal of continuity {cid}"))
            return True
        except EngineError:
            if not await port.matter_exists(matter_id):
                raise
    await port.matter_stat(matter_id)
    return False


async def _ensure_matter(port, kind: str, matter_id: str, state: Dict[str, Any], cid: str) -> bool:
    """Create the Matter, or verify an existing one is the same creation (idempotent success)."""
    if not await port.matter_exists(matter_id):
        try:
            await port.matter_create(matter_id, matter_descriptor(kind, matter_id, f"{kind} of continuity {cid}"), state)
            return True
        except EngineError:
            if not await port.matter_exists(matter_id):
                raise
    _verify(kind, matter_id, (await port.matter_read(matter_id)).content, state)
    return False


# immutable fields that must agree for an existing Matter to count as the same creation
_IDENTITY = {"continuity": ("id", "initial_intention", "pilot_ref", "parent_id", "lineage_id", "generation", "predecessor_id", "journal_ref"), "mailbox": ("continuity_id",), "runtime": ("continuity_id",)}


def _verify(kind: str, matter_id: str, existing: Dict[str, Any], wanted: Dict[str, Any]) -> None:
    for key in _IDENTITY[kind]:
        if existing.get(key) != wanted.get(key):
            raise ConsistencyError(f"existing {matter_id} contradicts the requested creation on {key!r}",
                                   details={"matter_id": matter_id, "field": key})
    validate_state(kind, existing)
