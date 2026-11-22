# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

"""A callee that is busy refuses BEFORE running the call (the Matter loop of the engine, the pending-work queue of a
wrapper): asking again is safe. A call that timed out is never asked again, it may have run.

The rules are shared by the three runtimes and fixed by ``vectors/busy_retry.json``.
"""

from __future__ import annotations

import random
from typing import Any, Callable

from . import settings
from .constants import STATUS_ERROR

BUSY_REASONS = ("busy", "loop busy")


def busy_attempts() -> int:
    return settings.value("BRIQUE_BUSY_ATTEMPTS")


def is_busy(response: dict[str, Any]) -> bool:
    """Is this response body a refusal because the callee is busy?"""
    if not isinstance(response, dict) or str(response.get("status", "")) != STATUS_ERROR:
        return False
    error = response.get("error")
    details = error.get("details") if isinstance(error, dict) else None
    return isinstance(details, dict) and str(details.get("reason", "")) in BUSY_REASONS


def busy_pause_s(attempt: int, rnd: Callable[[], float] = random.random) -> float:
    """The pause after the refusal of attempt number ``attempt`` (0 for the first)."""
    base = settings.value("BRIQUE_BUSY_BASE_MS")
    cap = settings.value("BRIQUE_BUSY_CAP_MS")
    return min(cap, base * (2 ** min(attempt, 30))) * (0.5 + rnd()) / 1000.0
