"""Pure rule matching of the fake LLM. No Brique access: testable on its own.

Rule: {"when": {...}, "then": {"output": <pilot_output>} | {"invalid": <any>} | {"error": "<text>"}}

when keys (all present keys must hold):
  message_contains  substring of the current message content (JSON text when it is structured)
  message_id        exact id of the current message
  message_from      exact origin of the current message
  message_kind      exact kind of the current message
  stimulus          none | active_wait_result | execution_result | projection | recovery
  user_message      true when the news carries a user_message block (user messages absorbed by the chain in progress), false when it does not
  continuity        exact continuity id (the destination of the current message)
  call_index        n-th inference of that continuity (1-based, corrections count)
  correction        true for the single correction inference
  compaction        true for the inference that writes the summary of a succession
  pressure          none | advised | required: the context_pressure level carried by the request
  step              exact name of the pilot.infer step (a Capacity pilot's prompt)
  prompt_contains   substring of the prompt of the request (the system message: the pilot's definition, or the assembled prompt of a pilot.infer step)
Rule "then" may carry "usage": {"input_tokens": n} to script the context size the provider would report, and "wait_gate": "<name>" to
hold the answer until that gate is opened (open_gate): the call stays in flight, already recorded in llm_calls.
Templates in "then": {{cid}} continuity id, {{mid}} current message id, {{call}} call index, {{step}} pilot.infer step.
"""
from __future__ import annotations

import json
from typing import Any, Dict, List, Optional, Tuple


NEWS_KEYS = ("current_message", "current_intention", "continuity_id", "active_stimulus")


def news_of(messages: List[Dict[str, Any]]) -> Dict[str, Any]:
    """What is new in a request: the last JSON object of the conversation that carries the current message (or the step), looking back
    over the validation exchange of a correction. A request that ends with the new prompt of a step (plain text) has none."""
    for message in reversed(messages[-3:]):
        try:
            body = json.loads(message.get("content") or "{}")
        except ValueError:
            continue
        if isinstance(body, dict) and any(k in body for k in NEWS_KEYS):
            return body
    return {}


def initial_intention_of(system: str) -> Any:
    """The initial intention, written at the end of the stable system text of a Matter pilot."""
    marker = "Initial intention:\n"
    if marker not in system:
        return None
    try:
        return json.loads(system.rsplit(marker, 1)[1].split("\n\n", 1)[0])
    except ValueError:
        return None


def facts(request: Dict[str, Any], previous_calls_of_continuity: int) -> Dict[str, Any]:
    messages = request.get("messages") or []
    body = news_of(messages)
    meta = request.get("metadata") or {}                  # who asked and which step / view, when the request is a conversation
    current = meta.get("current_message") or body.get("current_message") or {}      # the metadata holds the whole message; the news only designates it
    content = current.get("content")
    stimulus = (body.get("active_stimulus") or {}).get("kind", "none")
    last = (messages[-1].get("content") or "") if messages else ""
    system = (messages[0].get("content") or "") if messages else ""
    return {
        "continuity": current.get("to") or body.get("continuity_id") or meta.get("continuity_id"),
        "compaction": "too full to go on" in system or "too full to go on" in last,       # asked in the last message of the conversation
        "step": body.get("step") or meta.get("step"),
        "prompt_text": system + "\n\n" + last if meta.get("step") else system,       # a conversation ends with the new prompt
        "pressure": (body.get("context_pressure") or {}).get("level", "none"),
        "message_id": current.get("id"),
        "message_kind": current.get("kind"),
        "message_from": current.get("from"),
        "message_text": content if isinstance(content, str) else json.dumps(content, ensure_ascii=False, sort_keys=True),
        "stimulus": stimulus,
        "user_message": bool((body.get("user_message") or {}).get("message_ids")),
        "call_index": previous_calls_of_continuity + 1,
        "correction": "validation_error" in last and len(messages) > 2,
        "initial_intention": body.get("initial_intention") or initial_intention_of(system),
        "projection_ids": meta.get("projection_ids"),
        "projection_definition": meta.get("projection_definition"),
        "body": body,
    }


def holds(when: Dict[str, Any], f: Dict[str, Any]) -> bool:
    for key, wanted in (when or {}).items():
        if key == "message_contains":
            if str(wanted) not in str(f["message_text"]):
                return False
        elif key == "prompt_contains":
            if str(wanted) not in str(f["prompt_text"]):
                return False
        elif key == "user_message":
            if f["user_message"] is not bool(wanted):
                return False
        elif key in ("message_id", "message_from", "message_kind", "stimulus", "continuity", "call_index", "correction", "compaction", "pressure", "step"):
            if f[key] != wanted:
                return False
        else:
            raise ValueError(f"unknown rule condition {key!r}")
    return True


def render(value: Any, f: Dict[str, Any]) -> Any:
    if isinstance(value, str):
        return (value.replace("{{cid}}", str(f["continuity"])).replace("{{mid}}", str(f["message_id"]))
                .replace("{{call}}", str(f["call_index"])).replace("{{step}}", str(f.get("step"))))
    if isinstance(value, list):
        return [render(v, f) for v in value]
    if isinstance(value, dict):
        return {k: render(v, f) for k, v in value.items()}
    return value


def select(rules: List[Dict[str, Any]], f: Dict[str, Any]) -> Tuple[Optional[int], Optional[Dict[str, Any]]]:
    for index, rule in enumerate(rules):
        if holds(rule.get("when", {}), f):
            return index, rule
    return None, None
