from __future__ import annotations

import copy
from typing import Any, Callable, Dict, Optional, Tuple

from wrapper.matter_client import ConcurrencyExhausted, retry_on_conflict

from .errors import ConcurrencyError, RevisionMismatch
from .locks import critical
from .patch import apply_operations
from .schemas import validate_state

# The retry of a guarded write that lost a race (BRIQUE_CAS_ATTEMPTS, BRIQUE_CAS_PAUSE_BASE_MS, BRIQUE_CAS_PAUSE_CAP_MS) is the one of the
# wrapper library: after a conflict, a pause of random length up to a bound that doubles up to a cap, so that writers that lost the same round do
# not come back together.


async def _guarded(step, attempts: Optional[int], key: str, ident: str):
    """Run `step` again when another writer got there first (RevisionMismatch); ConcurrencyError when the attempts are used up."""
    try:
        return await retry_on_conflict(step, conflict=(RevisionMismatch,), attempts=attempts, what=ident)
    except ConcurrencyExhausted as exc:
        raise ConcurrencyError(str(exc), details={key: ident, "attempts": exc.details.get("attempts")}) from exc


# compute(content) -> (new_content | None, result)
#   None as new_content means "nothing to write" (idempotent no-op).
Compute = Callable[[Dict[str, Any]], Tuple[Optional[Dict[str, Any]], Any]]


async def update_matter(port, matter_id: str, compute: Compute, *, continuity_id: Optional[str] = None,
                        schema: Optional[str] = None, attempts: Optional[int] = None):
    """Read, recompute, write guarded by expected_revision; bounded retry on conflict.

    When continuity_id is given, the whole read-recompute-write loop runs inside the short
    critical-section lock of that continuity (never held across anything else).
    Returns whatever compute returned as result.
    """
    async def step():
        view = await port.matter_read(matter_id)
        new_content, result = compute(copy.deepcopy(view.content))
        if new_content is None:
            return result
        if schema:
            validate_state(schema, new_content)
        await port.matter_write(matter_id, content=new_content, expected_revision=view.revision)
        return result

    if continuity_id is None:
        return await _guarded(step, attempts, "matter_id", matter_id)
    async with critical(continuity_id, yielding=port.yielding):
        return await _guarded(step, attempts, "matter_id", matter_id)


# ops(content) -> list of semantic operations, or None / [] when nothing must change.
Ops = Callable[[Dict[str, Any]], Optional[list]]


async def patch_matter(port, matter_id: str, ops: Ops, *, continuity_id: Optional[str] = None, attempts: Optional[int] = None) -> bool:
    """Read the Matter, derive semantic operations from what was read, apply them to the content and
    commit the whole content with one write guarded by expected_revision.

    On a revision conflict the target is re-read and the operations recomputed, so add and remove
    stay idempotent. Returns True when a write was made, False when nothing was needed.
    """
    async def step():
        view = await port.matter_read(matter_id)
        operations = ops(copy.deepcopy(view.content))
        if not operations:
            return False
        await port.matter_write(matter_id, content=apply_operations(view.content, operations), expected_revision=view.revision)
        return True

    if continuity_id is None:
        return await _guarded(step, attempts, "matter_id", matter_id)
    async with critical(continuity_id, yielding=port.yielding):
        return await _guarded(step, attempts, "matter_id", matter_id)


async def patch_structure(port, structure_id: str, ops: Ops, *, attempts: Optional[int] = None) -> bool:
    """Same protocol for a Structure: read the revision, patch with expected_revision, re-read on conflict.

    Creators and deleters of continuities all write the same Structure: left to fight over its revision (one wins per round) the last of forty
    simultaneous ones gave up after 12 attempts. The writers of this wrapper therefore take turns, under a short lock that gives the execution slot
    back while it waits; expected_revision stays the guarantee against a writer from outside, who is met by a new read after a random pause.
    """
    async def step():
        view = await port.structure_read(structure_id)
        operations = ops(copy.deepcopy(view.functional))
        if not operations:
            return False
        await port.structure_patch(structure_id, {"operations": operations}, view.revision)
        return True

    async with critical(f"structure:{structure_id}", yielding=port.yielding):
        return await _guarded(step, attempts, "structure_id", structure_id)
