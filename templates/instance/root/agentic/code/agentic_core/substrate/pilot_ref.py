"""What decides for a continuity: a pilot_definition Matter or a Capacity that carries a plan."""
from __future__ import annotations

import posixpath
from typing import Any, Dict, Optional

from .errors import AgenticError, InvalidRequest, NotFound
from .ids import CONTEXT

DEFAULT_MAX_INFERENCES = 100
LIMITS = {"max_inferences_per_message", "timeout_s", "wait_timeout_s", "context_tokens", "compaction_advised_ratio", "compaction_required_ratio"}
DEFAULT_TIMEOUT_S = 600.0


def is_capacity(ref: Dict[str, Any]) -> bool:
    return bool(ref.get("capacity"))


def _context_path(value: Any) -> Optional[str]:
    """The canonical absolute path of a context (no wildcard, no . or .., no double slash), or None."""
    if not isinstance(value, str):
        return None
    path = value.strip().rstrip("/") or "/"
    return path if path.startswith("/") and "*" not in path and posixpath.normpath(path) == path else None


def normalize(raw: Any) -> Dict[str, Any]:
    """Validate and normalize a pilot_ref. Exactly one of matter_id and capacity."""
    if not isinstance(raw, dict):
        raise InvalidRequest("pilot_ref must be an object")
    has_matter, has_capacity = bool(str(raw.get("matter_id") or "").strip()), bool(str(raw.get("capacity") or "").strip())
    if has_matter == has_capacity:
        raise InvalidRequest("pilot_ref designates exactly one of matter_id (a pilot_definition) and capacity (a Capacity pilot)")
    if has_matter:
        context = _context_path(raw.get("context", CONTEXT))
        if context is None:
            raise InvalidRequest("pilot_ref.context must be the absolute path of the context of the pilot_definition")
        for key in ("limits", "allowed_capacities", "forbidden_capacities", "allowed_contexts", "forbidden_contexts"):
            if raw.get(key) is not None:
                raise InvalidRequest(f"pilot_ref.{key} belongs to a Capacity pilot: a pilot_definition carries its own")
        return {"context": context, "matter_id": str(raw["matter_id"]).strip()}
    context, capacity = raw.get("context"), str(raw["capacity"]).strip()
    if not isinstance(context, str) or not context.startswith("/") or context.count("/") < 2:
        raise InvalidRequest("pilot_ref.context must be the absolute path of the Capacity context")
    if "/" in capacity:
        raise InvalidRequest("pilot_ref.capacity is a Capacity name, without any path")
    out: Dict[str, Any] = {"context": context, "capacity": capacity}
    for key in ("allowed_capacities", "forbidden_capacities"):
        listed = raw.get(key)
        if listed is not None:
            if not (isinstance(listed, list) and all(isinstance(p, str) and p.strip() for p in listed)):
                raise InvalidRequest(f"pilot_ref.{key} must be a list of patterns")
            out[key] = [p.strip() for p in listed]
    for key in ("allowed_contexts", "forbidden_contexts"):
        listed = raw.get(key)
        if listed is not None:
            if not (isinstance(listed, list) and all(_context_path(c) is not None for c in listed)):
                raise InvalidRequest(f"pilot_ref.{key} must be a list of canonical absolute context paths")
            out[key] = [_context_path(c) for c in listed]
    limits = raw.get("limits")
    if limits is not None:
        if not isinstance(limits, dict) or set(limits) - LIMITS:
            raise InvalidRequest(f"pilot_ref.limits accepts {', '.join(sorted(LIMITS))}")
        for key, value in limits.items():
            if isinstance(value, bool) or not isinstance(value, (int, float)) or value <= 0:
                raise InvalidRequest(f"pilot_ref.limits.{key} must be a positive number")
        for key in ("max_inferences_per_message", "context_tokens"):
            if key in limits and not isinstance(limits[key], int):
                raise InvalidRequest(f"pilot_ref.limits.{key} must be an integer")
        for key in ("compaction_advised_ratio", "compaction_required_ratio"):
            if key in limits and limits[key] > 1:
                raise InvalidRequest(f"pilot_ref.limits.{key} is a part of the window: at most 1")
        out["limits"] = dict(limits)
    return out


async def ensure_exists(port, ref: Dict[str, Any]) -> None:
    """The referenced Matter or Capacity must exist and BE A PILOT (checked at creation and at set_pilot).

    What runs is always a capacity: pilot.activate reads a Matter as its definition, a Capacity is the plan itself. So a Matter must be a pilot_definition
    (a Matter of any other kind is no pilot), and a Capacity must declare itself one (an ordinary capacity is invoked, never launched as a pilot)."""
    if is_capacity(ref):
        # read.meaning reads the elements of the addressed context (read.capacity only knows the engine-native ones); the objective section holds the declaration
        try:
            answer = await port.call_capacity(f"{ref['context']}/read.meaning",
                                              {"input": [{"element_kind": "capacity", "element_name": ref["capacity"], "sections": ["objective"]}]}, to_type="reflexive")
        except NotFound as exc:
            raise InvalidRequest("pilot_ref designates no existing Capacity", details={"pilot_ref": ref}) from exc
        except AgenticError as exc:
            raise InvalidRequest(f"pilot_ref Capacity cannot be read: {exc.message}", details={"pilot_ref": ref}) from exc
        results = answer.get("result") or []
        if not (results and results[0].get("ok") is True):
            raise InvalidRequest("pilot_ref designates no existing Capacity", details={"pilot_ref": ref})
        objective = ((results[0].get("descriptor") or {}).get("objective")) or {}
        if not isinstance((objective.get("agentic") or {}).get("pilot"), (dict, str, list)):
            raise InvalidRequest(
                f"pilot_ref designates the Capacity {ref['capacity']!r}, which does not declare itself a pilot: an ordinary capacity is invoked, not launched. "
                "A plan of a pilot declares objective.agentic.pilot in its descriptor.", details={"pilot_ref": ref, "missing": "objective.agentic.pilot"})
    else:
        if not await port.matter_exists(ref["matter_id"], ref.get("context")):
            raise InvalidRequest("pilot_ref designates no existing pilot_definition", details={"pilot_ref": ref})
        kind = (await port.matter_objective(ref["matter_id"], ref.get("context"))).get("type")
        if kind != "pilot_definition":
            raise InvalidRequest(f"pilot_ref designates the Matter {ref['matter_id']!r}, which is not a pilot_definition (its objective.type is {kind!r}): "
                                 "only a pilot_definition can be run as a pilot.", details={"pilot_ref": ref, "type": kind})


def max_inferences(ref: Dict[str, Any]) -> int:
    value = (ref.get("limits") or {}).get("max_inferences_per_message")
    return min(int(value), DEFAULT_MAX_INFERENCES) if isinstance(value, int) and value > 0 else DEFAULT_MAX_INFERENCES


def timeout_s(ref: Dict[str, Any]) -> float:
    value = (ref.get("limits") or {}).get("timeout_s")
    return float(value) if isinstance(value, (int, float)) and not isinstance(value, bool) and value > 0 else DEFAULT_TIMEOUT_S
