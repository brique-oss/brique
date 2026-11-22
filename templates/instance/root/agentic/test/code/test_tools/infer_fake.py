from __future__ import annotations

import asyncio
import hashlib
import json

import rules
from support import Port, now

CAP = "infer.fake"
POLL_S = 0.05
MAX_WAIT_S = 300.0
HEARTBEAT_S = 10.0


async def wait_gate(runtime, port, gate: str) -> None:
    """Hold the answer until the named gate is open in the gates matter (the one of test.slow): a model call that stays in flight."""
    await runtime.notify_running()
    waited = 0.0
    while waited < MAX_WAIT_S:
        state, _ = await port.read("gates")
        if gate in state.get("open", []):
            return
        await asyncio.sleep(POLL_S)
        waited += POLL_S
        if int(waited / HEARTBEAT_S) != int((waited - POLL_S) / HEARTBEAT_S):
            await runtime.notify_running()
    raise TimeoutError(f"gate {gate} was never opened")


# The log of the calls (llm_calls) is ONE Matter, and every call must be in it before its answer goes out. Written call by call, by read-modify-write with a
# retry on conflict, forty simultaneous calls fight over that Matter (each entry carries the whole system prompt): 62 s instead of 2 s for forty. The calls
# that arrive together are therefore written by ONE read-modify-write, in the order they arrived; the same entries, the same numbering, the same guarantee.
_pending: list = []          # (record, future) of the calls waiting for the next write of the log
_leading = False             # a call is writing the log right now (the one that found nobody writing: the leader)


async def log_call(port, record):
    """Run `record(content)` for this call on the content of llm_calls, written with the other calls that arrive together. Returns when it is written.

    The first call to arrive while nobody is writing becomes the leader: IT writes the batches, in its own task, until none is left. A separate task for
    the writer would inherit the context of the call that created it, a call that is over by the time the later batches go out.
    """
    global _leading
    future = asyncio.get_running_loop().create_future()
    _pending.append((record, future))
    if not _leading:
        _leading = True
        try:
            await _write_pending(port)
        finally:
            _leading = False
            for _, waiting in _pending:                  # the leader was interrupted: nobody is left to write them
                if not waiting.done():
                    waiting.set_exception(RuntimeError("the writer of the log of calls was interrupted"))
            _pending.clear()
    return await _waiting(port, future)


def _waiting(port, future):
    """Wait for the write of the log WITHOUT holding the execution slot of the wrapper: as many calls waiting for the writer as there are slots would
    stop the writer from taking its own slot back after its next call (the same freeze as a wait for a lock: see substrate/port.py, `yielding`)."""
    scheduler = getattr(getattr(port, "runtime", None), "scheduler", None)
    if scheduler is None:
        return future
    return _yielded(scheduler, future)


async def _yielded(scheduler, future):
    async with scheduler.yielding():
        return await future


async def _write_pending(port) -> None:
    while _pending:
        batch = list(_pending)
        _pending.clear()
        results: list = []

        def apply(content):
            results.clear()                     # a conflict makes `update` call this again on fresh content: start the batch over
            for record, _ in batch:
                results.append(record(content))
            return content, None
        try:
            await port.update("llm_calls", apply)
        except Exception as exc:                # every call of the batch learns it
            for _, future in batch:
                if not future.done():
                    future.set_exception(exc)
            continue
        for (_, future), (_, number) in zip(batch, results):
            if not future.done():
                future.set_result(number)


# <brique:capacity name="infer.fake">
async def infer_fake(runtime, params: dict) -> dict:
    port = Port(runtime, CAP)
    request = await params.value("request")        # a large request arrives as a transit reference: the runtime resolves it
    if not isinstance(request, dict):
        raise ValueError("request is required")
    script, _ = await port.read("llm_script")
    script_rules = script.get("rules")
    if not isinstance(script_rules, list):
        raise ValueError("llm_script matter has no rules list")

    chosen = {}

    def record(f):
        calls = f.setdefault("calls", [])
        cid = rules.facts(request, 0)["continuity"]
        previous = sum(1 for c in calls if c.get("continuity") == cid)
        facts = rules.facts(request, previous)
        index, rule = rules.select(script_rules, facts)
        body = facts["body"]
        entry = {
            "n": len(calls) + 1, "continuity": facts["continuity"], "call_index": facts["call_index"],
            "message_id": facts["message_id"], "message_kind": facts["message_kind"], "message_from": facts["message_from"],
            "stimulus": facts["stimulus"], "correction": facts["correction"], "compaction": facts["compaction"], "step": facts["step"],
            "pressure": facts["pressure"], "rule_index": index,
            "context_keys": list(body.keys()), "initial_intention": facts["initial_intention"],
            "current_intention": body.get("current_intention"),
            "projection_ids": [i for i in (facts["projection_ids"] or []) if not str(i).startswith("framework_")],     # the exchange, not the frame
            "projection_definition": facts["projection_definition"],
            "active_stimulus": body.get("active_stimulus"),
            "user_message": body.get("user_message"),
            "system_prompt": (request.get("messages") or [{}])[0].get("content"),
            "messages_digest": [hashlib.sha1((str(m.get("role")) + ":" + (m["content"] if isinstance(m.get("content"), str) else json.dumps(m.get("content"), sort_keys=True))).encode()).hexdigest()[:12]
                                for m in request.get("messages") or []],
            "provider": request.get("provider"), "at": now(),
        }
        calls.append(entry)
        chosen["rule"], chosen["facts"] = rule, facts
        return f, entry["n"]

    await log_call(port, record)                     # recorded before the answer is returned
    rule, facts = chosen["rule"], chosen["facts"]
    if rule is None:
        raise LookupError("no scripted rule matches: " + json.dumps(
            {k: facts[k] for k in ("continuity", "message_id", "message_text", "stimulus", "call_index", "correction")}, ensure_ascii=False))
    then = rule.get("then") or {}
    if then.get("wait_gate"):
        await wait_gate(runtime, port, str(then["wait_gate"]))        # the call is recorded above, the answer waits
    if "error" in then:
        raise RuntimeError(str(then["error"]))
    content = then["invalid"] if "invalid" in then else rules.render(then.get("output"), facts)
    return {"response": {
        "output": {"format": "json", "content": content},
        "resolved": {"provider": "fake", "model": "scripted"},
        "usage": dict({"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}, **(then.get("usage") or {})),
        "finish_reason": "stop", "duration_ms": 0,
    }}
# </brique:capacity>
