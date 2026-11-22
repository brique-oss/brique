"""What the plumbing of Brique and agentic costs, in tokens, in a real run.

The formalism is root/agentic/document/token_measurement.md. The input is the list of recorded inferences (`real_calls`): the canonical
request, the usage the provider billed and the output. Nothing here changes what a pilot does.

Absolute tokens only: nothing is compared with the amount of information of a test. Each distinct text is counted once by the provider
(the `tokens.count` capacity of /root/llm, passed in as `count`); without it the billed input is spread over the parts by characters.
"""
from __future__ import annotations

import hashlib
import json
from typing import Any, Awaitable, Callable, Dict, List, Optional, Tuple

FIXED = {"framework", "output_guide", "output_contract", "pilot_prompt", "pilot_rules", "known_capacities", "policy", "initial_intention"}
PER_TURN = {"state_block", "correction", "compaction_instruction", "tool_envelope", "tool_error", "decision_structure"}
PLUMBING = FIXED - {"pilot_prompt", "pilot_rules", "known_capacities", "policy", "initial_intention"} | PER_TURN
CONTENT = {"summary", "user_message", "continuity_message", "tool_payload", "decision_content", "step_prompt"}
ENGINE_KEYS = {"brique", "byte_range", "revision", "kind"}              # the engine's metadata around a Matter or an answer
DECISION_TEXT_KEYS = ("content", "intention_update")
DECISION_KEYS = {"intentions", "messages", "wait_group", "intention_update", "succession", "projection_request"}
SYSTEM_MARKERS = [                                                       # where the parts of the system text begin (see build_request)
    ("pilot_rules", "\n\nRules:\n"), ("output_guide", "\n\nReply with one JSON object matching the schema."),
    ("known_capacities", "\n\nAvailable capacities (target_capacity = ref):"), ("policy", "\n\nNever invoke (refused"),
    ("policy", "\n\nYou may invoke only:"), ("initial_intention", "\n\nInitial intention:\n")]

Counter = Callable[[Dict[str, Any]], Awaitable[int]]      # a canonical request -> the provider's input tokens


def dumps(value: Any) -> str:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))


def parse(text: str) -> Optional[Any]:
    if not isinstance(text, str) or not text or text[0] not in "{[":
        return None
    try:
        return json.loads(text)
    except ValueError:
        return None


def strip_engine(value: Any) -> Any:
    if isinstance(value, dict):
        return {k: strip_engine(v) for k, v in value.items() if k not in ENGINE_KEYS}
    if isinstance(value, list):
        return [strip_engine(v) for v in value]
    return value


def decision_content_chars(value: Any) -> int:
    total = 0
    if isinstance(value, dict):
        for key, item in value.items():
            total += len(item) if key in DECISION_TEXT_KEYS and isinstance(item, str) else decision_content_chars(item)
    elif isinstance(value, list):
        total += sum(decision_content_chars(item) for item in value)
    return total


def useful_chars(body: Any, length: int) -> int:
    """A decision of the common contract says its texts; an output of any other schema (a step of a plan) is content as a whole."""
    if isinstance(body, dict) and not (set(body) & DECISION_KEYS):
        return length
    return min(decision_content_chars(body), length)


def intention_targets(messages: List[Dict[str, Any]]) -> Dict[str, str]:
    """intention id -> capacity, read in the decisions the pilot already sent."""
    out: Dict[str, str] = {}
    for message in messages:
        if message.get("role") != "assistant":
            continue
        body = parse(message.get("content"))
        for item in (body or {}).get("intentions", []) if isinstance(body, dict) else []:
            if isinstance(item, dict) and item.get("id"):
                out[item["id"]] = item.get("target_capacity", "?")
    return out


