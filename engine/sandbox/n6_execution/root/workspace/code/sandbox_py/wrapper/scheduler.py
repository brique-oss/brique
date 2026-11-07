# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

from __future__ import annotations

import asyncio
import contextlib
import contextvars
from collections.abc import AsyncIterator, Awaitable, Callable
from typing import Any


class SchedulerFullError(Exception):
    """Raised when the bounded pending-work queue cannot accept more work."""


class _Lease:
    """The execution slot one piece of work holds, and how many of its calls wait for an answer."""

    def __init__(self) -> None:
        self.holding = True
        self.waiting = 0


# The lease of the work running in the current task; its sub-tasks share it.
_current_lease: contextvars.ContextVar[_Lease | None] = contextvars.ContextVar("brique_work_lease", default=None)


class WorkScheduler:
    """Bounded global execution, with FIFO task chains for the work that asks to be ordered."""

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

    def submit(self, key: str, work: Callable[[], Awaitable[Any]], *, ordered: bool = True) -> None:
        """Queue work. With ordered=True it runs after the work already queued under the same key;
        with ordered=False it only waits for a free slot, like any other work."""
        if not self._accepting or len(self._pending) >= self.max_concurrency + self.queue_limit:
            raise SchedulerFullError("wrapper pending-work queue is full")
        previous = self._tails.get(key) if ordered else None
        task = asyncio.create_task(self._run(key, previous, work), name=f"brique-wrapper-work:{key}")
        if ordered:
            self._tails[key] = task
        self._pending.add(task)
        task.add_done_callback(self._pending.discard)

    async def drain(self) -> None:
        while self._pending:
            await asyncio.gather(*tuple(self._pending), return_exceptions=True)

    async def close(self) -> None:
        self._accepting = False
        await self.drain()

    @contextlib.asynccontextmanager
    async def yielding(self) -> AsyncIterator[None]:
        """Give the execution slot back while this work waits for the answer to a call it made.

        Without it, as many calls as there are slots, each waiting for a call that needs a slot
        itself, would wait for each other for ever. The slot is taken back, in turn, once the answer
        is there. Outside scheduled work this does nothing.
        """
        lease = _current_lease.get()
        if lease is None:
            yield
            return
        lease.waiting += 1
        if lease.waiting == 1 and lease.holding:
            lease.holding = False
            self._active -= 1
            self._slots.release()
        try:
            yield
        finally:
            lease.waiting -= 1
            if lease.waiting == 0 and not lease.holding:
                await self._slots.acquire()
                lease.holding = True
                self._active += 1

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
            await self._slots.acquire()
            self._active += 1
            lease = _Lease()
            token = _current_lease.set(lease)
            try:
                await work()
            except Exception:
                # The runtime work wrapper records the full failure. A bad
                # item must not break the per-key chain.
                pass
            finally:
                _current_lease.reset(token)
                if lease.holding:
                    self._active -= 1
                    self._slots.release()
        finally:
            current = asyncio.current_task()
            if self._tails.get(key) is current:
                self._tails.pop(key, None)
