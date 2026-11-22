from __future__ import annotations

import hashlib
import json
from typing import Any, Dict, List, Optional

from substrate import pilot_ref as pilot_refs
from substrate import continuity_store, inference_window
from substrate.errors import ConsistencyError, InvalidRequest, NotFound, Refused
from substrate.conversation import conversation
from substrate.frame import split_frame
from substrate.ids import CONTEXT, check_continuity_id, matter_ids
from substrate.journal import append_trace, measure_line, usage_detail
from substrate.jsonschema import schema_problems, violations
from substrate.port import BriquePort, resolve_params
from substrate.schemas import extract_json, validate_value

CAP = "pilot.infer"
PART_SEPARATOR = "\n\n"          # between the parts of an assembled prompt: fixed, never configurable


# <brique:capacity name="pilot.infer">
async def pilot_infer(runtime, params: dict) -> dict:
    return await run(BriquePort(runtime, CAP), await resolve_params(params))      # a large parameter arrives as a transit reference
# </brique:capacity>


ORDERS = ("matters_first", "parts_first")


def matter_slot(params: Dict[str, Any], name: str) -> list:
    """The Matters bound under a name of the "@matter" input, in the order given: a slot holds one Matter or several."""
    slot = (params.get("@matter") or {}).get(name)
    entries = slot if isinstance(slot, list) else ([slot] if slot else [])
    refs = []
    for i, entry in enumerate(entries):
        context, matter_id = (entry.get("context"), entry.get("id")) if isinstance(entry, dict) else (None, None)
        if not (isinstance(context, str) and context.startswith("/") and isinstance(matter_id, str) and matter_id.strip()):
            raise InvalidRequest(f"{name}[{i}] must be a Matter reference")
        refs.append({"context": context, "id": matter_id})
    return refs


def valid_part(part: Any) -> bool:
    """A text part is a non-empty string; any other JSON value (an object, a list, a number, a boolean) is a part too; null is not."""
    if isinstance(part, str):
        return bool(part.strip())
    return part is not None


def part_text(part: Any) -> str:
    """A part as text: a string as it is, any other value as compact JSON with its keys in order, so the same value is the same text."""
    return part if isinstance(part, str) else json.dumps(part, ensure_ascii=False, sort_keys=True, separators=(",", ":"))


def check_prompt(params: Dict[str, Any]) -> tuple:
    """The prompt as given: (instructions, texts, matters, order). Exactly one of instructions, or texts and/or Matters.

    texts is the list of parts (the parameter prompt_parts: texts, or any JSON value given as compact JSON), matters the Matters bound
    under prompt_matters, order which of the two comes first when both are given."""
    instructions, texts, order = params.get("instructions"), params.get("prompt_parts"), params.get("prompt_order")
    matters = matter_slot(params, "prompt_matters")
    if instructions is not None and (texts is not None or matters):
        raise InvalidRequest("give instructions, or prompt_parts and/or prompt_matters, not both")
    if instructions is not None:
        if not isinstance(instructions, str) or not instructions.strip():
            raise InvalidRequest("instructions must be a non-empty string")
        return instructions, None, [], None
    if texts is None and not matters:
        raise InvalidRequest("instructions, prompt_parts or prompt_matters is required")
    if texts is not None and not (isinstance(texts, list) and texts and all(valid_part(t) for t in texts)):
        raise InvalidRequest("prompt_parts must be a non-empty list of non-empty texts or JSON values (null excepted)")
    if order is not None and order not in ORDERS:
        raise InvalidRequest(f"prompt_order must be one of {', '.join(ORDERS)}")
    if texts is not None and matters and order is None:
        raise InvalidRequest("prompt_order is required when both prompt_parts and prompt_matters are given")
    return None, texts, matters, order


async def assemble(port, texts: Optional[list], matters: list, order: Optional[str]) -> str:
    """The prompt as one text: the Matters (each its substance read as is) and the texts, in the order given inside each list and
    in the order asked between the two, a blank line between all of them. Nothing is interpreted, extracted or rewritten."""
    read = []
    for i, ref in enumerate(matters):
        try:
            text = await port.matter_read_text(ref["context"], ref["id"])
        except (NotFound, Refused) as exc:         # the engine says "refused" for a Matter it does not know: keep the class, say which part
            raise type(exc)(f"prompt_matters[{i}] ({ref['context']}/{ref['id']}) cannot be read: {exc.message}",
                            details=dict(exc.details, **ref)) from exc
        if not text.strip():
            raise InvalidRequest(f"prompt_matters[{i}]: the Matter is empty", details=ref)
        read.append(text)
    texts = [part_text(t) for t in (texts or [])]
    pieces = (texts + read) if order == "parts_first" else (read + texts)
    return PART_SEPARATOR.join(pieces)