def split_system(text: str, frame_text: Optional[str] = None) -> List[Dict[str, Any]]:
    """The parts of the system text (category, text). Without the frame text of the run, everything before the first marker is the framework."""
    found = sorted((text.find(marker), name) for name, marker in SYSTEM_MARKERS if text.find(marker) >= 0)
    head_end = found[0][0] if found else len(text)
    head, parts = text[:head_end], []
    if frame_text and head.startswith(frame_text):
        parts.append({"category": "framework", "text": frame_text})
        if len(head) > len(frame_text):
            parts.append({"category": "pilot_prompt" if found else "step_prompt", "text": head[len(frame_text):]})
    else:
        parts.append({"category": "framework", "text": head})
    for i, (start, name) in enumerate(found):
        parts.append({"category": name, "text": text[start:found[i + 1][0] if i + 1 < len(found) else len(text)]})
    return parts


def classify(message: Dict[str, Any], targets: Dict[str, str], frame_text: Optional[str] = None) -> List[Dict[str, Any]]:
    """The parts of one message: {category, chars, capacity?}; for the system text also the part's own text. The chars add up to the message."""
    text = message.get("content")
    text = text if isinstance(text, str) else dumps(text)
    role, body = message.get("role"), parse(text)
    if role == "system":
        return [dict(p, chars=len(p["text"]), unit="system") for p in split_system(text, frame_text)]
    if role == "assistant":
        if body is None:
            return [{"category": "decision_content", "chars": len(text)}]
        useful = useful_chars(body, len(text))
        return [{"category": "decision_content", "chars": useful}, {"category": "decision_structure", "chars": len(text) - useful}]
    if text.startswith("Summary of the previous continuity"):
        return [{"category": "summary", "chars": len(text)}]
    if isinstance(body, dict):
        if "current_intention" in body:
            stimulus = body.get("active_stimulus")
            kind = "compaction_instruction" if isinstance(stimulus, dict) and stimulus.get("kind") == "compaction" else "state_block"
            return [{"category": kind, "chars": len(text)}]
        if "validation_error" in body:
            return [{"category": "correction", "chars": len(text)}]
        if "intention_id" in body:
            capacity = targets.get(body["intention_id"], "?")
            response = body.get("response") if isinstance(body.get("response"), dict) else {}
            if response.get("status") == "error" or "error" in response:
                return [{"category": "tool_error", "chars": len(text), "capacity": capacity}]
            payload = min(len(dumps(strip_engine(response.get("payload")))) if "payload" in response else 0, len(text))
            return [{"category": "tool_payload", "chars": payload, "capacity": capacity},
                    {"category": "tool_envelope", "chars": len(text) - payload, "capacity": capacity}]
        if set(body) >= {"from", "content"}:
            return [{"category": "continuity_message", "chars": len(text)}]
    return [{"category": "user_message", "chars": len(text)}]


def unit_key(role: str, text: str) -> str:
    return role + ":" + hashlib.sha1(text.encode()).hexdigest()


def segments_of(request: Dict[str, Any], frame_text: Optional[str] = None) -> List[Dict[str, Any]]:
    """The segments in the order the provider reads them: the output schema, then the messages (the system text in its parts).

    A `unit` is a text the provider counts: a part of the system text, a message, the schema. A message cut in two categories is one unit."""
    segments: List[Dict[str, Any]] = []
    output = request.get("output") or {}
    if output:
        text = dumps(output.get("schema", output))
        segments.append({"category": "output_contract", "chars": len(text), "unit_key": unit_key("schema", text), "unit_role": "schema", "unit_text": text,
                         "unit_chars": len(text), "key": unit_key("schema", text)})
    messages = request.get("messages") or []
    targets = intention_targets(messages)
    for message in messages:
        text = message.get("content")
        text = text if isinstance(text, str) else dumps(text)
        role = message.get("role", "?")
        for part in classify(message, targets, frame_text):
            if part["chars"] <= 0:
                continue
            if role == "system":
                key = unit_key("system", part["text"])
                segments.append(dict(part, unit_key=key, unit_role="system", unit_text=part["text"], unit_chars=len(part["text"]), key=key))
            else:
                key = unit_key(role, text)
                segments.append(dict(part, unit_key=key, unit_role=role, unit_text=text, unit_chars=len(text), key=key + ":" + part["category"]))
    return segments


