from __future__ import annotations

import asyncio
from typing import Dict

# Process-local liveness of cognitive runs. It is deliberately NOT durable: after a crash the
# set is empty, so a runtime that still says running/waiting names an orphan run, which is
# exactly what recovery has to detect. The durable truth stays in the runtime Matter.

_live: Dict[str, str] = {}
_events: Dict[str, asyncio.Event] = {}


def mark_live(continuity_id: str, run_id: str) -> None:
    _live[continuity_id] = run_id
    _events.setdefault(continuity_id, asyncio.Event())


def drop_live(continuity_id: str, run_id: str) -> None:
    if _live.get(continuity_id) == run_id:
        _live.pop(continuity_id, None)
        event = _events.pop(continuity_id, None)
        if event is not None:
            event.set()                  # an activation waiting for this run learns that it ended


def is_live(continuity_id: str, run_id) -> bool:
    return bool(run_id) and _live.get(continuity_id) == run_id


def notify(continuity_id: str) -> None:
    """Tell a live waiting run that durable state changed (an optimization; polling is the fallback)."""
    event = _events.get(continuity_id)
    if event is not None:
        event.set()


async def wait_change(continuity_id: str, timeout_s: float) -> None:
    event = _events.get(continuity_id)
    if event is None:
        await asyncio.sleep(timeout_s)
        return
    try:
        await asyncio.wait_for(event.wait(), timeout=timeout_s)
    except asyncio.TimeoutError:
        pass
    event.clear()
