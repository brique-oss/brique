from __future__ import annotations

from typing import Any, Dict, Optional

from . import continuity_store


async def append_trace(port, continuity_id: str, kind: str, content: Any, *, to: Optional[str] = None,
                       correlation: Optional[Dict[str, Any]] = None, message_id: Optional[str] = None,
                       sender: Optional[str] = None) -> Dict[str, Any]:
    """Record a fact of the continuity itself in its journal: one appended line, whose seq is its line number.

    The pilot's own facts (emitted intentions, intention updates, projections, errors...) are facts of the history,
    not stimuli: they get a seq like any message but NO mailbox entry, so they are never consumed. Messages coming from
    outside always go through mailbox.send. The same id is recorded once (idempotent). The kind is the characterization
    of the line, set by the tool that writes it.
    """
    entry = {"kind": kind, "content": content, "to": to, "correlation": correlation, "id": message_id, "from": sender}
    stored = await continuity_store.append(port, continuity_id, [entry])
    return stored[0]


def usage_detail(usage: Dict[str, Any], response: Optional[Dict[str, Any]] = None, window: Optional[int] = None) -> Dict[str, Any]:
    """The breakdown of what a provider reported for one inference, each field only if known.

    input_tokens stays the total context; the cache fields split it: read from the cache, written to it, and the rest (uncached).
    The uncached part is stated only when the provider says something of the cache (a provider without a cache would otherwise
    look like all its input was uncached by choice)."""
    detail: Dict[str, Any] = {}
    total = usage.get("input_tokens")
    read, created = usage.get("cached_input_tokens"), usage.get("cache_creation_input_tokens")
    if isinstance(read, int):
        detail["cache_read_tokens"] = read
    if isinstance(created, int):
        detail["cache_creation_tokens"] = created
    if isinstance(total, int) and (isinstance(read, int) or isinstance(created, int)):
        detail["uncached_input_tokens"] = max(0, total - (read if isinstance(read, int) else 0) - (created if isinstance(created, int) else 0))
    if isinstance(usage.get("output_tokens"), int):
        detail["output_tokens"] = usage["output_tokens"]
    resolved = (response or {}).get("resolved") or {}
    if resolved.get("model"):
        detail["model"] = f"{resolved['provider']}/{resolved['model']}" if resolved.get("provider") else str(resolved["model"])
    if isinstance((response or {}).get("duration_ms"), (int, float)):
        detail["duration_ms"] = (response or {})["duration_ms"]
    if isinstance(window, int) and window > 0:
        detail["window_tokens"] = window
    return detail


def measure_line(cid: str, message_id: Optional[str], tokens: int, *, estimated: bool,
                 detail: Optional[Dict[str, Any]] = None) -> Dict[str, Any]:
    """The follow-up fact that says how many tokens the context of an inference held (kind event, inference_measured).

    `detail` is the usage breakdown (usage_detail) of a reported, not estimated, measure."""
    content: Dict[str, Any] = {"event": "inference_measured", "input_tokens": tokens}
    if message_id:
        content["message_id"] = message_id
    if estimated:
        content["estimated"] = True
    elif detail:
        content.update(detail)
    return {"kind": "event", "content": content}
