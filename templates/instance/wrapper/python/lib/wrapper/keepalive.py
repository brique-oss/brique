# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

"""Acknowledge a long-running capacity: an immediate `running`, then a heartbeat, until the call returns.

The engine runtime turns the caller's timeout into an inactivity budget that every `running` response renews, and the hosted function still
returns once, normally, with the single final response. ``notify_running`` is a no-op outside a capacity call, so this is always safe.

The heartbeat belongs to the one who writes the capacity, on purpose: it is sent from a task of its own, so it would go on saying "running" for a
coroutine that is stuck, and the budget would no longer protect the caller. The library gives the means, not a heartbeat of its own.

    async with runtime.keepalive():
        await a_call_that_may_last()
"""

from __future__ import annotations

import asyncio
import contextlib
from collections.abc import AsyncIterator
from typing import Any

from . import settings

@contextlib.asynccontextmanager
async def keepalive(runtime: Any, every_s: float | None = None) -> AsyncIterator[None]:
    period = settings.value("BRIQUE_HEARTBEAT_MS") / 1000.0 if every_s is None else every_s
    await runtime.notify_running()

    async def beat() -> None:
        while True:
            await asyncio.sleep(period)
            await runtime.notify_running()

    task = asyncio.ensure_future(beat())     # copies the context: the heartbeat belongs to this very call
    try:
        yield
    finally:
        task.cancel()
        with contextlib.suppress(asyncio.CancelledError, Exception):
            await task
