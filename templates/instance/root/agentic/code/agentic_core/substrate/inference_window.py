"""The inference window of a continuity.

An inference is a contiguous unit of the journal: from the reading of its input to the writing of its decision, no other line is
written in the continuity and no other inference runs for it. Otherwise the answer of a tool would land between what the model read and
what it decided, and the journal, read back as a conversation, would say that the decision saw what it did not see.

The window is process-local state, deliberately not durable: the wrapper is unique, so whoever holds it and whoever waits for it
disappear together, and the journal already says that an inference was interrupted (a prompt without its completion). It guards only
the door of the journal (continuity_store.append): the state Matters stay writable, each under its own guard. It is not a critical-section
lock: it is held for an inference, and the door is waited for BEFORE taking that lock (the holder needs the lock to write its decision).

Who writes while the window is closed
  - its holder (the inference step, or the request of a user that opened it);
  - the loop of the run of the continuity, whose identity is NOT inherited by the tasks it spawns (the delivery of the answer of an awaited
    tool is an outside writer);
  - nobody else: they wait, in arrival order.

A user request that arrives while a run is working opens the window in the very operation that writes it and then *reserves* it for the
run: the reservation closes the door to outside writers (not to the loop) until the loop absorbs the request, or until it expires.
"""
from __future__ import annotations

import asyncio
import contextvars
import itertools
import time
from collections import deque
from contextlib import asynccontextmanager, contextmanager
from typing import Any, Deque, Dict, Iterable, Optional, Set, Tuple

from .settings import setting

RESERVATION_TTL_S = setting("RESERVATION_TTL_S", 30.0, 0.0, "how long a reservation waits for the loop before the request becomes a normal message")

# (continuity_id, role, token): role "holder" (token of the window it holds) or "loop" (the run of the continuity)
_writer: contextvars.ContextVar[Optional[Tuple[str, str, Any]]] = contextvars.ContextVar("inference_writer", default=None)
_tokens = itertools.count(1)


class _Window:
    __slots__ = ("cid", "holder", "queue", "reserved", "stamp")

    def __init__(self, cid: str) -> None:
        self.cid = cid
        self.holder: Optional[int] = None
        self.queue: Deque[Tuple[str, asyncio.Future, Optional[int], bool]] = deque()   # ("write" | "hold", future, token, is the loop), arrival order
        self.reserved: Optional[Tuple[Set[str], float]] = None                   # (message ids, expiry on the monotonic clock)
        self.stamp = 0                                                           # identifies a reservation for its expiry timer


_windows: Dict[str, _Window] = {}


def _window(cid: str) -> _Window:
    window = _windows.get(cid)
    if window is None:
        window = _windows[cid] = _Window(cid)
    return window


def _drop_if_idle(window: _Window) -> None:
    if window.holder is None and window.reserved is None and not window.queue:
        _windows.pop(window.cid, None)


def _expire_reservation(window: _Window) -> None:
    if window.reserved is not None and window.reserved[1] <= time.monotonic():
        window.reserved = None


def _next_waiter(window: _Window) -> Optional[Tuple[str, asyncio.Future, Optional[int], bool]]:
    """Who may go on now. In arrival order while nothing is reserved. Under a reservation only the LOOP of the run goes on: the reservation closes the door
    to the outside writers, never to the loop, and the loop may have asked for the window BEFORE the reservation was made, so it can sit behind an outside
    writer in the queue: it is served first (the others wait for the loop to absorb the request, or for the reservation to expire)."""
    if window.reserved is None:
        return window.queue[0] if window.queue else None
    return next((entry for entry in window.queue if entry[3]), None)


def _advance(window: _Window) -> None:
    """Serve the queue while the window is free: in arrival order, the loop first under a reservation."""
    _expire_reservation(window)
    while window.holder is None:
        entry = _next_waiter(window)
        if entry is None:
            break
        window.queue.remove(entry)
        kind, future, token, _is_loop = entry
        if future.done():
            continue                                        # its waiter was cancelled
        if kind == "write":
            future.set_result(None)                         # a writer passes the door and does not hold the window
            continue
        window.holder = token
        future.set_result(None)
        break
    _drop_if_idle(window)


