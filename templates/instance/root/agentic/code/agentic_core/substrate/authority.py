"""Whose authority the work of agentic_core runs with when agentic_core called itself.

When agentic_core is a restricted principal of the policy (local.agentic_core), a call it makes to one of its own capacities through the engine
is admitted under its own principal, and the engine serves it with a delegation: a handle that lets the callee act on behalf of the caller while
that request is open, for a bounded number of calls and a bounded time. A run of pilot.activate lasts as long as the work of its continuity, far
beyond those bounds, so the calls of a run started by agentic_core itself must not depend on that handle. They carry agentic_core's own
authority instead, as every call it makes without a delegation does: the principal is the same, so nothing is gained.

The test is the origin the engine delivered: a restricted wrapper cannot choose it outside its own boundary (the engine pins it), so a call whose
origin is this context and one of the capacities agentic_core itself binds was emitted by agentic_core. A call from anyone else (a browser, an
MCP client, another wrapper, a DSL plan) keeps its delegation and therefore the rights of its own caller: no caller can borrow agentic_core's.
"""
from __future__ import annotations

from wrapper.execution import current_delegation, current_intention

from .ids import CONTEXT

# The capacities agentic_core binds (bindings.py); a contract test keeps the two lists equal.
OWN_CAPACITIES = frozenset({
    "continuity.create", "continuity.delete", "continuity.project", "continuity.succeed", "continuity.stranded",
    "input.answer", "input.request", "mailbox.send", "mailbox.read", "mailbox.ack", "mailbox.release",
    "pilot.activate", "pilot.infer", "pilot.launch", "reading.mark",
})


def served_for_itself() -> bool:
    """Was the capacity being served called by agentic_core itself?"""
    intention = current_intention.get()
    origin = (intention or {}).get("from") if isinstance(intention, dict) else None
    if not isinstance(origin, dict):
        return False
    return origin.get("context") == CONTEXT and origin.get("cap") in OWN_CAPACITIES


def own_authority_when_self_called() -> bool:
    """Drop the delegation of a request agentic_core sent to itself, for the rest of this task and the tasks it creates. Returns whether it did."""
    if current_delegation.get() and served_for_itself():
        current_delegation.set("")
        return True
    return False
