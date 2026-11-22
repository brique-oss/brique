"""The small JSON Schema subset a structured inference step uses, validated here because the engine enforces none.

Supported: type (object, array, string, integer, number, boolean, null; or a list of them), enum, properties, required,
additionalProperties, items, anyOf, description. Anything else in a schema is ignored by the validator, never trusted.
"""
from __future__ import annotations

from typing import Any, Dict, List

# Limits of the strictest provider we know (Anthropic structured outputs), kept with a margin.
MAX_OPTIONAL_PARAMETERS = 20
MAX_UNION_PARAMETERS = 12


def _type_ok(expected: str, value: Any) -> bool:
    if expected == "object":
        return isinstance(value, dict)
    if expected == "array":
        return isinstance(value, list)
    if expected == "string":
        return isinstance(value, str)
    if expected == "boolean":
        return isinstance(value, bool)
    if expected == "integer":
        return isinstance(value, int) and not isinstance(value, bool)
    if expected == "number":
        return isinstance(value, (int, float)) and not isinstance(value, bool)
    if expected == "null":
        return value is None
    return False


def violations(schema: Dict[str, Any], value: Any, path: str = "$") -> List[str]:
    """Every way `value` departs from `schema`; empty when it conforms."""
    out: List[str] = []
    if not isinstance(schema, dict):
        return out
    if "anyOf" in schema:
        branches = [violations(b, value, path) for b in schema["anyOf"]]
        if not any(not b for b in branches):
            out.append(f"{path}: matches none of the allowed forms")
        return out
    declared = schema.get("type")
    if declared is not None:
        types = declared if isinstance(declared, list) else [declared]
        if not any(_type_ok(t, value) for t in types):
            out.append(f"{path}: expected {declared}, got {type(value).__name__}")
            return out
    if "enum" in schema and value not in schema["enum"]:
        out.append(f"{path}: {value!r} is not one of {schema['enum']}")
    if isinstance(value, dict):
        props = schema.get("properties") or {}
        for key in schema.get("required") or []:
            if key not in value:
                out.append(f"{path}.{key}: required value is missing")
        if schema.get("additionalProperties") is False:
            for key in value:
                if key not in props:
                    out.append(f"{path}.{key}: unexpected property")
        for key, sub in props.items():
            if key in value:
                out.extend(violations(sub, value[key], f"{path}.{key}"))
    if isinstance(value, list) and isinstance(schema.get("items"), dict):
        for i, element in enumerate(value):
            out.extend(violations(schema["items"], element, f"{path}[{i}]"))
    return out


def schema_problems(schema: Any) -> List[str]:
    """Why a schema cannot be sent to a strict structured-output provider; empty when it can."""
    problems: List[str] = []
    optional = unions = 0

    def walk(node: Any, path: str) -> None:
        nonlocal optional, unions
        if not isinstance(node, dict):
            return
        if "anyOf" in node or isinstance(node.get("type"), list):
            unions += 1
        if node.get("type") == "object" or "properties" in node:
            if "properties" not in node:
                problems.append(f"{path}: an object needs explicit properties")
            if node.get("additionalProperties") is not False:
                problems.append(f"{path}: an object must set additionalProperties to false")
            props = node.get("properties") or {}
            optional += len(set(props) - set(node.get("required") or []))
            for key, sub in props.items():
                walk(sub, f"{path}.{key}")
        if isinstance(node.get("items"), dict):
            walk(node["items"], f"{path}[]")
        for branch in node.get("anyOf") or []:
            walk(branch, path)

    if not isinstance(schema, dict) or schema.get("type") != "object":
        return ["$: the root of an output schema must be an object"]
    walk(schema, "$")
    if optional > MAX_OPTIONAL_PARAMETERS:
        problems.append(f"$: {optional} optional parameters (limit {MAX_OPTIONAL_PARAMETERS})")
    if unions > MAX_UNION_PARAMETERS:
        problems.append(f"$: {unions} union-typed parameters (limit {MAX_UNION_PARAMETERS})")
    return problems
