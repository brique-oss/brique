from __future__ import annotations

import asyncio
import copy
import json
from typing import Any, Dict, List, Optional

from substrate import continuity_store
from substrate.errors import InvalidRequest, NotFound
from substrate.ids import check_continuity_id, matter_ids
from substrate.continuity_store import size_of
from substrate.kinds import BUDGET_PROTECTED_KINDS, PINNED_KINDS
from substrate.port import BriquePort, resolve_params
from substrate.schemas import load_schema, validate_value

CAP = "continuity.project"
DEFINITION_KEYS = [k for k in load_schema("projection_definition")["functional"] if not k.startswith("_")]
LIST_KEYS = ("include_kinds", "exclude_kinds", "message_ids", "continuity_ids")


# <brique:capacity name="continuity.project">
async def continuity_project(runtime, params: dict) -> dict:
    return await run(BriquePort(runtime, CAP), await resolve_params(params))      # a large parameter arrives as a transit reference
# </brique:capacity>


async def run(port, params: Dict[str, Any]) -> Dict[str, Any]:
    """Deterministic, non-cognitive view of a continuity. Reads only; never mutates anything.

    The source is the continuity (the in-memory object built from the journal): by default the whole thread of the LLM.
    The default projection of the pilot is a value supplied by the caller; this capacity never reads pilot_definition.
    """
    cid = check_continuity_id(params.get("continuity_id"))
    requested = params.get("projection") or {}
    mode = params.get("mode") or "merge"
    if mode not in ("merge", "replace"):
        raise InvalidRequest("mode must be merge or replace")
    validate_value("projection_definition", requested)
    default = params.get("default_projection")
    if default is not None:
        validate_value("projection_definition", default)
    budget = params.get("budget", requested.get("budget"))
    if budget is not None and (not isinstance(budget, int) or isinstance(budget, bool) or budget <= 0):
        raise InvalidRequest("budget must be a positive integer (maximum size of the selected messages, in characters)")

    ids = matter_ids(cid)
    definition = merge_definitions(copy.deepcopy(default) if default else {}, requested) if mode == "merge" else copy.deepcopy(requested)
    try:
        header, journal = await asyncio.gather(port.matter_read(ids.continuity), continuity_store.get(port, cid))     # the contract resolves both
        continuity = header.content                                                                                 # the header: parent, children
    except NotFound as exc:
        raise NotFound("the continuity does not exist", details={"continuity_id": cid}) from exc

    # --- optional explicit inter-continuity composition -----------------------------------
    related_ids: List[str] = []
    if definition.get("include_parent") and continuity.get("parent_id"):
        related_ids.append(continuity["parent_id"])
    if definition.get("include_children"):
        related_ids.extend(continuity.get("children", []))
    related_ids.extend(definition.get("continuity_ids", []))
    related_ids = [r for r in dict.fromkeys(related_ids) if r != cid]

    sizes: Dict[str, int] = dict(journal.sizes)
    groups = [(cid, await _source(port, cid, journal, definition))]
    for related_id in related_ids:
        try:
            other = await continuity_store.get(port, related_id)
            groups.append((related_id, await _source(port, related_id, other, definition)))
            sizes.update({f"{related_id}\0{k}": v for k, v in other.sizes.items()})
        except NotFound as exc:
            raise NotFound("an explicitly referenced continuity does not exist", details={"continuity_id": related_id}) from exc

    # the pinned lines (the cognitive frame) are set aside: no filter, limit or budget touches them; they come back first
    pinned = [m for m in journal.thread() if m["kind"] in PINNED_KINDS]
    selected = [(g, select([m for m in msgs if m["kind"] not in PINNED_KINDS], definition)) for g, msgs in groups]      # the limit never counts the frame
    effective_budget = budget if budget is not None else definition.get("budget")
    selected, omitted = apply_budget(selected, effective_budget, sizes, cid)
    selected = [(g, pinned + msgs if g == cid else msgs) for g, msgs in selected]

    selected = [(g, copy.deepcopy(msgs)) for g, msgs in selected]       # copy only what survived the limit and the budget
    own = [m for g, msgs in selected if g == cid for m in msgs]
    related = [{"continuity_id": g, "messages": msgs} for g, msgs in selected if g != cid]
    view: Dict[str, Any] = {"definition": definition, "messages": own}
    if related:
        view["related"] = related
    selected_ids = [m["id"] for _, msgs in selected for m in msgs]
    return {"continuity_id": cid, "projection": view, "selected_message_ids": selected_ids, "omitted_count": omitted, "budget": effective_budget}


