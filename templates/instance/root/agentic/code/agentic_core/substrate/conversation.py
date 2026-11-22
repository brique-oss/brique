"""The journal is the conversation: each line of a projection becomes a message with its role, in the order of the journal.

One rule for both sorts of pilots. A request to a model is then the cognitive frame (the system text), the conversation so far, and what
is new: the part of the request that already was in the previous one is byte for byte the same, which is what a provider's prompt cache
reads. The text of a line is written compactly with ordered keys, so the same line is always the same bytes.

  framework                                  the system text (set aside by split_frame, not a message)
  user, continuity, summary                  user
  prompt (the assembled prompt of a step)    user
  completion (the output of a step)          assistant
  pilot, capacity_intention                  assistant
  capacity_response                          user (with the id of the intention it answers)
  event, projection, error, control          nothing: the follow-up of the machinery is not said to the model

The conversation of a pilot_definition leaves out the lines of effects (skip_kinds: pilot, capacity_intention): its decision, a completion
line written before the effects, already says them in the form the model wrote them.
"""
from __future__ import annotations

import json
from typing import Any, Dict, List, Optional, Tuple

from .frame import split_frame


def text_of(value: Any) -> str:
    """A text as it is; any other JSON value as compact JSON with its keys in order."""
    if isinstance(value, str):
        return value
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))


def message_of(line: Dict[str, Any]) -> Optional[Dict[str, str]]:
    """The message a journal line says to the model, or None when the line is not part of the conversation."""
    kind, content = line.get("kind"), line.get("content")
    if kind == "user":
        return {"role": "user", "content": text_of(content)}
    if kind == "continuity":
        return {"role": "user", "content": text_of({"from": line.get("from"), "content": content})}
    if kind == "summary":
        return {"role": "user", "content": "Summary of the previous continuity:\n" + text_of(content)}
    if kind == "prompt":
        return {"role": "user", "content": text_of(content.get("instructions") if isinstance(content, dict) else content)}
    if kind == "completion":
        return {"role": "assistant", "content": text_of(content.get("output") if isinstance(content, dict) else content)}
    if kind == "pilot":
        return {"role": "assistant", "content": text_of(content.get("content") if isinstance(content, dict) and "content" in content else content)}
    if kind == "capacity_intention":
        return {"role": "assistant", "content": text_of(content)}
    if kind == "capacity_response":
        # the link between an answer and the intention it answers is information: it is said in the answer itself
        intention_id = (line.get("correlation") or {}).get("intention_id")
        if intention_id and isinstance(content, dict):
            content = dict(content, intention_id=intention_id)
        return {"role": "user", "content": text_of(content)}
    return None


def conversation(view: Dict[str, Any], skip_kinds: Tuple[str, ...] = ()) -> Tuple[List[str], List[Dict[str, str]]]:
    """(texts of the frame, messages of the conversation) of a projection view. Related continuities, when the projection asked for
    them, follow the own lines, each line tagged with its continuity. The lines of the kinds in skip_kinds are not messages."""
    frame, shown = split_frame(view)
    messages = [m for m in (message_of(line) for line in shown.get("messages") or [] if line.get("kind") not in skip_kinds) if m is not None]
    for group in shown.get("related") or []:
        for line in group.get("messages") or []:
            if line.get("kind") in ("framework", "event", "projection", "error", "control") + tuple(skip_kinds):
                continue
            said = message_of(line)
            if said is not None:
                messages.append({"role": "user", "content": text_of({"continuity_id": group.get("continuity_id"), "role": said["role"], "content": said["content"]})})
    return frame, messages