def _expire(cid: str, stamp: int) -> None:
    window = _windows.get(cid)
    if window is not None and window.stamp == stamp and window.reserved is not None:
        window.reserved = None
        _advance(window)


def _role(cid: str) -> Optional[Tuple[str, str, Any]]:
    role = _writer.get()
    return role if role is not None and role[0] == cid else None


def closed_for(cid: str) -> bool:
    """Would a write by the current context have to wait?"""
    window = _windows.get(cid)
    if window is None:
        return False
    role = _role(cid)
    if role is not None and (role[1] == "loop" or role[2] == window.holder):
        return False
    _expire_reservation(window)
    return window.holder is not None or window.reserved is not None or bool(window.queue)


async def door(cid: str) -> None:
    """The door of the journal: returns when the current context may write."""
    if not closed_for(cid):
        return
    window = _windows.get(cid)
    if window is None:
        return
    future = asyncio.get_running_loop().create_future()
    window.queue.append(("write", future, None, False))
    _advance(window)
    await _wait(window, future)


async def _wait(window: _Window, future: asyncio.Future, token: Optional[int] = None) -> None:
    try:
        await future
    except BaseException:
        if not future.done():
            future.cancel()
        elif token is not None and window.holder == token:
            window.holder = None                            # served, then cancelled before it could run: nobody would release it
        _advance(window)                                    # a cancelled head must not stall the queue
        raise


class Handle:
    """What `hold` gives its caller."""

    def __init__(self, token: int) -> None:
        self.token = token
        self.reserve_ids: Set[str] = set()

    def reserve(self, message_ids: Iterable[str]) -> None:
        """At exit, reserve the window for the run that will absorb these requests."""
        self.reserve_ids |= set(message_ids)


@asynccontextmanager
async def hold(cid: str, adopt: Iterable[str] = (), yielding=None):
    """Hold the window of the continuity for one inference (or for the write of a request). FIFO with the other writers.

    `adopt` names messages whose reservation this holder takes over: it is then served at once, without waiting behind
    the reservation. The loop of the run is never blocked by a reservation (it only closes the door to others). `yielding` is the port's:
    the wait for the turn gives back the execution slot of the wrapper."""
    window = _window(cid)
    token = next(_tokens)
    adopt = set(adopt)
    role = _role(cid)
    is_loop = role is not None and role[1] == "loop"
    _expire_reservation(window)
    if window.reserved is not None and adopt & window.reserved[0]:
        window.reserved = (window.reserved[0] - adopt, window.reserved[1]) if window.reserved[0] - adopt else None
    free = window.holder is None
    if free and (is_loop or (window.reserved is None and not window.queue)):
        window.holder = token                               # the loop holds under a reservation made for it: it is the one that lifts it
    else:
        future = asyncio.get_running_loop().create_future()
        window.queue.append(("hold", future, token, is_loop))
        _advance(window)
        if yielding is not None and not future.done():
            async with yielding():                           # a wait for the window must not hold the execution slot of the wrapper
                await _wait(window, future, token)
        else:
            await _wait(window, future, token)
    handle = Handle(token)
    reset = _writer.set((cid, "holder", token))
    try:
        yield handle
    finally:
        _writer.reset(reset)
        window.holder = None
        if handle.reserve_ids:
            window.reserved = (set(handle.reserve_ids), time.monotonic() + RESERVATION_TTL_S)
            window.stamp += 1
            asyncio.get_running_loop().call_later(RESERVATION_TTL_S, _expire, cid, window.stamp)
        _advance(window)


def enter_loop(cid: str):
    """The current task is the loop of the run of the continuity: the reservation does not close the door to it."""
    return _writer.set((cid, "loop", None))


def leave_loop(reset) -> None:
    _writer.reset(reset)


def forget_role() -> None:
    """A task spawned by the loop is an outside writer: it does not inherit the identity of its parent."""
    _writer.set(None)


def clear(cid: str) -> None:
    """The run ended, or the continuity is suspended or deleted: no reservation outlives it, the requests become normal messages."""
    window = _windows.get(cid)
    if window is None:
        return
    window.reserved = None
    _advance(window)


def reserved_ids(cid: str) -> Set[str]:
    window = _windows.get(cid)
    if window is None or window.reserved is None:
        return set()
    _expire_reservation(window)
    return set(window.reserved[0]) if window.reserved else set()
