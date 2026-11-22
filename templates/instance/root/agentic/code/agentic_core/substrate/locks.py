from __future__ import annotations

from wrapper.scheduler import KeyedLocks

# Critical-section locks keyed by continuity_id. Infrastructure, not business state: a lock is a local optimization that reduces revision
# conflicts inside one wrapper process. expected_revision on every write stays the actual guarantee. The mechanism (sorted keys, a wait that
# gives the execution slot back) is the one of the wrapper library; this is the table of this process.
#
# Scope rule: a lock is held only around "read, recompute, guarded write". It is never held across an LLM inference, an external invoke or a
# wait, and a parent lock is never held around a call on a child.

_table = KeyedLocks()
_locks = _table.entries      # continuity_id -> [lock, holders]; read by the tests that check that nothing leaks


def critical(*continuity_ids: str, yielding=None):
    """`yielding` (the port's): given, a wait for a lock that is taken does not hold the execution slot of the wrapper."""
    return _table.critical(*continuity_ids, yielding=yielding)
