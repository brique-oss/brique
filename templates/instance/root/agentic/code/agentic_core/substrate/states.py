from __future__ import annotations

from typing import Any, Dict, List, Optional

from .ids import CONTEXT, MatterIds, now

# schema constraining the content (substance) of each kind of Matter
DATA_SCHEMA = {
    "continuity": "continuity_data",
    "journal": "message",                 # the schema of ONE line
    "mailbox": "mailbox_data",
    "runtime": "runtime_data",
    "pilot_definition": "pilot_definition_data",
    "reading": "reading_data",
}
SCHEMA_REF = {kind: f"{CONTEXT}/schema/{name}" for kind, name in DATA_SCHEMA.items()}


def matter_descriptor(kind: str, name: str, description: str) -> Dict[str, Any]:
    """Descriptor seed for matter.create: a brique-mode Matter whose substance is one JSON document.
    The content itself travels as the substance, never in the descriptor. The journal is line-framed (JSON Lines)."""
    brique = {"kind": "matter", "substance_mode": "brique", "substance_type": "text"}
    fmt = "json"
    if kind == "journal":
        brique["substance_framing"] = "lines"
        fmt = "jsonl"
    return {
        "brique": brique,
        "objective": {"name": name, "type": kind, "description": description},
        "functional": {"format": fmt, "schema_ref": SCHEMA_REF[kind]},
        "subjective": {},
    }


def initial_continuity(continuity_id: str, pilot_ref: Dict[str, Any], initial_intention: Any, parent_id: Optional[str], *,
                       current_intention: Any = None, lineage_id: Optional[str] = None, generation: int = 1,
                       predecessor_id: Optional[str] = None) -> Dict[str, Any]:
    state = {
        "id": continuity_id,
        "created_at": now(),
        "parent_id": parent_id,
        "children": [],
        "initial_intention": initial_intention,
        "current_intention": initial_intention if current_intention is None else current_intention,
        "pilot_ref": dict(pilot_ref),                 # already normalized: a Matter form or a Capacity form
        "status": "active",
        "lineage_id": lineage_id or continuity_id,
        "generation": generation,
        "journal_ref": {"context": CONTEXT, "matter_id": f"journal_{continuity_id}"},
    }
    if predecessor_id is not None:
        state["predecessor_id"] = predecessor_id
    return state


def initial_mailbox(continuity_id: str) -> Dict[str, Any]:
    return {"continuity_id": continuity_id, "entries": []}


def initial_runtime(continuity_id: str) -> Dict[str, Any]:
    return {"continuity_id": continuity_id, "status": "idle", "run_id": None, "current_message_id": None, "active_wait_groups": []}


def find_entry(entries: List[Dict[str, Any]], message_id: str) -> Optional[Dict[str, Any]]:
    for entry in entries:
        if entry.get("message_id") == message_id:
            return entry
    return None


def find_message(messages: List[Dict[str, Any]], message_id: str) -> Optional[Dict[str, Any]]:
    for message in messages:
        if message.get("id") == message_id:
            return message
    return None


def next_seq(messages: List[Dict[str, Any]]) -> int:
    """seq derives exclusively from continuity.messages: 1 when empty, otherwise max(seq) + 1."""
    return 1 if not messages else max(int(m.get("seq", 0)) for m in messages) + 1


def structure_reference(matter_id: str) -> Dict[str, Any]:
    """Canonical reference of a continuity inside the continuities Structure."""
    return {"kind": "matter_ref", "context": CONTEXT, "matter_id": matter_id, "role": "continuity"}


def check_mailbox_consistency(mailbox_state: Dict[str, Any], continuity_id: str) -> Optional[str]:
    """Returns a description when the mailbox ownership state contradicts itself, else None."""
    if mailbox_state.get("continuity_id") != continuity_id:
        return "the mailbox does not belong to this continuity"
    seen = set()
    for e in mailbox_state.get("entries", []):
        if e["message_id"] in seen:
            return f"duplicate entry for message {e['message_id']}"
        seen.add(e["message_id"])
        if e["status"] == "claimed" and not e.get("claimed_by"):
            return f"claimed entry {e['message_id']} has no owner"
        if e["status"] in ("pending", "failed") and e.get("claimed_by"):
            return f"{e['status']} entry {e['message_id']} still has an owner"
    return None
