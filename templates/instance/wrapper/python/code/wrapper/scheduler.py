# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

from __future__ import annotations

import asyncio
from collections.abc import Awaitable, Callable
from typing import Any


class SchedulerFullError(Exception):
    """Raised when the bounded pending-work queue cannot accept more work."""


class WorkScheduler:
    """Bounded global execution with FIFO task chains per addressed binding."""

    def __init__(self, max_concurrency: int, queue_limit: int) -> None:
        if max_concurrency < 1:
            raise ValueError("max_concurrency must be at least 1")
        if queue_limit < 1:
            raise ValueError("queue_limit must be at least 1")
        self.max_concurrency = max_concurrency
        self.queue_limit = queue_limit
        self._slots = asyncio.Semaphore(max_concurrency)
        self._pending: set[asyncio.Task[None]] = set()
        self._tails: dict[str, asyncio.Task[None]] = {}
        self._active = 0
        self._accepting = True

    def start(self) -> None:
        self._accepting = True

    def submit(self, key: str, work: Callable[[], Awaitable[Any]]) -> None:
        if not self._accepting or len(self._pending) >= self.max_concurrency + self.queue_limit:
            raise SchedulerFullError("wrapper pending-work queue is full")
        previous = self._tails.get(key)
        task = asyncio.create_task(self._run(key, previous, work), name=f"brique-wrapper-work:{key}")
        self._tails[key] = task
        self._pending.add(task)
        task.add_done_callback(self._pending.discard)

    async def drain(self) -> None:
        while self._pending:
            await asyncio.gather(*tuple(self._pending), return_exceptions=True)

    async def close(self) -> None:
        self._accepting = False
        await self.drain()

    @property
    def queued(self) -> int:
        return max(0, len(self._pending) - self._active)

    async def _run(
        self,
        key: str,
        previous: asyncio.Task[None] | None,
        work: Callable[[], Awaitable[Any]],
    ) -> None:
        try:
            if previous is not None:
                await asyncio.gather(previous, return_exceptions=True)
            async with self._slots:
                self._active += 1
                try:
                    await work()
                except Exception:
                    # The runtime work wrapper records the full failure. A bad
                    # item must not break the per-key chain.
                    pass
                finally:
                    self._active -= 1
        finally:
            current = asyncio.current_task()
            if self._tails.get(key) is current:
                self._tails.pop(key, None)
