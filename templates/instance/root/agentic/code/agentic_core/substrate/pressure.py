"""Compaction pressure: how full the model context is, in tokens (never in bytes)."""
from __future__ import annotations

import json
from typing import Any, Dict, Optional

DEFAULT_ADVISED_RATIO = 0.60
DEFAULT_REQUIRED_RATIO = 0.85
CHARS_PER_TOKEN = 4


def estimate_tokens(value: Any) -> int:
    """Coarse estimation used before the provider has reported a real measure."""
    return max(1, len(json.dumps(value, ensure_ascii=False)) // CHARS_PER_TOKEN)


def window(pilot: Dict[str, Any]) -> Optional[int]:
    value = (pilot.get("limits") or {}).get("context_tokens")
    return value if isinstance(value, int) and not isinstance(value, bool) and value > 0 else None


def _ratio(pilot: Dict[str, Any], key: str, default: float) -> float:
    value = (pilot.get("limits") or {}).get(key)
    return float(value) if isinstance(value, (int, float)) and not isinstance(value, bool) and 0 < value <= 1 else default


def pressure(pilot: Dict[str, Any], tokens: Optional[int]) -> Optional[Dict[str, Any]]:
    """None when no window is declared or nothing was measured; else the level of the context pressure."""
    size = window(pilot)
    if size is None or tokens is None:
        return None
    advised = _ratio(pilot, "compaction_advised_ratio", DEFAULT_ADVISED_RATIO)
    required = max(_ratio(pilot, "compaction_required_ratio", DEFAULT_REQUIRED_RATIO), advised)
    ratio = tokens / size
    level = "required" if ratio >= required else "advised" if ratio >= advised else "none"
    return {"tokens": tokens, "window": size, "ratio": round(ratio, 3), "level": level}
