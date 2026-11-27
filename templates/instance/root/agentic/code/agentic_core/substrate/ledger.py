"""The ledger of the calls a continuity made, written by the code from its journal when it succeeds (see continuity_succession.md).

What is deterministic is the code's: the model writes the analysis of a summary, the runtime appends this exact list. A free excerpt of a result
never enters it, because a result is external data and the successor reads the summary as its own; only the identifiers a result carries do.
"""
from __future__ import annotations

import json
import re
from typing import Any, Dict, List, Optional, Tuple

from .families import family_of
from .mcp import CALL_CAPACITY, outcome_of

MARKER = "\n\nLedger of the calls made so far (written by the runtime from the journal, exact; never repeat an effect listed here)"
LIMIT = 40                 # entries kept, the most recent
PARAMS_CHARS = 160
ERROR_CHARS = 120
MAX_IDS = 6


def compact(value: Any, limit: int) -> str:
    text = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")) if not isinstance(value, str) else value
    return text if len(text) <= limit else text[: limit - 1] + "…"


def identifiers(payload: Any, depth: int = 0, out: Optional[List[Tuple[str, str]]] = None) -> List[Tuple[str, str]]:
    """The scalar fields named id or ending in _id that a result carries (a few levels deep): the identifiers of what the call produced."""
    found = [] if out is None else out
    if isinstance(payload, dict) and depth <= 3:
        for key, item in payload.items():
            if (key == "id" or str(key).endswith("_id")) and isinstance(item, (str, int)) and not isinstance(item, bool):
                found.append((key, str(item)))
            elif isinstance(item, dict):
                identifiers(item, depth + 1, found)
    return found


def line_of(intention: Dict[str, Any], answer: Optional[Dict[str, Any]]) -> str:
    ref = str(intention.get("target_capacity"))
    if ref == CALL_CAPACITY:
        # an MCP call: the tool and the outcome class, never an argument value nor a field of a remote result
        parameters = intention.get("parameters") if isinstance(intention.get("parameters"), dict) else {}
        head = f"- MCP tool {parameters.get('tool')} (call {parameters.get('call_id')}) -> "
        return head + (outcome_of(answer) if answer is not None else "unanswered")
    head = f"- {ref} {compact(intention.get('parameters'), PARAMS_CHARS)} -> "
    if answer is None:
        return head + ("unanswered" if intention.get("await_response") else "sent, no answer awaited")
    if answer.get("unresolved") or (answer.get("response") or {}).get("unresolved"):
        return head + "interrupted: state unknown (do not replay without a decision)"
    response = answer.get("response") if isinstance(answer.get("response"), dict) else {}
    if response.get("status") == "error" or "error" in response or answer.get("outcome") == "error":
        error = response.get("error") or answer.get("error") or {}
        error = error if isinstance(error, dict) else {"message": error}
        return head + f"error {error.get('code', '')}: {compact(str(error.get('message', '')), ERROR_CHARS)}".replace("error : ", "error ")
    line = head + "ok"
    if family_of(ref, {}) != "reflexive":
        ids = identifiers(response.get("payload"))[:MAX_IDS]
        if ids:
            line += " " + " ".join(f"{k}={v}" for k, v in ids)
    return line


def calls_of(thread: List[Dict[str, Any]]) -> List[str]:
    """One line per call of the thread, in order; identical lines are grouped (x3)."""
    answers: Dict[str, Dict[str, Any]] = {}
    for message in thread:
        if message.get("kind") == "capacity_response":
            intention_id = (message.get("correlation") or {}).get("intention_id")
            if intention_id and isinstance(message.get("content"), dict):
                answers[intention_id] = message["content"]
    lines: List[str] = []
    counts: Dict[str, int] = {}
    for message in thread:
        if message.get("kind") == "capacity_intention" and isinstance(message.get("content"), dict):
            intention = message["content"]
            line = line_of(intention, answers.get(intention.get("id")))
            if line in counts:
                counts[line] += 1
            else:
                counts[line] = 1
                lines.append(line)
    return [f"{line} (x{counts[line]})" if counts[line] > 1 else line for line in lines]


def previous_ledger(thread: List[Dict[str, Any]]) -> Tuple[List[str], int]:
    """The ledger carried by the summary this continuity started from (a successor of a successor keeps the whole story), and the omitted count."""
    for message in thread:
        if message.get("kind") == "summary" and isinstance(message.get("content"), str) and MARKER in message["content"]:
            block = message["content"].split(MARKER, 1)[1]
            lines = [row for row in block.splitlines() if row.startswith("- ")]
            omitted = re.search(r"\((\d+) earlier calls omitted\)", block)
            return lines, int(omitted.group(1)) if omitted else 0
    return [], 0


def build_ledger(thread: List[Dict[str, Any]]) -> str:
    """The text appended to a summary; empty when no call was ever made."""
    carried, omitted = previous_ledger(thread)
    lines = carried + calls_of(thread)
    if not lines:
        return ""
    if len(lines) > LIMIT:
        omitted += len(lines) - LIMIT
        lines = lines[-LIMIT:]
    head = MARKER + ":"
    return head + ("\n" + f"({omitted} earlier calls omitted)" if omitted else "") + "\n" + "\n".join(lines)


def with_ledger(summary: Any, ledger: str) -> Any:
    if not ledger:
        return summary
    text = summary if isinstance(summary, str) else json.dumps(summary, ensure_ascii=False, sort_keys=True)
    return text + ledger
