# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

"""A diagnostic on demand for a wrapper that stops answering.

A frozen wrapper (its CPU at zero, calls waiting for it) says nothing about what it waits for. On the signal SIGUSR1 this writes, to a file, the
state of the execution slots and every asynchronous task of the process with the chain of what it is awaiting: which coroutine, at which line,
and on which future or lock it is parked.

    kill -USR1 <pid of the wrapper>           the file is $TMPDIR/brique_wrapper_tasks_<pid>.txt

It only reads: it changes nothing, and does nothing until the signal comes. Hosted code that holds state worth seeing in a stall (a queue, a
reservation) adds a line for it with ``install(extra=...)``.
"""

from __future__ import annotations

import asyncio
import os
import signal
import tempfile
import time
from collections.abc import Callable, Iterable
from typing import Any

from . import settings
from .scheduler import schedulers

_configured = False
_path: str | None = None
_extra: tuple[Callable[[], str], ...] = ()
_signal_installed = False


def _chain(coro: Any) -> list[str]:
    """The awaited chain of a suspended coroutine, from its own frame down to the thing it is parked on."""
    lines: list[str] = []
    seen = 0
    while coro is not None and seen < 40:
        seen += 1
        frame = getattr(coro, "cr_frame", None) or getattr(coro, "gi_frame", None)
        if frame is not None:
            lines.append(f"    {frame.f_code.co_filename.rsplit('/', 1)[-1]}:{frame.f_lineno} in {frame.f_code.co_name}")
        nxt = getattr(coro, "cr_await", None) or getattr(coro, "gi_yieldfrom", None)
        if nxt is None:
            break
        if not hasattr(nxt, "cr_frame") and not hasattr(nxt, "gi_frame"):     # a future, an event, a lock: the leaf
            lines.append(f"    parked on {nxt!r}"[:300])
            break
        coro = nxt
    return lines


def scheduler_lines() -> list[str]:
    lines = []
    for scheduler in list(schedulers):
        try:
            stats = scheduler.stats()
            lines.append(
                f"scheduler: slots free {stats['free_slots']} of {stats['max_concurrency']}, active {stats['active']}, pending {stats['pending']}, "
                f"queued {stats['queued']} of {stats['queue_limit']}, refused {stats['refused']}, locks {stats['locks']}, "
                f"ordered chains {stats['ordered_chains']} ({stats['ordered_keys']})"
            )
        except Exception as exc:
            lines.append(f"scheduler: unreadable ({exc})")
    return lines or ["scheduler: none"]


def dump_tasks(path: str | None = None, extra: Iterable[Callable[[], str]] | None = None) -> str:
    path = path or _path or os.path.join(tempfile.gettempdir(), f"brique_wrapper_tasks_{os.getpid()}.txt")
    try:
        tasks = asyncio.all_tasks(asyncio._get_running_loop() or asyncio.get_event_loop())
    except Exception as exc:                                    # no loop: say so rather than fail
        with open(path, "w", encoding="utf-8") as out:
            out.write(f"no event loop to inspect: {exc}\n")
        return path
    with open(path, "w", encoding="utf-8") as out:
        out.write(f"pid {os.getpid()} at {time.strftime('%H:%M:%S')}: {len(tasks)} tasks\n")
        for line in scheduler_lines():
            out.write(line + "\n")
        out.write(f"settings not at their default: {settings.not_default() or 'none'}\n")
        for section in tuple(extra) if extra is not None else _extra:
            try:
                out.write(section() + "\n")
            except Exception as exc:
                out.write(f"extra section unreadable ({exc})\n")
        out.write("\n")
        for task in sorted(tasks, key=lambda t: t.get_name()):
            state = "done" if task.done() else "pending"
            out.write(f"- {task.get_name()} [{state}] {task.get_coro()!r}"[:260] + "\n")
            if not task.done():
                out.write("\n".join(_chain(task.get_coro())) + "\n")
    return path


def install(path: str | None = None, extra: Iterable[Callable[[], str]] = (), *, default: bool = False) -> None:
    """Register the signal. The first call to say what to write wins over the default one the runtime makes; harmless where signals are
    unavailable (not the main thread, Windows)."""
    global _configured, _path, _extra, _signal_installed
    if not (default and _configured):
        _path, _extra = path, tuple(extra)
        _configured = _configured or not default
    if _signal_installed or not hasattr(signal, "SIGUSR1"):
        return
    try:
        signal.signal(signal.SIGUSR1, lambda _signum, _frame: dump_tasks())
        _signal_installed = True
    except (ValueError, OSError):
        pass
