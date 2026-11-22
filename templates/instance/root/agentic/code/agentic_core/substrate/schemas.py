from __future__ import annotations

import json
from pathlib import Path
from typing import Any, Dict, List, Optional

from .errors import InvalidRequest

# The engine never enforces schemas (the descriptor's functional.schema_ref is informational), so
# agentic_core validates every state it writes and every structured value it accepts.
# A schema gabarit is the contract: keys carry [] (open), a list of admissible values,
# a nested object, or {schema_ref, cardinality}. A key is required when its note starts
# with "Required." or "Required and"; any other wording stays documentary.

SCHEMA_DIR = Path(__file__).resolve().parents[3] / "schema"
_cache: Dict[str, Dict[str, Any]] = {}


def load_schema(name: str) -> Dict[str, Any]:
    key = str(name).rsplit("/", 1)[-1]
    if key not in _cache:
        with open(SCHEMA_DIR / f"{key}.json", "r", encoding="utf-8") as fh:
            _cache[key] = json.load(fh)
    return _cache[key]


def _gabarit(schema: Dict[str, Any]) -> Dict[str, Any]:
    """A data schema holds the gabarit of the content directly in its functional section."""
    return schema.get("functional", {})


def _is_required(note: Any) -> bool:
    text = str(note or "").lstrip()
    return text.startswith("Required.") or text.startswith("Required and")


def _check(gabarit: Dict[str, Any], value: Any, path: str, errors: List[str]) -> None:
    if not isinstance(value, dict):
        errors.append(f"{path or '<root>'}: expected an object")
        return
    keys = [k for k in gabarit if not k.startswith("_")]
    for key in value:
        if key not in keys:
            errors.append(f"{path}{key}: unexpected key")
    for key in keys:
        spec = gabarit[key]
        present = key in value and value[key] is not None
        if not present:
            if _is_required(gabarit.get(f"_{key}_note")) and key not in value:
                errors.append(f"{path}{key}: required key is missing")
            elif _is_required(gabarit.get(f"_{key}_note")) and value.get(key) is None and key in value:
                errors.append(f"{path}{key}: required value is null")
            continue
        item = value[key]
        if isinstance(spec, list):
            if spec and item not in spec:
                errors.append(f"{path}{key}: {item!r} is not one of {spec}")
        elif isinstance(spec, dict) and "schema_ref" in spec:
            sub = _gabarit(load_schema(spec["schema_ref"]))
            if spec.get("cardinality") == "many":
                if not isinstance(item, list):
                    errors.append(f"{path}{key}: expected a list")
                    continue
                for i, element in enumerate(item):
                    _check(sub, element, f"{path}{key}[{i}].", errors)
            else:
                _check(sub, item, f"{path}{key}.", errors)
        elif isinstance(spec, dict):
            _check(spec, item, f"{path}{key}.", errors)


def validate_value(schema_name: str, value: Any) -> None:
    errors: List[str] = []
    _check(_gabarit(load_schema(schema_name)), value, "", errors)
    if errors:
        raise InvalidRequest(f"{schema_name} violates its schema: " + "; ".join(errors[:8]), details={"schema": schema_name, "errors": errors})


def validate_state(kind: str, content: Dict[str, Any]) -> None:
    """Validate the content (substance) of a Matter of the given kind against its data schema."""
    name = kind if kind.endswith("_data") else f"{kind}_data"
    validate_value(name, content)


def llm_schema(schema_name: str) -> Dict[str, Any]:
    """JSON-Schema-like description of a gabarit, for constrained structured output requests.

    Derived from the schema element itself, so the contract has a single source of truth.
    """
    def describe(gabarit: Dict[str, Any]) -> Dict[str, Any]:
        props: Dict[str, Any] = {}
        for key, spec in gabarit.items():
            if key.startswith("_"):
                continue
            note = gabarit.get(f"_{key}_note")
            if isinstance(spec, list):
                node: Dict[str, Any] = {"enum": spec} if spec else {}
            elif isinstance(spec, dict) and "schema_ref" in spec:
                inner = describe(_gabarit(load_schema(spec["schema_ref"])))
                node = {"type": "array", "items": inner} if spec.get("cardinality") == "many" else inner
            else:
                node = describe(spec)
            if note:
                node["description"] = note
            props[key] = node
        required = [k for k in props if _is_required(gabarit.get(f"_{k}_note"))]
        out: Dict[str, Any] = {"type": "object", "properties": props, "additionalProperties": False}
        if required:
            out["required"] = required
        return out

    return describe(_gabarit(load_schema(schema_name)))


# --- provider-facing projection of a gabarit ----------------------------------------------------------
# Structured-output providers (OpenAI strict, Anthropic json_schema) accept only closed objects whose
# properties are all required and typed. The Brique gabarit leaves many values open ([]), so the
# projection types each leaf from this table by key name, and carries the genuinely free values
# (arbitrary objects) as JSON text that decode_provider_output turns back into values.
LEAF_INTEGER = {"n", "limit", "min_seq", "max_seq", "budget"}
LEAF_BOOLEAN = {"await_response", "include_parent", "include_children"}
LEAF_STRING_ARRAY = {"include_kinds", "exclude_kinds", "message_ids", "continuity_ids"}
LEAF_FREE_JSON = {"parameters", "content", "intention_update", "projection", "correlation"}
FREE_JSON_HINT = None                      # said once, in the guide: free values are JSON written as text
# Structured-output providers cap optional and union-typed parameters (Anthropic: 24 and 16). Rarely filled objects
# travel as JSON text, whose keys are named here so the model still knows them; decoding and the canonical
# validation restore and check the full structure.
FREE_JSON_HINTS = {
    "projection": "JSON object as text. Keys: include_kinds, exclude_kinds, from, to, min_seq, max_seq, message_ids, limit, "
                  "selection (first|last|all), budget, include_parent, include_children, continuity_ids.",
    "correlation": "JSON object as text. Keys: causation_id, intention_id, wait_group_id, continuity_id.",
}
SYSTEM_MANAGED = "Assigned by agentic_core"   # a note carrying this phrase marks a value the model never provides