def step_key(message_id: Optional[str], step: str, instructions: str, output_schema: Dict[str, Any], model: Dict[str, Any]) -> str:
    """Identity of one inference step: the same step of the same message with the same content is the same step.
    The prompt is the text sent, however it was given: the same text is the same step. A plan whose rounds can read alike must tell them
    apart in the prompt (pilot.activate does: the stimulus of a resumption carries the id of its wait group, or of the run)."""
    blob = json.dumps([message_id, step, instructions, output_schema, {"provider": model["provider"], "model": model["model"]}],
                      ensure_ascii=False, sort_keys=True)
    return hashlib.sha256(blob.encode("utf-8")).hexdigest()[:20]


async def run(port, params: Dict[str, Any]) -> Dict[str, Any]:
    """An inference step lasts as long as the model: acknowledged as long-running."""
    async with port.keepalive():
        return await _run(port, params)


async def _run(port, params: Dict[str, Any]) -> Dict[str, Any]:
    """One prompt of a Capacity pilot, passed through the continuity: recorded, memoized, validated."""
    # --- #validate ---------------------------------------------------------------------
    cid = check_continuity_id(params.get("continuity_id"))
    run_id = params.get("run_id")
    step = params.get("step")
    output_schema, model = params.get("output_schema"), params.get("model")
    if not isinstance(run_id, str) or not run_id:
        raise InvalidRequest("run_id is required")
    if not isinstance(step, str) or not step.strip():
        raise InvalidRequest("step is required")
    instructions, texts, matters, order = check_prompt(params)
    if not isinstance(output_schema, dict):
        raise InvalidRequest("output_schema is required")
    if not (isinstance(model, dict) and isinstance(model.get("provider"), str) and isinstance(model.get("model"), str)):
        raise InvalidRequest("model must carry provider and model")
    problems = schema_problems(output_schema)
    if problems:
        raise InvalidRequest("output_schema cannot be sent to a structured-output provider: " + "; ".join(problems[:4]),
                             details={"problems": problems})
    projection = params.get("projection")
    if projection is not None:
        validate_value("projection_definition", projection)
    framework = params.get("framework")
    framework = False if framework is None else framework
    if not isinstance(framework, bool):
        raise InvalidRequest("framework must be a boolean")
    if params.get("input") is not None:
        raise InvalidRequest("input is not a parameter: the data of a step is a part of its prompt (an element of prompt_parts)")
    limit = params.get("max_inferences")
    limit = min(limit, pilot_refs.DEFAULT_MAX_INFERENCES) if isinstance(limit, int) and not isinstance(limit, bool) and limit > 0 \
        else pilot_refs.DEFAULT_MAX_INFERENCES

    # --- #own: the run must own the continuity -----------------------------------------------
    ids = matter_ids(cid)
    try:
        await port.matter_read(ids.continuity)
        journal = await continuity_store.get(port, cid)          # the in-memory continuity, rebuilt from the journal when absent
        rt = (await port.matter_read(ids.runtime)).content
    except NotFound as exc:
        raise NotFound("the continuity does not exist", details={"continuity_id": cid}) from exc
    if rt.get("run_id") != run_id or rt["status"] not in ("running", "waiting"):
        raise Refused("only the run that owns the continuity may infer for it", details={"continuity_id": cid, "run_id": run_id})
    message_id = rt.get("current_message_id")
    if instructions is None:
        instructions = await assemble(port, texts, matters, order)       # from here on the prompt is one text, however it was given

    # --- #memoize: a recorded step is returned without calling the LLM -------------------------------
    key = step_key(message_id, step.strip(), instructions, output_schema, model)
    prompt_id, completion_id = f"prompt_{cid}_{key}", f"completion_{cid}_{key}"
    done = journal.message(completion_id)
    if done is not None:
        content = done["content"]
        return {"output": content["output"], "usage": content.get("usage") or {}, "step": step, "replayed": True}

    # --- #bound: runaway cost is refused, not discovered on the invoice ----------------------------------------
    used = sum(1 for m in journal.thread() if m["kind"] == "prompt" and (m.get("correlation") or {}).get("causation_id") == message_id)
    if not journal.has(prompt_id) and used >= limit:
        raise Refused(f"the message used its {limit} inferences", details={"continuity_id": cid, "message_id": message_id, "limit": limit})

    # --- #prompt: recorded before the model is asked -----------------------------------------------------------
    # With the continuity the journal is the conversation: the frame, the lines of the journal as messages with their roles, then the new
    # prompt. What was sent at the previous step is the start of what is sent now, so the provider can read it from its cache. Without it the
    # step sees only its prompt (and the frame when it asks).
    # the window of the continuity from the reading of the projection to the completion: nothing else is written between the prompt and its
    # completion, and no other inference runs for this continuity
    async with inference_window.hold(cid, yielding=port.yielding):
        frame: List[str] = []
        history: List[Dict[str, Any]] = []
        if projection is not None:
            view = await port.call_capacity(f"{CONTEXT}/continuity.project", {"continuity_id": cid, "projection": projection, "mode": "replace"})
            shown = view.get("projection", view)
            # an interrupted step asked again finds its own prompt in the journal: it is the new prompt, not a message of the history
            shown = dict(shown, messages=[m for m in shown.get("messages") or [] if m.get("id") != prompt_id])
            frame, history = conversation(shown)
        elif framework:                                                      # without the continuity, the frame only when the step asks
            frame, _ = split_frame({"messages": journal.thread()})
            if not frame:
                raise ConsistencyError("this continuity has no cognitive frame to give", details={"continuity_id": cid})
        correlation = {"causation_id": message_id} if message_id else {}
        await append_trace(port, cid, "prompt", {"step": step, "instructions": instructions,
                                                 "model": {"provider": model["provider"], "model": model["model"]}},
                           to=f"model:{model['provider']}/{model['model']}", correlation=correlation, message_id=prompt_id)

        # --- #infer ------------------------------------------------------------------------------------------------------
        if projection is not None:
            messages: List[Dict[str, Any]] = []
            if frame:
                messages.append({"role": "system", "content": PART_SEPARATOR.join(frame), "cache": True})
            messages += [dict(m) for m in history]
            if history:
                messages[-1]["cache"] = True                                 # the end of the history: the provider reads the cache up to here next time
            messages.append({"role": "user", "content": instructions})
        else:
            # the system text (frame, then prompt) is the stable part of the request: marked so the provider can cache it as a prefix
            messages = [{"role": "system", "content": PART_SEPARATOR.join(frame + [instructions]), "cache": True},
                        {"role": "user", "content": json.dumps({"continuity_id": cid, "step": step}, ensure_ascii=False)}]
        request = {
            "provider": model["provider"], "model": model["model"], "messages": messages,
            "metadata": {"continuity_id": cid, "step": step},                # for whoever observes the request: no provider reads it
            "parameters": model.get("parameters") or {},
            "requirements": {"provider": model["provider"], "model": model["model"]},
            "output": {"format": "json_schema", "schema": output_schema},
        }
        payload = await port.call_capacity("/root/llm/infer", {"request": request, "provider": model["provider"]})
        response = payload.get("response") or {}
        output = (response.get("output") or {}).get("content")
        if isinstance(output, str):
            output = extract_json(output)
        problems = violations(output_schema, output)
        if problems:
            await append_trace(port, cid, "error", {"event": "step_output_invalid", "step": step, "errors": problems[:8]},
                               correlation={"causation_id": prompt_id})
            raise InvalidRequest(f"the output of step {step!r} does not conform to its schema: " + "; ".join(problems[:4]),
                                 details={"step": step, "errors": problems})
        usage = response.get("usage") or {}

        # --- #complete: the answer is part of the history ------------------------------------------------------------------
        lines = [{"kind": "completion", "content": {"step": step, "output": output, "usage": usage}, "to": f"continuity:{cid}",
                  "from": f"model:{model['provider']}/{model['model']}", "correlation": {"causation_id": prompt_id}, "id": completion_id}]

        # --- #measure: only an inference that saw the journal says how full the journal is. The measure is a follow-up fact
        # (an event line), written in the same append as the completion. ---------------------------------------------------------
        tokens = usage.get("input_tokens")
        if projection is not None and isinstance(tokens, int) and tokens > 0:
            lines.append(measure_line(cid, message_id, tokens, estimated=False, detail=usage_detail(usage, response)))
        await continuity_store.append(port, cid, lines)
        return {"output": output, "usage": usage, "step": step, "replayed": False}
