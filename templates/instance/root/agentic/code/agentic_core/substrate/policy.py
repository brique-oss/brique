"""The policy of a pilot: which capacities its intentions may address. It is code, applied before any effect, never a request to the model.

  forbidden_capacities  patterns of references the pilot may never invoke (always wins)
  allowed_capacities    patterns it may invoke; absent, everything; present, only these and the contract readers
  a pattern             a reference where * stands for any text, / included (/root/edit.* , */read.meaning)
  forbidden_contexts    contexts whose capacities, and those of the contexts nested in them, the pilot may never invoke (always wins)
  allowed_contexts      contexts it may use; with allowed_capacities, a listed capacity or a listed context is enough
"""
from __future__ import annotations

import posixpath
import re
from typing import Any, Dict, List, Optional, Tuple

# agentic_core exposes these two to every pilot (a pilot cannot use a capacity it never read the contract of): it allows them too
READERS = ("/root/read.capacity", "*/read.meaning")


def matches(pattern: str, ref: str) -> bool:
    return re.fullmatch(".*".join(re.escape(part) for part in pattern.split("*")), ref) is not None


def patterns(value: Any) -> List[str]:
    return [p for p in value if isinstance(p, str) and p] if isinstance(value, list) else []


def context_of(ref: str) -> str:
    return ref.rpartition("/")[0]


def in_context(ref: str, context: str) -> bool:
    """The capacity is addressed to this context or to a context nested in it."""
    where, base = context_of(ref), context.rstrip("/")
    return where == base or where.startswith(base + "/")


def policy_of(pilot: Dict[str, Any], capacity_ref: Optional[Dict[str, Any]]) -> Tuple[List[str], List[str], List[str], List[str]]:
    """(allowed capacities, forbidden capacities, allowed contexts, forbidden contexts) of what decides: the pilot_ref of a Capacity pilot,
    the pilot_definition otherwise."""
    source = capacity_ref if capacity_ref is not None else pilot
    return (patterns(source.get("allowed_capacities")), patterns(source.get("forbidden_capacities")),
            patterns(source.get("allowed_contexts")), patterns(source.get("forbidden_contexts")))


def refusal(allowed: List[str], forbidden: List[str], ref: Any, allowed_contexts: List[str] = (), forbidden_contexts: List[str] = ()) -> Optional[str]:
    """Why the pilot may not invoke this reference, or None when it may."""
    if not isinstance(ref, str) or not ref.startswith("/") or posixpath.normpath(ref) != ref:
        return "the target is not a canonical absolute reference"
    for pattern in forbidden:
        if matches(pattern, ref):
            return f"forbidden by the pilot policy ({pattern})"
    for context in forbidden_contexts:
        if in_context(ref, context):
            return f"forbidden by the pilot policy (context {context})"
    if (allowed or allowed_contexts) and not (any(matches(p, ref) for p in list(allowed) + list(READERS)) or any(in_context(ref, c) for c in allowed_contexts)):
        return "not among the capacities or contexts this pilot may use"
    return None