def _first_sentence(note: Any) -> str:
    """First informative sentence of a note ("Optional." / "Required." carry nothing for a model)."""
    for sentence in str(note or "").split(". "):
        sentence = sentence.strip().rstrip(".")
        if sentence and sentence not in ("Optional", "Required"):
            return sentence + "."
    return ""


def provider_schema(schema_name: str, *, describe_depth: int = 1, style: str = "nullable") -> Dict[str, Any]:
    """Strict-compatible JSON Schema of a gabarit: closed, fully typed objects.

    style "nullable": every property required and nullable (OpenAI-style strict mode).
    style "optional": only the properties that are always present are required and nothing is a union
    (Anthropic accepts at most 16 union-typed parameters, which the nullable form far exceeds).
    """
    if style not in ("nullable", "optional"):
        raise ValueError("style must be nullable or optional")

    def wrap(node: Dict[str, Any], description: Optional[str] = None) -> Dict[str, Any]:
        if style == "optional":
            return dict(node, description=description) if description else node
        out = {"anyOf": [node, {"type": "null"}]}
        return {"description": description, **out} if description else out

    def leaf(key: str, spec: List[Any]) -> Dict[str, Any]:
        if spec:                                                  # admissible values
            return wrap({"type": "string", "enum": list(spec)})
        if key in LEAF_INTEGER:
            return wrap({"type": "integer"})
        if key in LEAF_BOOLEAN:
            return wrap({"type": "boolean"})
        if key in LEAF_STRING_ARRAY:
            return wrap({"type": "array", "items": {"type": "string"}})
        if key in LEAF_FREE_JSON:
            return wrap({"type": "string"}, FREE_JSON_HINTS.get(key, FREE_JSON_HINT))
        return wrap({"type": "string"})

    def build(gabarit: Dict[str, Any], depth: int) -> Dict[str, Any]:
        props: Dict[str, Any] = {}
        mandatory = []
        for key, spec in gabarit.items():
            if key.startswith("_"):
                continue
            note_text = str(gabarit.get(f"_{key}_note") or "")
            if SYSTEM_MANAGED in note_text:
                continue                                   # not the model's to provide
            if key in LEAF_FREE_JSON:
                node = leaf(key, [])
            elif isinstance(spec, list):
                node = leaf(key, spec)
            elif isinstance(spec, dict) and "schema_ref" in spec:
                inner = build(_gabarit(load_schema(spec["schema_ref"])), depth + 1)
                node = wrap({"type": "array", "items": inner} if spec.get("cardinality") == "many" else inner)
            else:
                node = wrap(build(spec, depth + 1))
            note = _first_sentence(gabarit.get(f"_{key}_note"))
            if note and depth < describe_depth and "description" not in node:
                node = {"description": note, **node}
            props[key] = node
            if _is_required(note_text):
                mandatory.append(key)
        required = list(props) if style == "nullable" else mandatory
        return {"type": "object", "properties": props, "required": required, "additionalProperties": False}

    return build(_gabarit(load_schema(schema_name)), 0)


def count_unions(node: Any) -> int:
    """Number of union-typed parameters (type arrays or anyOf) of a JSON Schema."""
    if isinstance(node, dict):
        own = 1 if ("anyOf" in node or isinstance(node.get("type"), list)) else 0
        return own + sum(count_unions(v) for v in node.values())
    if isinstance(node, list):
        return sum(count_unions(v) for v in node)
    return 0


def decode_provider_output(schema_name: str, value: Any) -> Any:
    """Inverse of the projection: drop the nulls of absent optional values and decode free JSON text.

    Idempotent: a canonical value (already decoded, no nulls) comes back unchanged.
    """
    def decode_free(text: str) -> Any:
        stripped = text.strip()
        if stripped[:1] in ("{", "["):
            try:
                return json.loads(stripped)
            except ValueError:
                return text
        return text

    def walk(gabarit: Dict[str, Any], node: Any) -> Any:
        if not isinstance(node, dict):
            return node
        out: Dict[str, Any] = {}
        for key, item in node.items():
            if item is None:
                continue                                          # an absent optional value
            spec = gabarit.get(key)
            if key in LEAF_FREE_JSON and isinstance(item, str):
                out[key] = decode_free(item)
            elif isinstance(spec, dict) and "schema_ref" in spec:
                sub = _gabarit(load_schema(spec["schema_ref"]))
                if spec.get("cardinality") == "many" and isinstance(item, list):
                    out[key] = [walk(sub, e) for e in item]
                else:
                    out[key] = walk(sub, item)
            elif isinstance(spec, dict):
                out[key] = walk(spec, item)
            else:
                out[key] = item
        return out

    return walk(_gabarit(load_schema(schema_name)), value)


def extract_json(text: str) -> Any:
    """Tolerant reading of a textual answer: strict JSON first, then a fenced or surrounded JSON object."""
    try:
        return json.loads(text)
    except ValueError:
        pass
    body = text.strip()
    if body.startswith("```"):
        body = body.split("\n", 1)[1] if "\n" in body else body
        body = body.rsplit("```", 1)[0]
        try:
            return json.loads(body.strip())
        except ValueError:
            pass
    start, end = text.find("{"), text.rfind("}")
    if 0 <= start < end:
        try:
            return json.loads(text[start:end + 1])
        except ValueError:
            pass
    return text
