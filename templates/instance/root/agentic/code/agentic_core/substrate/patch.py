"""Semantic operations applied to the content (substance) of a Matter.

The engine's semantic_patch never reaches a Matter's substance, so agentic_core derives its operations
from what it just read, applies them locally and commits the whole document with one guarded write.
The operations keep the engine's semantics: add and remove are idempotent on a list value.
"""
from __future__ import annotations

import copy
from typing import Any, Dict, List


def apply_operations(document: Dict[str, Any], operations: List[Dict[str, Any]]) -> Dict[str, Any]:
    doc = copy.deepcopy(document)
    for op in operations:
        path = op["path"]
        parent = doc
        for segment in path[:-1]:
            parent = parent[segment]
        key, kind = path[-1], op["op"]
        if kind == "add":
            target = parent.setdefault(key, [])
            if op["value"] not in target:
                target.append(copy.deepcopy(op["value"]))
        elif kind == "remove":
            if key in parent and op["value"] in parent[key]:
                parent[key].remove(op["value"])
        elif kind == "delete":
            parent.pop(key, None)
        elif kind == "set":
            parent[key] = copy.deepcopy(op["value"])
        else:
            raise ValueError(f"unsupported semantic operation {kind!r}")
    return doc