def base_request(request: Dict[str, Any], messages: List[Dict[str, Any]], output: Optional[Dict[str, Any]] = None) -> Dict[str, Any]:
    model = request.get("model") or "claude-haiku-4-5-20251001"
    out = {"provider": request.get("provider") or "anthropic", "model": model, "messages": messages,
           "parameters": {"max_output_tokens": 1}, "requirements": {"provider": request.get("provider") or "anthropic", "model": model}}
    if output:
        out["output"] = output
    return out


async def count_units(calls: List[Dict[str, Any]], frame_text: Optional[str], count: Counter) -> Dict[str, int]:
    """The exact tokens of each distinct text of the run, as the apport of that text over a base request."""
    first = next((c["request"] for c in calls if c.get("request")), None)
    if first is None:
        return {}
    base_messages = [{"role": "user", "content": "x"}]
    base = await count(base_request(first, base_messages))
    schema = next((c["request"]["output"] for c in calls if (c.get("request") or {}).get("output")), None)
    exact: Dict[str, int] = {}
    seen: Dict[str, Dict[str, Any]] = {}
    for call in calls:
        for segment in segments_of(call.get("request") or {}, frame_text):
            seen.setdefault(segment["unit_key"], segment)
    for key, segment in seen.items():
        role, text = segment["unit_role"], segment["unit_text"]
        if role == "schema":
            with_schema = await count(base_request(first, base_messages, schema))
            exact[key] = max(with_schema - base, 0)
        elif role == "system":
            exact[key] = max(await count(base_request(first, [{"role": "system", "content": text}] + base_messages)) - base, 0)
        elif role == "assistant":
            exact[key] = max(await count(base_request(first, base_messages + [{"role": "assistant", "content": text}])) - base, 0)
        else:
            exact[key] = max(await count(base_request(first, [{"role": "user", "content": text}])) - base, 0)
    return exact


def allocate(segments: List[Dict[str, Any]], input_tokens: int, cached_tokens: int, exact: Optional[Dict[str, int]]) -> None:
    """Tokens of each segment: the unit's exact count shared inside its message by characters; else the billed input spread by characters.
    Then the cached prefix, in order."""
    if exact:
        floats = [exact.get(s["unit_key"], 0) * s["chars"] / (s["unit_chars"] or 1) for s in segments]      # a message's parts share its exact count
        total_target = round(sum(floats))
    else:
        total = sum(s["chars"] for s in segments) or 1
        floats = [s["chars"] * input_tokens / total for s in segments]
        total_target = input_tokens
    floors = [int(x) for x in floats]
    for i in sorted(range(len(segments)), key=lambda i: floats[i] - floors[i], reverse=True)[: max(total_target - sum(floors), 0)]:
        floors[i] += 1
    cached_left = min(cached_tokens, input_tokens)
    for segment, tokens in zip(segments, floors):
        taken = min(tokens, cached_left)
        cached_left -= taken
        segment.update(tokens=tokens, cached=taken)


def output_split(call: Dict[str, Any]) -> Tuple[int, int]:
    """(content, structure) output tokens of an inference, by characters of what the model said."""
    out_tokens = (call.get("usage") or {}).get("output_tokens", 0)
    content = ((call.get("response") or {}).get("output") or {}).get("content")
    text = content if isinstance(content, str) else dumps(content)
    if not text or text == "null":
        return 0, out_tokens
    body = parse(text)
    useful = len(text) if body is None else useful_chars(body, len(text))
    if isinstance(body, dict) and isinstance(body.get("succession"), dict):
        useful = len(text)                                  # a summary is content
    useful_tokens = round(out_tokens * useful / len(text))
    return useful_tokens, out_tokens - useful_tokens


