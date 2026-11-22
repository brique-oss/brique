"""Hard crashes: the process of the agentic_core wrapper is killed with SIGKILL, with no chance to stop cleanly.

The engine does not restart a dead wrapper by itself: it starts a new one at the next call addressed to it. What the dead process
leaves is the durable state only (the Matters of the continuities), which is what the scenarios check.
"""
import os
import signal
import subprocess
import sys
import time
import unittest

WRAPPER, CONTEXT = "agentic_core", "/root/agentic"
ON_MAC = sys.platform == "darwin"
SKIP_HARD_KILL = not ON_MAC
SKIP_HARD_KILL_REASON = "finding the wrapper process by its environment uses `ps eww` (macOS)"


def wrapper_pids():
    """The processes of the agentic_core wrapper: the ones whose environment names it and its context."""
    out = subprocess.run(["ps", "eww", "-ax", "-o", "pid=,command="], capture_output=True, text=True).stdout
    pids = []
    for line in out.splitlines():
        tokens = set(line.split())
        if f"BRIQUE_WRAPPER_NAME={WRAPPER}" in tokens and f"BRIQUE_CTX_ID={CONTEXT}" in tokens and "main.py" in tokens:
            pids.append(int(line.split()[0]))
    return pids


def kill_wrapper(timeout: float = 5.0) -> int:
    """SIGKILL the one running wrapper and wait until the process is gone. Returns its pid."""
    pids = wrapper_pids()
    if len(pids) != 1:
        raise unittest.SkipTest(f"expected exactly one {WRAPPER} wrapper process, found {pids}")
    os.kill(pids[0], signal.SIGKILL)
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            os.kill(pids[0], 0)
        except ProcessLookupError:
            return pids[0]
        time.sleep(0.02)
    raise AssertionError(f"the wrapper process {pids[0]} did not die")
