"""A diagnostic on demand for a wrapper that stops answering.

The mechanism (the signal SIGUSR1, the state of the execution slots, every task with the chain of what it awaits) is the one of the wrapper
library (wrapper/diagnostic.py). This adds what only `agentic_core` knows: the inference windows.

    kill -USR1 <pid of the wrapper>           the file is $TMPDIR/agentic_core_tasks_<pid>.txt

It only reads: it changes nothing, and does nothing until the signal comes.
"""
from __future__ import annotations

import os
import tempfile
import time

from wrapper import diagnostic as library


def _windows_state() -> str:
    """The inference windows that are not idle: who holds each one, who waits in which order, and what is reserved until when. A window held by a
    task that no longer exists, or a reservation nobody will lift, is what a stall of mailbox.send and pilot.activate looks like from outside."""
    try:
        import sys
        window_module = sys.modules.get("substrate.inference_window")
        if window_module is None:                              # loaded by file path, not by name: look for it among the modules that have the state
            window_module = next((m for m in list(sys.modules.values()) if getattr(m, "__name__", "").endswith("inference_window") and hasattr(m, "_windows")), None)
        if window_module is None:
            return "windows: module not found"
        now = time.monotonic()
        lines = []
        for cid, w in sorted(window_module._windows.items()):
            reserved = f", reserved {sorted(w.reserved[0])} for another {max(0.0, w.reserved[1] - now):.1f} s" if w.reserved else ""
            queue = [(kind, token, "loop" if len(entry) > 3 and entry[3] else "outside") for entry in w.queue for kind, _future, token in [entry[:3]]]
            lines.append(f"  window {cid}: holder={w.holder}, queue={queue}{reserved}")
        return "windows: " + (f"{len(lines)} not idle\n" + "\n".join(lines) if lines else "all idle")
    except Exception as exc:
        return f"windows: unreadable ({exc})"


def dump_tasks(path: str | None = None) -> str:
    return library.dump_tasks(path or _path(), extra=[_windows_state])


def _path() -> str:
    return os.path.join(tempfile.gettempdir(), f"agentic_core_tasks_{os.getpid()}.txt")


def install() -> None:
    """Register the signal; harmless where signals are unavailable (not the main thread, Windows)."""
    library.install(_path(), [_windows_state])