def profile(calls: List[Dict[str, Any]], frame_text: Optional[str] = None, exact: Optional[Dict[str, int]] = None) -> Dict[str, Any]:
    """`exact` comes from count_units; without it the tokens are an estimate and the report says so."""
    seen: Dict[str, set] = {}
    inferences: List[Dict[str, Any]] = []
    for index, call in enumerate(calls):
        request, usage = call.get("request") or {}, call.get("usage") or {}
        if not request or not usage:
            continue
        continuity = (request.get("metadata") or {}).get("continuity_id", "?")
        segments = segments_of(request, frame_text)
        allocate(segments, usage.get("input_tokens", 0), usage.get("cached_input_tokens", 0), exact)
        known = seen.setdefault(continuity, set())
        for segment in segments:
            segment["replayed"] = segment["key"] in known
        known.update(s["key"] for s in segments)
        content_out, structure_out = output_split(call)
        inferences.append({
            "index": index, "continuity": continuity,
            "phase": "compaction" if any(s["category"] == "compaction_instruction" for s in segments) else "turn",
            "billed_input": usage.get("input_tokens", 0), "counted_input": sum(s["tokens"] for s in segments),
            "cached_tokens": min(usage.get("cached_input_tokens", 0), usage.get("input_tokens", 0)),
            "output_tokens": usage.get("output_tokens", 0), "output_content": content_out, "output_structure": structure_out,
            "segments": segments})
    return summarize(inferences, exact is not None)


def summarize(inferences: List[Dict[str, Any]], is_exact: bool) -> Dict[str, Any]:
    n = len(inferences) or 1
    by_category: Dict[str, Dict[str, int]] = {}
    by_capacity: Dict[str, Dict[str, int]] = {}
    by_continuity: Dict[str, Dict[str, int]] = {}
    by_phase: Dict[str, Dict[str, int]] = {}
    fixed_one: Dict[str, int] = {}
    per_inference: List[Dict[str, Any]] = []
    for inference in inferences:
        history = history_new = history_replayed = 0
        for segment in inference["segments"]:
            cat = segment["category"]
            row = by_category.setdefault(cat, {"occurrences": 0, "tokens": 0, "cached": 0, "new": 0, "replayed": 0})
            row["occurrences"] += 1
            row["tokens"] += segment["tokens"]
            row["cached"] += segment["cached"]
            row["replayed" if segment["replayed"] else "new"] += segment["tokens"]
            if cat in FIXED:
                fixed_one[cat] = max(fixed_one.get(cat, 0), segment["tokens"])
            else:
                history += segment["tokens"]
                history_new += 0 if segment["replayed"] else segment["tokens"]
                history_replayed += segment["tokens"] if segment["replayed"] else 0
            if segment.get("capacity"):
                cap = by_capacity.setdefault(segment["capacity"], {"tokens": 0, "payload": 0, "envelope": 0, "error": 0})
                cap["tokens"] += segment["tokens"]
                kind = {"tool_payload": "payload", "tool_envelope": "envelope", "tool_error": "error"}.get(cat)
                if kind:
                    cap[kind] += segment["tokens"]
        for table, key in ((by_continuity, inference["continuity"]), (by_phase, inference["phase"])):
            r = table.setdefault(key, {"inferences": 0, "input_tokens": 0, "output_tokens": 0})
            r["inferences"] += 1
            r["input_tokens"] += inference["billed_input"]
            r["output_tokens"] += inference["output_tokens"]
        per_inference.append({k: v for k, v in inference.items() if k != "segments"}
                             | {"fixed": sum(s["tokens"] for s in inference["segments"] if s["category"] in FIXED),
                                "history": history, "history_new": history_new, "history_replayed": history_replayed,
                                "gap": inference["billed_input"] - inference["counted_input"]})
    per_turn = {cat: {"occurrences": row["occurrences"], "tokens": row["tokens"], "per_occurrence": round(row["tokens"] / row["occurrences"], 1)}
                for cat, row in by_category.items() if cat in PER_TURN and row["occurrences"]}
    by_category_sorted = dict(sorted(by_category.items(), key=lambda kv: -kv[1]["tokens"]))
    return {
        "exact": is_exact, "inferences": len(inferences),
        "billed_input": sum(i["billed_input"] for i in inferences), "counted_input": sum(i["counted_input"] for i in inferences),
        "cached_tokens": sum(i["cached_tokens"] for i in inferences), "output_tokens": sum(i["output_tokens"] for i in inferences),
        "output_content": sum(i["output_content"] for i in inferences), "output_structure": sum(i["output_structure"] for i in inferences),
        "fixed_per_inference": {"by_element": dict(sorted(fixed_one.items(), key=lambda kv: -kv[1])), "total": sum(fixed_one.values())},
        "per_turn": dict(sorted(per_turn.items(), key=lambda kv: -kv[1]["tokens"])),
        "history": {"sent": sum(i["history"] for i in per_inference), "new": sum(i["history_new"] for i in per_inference),
                    "replayed": sum(i["history_replayed"] for i in per_inference),
                    "last_inference_by_continuity": {i["continuity"]: i["history"] for i in per_inference}},
        "by_category": by_category_sorted, "by_capacity": dict(sorted(by_capacity.items(), key=lambda kv: -kv[1]["tokens"])),
        "by_continuity": by_continuity, "by_phase": by_phase, "per_inference": per_inference,
    }


