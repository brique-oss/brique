"""The cognitive frame of a continuity: the pinned lines (kind framework) of a projection, set at the head of the system text.

A projection always carries the frame first (continuity.project); a request to a model puts its text at the start of the system text,
once, and leaves it out of the view of the journal it sends.
"""
from __future__ import annotations

from typing import Any, Dict, List, Tuple

from .kinds import PINNED_KINDS


def split_frame(view: Dict[str, Any]) -> Tuple[List[str], Dict[str, Any]]:
    """(texts of the pinned lines, the view without them). A view without such lines comes back as it is."""
    messages = view.get("messages") or []
    pinned = [m for m in messages if m.get("kind") in PINNED_KINDS]
    if not pinned:
        return [], view
    texts = []
    for m in pinned:
        content = m.get("content")
        text = content.get("text") if isinstance(content, dict) else content
        if isinstance(text, str) and text.strip():
            texts.append(text)
    return texts, dict(view, messages=[m for m in messages if m.get("kind") not in PINNED_KINDS])