async def _source(port, cid: str, journal, definition: Dict[str, Any]) -> List[Dict[str, Any]]:
    """The lines a projection chooses from. By default the continuity: the thread of the LLM. A projection that asks for the
    follow-up kinds (event, projection, error) is served from the journal itself; control and explicit message ids from the
    retained lines."""
    kinds = set(definition.get("include_kinds") or [])
    if kinds & {"event", "projection", "error"}:
        return await continuity_store.all_messages(port, cid)
    if "control" in kinds or definition.get("message_ids"):
        return journal.messages()
    return journal.thread()


def merge_definitions(default: Dict[str, Any], requested: Dict[str, Any]) -> Dict[str, Any]:
    """merge: the request completes the default projection and never silently removes its base."""
    out = copy.deepcopy(default)
    for key, value in requested.items():
        if key in LIST_KEYS:
            out[key] = list(dict.fromkeys(list(out.get(key, [])) + list(value)))
        elif key == "correlation":
            out[key] = dict(out.get(key, {}), **value)
        else:
            out[key] = copy.deepcopy(value)
    return out


def _as_list(value: Any) -> Optional[List[Any]]:
    if value is None:
        return None
    return list(value) if isinstance(value, (list, tuple)) else [value]


def select(messages: List[Dict[str, Any]], d: Dict[str, Any]) -> List[Dict[str, Any]]:
    """Filter, select and order by canonical seq. No interpretation of the content.

    The sources are already in seq order (journal order), so nothing is sorted. The messages returned are the shared ones
    of the in-memory continuity: the caller copies what it keeps (see run), after the limit and the budget.
    """
    include, exclude = d.get("include_kinds"), set(d.get("exclude_kinds") or [])
    senders, targets, ids = _as_list(d.get("from")), _as_list(d.get("to")), d.get("message_ids")
    wanted_corr = d.get("correlation") or {}
    out = []
    for m in messages:
        if include and m["kind"] not in include:
            continue
        if m["kind"] in exclude:
            continue
        if senders is not None and m.get("from") not in senders:
            continue
        if targets is not None and m.get("to") not in targets:
            continue
        if d.get("min_seq") is not None and m["seq"] < d["min_seq"]:
            continue
        if d.get("max_seq") is not None and m["seq"] > d["max_seq"]:
            continue
        if ids and m["id"] not in ids:
            continue
        corr = m.get("correlation") or {}
        if any(corr.get(k) != v for k, v in wanted_corr.items()):
            continue
        out.append(m)
    limit, selection = d.get("limit"), d.get("selection") or "all"
    if limit is not None and selection == "first":
        out = out[:limit]
    elif limit is not None:             # all (limited) and last both keep the most recent
        out = out[-limit:] if limit else []
    return out


def apply_budget(selected, budget, sizes=None, own_cid=None):
    """Reduce the selection deterministically: drop the oldest messages until the size fits (a protected kind is counted, never dropped).

    The size of a retained message is the one computed once when it entered the in-memory continuity.
    """
    if budget is None:
        return selected, 0
    selected = [(g, list(msgs)) for g, msgs in selected]

    def size_of_message(group: str, m: Dict[str, Any]) -> int:
        key = m["id"] if group == own_cid else f"{group}\0{m['id']}"
        known = (sizes or {}).get(key)
        return known if known is not None else size_of(m)

    candidates = sorted((m["seq"], i, j) for i, (_, msgs) in enumerate(selected) for j, m in enumerate(msgs))
    total = sum(size_of_message(g, m) for g, msgs in selected for m in msgs)
    dropped = set()
    for seq, i, j in candidates:
        if total <= budget:
            break
        if selected[i][1][j].get("kind") in BUDGET_PROTECTED_KINDS:
            continue                                          # the summary of a succession stays: the reduction goes on with the next oldest
        dropped.add((i, j))
        total -= size_of_message(selected[i][0], selected[i][1][j])
    kept = [(g, [m for j, m in enumerate(msgs) if (i, j) not in dropped]) for i, (g, msgs) in enumerate(selected)]
    return kept, len(dropped)