def render(report: Dict[str, Any]) -> str:
    fixed, hist = report["fixed_per_inference"], report["history"]
    lines = [
        f"{report['inferences']} inferences ({'exact provider counts' if report['exact'] else 'ESTIMATE from the billed input'}): "
        f"billed input {report['billed_input']}, counted {report['counted_input']}, gap {report['billed_input'] - report['counted_input']}, "
        f"read from cache {report['cached_tokens']}, output {report['output_tokens']} (content {report['output_content']}, structure {report['output_structure']})",
        "", f"FIXED, paid by every inference: {fixed['total']} tokens"]
    for name, tokens in fixed["by_element"].items():
        lines.append(f"  {name:24}{tokens:>7}")
    lines += ["", f"PER TURN (added by the exchanges)  {'occurrences':>11}{'tokens':>8}{'per occurrence':>16}"]
    for name, row in report["per_turn"].items():
        lines.append(f"  {name:24}{row['occurrences']:>9}{row['tokens']:>8}{row['per_occurrence']:>16}")
    lines += ["", f"HISTORY sent over the run: {hist['sent']} tokens, of which new {hist['new']}, replayed {hist['replayed']}",
              "", f"{'category':24}{'occ':>5}{'tokens':>8}{'cached':>8}{'new':>8}{'replayed':>9}"]
    for name, row in report["by_category"].items():
        lines.append(f"{name:24}{row['occurrences']:>5}{row['tokens']:>8}{row['cached']:>8}{row['new']:>8}{row['replayed']:>9}")
    if report["by_capacity"]:
        lines += ["", f"{'tool results by capacity':52}{'tokens':>8}{'payload':>9}{'envelope':>9}{'error':>7}"]
        for name, row in list(report["by_capacity"].items())[:12]:
            lines.append(f"{name[-50:]:52}{row['tokens']:>8}{row['payload']:>9}{row['envelope']:>9}{row['error']:>7}")
    lines += ["", "by phase: " + ", ".join(f"{k} {v['inferences']} inf / {v['input_tokens']} in / {v['output_tokens']} out" for k, v in report["by_phase"].items()),
              "per inference (billed in, fixed, history, cached, out): " + "; ".join(
                  f"{i['index']}:{i['billed_input']}/{i['fixed']}/{i['history']}/{i['cached_tokens']}/{i['output_tokens']}" for i in report["per_inference"])]
    return "\n".join(lines)
