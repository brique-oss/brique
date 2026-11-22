from __future__ import annotations

from typing import Any, Dict, Tuple

from . import pilot_ref as pilot_ref_module
from .cas import update_matter
from .errors import InvalidRequest
from .ids import CONTEXT, MatterIds

# Control messages carry their action in content:
#   {"action": "suspend"} | {"action": "resume"}
#   {"action": "set_intention", "intention": <any>}
#   {"action": "set_pilot", "pilot_ref": {"context": "/root/agentic", "matter_id": "<id>"}}
#                        or {"action": "set_pilot", "pilot_ref": {"context": "<ctx>", "capacity": "<name>", "limits": {...}}}
#   {"action": "delete", "cascade": <bool, default false>}

ACTIONS = ("suspend", "resume", "set_intention", "set_pilot", "delete")

APPLIED = "applied"      # applied (or consumed as a functional no-op)
DEFERRED = "deferred"    # cannot apply now, stays pending for a safe point
DELETED = "deleted"      # continuity.delete was delegated; terminal


def parse_control(content: Any) -> Tuple[str, Dict[str, Any]]:
    if not isinstance(content, dict):
        raise InvalidRequest("a control message needs an object content")
    action = content.get("action")
    if action not in ACTIONS:
        raise InvalidRequest(f"unknown control action {action!r}", details={"actions": list(ACTIONS)})
    if action == "set_intention" and content.get("intention") is None:
        raise InvalidRequest("set_intention needs content.intention")
    if action == "set_pilot":
        pilot_ref_module.normalize(content.get("pilot_ref"))      # shape only: existence is checked when it is applied
    return action, content


async def set_intention(port, continuity_id: str, ids: MatterIds, intention: Any) -> None:
    def compute(f):
        f["current_intention"] = intention
        return f, None
    await update_matter(port, ids.continuity, compute, continuity_id=continuity_id, schema="continuity")


async def set_pilot(port, continuity_id: str, ids: MatterIds, pilot_ref: Dict[str, Any]) -> None:
    new_ref = pilot_ref_module.normalize(pilot_ref)
    await pilot_ref_module.ensure_exists(port, new_ref)

    def compute(f):
        f["pilot_ref"] = new_ref
        return f, None
    await update_matter(port, ids.continuity, compute, continuity_id=continuity_id, schema="continuity")


async def suspend(port, continuity_id: str, ids: MatterIds, *, allowed: Tuple[str, ...], run_id=None) -> bool:
    """idle (or the owning run) -> suspended, run_id removed. Idempotent when already suspended."""
    def compute(f):
        if f["status"] == "suspended":
            return None, True
        if f["status"] not in allowed or (run_id is not None and f.get("run_id") != run_id):
            return None, False
        f["status"] = "suspended"
        f["run_id"] = None
        return f, True
    return await update_matter(port, ids.runtime, compute, continuity_id=continuity_id, schema="runtime")


async def resume(port, continuity_id: str, ids: MatterIds) -> bool:
    """suspended -> idle. Outside suspended it is a consumed functional no-op (returns False)."""
    def compute(f):
        if f["status"] != "suspended":
            return None, False
        f["status"] = "idle"
        f["run_id"] = None
        return f, True
    return await update_matter(port, ids.runtime, compute, continuity_id=continuity_id, schema="runtime")


async def apply_when_not_running(port, continuity_id: str, ids: MatterIds, action: str, content: Dict[str, Any], status: str) -> str:
    """Immediate application used by mailbox.send. idle: every control; suspended: resume and delete (a suspended continuity has no run: nothing is in
    progress, so deleting it is safe, and a continuity one cannot delete because it is stopped would be stuck for ever)."""
    if status == "suspended":
        if action == "delete":
            await port.call_capacity(f"{CONTEXT}/continuity.delete", {"continuity_id": continuity_id, "cascade": bool(content.get("cascade", False))})
            return DELETED
        if action != "resume":
            return DEFERRED
        await resume(port, continuity_id, ids)
        return APPLIED
    if status != "idle":
        return DEFERRED
    if action == "delete":
        await port.call_capacity(f"{CONTEXT}/continuity.delete", {"continuity_id": continuity_id, "cascade": bool(content.get("cascade", False))})
        return DELETED
    if action == "suspend":
        await suspend(port, continuity_id, ids, allowed=("idle",))
    elif action == "set_intention":
        await set_intention(port, continuity_id, ids, content["intention"])
    elif action == "set_pilot":
        await set_pilot(port, continuity_id, ids, content["pilot_ref"])
    return APPLIED   # resume outside suspended is a consumed no-op


async def apply_at_safe_point(port, continuity_id: str, ids: MatterIds, run_id: str, action: str, content: Dict[str, Any]) -> str:
    """Application by the owning run at a safe point (status running or waiting)."""
    if action == "delete":
        await port.call_capacity(f"{CONTEXT}/continuity.delete", {"continuity_id": continuity_id, "cascade": bool(content.get("cascade", False))})
        return DELETED
    if action == "suspend":
        await suspend(port, continuity_id, ids, allowed=("running", "waiting"), run_id=run_id)
    elif action == "set_intention":
        await set_intention(port, continuity_id, ids, content["intention"])
    elif action == "set_pilot":
        await set_pilot(port, continuity_id, ids, content["pilot_ref"])
    return APPLIED   # resume while running is a consumed no-op
