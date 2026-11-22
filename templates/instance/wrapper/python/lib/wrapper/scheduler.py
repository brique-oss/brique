# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

from __future__ import annotations

import asyncio
import contextlib
import contextvars
import weakref
from collections.abc import AsyncIterator, Awaitable, Callable
from typing import Any


class SchedulerFullError(Exception):
    """Raised when the bounded pending-work queue cannot accept more work."""


class _Lease:
    """The execution slot one piece of work holds, and how many of its calls wait for an answer."""

    def __init__(self) -> None:
        self.holding = True
        self.waiting = 0
        self.ended = False      # the work is over and its slot is back: a task it started that is still alive owns no slot


# The lease of the work running in the current task; its sub-tasks share it.
_current_lease: contextvars.ContextVar[_Lease | None] = contextvars.ContextVar("brique_work_lease", default=None)


class KeyedLocks:
    """Locks by key, for a short "read, compute, guarded write" on shared state.

    The keys of one block are taken in sorted order, so two blocks that name the same keys in a different order cannot wait for each
    other. A lock is local to this process and reduces conflicts; the guard on the write (an expected revision) stays the guarantee.
    Do not hold one across a long wait (an inference, a call that may last).
    """

    def __init__(self) -> None:
        self.entries: dict[str, list[Any]] = {}      # key -> [lock, number of blocks that hold it or wait for it]

    @contextlib.asynccontextmanager
    async def critical(self, *keys: str, yielding: Callable[[], Any] | None = None) -> AsyncIterator[None]:
        """``yielding`` (the scheduler's): given, a wait for a lock that is taken does not hold the execution slot of the wrapper. Without that,
        as many blocks waiting for the same key as there are slots would stop the wrapper, the one that holds the lock needing a slot itself
        to make progress."""
        ordered = sorted({str(key) for key in keys})
        acquired: list[str] = []
        try:
            for key in ordered:
                entry = self.entries.get(key)
                if entry is None:
                    entry = self.entries[key] = [asyncio.Lock(), 0]
                entry[1] += 1
                try:
                    if yielding is not None and entry[0].locked():
                        async with yielding():
                            await entry[0].acquire()
                    else:
                        await entry[0].acquire()
                except BaseException:
                    self._leave(key, entry)
                    raise
                acquired.append(key)
            yield
        finally:
            for key in reversed(acquired):
                entry = self.entries[key]
                entry[0].release()
                self._leave(key, entry)

    def _leave(self, key: str, entry: list[Any]) -> None:
        entry[1] -= 1
        if entry[1] <= 0 and not entry[0].locked():
            self.entries.pop(key, None)


# The schedulers alive in this process, for the diagnostic on demand (wrapper.diagnostic).
schedulers: weakref.WeakSet["WorkScheduler"] = weakref.WeakSet()


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
        self.locks = KeyedLocks()
        self.refused = 0                                # works refused because the queue was full, since the start
        schedulers.add(self)

    def start(self) -> None:
        self._accepting = True

    def submit(self, key: str, work: Callable[[], Awaitable[Any]], *, ordered: bool = True) -> None:
        """Queue work. With ordered=True it runs after the work already queued under the same key;
        with ordered=False it only waits for a free slot, like any other work."""
        if not self._accepting or len(self._pending) >= self.max_concurrency + self.queue_limit:
            self.refused += 1
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
        if lease is None or lease.ended:        # a task that outlives its work (a background wake) shares a lease that has already given its slot back
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
            if lease.waiting == 0 and not lease.holding and not lease.ended:
                await self._slots.acquire()
                if lease.ended:                  # the work ended while this waited for its slot: nobody would give it back
                    self._slots.release()
                else:
                    lease.holding = True
                    self._active += 1

    def critical(self, *keys: str) -> contextlib.AbstractAsyncContextManager[None]:
        """Hold the lock of every key for the length of the block (see ``KeyedLocks``). A wait for a taken lock gives the slot back."""
        return self.locks.critical(*keys, yielding=self.yielding)

    def stats(self) -> dict[str, Any]:
        """A snapshot of the execution slots, for a diagnostic or a metric: nothing here changes the scheduler."""
        return {
            "max_concurrency": self.max_concurrency,
            "queue_limit": self.queue_limit,
            "free_slots": self._slots._value,
            "active": self._active,
            "pending": len(self._pending),
            "queued": self.queued,
            "refused": self.refused,
            "ordered_chains": len(self._tails),
            "ordered_keys": sorted(self._tails)[:6],
            "locks": len(self.locks.entries),
            "accepting": self._accepting,
        }

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
                lease.ended = True
                if lease.holding:
                    lease.holding = False
                    self._active -= 1
                    self._slots.release()
        finally:
            current = asyncio.current_task()
            if self._tails.get(key) is current:
                self._tails.pop(key, None)
